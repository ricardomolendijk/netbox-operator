package reconciler

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// ready records a reconcile that got the object where the spec says it should be.
func (p *pass) ready(ctx context.Context) (ctrl.Result, error) {
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
	p.result = out.result
	log := logf.FromContext(ctx).WithValues("reason", out.reason, "action", "stop")

	// A write that 404s means the object went away between locating it and writing it.
	// Clearing the id is what lets the next pass re-create or re-adopt it instead of
	// retrying a dead id forever.
	var notFound *netbox.NotFoundError
	if errors.As(err, &notFound) {
		p.obj.NetBoxStatus().ID, p.obj.NetBoxStatus().Adopted = 0, false
	}

	if out.severe {
		log.Error(err, "reconcile stopped")
	} else {
		log.V(1).Info("reconcile waiting", "err", err.Error())
	}

	if out.event != "" {
		p.engine.warn(p.obj, out.event, "%s", err.Error())
	}

	p.condition(netboxv1alpha1.ConditionReady, false, out.reason, err.Error())

	return p.finish(ctx, out.requeue)
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
	status := p.obj.NetBoxStatus()
	status.ObservedGeneration = p.obj.GetGeneration()

	if equality.Semantic.DeepEqual(p.before, status) {
		logf.FromContext(ctx).V(1).Info("status unchanged; not writing", "action", "none")

		return ctrl.Result{RequeueAfter: jitter(requeue)}, nil
	}

	if err := p.engine.Status.UpdateStatus(ctx, p.obj); err != nil {
		// The NetBox side may well have succeeded, but a reconcile whose status never
		// landed is not a success: observedGeneration is stale and the next pass will do
		// the work again. Counted as an error so it cannot hide inside `updated`.
		p.result = metrics.ResultError

		return ctrl.Result{}, fmt.Errorf("updating the status of %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	return ctrl.Result{RequeueAfter: jitter(requeue)}, nil
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

// jitter spreads a requeue by up to a tenth either way, so that objects created together
// -- a whole manifest applied at once -- do not resync in lockstep for the rest of their
// lives and turn one NetBox into the bottleneck.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}

	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}

	return d - time.Duration(spread/2) + time.Duration(rand.Int64N(spread)) //nolint:gosec // spreading load, not a secret
}

// warn records an Event for a state that needs a human.
func (e *Engine) warn(obj Object, reason, format string, args ...any) {
	if e.Events == nil {
		return
	}

	e.Events.Eventf(obj, "Warning", reason, format, args...)
}
