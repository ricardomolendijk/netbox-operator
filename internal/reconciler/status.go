package reconciler

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// ready records a reconcile that got the object where the spec says it should be.
//
// Except when a reference did not: RefsResolved=False implies Ready=False with
// ReasonWaitingForRef (issue #132). On a kind where the reference is not part of the natural
// key, the object is created without it -- and reporting Ready there would mean `kubectl
// apply` succeeding, `kubectl wait --for=condition=Ready` passing, and NetBox never seeing
// the value. Ready=False is what makes the omission something an automation can notice.
func (p *pass) ready(ctx context.Context) (ctrl.Result, error) {
	if p.refs.message != "" {
		p.condition(netboxv1alpha1.ConditionReady, false,
			netboxv1alpha1.ReasonWaitingForRef, p.refs.message)

		// resync() rather than driftResync(): an object with an unresolved reference has not
		// settled, so driftMode: Off must not switch off the one retry that will ever resolve
		// it. Same argument stop() makes for an object waiting on its endpoint.
		return p.finish(ctx, p.refs.wait(p.resync()))
	}

	p.condition(netboxv1alpha1.ConditionReady, true, netboxv1alpha1.ReasonSynced,
		fmt.Sprintf("netbox %s/%d matches the spec", p.desc.Endpoint, p.obj.NetBoxStatus().ID))

	return p.finish(ctx, p.driftResync())
}

// pending records a reconcile that deliberately did not finish the job: an endpoint in
// DryRun, or one whose driftMode is Report. The object is not Ready, and saying otherwise
// would make `kubectl wait` lie about a write that never happened -- which is the whole
// reason Report reports Ready=False rather than treating "reported as configured" as
// success.
func (p *pass) pending(ctx context.Context, reason, message string) (ctrl.Result, error) {
	p.condition(netboxv1alpha1.ConditionReady, false, reason, message)

	return p.finish(ctx, p.driftResync())
}

// stop records why this reconcile could not proceed and when to try again.
//
// Every non-success exit goes through here, so the mapping from failure to condition to
// requeue exists once. It returns no error: the caller's error is the object's state, and
// returning it would add controller-runtime backoff on top of a requeue that was already
// chosen deliberately.
func (p *pass) stop(ctx context.Context, err error) (ctrl.Result, error) {
	out := classify(err, p.resync())

	// Counted on every pass, including the repeats whose Event and error line are
	// suppressed below. The asymmetry is deliberate: reconcile_total is a count of
	// reconciles, not of changes, so a rate() over it is the retry rate of a standing
	// failure -- exactly the signal that would disappear if the metric were made
	// transition-triggered like the Event. Do not "fix" it to match.
	p.result = out.result

	log := logf.FromContext(ctx).WithValues("reason", out.reason, "action", "stop")

	// A write that 404s means the object went away between locating it and writing it.
	// Clearing the id is what lets the next pass re-create or re-adopt it instead of
	// retrying a dead id forever.
	var notFound *netbox.NotFoundError
	if errors.As(err, &notFound) {
		p.obj.NetBoxStatus().ID, p.obj.NetBoxStatus().Adopted = 0, false
	}

	// Whether this pass is entering the failed state or repeating one the object is already
	// in. Read before the condition below overwrites it: the stored condition is the only
	// memory the engine has across passes, which is what makes this a guard rather than a
	// cache.
	changed := p.transitioned(netboxv1alpha1.ConditionReady, metav1.ConditionFalse, out.reason)

	switch {
	case out.severe && changed:
		log.Error(err, "reconcile stopped")
	case out.severe:
		// Debug on a repeat. A Conflict or an Invalid requeues at the endpoint's resync,
		// so at error a spec NetBox keeps rejecting is an identical error line every ten
		// minutes for the lifetime of the process -- and this is the path every object of
		// every kind takes, so it buries whatever is actually new. The information is
		// still here for anyone who turns the verbosity up, and the condition below
		// carries the standing state (CONTRIBUTING.md, "Logging"; NBO-010 made the same
		// call in the endpoint controller's fail()).
		log.V(1).Info("reconcile is still stopped", "err", err.Error())
	default:
		log.V(1).Info("reconcile waiting", "err", err.Error())
	}

	// On the transition only. An Event is an API object: it costs etcd, it counts against
	// the namespace's retention, and a duplicate every resync evicts the Events somebody
	// actually needed. classify has already decided whether this state is worth an Event at
	// all; this decides whether it is worth saying again (see outcome.event).
	if out.event != "" && changed {
		p.engine.warn(p.obj, out.event, "%s", out.message(err))
	}

	// Every pass, unguarded. The condition is the standing state -- which is precisely why
	// the Event and the error line above need not repeat -- so it has to keep carrying this
	// pass's reason, message and observedGeneration. finish() still writes nothing when
	// none of the three moved.
	p.condition(netboxv1alpha1.ConditionReady, false, out.reason, out.message(err))

	return p.finish(ctx, out.requeue)
}

// transitioned reports whether writing this condition would change the object's state.
//
// Status and reason only; the message is deliberately excluded. A stop message is the
// underlying error's own wording -- a timeout whose text differs by a millisecond, a
// NetBox body that lists the same field errors in another order -- and none of that is a
// state change. Keying on it would re-fire the Event and the error line on every retry,
// which is the whole thing the guard exists to prevent.
//
// It reads the status as stored rather than the live conditions, so it answers "has this
// changed since the last pass" even for a condition an earlier step of this same pass has
// already touched.
func (p *pass) transitioned(condType string, status metav1.ConditionStatus, reason string) bool {
	existing := meta.FindStatusCondition(p.before.Conditions, condType)

	return existing == nil || existing.Status != status || existing.Reason != reason
}

// newDrift reports whether the drift a suppressed write is leaving standing is worth
// saying again, given the Synced reason and the field list this pass would report.
//
// Two questions, because either moves on its own. The Synced reason changes when the reason
// nothing was sent changes -- an endpoint switched between DryRun and Report reports the
// same drift for a different cause, and "would have updated" and "drift detected" do not
// read alike in `kubectl describe`. The DriftDetected message changes when the drift itself
// does, which is the case transitioned() alone would miss: a second field edited in NetBox
// is new information about the same standing state.
//
// This is the one place a condition's message is part of the comparison, and it is not an
// exception to transitioned()'s rule so much as the other side of it. There the message is
// an error's own wording, which drifts without the state moving; here it is the list of
// fields NetBox and the spec disagree on, which is the state.
func (p *pass) newDrift(syncedReason, drift string) bool {
	if p.transitioned(netboxv1alpha1.ConditionSynced, metav1.ConditionFalse, syncedReason) {
		return true
	}

	existing := meta.FindStatusCondition(p.before.Conditions, netboxv1alpha1.ConditionDriftDetected)

	return existing == nil || existing.Status != metav1.ConditionTrue || existing.Message != drift
}

// condition sets one condition, always stamping the generation it was observed at.
func (p *pass) condition(condType string, ok bool, reason, message string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(&p.obj.NetBoxStatus().Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: p.obj.GetGeneration(),
	})
}

// finish is the single exit from a reconcile.
//
// It always sets observedGeneration -- forgetting it makes `kubectl wait` lie, which is the
// kind of bug nobody notices until an automation hangs -- and it writes nothing when
// nothing changed. That second half is what makes a no-drift resync free: without it every
// object in the cluster would take a status write every resync period, for no new
// information.
func (p *pass) finish(ctx context.Context, requeue time.Duration) (ctrl.Result, error) {
	// Here rather than as a step in Reconcile, and that placement is the decision. This is
	// the one exit every path shares, so a parent's children converge whether the pass
	// created its NetBox object, corrected drift, found none, or stopped; it runs after
	// applyWrite has established status.id, which is the guard materialisation depends on;
	// and it runs *before* the comparison below, so status.children and ChildrenReady land in
	// the same write as everything else this pass decided and a no-op materialisation still
	// writes nothing at all. A step in Reconcile would have needed the call in four places,
	// one of which somebody would eventually forget.
	if wait := p.materialise(ctx); wait > 0 && (requeue == 0 || wait < requeue) {
		requeue = wait
	}

	status := p.obj.NetBoxStatus()
	status.ObservedGeneration = p.obj.GetGeneration()

	if equality.Semantic.DeepEqual(p.before, status) {
		logf.FromContext(ctx).V(1).Info("status unchanged; not writing", "action", "none")

		return ctrl.Result{RequeueAfter: Jitter(requeue)}, nil
	}

	if err := p.engine.Status.UpdateStatus(ctx, p.obj); err != nil {
		// Before either branch below decides what the failure means, because it is true of
		// both: everything this pass computed can be computed again except the id of an object
		// it has just created, and that one is lost unless it is written now.
		recorded := p.recordID(ctx)

		if staleStatusWrite(ctx, err) {
			if recorded != nil {
				// The refused status write is the ordinary outcome of a cached read and would
				// have been a requeue; failing to keep the id is not, so this is the error the
				// pass reports and the one controller-runtime backs off on.
				p.result = metrics.ResultError

				return ctrl.Result{}, recorded
			}

			p.result = metrics.ResultWaiting

			return ctrl.Result{RequeueAfter: Jitter(staleRetry)}, nil
		}

		// The NetBox side may well have succeeded, but a reconcile whose status never
		// landed is not a success: observedGeneration is stale and the next pass will do
		// the work again. Counted as an error so it cannot hide inside `updated`.
		p.result = metrics.ResultError

		return ctrl.Result{}, fmt.Errorf("updating the status of %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	return ctrl.Result{RequeueAfter: Jitter(requeue)}, nil
}

// recordID persists the id of a NetBox object this pass created, when the status write that
// was carrying it did not land.
//
// This is the exception staleStatusWrite's reasoning does not cover, and the one it costs the
// most to be wrong about. Dropping a refused pass's conclusions is safe because "the next pass
// reads the caught-up copy and reaches the same conclusions again if they still hold" -- true
// of every conclusion but one. *I created NetBox object 4001* cannot be reached again: the API
// server's copy has status.id == 0, so the next pass falls through to the natural key, finds
// the object the operator itself made, has no evidence it is its own, and refuses to adopt on a
// guess. The CR is then Ready=False/Conflict for ever, advising spec.onConflict: Adopt on an
// object nothing else has ever touched -- which is exactly the advice issue #252 established is
// harmful -- and NetBox holds an orphan (issues #289 and #291).
//
// So the id goes back on its own, through a writer that carries no resourceVersion and
// therefore cannot lose the same race twice (IDWriter). Nothing else from the refused pass goes
// with it: an id is a fact NetBox minted for a POST that has already happened, while a
// condition or a hash is a conclusion drawn from a read that turned out to be stale, and the
// whole of #252's second half is that those two must not be written by the same call.
//
// Only for an id this pass proved by writing to NetBox (applyWrite sets newID). An adoption is
// deliberately not included: the next pass matches the same natural key, and its policy is
// Adopt by construction, so it takes the same object over again.
func (p *pass) recordID(ctx context.Context) error {
	if p.newID == 0 {
		return nil
	}

	if p.engine.IDs == nil {
		return fmt.Errorf("%w: no id writer, so the netbox object this pass created cannot be "+
			"recorded", errNotConfigured)
	}

	if err := p.engine.IDs.RecordID(ctx, p.obj, p.newID); err != nil {
		err = fmt.Errorf("recording netbox %s/%d on %s/%s: %w", p.desc.Endpoint, p.newID,
			p.obj.GetNamespace(), p.obj.GetName(), err)

		// At error level, and one of the few places that earns it: the object exists in NetBox
		// and nothing in Kubernetes knows its id, which is the one state this engine cannot
		// reconcile its way out of on an endpoint with no provenance stamp to recognise it by.
		logf.FromContext(ctx).Error(err, "the netbox object this pass created may be orphaned",
			"netboxID", p.newID, "action", "record")

		return err
	}

	logf.FromContext(ctx).Info("kept the id of the netbox object this pass created, past a "+
		"status write that lost", "netboxID", p.newID, "action", "record")

	return nil
}

// staleStatusWrite reports whether err is a status write the API server refused because
// another pass of the same object got there first -- and says so in the log when it is.
//
// Not a failure, and this is the difference the whole of issue #252's second half is about.
// The engine reconciles from an informer cache, so two passes of one key overlap routinely: the
// second carries the resourceVersion of a copy the first has already superseded and its write
// is refused. Returning that as an error put an `error` line, a failed reconcile in
// reconcile_total and controller-runtime backoff behind every object of a graph that had
// converged correctly -- 18 error reconciles and 27 error lines for a 17-object graph on the
// e2e run that found this.
//
// Nothing is re-applied over the copy that won. This pass computed its status from the read
// that turned out to be stale, so re-sending it could put back a status.id or a condition the
// winning write had already moved past -- and a status.id written back as zero is the shape
// that creates a second NetBox object. The next pass reads the caught-up copy and reaches the
// same conclusions again if they still hold, which is what makes dropping them safe.
func staleStatusWrite(ctx context.Context, err error) bool {
	if !apierrors.IsConflict(err) {
		return false
	}

	logf.FromContext(ctx).V(1).Info("another pass wrote this status first; reconciling again",
		"action", "requeue", "conflict", err.Error())

	return true
}

// resync is this endpoint's drift re-check interval.
func (p *pass) resync() time.Duration {
	if p.endpoint.Resync > 0 {
		return p.endpoint.Resync
	}

	return DefaultResync
}

// driftResync is the requeue for a pass that settled, and so the interval at which a
// NetBox-side edit that nothing in Kubernetes touched gets noticed.
//
// Zero under driftMode: Off, which is the whole of what "no periodic resync" means -- a CR
// event still reconciles, because a watch is not a requeue. Deliberately not used by
// stop(): an object waiting for its endpoint, or refused a NetBox object it conflicts
// with, has not settled, and turning off drift re-checks must not also turn off the retry
// that is the only thing that will ever get such an object unstuck.
func (p *pass) driftResync() time.Duration {
	if p.endpoint.DriftMode == netboxv1alpha1.DriftOff {
		return 0
	}

	return p.resync()
}

// Jitter spreads a requeue by up to a tenth either way, so that objects created together
// -- a whole manifest applied at once -- do not resync in lockstep for the rest of their
// lives and turn one NetBox into the bottleneck.
//
// Exported because the endpoint controller requeues on its own timers and needs the same
// spread for the same reason. One definition rather than two: two components of one binary
// disagreeing about a convention this package has already written down is how the
// convention stops being one. A tenth either way is deliberately narrow -- unlike the full
// jitter the NetBox client uses for retry backoff, it spreads a schedule without moving an
// interval out of the tier its caller chose.
func Jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}

	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}

	return d - time.Duration(spread/2) + time.Duration(rand.Int64N(spread)) //nolint:gosec // spreading load, not a secret
}

// warn records an Event for a state that needs a human. Callers emit only on a transition
// into that state: an Event per resync would put one line per object per interval into the
// namespace, and `kubectl describe` would show a page of the same sentence.
func (e *Engine) warn(obj Object, reason, format string, args ...any) {
	emit(e.Events, obj, nil, "Warning", reason, format, args...)
}
