package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Deletion intervals. A refused delete is not a failure to retry harder: it will keep
// being refused until something else is deleted, so the interval is about how promptly a
// chain converges rather than about how fast NetBox recovers.
const (
	// protectedRetryBase is the wait after the first refused delete. Short, because the
	// common case is a chain of a few objects being deleted together and the dependent is
	// about to go.
	//
	// Two seconds rather than the ten this shipped with, and the change is a consequence of
	// #289 rather than a tuning preference. The interval was never observed until the storm
	// it was supposed to govern was fixed, and what it governs is teardown latency: every
	// object deleted at the same time as its dependent is refused once, so a chain of depth
	// d converges after (2^(d-1) - 1) intervals. At ten seconds a four-deep chain -- prefix,
	// VLAN, VLAN group, tenant, which is an ordinary NetBox graph and is the one
	// test/e2e/fixtures/graph tears down -- needs 150 s, past the gate's two-minute budget
	// and past what anyone watching a `kubectl delete` would call working. At two it needs
	// 30 s, and the ceiling below still keeps a permanently blocked object down to twelve
	// requests an hour.
	protectedRetryBase = 2 * time.Second

	// protectedRetryCap is the ceiling on that backoff. A delete blocked by an object
	// nobody is going to remove must not spin, and must not back off past a horizon where
	// nobody would notice it recovering either.
	protectedRetryCap = 5 * time.Minute

	// protectedBackoffShiftCap keeps the doubling from overflowing the shift on an object
	// that has been stuck for a very long time. Anything at or above it is the cap.
	protectedBackoffShiftCap = 16

	// protectedEventAfter is how many refusals it takes before the block is reported as an
	// Event. An Event per attempt is noise at cluster scale; no Event at all makes a
	// permanently stuck deletion silent, which is worse.
	//
	// Five, which with the base above is thirty seconds of refusals. It moved with the base
	// so that the *elapsed time* before a human is told stays roughly what it was: an Event
	// on the third refusal would now fire six seconds into an ordinary cascade, which is a
	// warning about something that is working.
	protectedEventAfter = 5
)

// FinalizerWriter persists an object's metadata.finalizers.
//
// Separate from StatusWriter because a finalizer lives in metadata rather than status, and
// named for the one thing the engine does with it so that the implementation is free to
// use a patch scoped to that field. The engine never touches a spec
// (docs/decisions/0005-gitops-coexistence.md), and keeping the two writers apart is what
// makes that checkable rather than merely intended.
type FinalizerWriter interface {
	UpdateFinalizers(ctx context.Context, obj client.Object) error
}

// claim adds the finalizer and persists it, before the engine writes anything to NetBox.
//
// The write is synchronous and this pass stops if it fails, which is the whole point of
// the ordering: a finalizer that exists only in memory while a POST goes out is the
// add-after-create window wearing a disguise, and that window is how an orphan is made --
// the process dies between creating a NetBox object and recording that something has to
// clean it up, and nothing is left that knows the object exists.
//
// The cost of taking responsibility this early is a CR that carries a finalizer while
// nothing exists in NetBox yet. That is paid for by the status.id == 0 step of the
// deletion sequence, which drops the finalizer without a NetBox call, so the early claim
// cannot make a CR undeletable.
func (e *Engine) claim(ctx context.Context, obj Object) error {
	return takeFinalizer(ctx, e.Finalizers, obj)
}

// takeFinalizer is Engine.claim's body, over any CR rather than only an engine-driven one.
//
// Shared because the allocation engine needs exactly this, for exactly the same reason
// (claim.go): a claim that POSTs to an allocation endpoint before its finalizer is durable
// is a CR that can die owing NetBox an object nobody is left to account for. Two copies of
// the add-then-persist-then-roll-back-on-failure sequence would be two places for that
// ordering to be got wrong.
func takeFinalizer(ctx context.Context, writer FinalizerWriter, obj client.Object) error {
	if controllerutil.ContainsFinalizer(obj, netboxv1alpha1.Finalizer) {
		return nil
	}

	// A missing collaborator is a wiring mistake, and it must say so rather than panic
	// halfway through a pass. Claiming runs before any NetBox call, so failing here is
	// also the safest place to fail: nothing has been created that could leak.
	if writer == nil {
		return fmt.Errorf("%w: no FinalizerWriter is wired", errNotConfigured)
	}

	controllerutil.AddFinalizer(obj, netboxv1alpha1.Finalizer)

	if err := writer.UpdateFinalizers(ctx, obj); err != nil {
		// Put the in-memory object back the way the API server still sees it. Leaving the
		// finalizer on a copy the API server rejected would let a later status write
		// succeed against an object that never got protected.
		controllerutil.RemoveFinalizer(obj, netboxv1alpha1.Finalizer)

		return fmt.Errorf("adding the finalizer to %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	logf.FromContext(ctx).Info("took responsibility for the netbox object", "action", "claim")

	return nil
}

// deleting runs the deletion sequence for a CR that has a deletion timestamp.
//
// The order of the steps is the design. Everything that needs no NetBox call is answered
// first, so a Retain migration and a CR that never got as far as creating anything both
// complete while NetBox is unreachable -- an escape hatch that only works when it is not
// needed is not an escape hatch.
func (p *pass) deleting(ctx context.Context) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(p.obj, netboxv1alpha1.Finalizer) {
		// Ours is already off, or was never on. Whatever is keeping this object alive is
		// somebody else's finalizer, and requeueing against it would be a busy loop.
		logf.FromContext(ctx).V(1).Info("no finalizer of ours to release", "action", "none")

		return ctrl.Result{}, nil
	}

	if out, ok := p.releaseWithoutDeleting(); ok {
		return p.release(ctx, out)
	}

	// After releaseWithoutDeleting, so the two existing ways out still work: the break-glass
	// annotation drops the finalizer without calling NetBox, and `deletionPolicy: Retain`
	// deletes nothing, so neither needs a decision about data this delete is not going to
	// destroy. Before the endpoint is resolved, because the refusal is the operator's and
	// does not depend on NetBox being reachable.
	if blocked, ok := p.dataLossBlocked(); ok {
		return p.blocked(ctx, netboxv1alpha1.ReasonDataLossBlocked, p.resync(), blocked)
	}

	// After the no-NetBox-call cases and before the endpoint, because it needs neither: a
	// Retain migration still completes with NetBox unreachable, and a parent whose children
	// are still here does not need a client to know it has to wait.
	if dependents, waiting := p.pendingDependents(ctx); waiting {
		return p.blocked(ctx, netboxv1alpha1.ReasonPendingDependents, childRetry, fmt.Sprintf(
			"cannot delete netbox %s/%d yet: the child CRs this object materialised still exist "+
				"(%s), and their own finalizers have to remove their netbox objects first",
			p.desc.Endpoint, p.obj.NetBoxStatus().ID, dependents))
	}

	endpoint, ok := p.engine.Endpoints.Endpoint(ctx,
		p.obj.GetNamespace(), p.obj.NetBoxSpec().EndpointRef)
	if !ok {
		// A CR with no recorded id has no client to search with, and this is the answer
		// releaseWithoutDeleting gave outright before the stamp made a search possible.
		// Keeping it here is what stops that search from becoming a way to block a deletion
		// that needs no NetBox call at all: a CR that never created anything still completes
		// while NetBox is down, which is the whole point of the ordering in this function.
		if p.obj.NetBoxStatus().ID == 0 {
			return p.release(ctx, p.nothingToDelete(false))
		}

		// Blocking rather than orphaning. The object is real, its id is known, and it will
		// still be deletable when the endpoint comes back; dropping the finalizer here
		// would turn a wait that resolves itself into an orphan that nothing ever finds,
		// because there is no orphan sweeper (NBO-046). Reversible beats irreversible, and
		// the annotation is there for a human who has decided otherwise.
		return p.blocked(ctx, netboxv1alpha1.ReasonWaitingForEndpoint, endpointRetry,
			fmt.Sprintf("cannot delete netbox %s/%d: netboxendpoint %q in namespace %q has no ready client; "+
				"the finalizer stays on rather than leaving the object behind. Set the annotation %s=true to "+
				"drop it and accept the orphan",
				p.desc.Endpoint, p.obj.NetBoxStatus().ID, p.obj.NetBoxSpec().EndpointRef,
				p.obj.GetNamespace(), netboxv1alpha1.SkipFinalizerAnnotation))
	}
	p.endpoint = endpoint

	// Last, because everything above is a decision this pass can make for itself and this is
	// the one step that costs a request. A pass that arrives before the backoff has run out
	// re-queues for the remainder and writes nothing at all -- see deletionHold.
	if remaining, holding := deletionHold(
		p.obj.NetBoxStatus().DeletionAttempts, p.obj.NetBoxStatus().LastDeletionAttempt, time.Now(),
	); holding {
		logf.FromContext(ctx).V(1).Info("holding off the next delete attempt",
			"action", "delete", "netboxID", p.obj.NetBoxStatus().ID, "in", remaining.String())

		return p.finish(ctx, remaining)
	}

	if p.obj.NetBoxStatus().ID == 0 {
		return p.deleteByStamp(ctx)
	}

	return p.deleteObject(ctx)
}

// deleteByStamp deletes the object a lost status write left behind, found by the provenance
// stamp rather than by an id nothing recorded.
//
// Only reachable on an endpoint that stamps `k8s_uid` on this kind, because
// releaseWithoutDeleting answers every other CR with an unset id without a NetBox call at
// all. The search is `?cf_k8s_uid=<this CR's metadata.uid>`, and it is not the natural-key
// lookup this file refuses to do wearing a disguise: metadata.uid is assigned by the API
// server, never reused, and written into that field by this operator for this one CR, so a
// match was created by this CR and by nothing else. It is the same evidence duplicate.go
// picks one address out of several with, and the same shape of recovery the allocation
// engine already performs against a lost allocation (claim.go, findByIdentity).
//
// A search that fails or comes back ambiguous blocks and keeps the finalizer, for the reason
// the endpoint-unavailable case blocks: "there may be an object of mine out there and I could
// not check" has one reversible answer, and orphaning is not it.
func (p *pass) deleteByStamp(ctx context.Context) (ctrl.Result, error) {
	// The other half of the question releaseWithoutDeleting could only half-answer: this
	// endpoint writes no uid, so there is nothing to search by and nothing has changed.
	if !p.stampIdentifies() {
		return p.release(ctx, p.nothingToDelete(false))
	}

	params := netbox.Params{}.Match(customFieldFilter+p.endpoint.Provenance.UIDField,
		netbox.LookupExact, string(p.obj.GetUID()))

	live, err := p.endpoint.Client.GetOne(ctx, p.desc.Endpoint, params)
	if err != nil {
		out := classify(err, p.resync())

		return p.blocked(ctx, out.reason, out.requeue, fmt.Sprintf(
			"searching netbox %s for this object's own stamp %v: %v", p.desc.Endpoint, params, err))
	}

	if live == nil {
		return p.release(ctx, p.nothingToDelete(true))
	}

	id, ok := live.ID()
	if !ok {
		return p.blocked(ctx, netboxv1alpha1.ReasonAPIError, p.resync(), fmt.Sprintf(
			"netbox %s answered %v with an object carrying no id", p.desc.Endpoint, params))
	}

	logf.FromContext(ctx).Info("recovered a netbox object from its provenance stamp",
		"action", "recover", "netboxID", id)
	p.obj.NetBoxStatus().ID = int64(id)

	return p.deleteObject(ctx)
}

// deletionHold reports how long is left of the backoff after the last refused delete, and
// whether this pass must therefore send nothing.
//
// **This is what makes the backoff a backoff** (#289). The interval protected() chooses is
// carried in a ctrl.Result, and a ctrl.Result only says when to come back *at the latest*:
// the status write that records the refusal is itself an event on the object, so the
// controller wakes immediately, and a pass that cannot tell an early wake from a due one
// sends the DELETE again. Measured on the shipped code, that is a refused DELETE and a status
// write about every three milliseconds, for as long as whatever is referencing the object
// exists -- against NetBox and against the API server at once, which is how five CRs
// referenced by other fixtures failed to be deleted inside two minutes while nothing in the
// log said a delete had even been refused.
//
// So the schedule is read off the clock rather than off what happened to wake the pass. Any
// number of wake-ups between two attempts cost one cached read each and nothing else, and the
// caller still requeues for the remainder, so the object comes back on its own if nothing
// else wakes it.
//
// A last attempt in the future is treated as due rather than as a very long wait: clocks move
// backwards, and the one thing this must never do is turn a wait that resolves itself into a
// finalizer nobody can get off.
func deletionHold(attempts int32, last *metav1.Time, now time.Time) (time.Duration, bool) {
	if attempts <= 0 || last == nil {
		return 0, false
	}

	elapsed := now.Sub(last.Time)
	if elapsed < 0 {
		return 0, false
	}

	if wait := protectedBackoff(attempts); elapsed < wait {
		return wait - elapsed, true
	}

	return 0, false
}

// pendingDependents names the child CRs this object materialised that are still in the
// cluster, and whether to keep the finalizer on for them.
//
// **This is what orders a cascade**, not blockOwnerDeletion. That flag takes effect only
// under *foreground* propagation and `kubectl delete` defaults to background, so under the
// default the garbage collector removes the parent and its children concurrently. Without
// this wait the parent's NetBox object would often be deleted first, which NetBox refuses
// with PROTECT while its interfaces still exist: the right end state, reached through a
// queue of 409s and a Deleting condition pointing at the wrong cause.
//
// Read off status.children rather than by listing every kind in the group. That is the record
// of what this object materialised, written by the pass that materialised it, and a kind that
// materialised nothing has an empty list and takes no API call at all.
//
// A read that *failed* counts as a child that is still here. Waiting is the reversible
// answer; deleting the parent's NetBox object because a list call timed out is not.
func (p *pass) pendingDependents(ctx context.Context) (string, bool) {
	children := p.obj.NetBoxStatus().Children
	if len(children) == 0 || p.engine.Children == nil {
		return "", false
	}

	alive := make([]string, 0, len(children))

	for _, child := range children {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(netboxv1alpha1.GroupVersion.WithKind(child.Kind))

		err := p.engine.Children.Get(ctx,
			client.ObjectKey{Namespace: p.obj.GetNamespace(), Name: child.Name}, live)

		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			alive = append(alive, fmt.Sprintf("%s %s (unreadable: %v)", child.Kind, child.Name, err))
		default:
			alive = append(alive, child.Kind+" "+child.Name)
		}
	}

	if len(alive) == 0 {
		return "", false
	}

	return strings.Join(alive, ", "), true
}

// release is the finalizer coming off, and why.
type release struct {
	// event is the Event reason recorded for it.
	event string

	// message is what the Event says.
	message string

	// warn marks a release that leaves something behind in NetBox. A human has to see
	// that; the rest is routine.
	warn bool
}

// releaseWithoutDeleting reports the cases where the finalizer comes off with no NetBox
// call at all.
func (p *pass) releaseWithoutDeleting() (release, bool) {
	id := p.obj.NetBoxStatus().ID

	// First, and unconditionally: the break-glass overrides every other consideration,
	// including a delete that would otherwise be attempted.
	if p.obj.GetAnnotations()[netboxv1alpha1.SkipFinalizerAnnotation] == "true" {
		return release{
			event: netboxv1alpha1.EventFinalizerSkipped,
			message: fmt.Sprintf("%s=true: dropping the finalizer without calling netbox, so %s/%d is left behind",
				netboxv1alpha1.SkipFinalizerAnnotation, p.desc.Endpoint, id),
			warn: true,
		}, true
	}

	if deletionPolicyOf(p.obj.NetBoxSpec().DeletionPolicy) == netboxv1alpha1.DeletionRetain {
		return release{
			event: netboxv1alpha1.EventRetained,
			message: fmt.Sprintf("spec.deletionPolicy is Retain: netbox %s/%d is left in place",
				p.desc.Endpoint, id),
		}, true
	}

	// status.id is the operator's claim on an object. Without one there is nothing it can
	// prove it owns *by natural key*, and it will not go looking that way: a natural-key
	// lookup at deletion time would find whatever happens to match right now, which is how
	// an operator deletes somebody else's data. An object adopted under onConflict: Adopt
	// has an id and is deleted; one this CR never wrote does not and is not.
	//
	// Two things produce an unset id -- nothing was ever created, or a create succeeded and
	// the status write recording its id did not. On an endpoint that stamps `k8s_uid` on this
	// kind the two are distinguishable and deleteByStamp asks, because that stamp holds this
	// CR's own metadata.uid and nothing else in NetBox can be carrying it.
	//
	// Without such a stamp nothing has changed and nothing can: the id and the hash that
	// would have distinguished the two cases are written together in the update that failed.
	// So it names both rather than picking the flattering one, and an unstamped endpoint
	// keeps the one place where the operator can leave an orphan it will never find again.
	//
	// mayBeStamped rather than stampIdentifies, because the endpoint is deliberately not
	// resolved yet: this is the half of the question a CR answers about itself, and the
	// other half is asked in deleteByStamp once there is a client to ask it with.
	if id == 0 && !p.mayBeStamped() {
		return p.nothingToDelete(false), true
	}

	return release{}, false
}

// nothingToDelete is the finalizer coming off with no NetBox object to remove, in the two
// wordings the two ways of reaching it have earned.
//
// searched is the difference between "the operator cannot tell" and "the operator looked".
// A CR whose endpoint stamps its uid gets the second, and it is a stronger statement than
// this file has ever been able to make: no object in NetBox carries this CR's stamp, so
// nothing was ever created and nothing is left behind.
func (p *pass) nothingToDelete(searched bool) release {
	if searched {
		return release{
			event: netboxv1alpha1.EventNothingToDelete,
			message: fmt.Sprintf(
				"nothing was deleted: no netbox %s carries this object's %s=%s stamp, so the create"+
					" never happened and nothing is left behind",
				p.desc.Endpoint, p.endpoint.Provenance.UIDField, p.obj.GetUID()),
		}
	}

	return release{
		event: netboxv1alpha1.EventNothingToDelete,
		message: "no netbox object is recorded in status.id, so nothing was deleted; " +
			"either nothing was ever created, or a create succeeded and the status write " +
			"recording its id did not" + lookedFor(p.obj.NetBoxStatus().NaturalKey, p.desc.Endpoint),
	}
}

// dataLossBlocked reports whether this delete is one the operator refuses to make, and why.
//
// The case is narrow and it is not "deleting this is inconvenient": that is
// spec.deletionPolicy: Retain, which is the user stating an intent rather than the operator
// refusing one. This is a delete NetBox performs happily
// and which destroys data on *other* objects, so the engine's usual safety net -- send the
// DELETE, let NetBox refuse it with a `PROTECT`, report Protected and retry -- cannot fire.
// extras.CustomField is the case: its values live in each object's own `custom_field_data`
// JSON, so there are no rows to protect, and a `pre_delete` signal strips the key from every
// object of every assigned type (netbox/extras/signals.py, handle_cf_deleted).
//
// The finalizer stays on, which is what makes this reversible: the CR is still here, the
// NetBox object is still here, and either annotating the CR or switching it to
// `deletionPolicy: Retain` finishes the deletion. Nothing about this state resolves on its
// own, so it requeues at the endpoint's resync rather than fast -- and the CR change that
// clears it wakes the controller anyway.
func (p *pass) dataLossBlocked() (string, bool) {
	if !p.desc.DataLossOnDelete {
		return "", false
	}

	if p.obj.GetAnnotations()[netboxv1alpha1.AllowDataLossAnnotation] == "true" {
		return "", false
	}

	return fmt.Sprintf(
		"deleting netbox %s/%d destroys this field's stored value on every object in netbox that has "+
			"one, and netbox does not refuse it; the finalizer stays on. Set the annotation %s=true to "+
			"accept the loss, or spec.deletionPolicy: Retain to keep the netbox object "+
			"(docs/concepts/deletion.md)",
		p.desc.Endpoint, p.obj.NetBoxStatus().ID, netboxv1alpha1.AllowDataLossAnnotation), true
}

// lookedFor names the lookup that would have identified the object, when one was recorded.
// It is the only lead a human has when the id was lost, so it goes in the Event rather than
// only in a log line the CR's disappearance makes hard to find.
func lookedFor(key map[string]string, endpoint string) string {
	if len(key) == 0 {
		return ""
	}

	return fmt.Sprintf(". If it was, an object matching %v in %s is left behind", key, endpoint)
}

// deleteObject issues the DELETE and decides what its answer means.
func (p *pass) deleteObject(ctx context.Context) (ctrl.Result, error) {
	id := int(p.obj.NetBoxStatus().ID)
	deleted, err := p.endpoint.Client.Delete(ctx, p.desc.Endpoint, id)

	var notFound *netbox.NotFoundError
	var protected *netbox.ProtectedError

	switch {
	case err == nil:
		return p.release(ctx, p.deleted(id, deleted))
	// Already gone is the end state the CR asked for, reached by somebody else. Calling it
	// a failure would keep the finalizer on forever waiting for a delete that can never
	// succeed, because there is nothing left to delete.
	case errors.As(err, &notFound):
		return p.release(ctx, release{
			event:   netboxv1alpha1.EventDeleted,
			message: fmt.Sprintf("netbox %s/%d was already gone", p.desc.Endpoint, id),
		})
	case errors.As(err, &protected):
		return p.protected(ctx, err)
	}

	// Everything else is about NetBox's availability rather than about this object, so the
	// existing table picks the interval and the finalizer stays on.
	out := classify(err, p.resync())

	return p.blocked(ctx, out.reason, out.requeue,
		fmt.Sprintf("deleting netbox %s/%d: %v", p.desc.Endpoint, id, err))
}

// deleted describes a delete that came back clean.
//
// A suppressed answer is a DryRun that sent nothing, and it is read from the answer itself
// rather than from the endpoint's mode: carrying the mode alongside would be a second source
// of truth for the same fact, and whichever of the two drifted would have the Event claim a
// deletion that never happened.
func (p *pass) deleted(id int, out netbox.Object) release {
	if netbox.Suppressed(out) {
		return release{
			event: netboxv1alpha1.EventDeleted,
			message: fmt.Sprintf("dry run: netbox %s/%d was not deleted and is left in place",
				p.desc.Endpoint, id),
			warn: true,
		}
	}

	return release{
		event:   netboxv1alpha1.EventDeleted,
		message: fmt.Sprintf("deleted netbox %s/%d", p.desc.Endpoint, id),
	}
}

// protected records a delete NetBox refused because something still references the object.
//
// This is the claim the whole design rests on: PROTECT plus a backed-off retry *is* the
// topological sort, so there is no deletion-ordering table anywhere in the codebase. The
// dependent's own deletion unblocks this one and the next attempt finds it unblocked.
// Nothing is forced and no cascade parameter is sent -- forcing would delete data the user
// never asked to delete, and a hand-maintained order would have to stay in step with 159
// models, which NetBox's own answer does for free.
//
// It is not an error return. An error means controller-runtime backoff on top of the
// interval chosen here, and it would report a state that needs a human as a controller
// failure when the thing that fixes it is another object being deleted.
func (p *pass) protected(ctx context.Context, err error) (ctrl.Result, error) {
	status := p.obj.NetBoxStatus()
	status.DeletionAttempts++

	// Stamped in the same write as the count it belongs to, because the two are one fact and
	// a count nothing can date is what #289 was. Serialisation truncates it to the second,
	// which can bring the next attempt forward by under a second and changes nothing that
	// matters at these intervals.
	now := metav1.Now()
	status.LastDeletionAttempt = &now

	wait := protectedBackoff(status.DeletionAttempts)

	// Before the threshold Event, because a cascade that clears the block makes
	// DeleteBlocked the wrong thing to have said: the deletion is proceeding, and it is
	// proceeding because the user asked for exactly this.
	//
	// A nil Referrers is a supported wiring rather than an error. The engine then reports
	// the refusal the way it did before the annotation existed, which is the behaviour a
	// test that never wired one is entitled to.
	if p.cascades() && p.engine.Referrers != nil {
		out, cascadeErr := p.cascade(ctx)
		if cascadeErr != nil {
			return ctrl.Result{}, cascadeErr
		}

		if out.any() {
			// A Warning, and only when this pass deleted something. Deleting Kubernetes
			// objects the user did not name is not a thing to record at debug -- and
			// repeating it on every retry while the same referrers finish going is how the
			// Events somebody needed get evicted from the namespace.
			if len(out.deleted) > 0 {
				p.engine.warn(p.obj, netboxv1alpha1.EventCascadeDeleted,
					"netbox refused to delete %s/%d, so %s=true deleted the CRs referencing it: %s",
					p.desc.Endpoint, status.ID, netboxv1alpha1.CascadeDeleteAnnotation,
					strings.Join(out.deleted, ", "))
			}

			return p.blocked(ctx, netboxv1alpha1.ReasonCascading, wait,
				out.message(p.desc.Endpoint, status.ID, err))
		}
	}

	// Once, at the threshold. NetBox's body names the protected relation, and it is
	// carried through verbatim: "cannot delete" without a reason is the worst possible
	// operator experience.
	if status.DeletionAttempts == protectedEventAfter {
		p.engine.warn(p.obj, netboxv1alpha1.EventDeleteBlocked,
			"netbox has refused to delete %s/%d %d times: %v",
			p.desc.Endpoint, status.ID, status.DeletionAttempts, err)
	}

	return p.blocked(ctx, netboxv1alpha1.ReasonProtected, wait,
		fmt.Sprintf("%v; attempt %d, retrying in %s", err, status.DeletionAttempts, wait))
}

// protectedBackoff doubles the wait per refusal, up to protectedRetryCap.
func protectedBackoff(attempts int32) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}

	if shift >= protectedBackoffShiftCap {
		return protectedRetryCap
	}

	if wait := protectedRetryBase << shift; wait < protectedRetryCap {
		return wait
	}

	return protectedRetryCap
}

// blocked records a deletion that did not finish, and when to decide again.
//
// The finalizer stays on, which is the only reason any of this is safe to report as a
// condition rather than an error: the NetBox object is still there and still claimed.
func (p *pass) blocked(ctx context.Context, reason string, requeue time.Duration, message string) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("deletion is blocked",
		"action", "delete", "reason", reason, "netboxID", p.obj.NetBoxStatus().ID, "cause", message)
	p.condition(netboxv1alpha1.ConditionDeleting, false, reason, message)

	return p.finish(ctx, requeue)
}

// release removes the finalizer, which is what lets Kubernetes finish deleting the CR.
//
// The Event is emitted after the removal has been accepted, not before: an Event announcing
// a release that then failed to persist is a record of something that did not happen. No
// status is written either -- the object is about to stop existing, so a status update
// races the delete and nothing would ever read it. The Event is the record that outlives
// the CR.
func (p *pass) release(ctx context.Context, out release) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(p.obj, netboxv1alpha1.Finalizer)

	if err := p.engine.Finalizers.UpdateFinalizers(ctx, p.obj); err != nil {
		controllerutil.AddFinalizer(p.obj, netboxv1alpha1.Finalizer)

		return ctrl.Result{}, fmt.Errorf("removing the finalizer from %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	logf.FromContext(ctx).Info("released the finalizer",
		"action", "delete", "reason", out.event, "detail", out.message)

	if out.warn {
		p.engine.warn(p.obj, out.event, "%s", out.message)

		return ctrl.Result{}, nil
	}

	p.engine.event(p.obj, out.event, "%s", out.message)

	return ctrl.Result{}, nil
}

// deletionPolicyOf returns the effective deletion policy: the spec's when it states one, and
// Delete otherwise.
//
// One default for every kind, which is the whole of the rule since #304. It was not always:
// decision #176 made the IPAM kinds default to Retain, on the argument that deleting an
// ipam.IPAddress frees the address for reallocation while deleting a tag costs nothing. The
// argument was right about the risk and wrong about where to put it. A default of Retain
// means `kubectl delete` leaves the NetBox object behind and the CR disappears with an Event
// nobody reads -- and the object it leaves is the one NetBox then cites, with a PROTECT, to
// refuse the delete of the *site* it belongs to. One namespace deleted that way leaves a
// NetBox nobody can clean up through the operator at all, which is what #304 was reported as.
//
// So the destructive-by-default risk is answered where it is visible instead: the delete goes
// out, NetBox refuses what is still referenced, and the CR stays with `Deleting=False` naming
// the blocker. A user who wants an object to outlive its CR writes `deletionPolicy: Retain`,
// which is one line in the manifest that says so, in Git, where the next reader can see it.
//
// It still takes the value it reads rather than an Object, because the allocation engine needs
// this rule over a Claim (claim.go), and this is the last word on whether the operator deletes
// somebody's data -- so it existing twice is not acceptable.
func deletionPolicyOf(policy netboxv1alpha1.DeletionPolicy) netboxv1alpha1.DeletionPolicy {
	if policy != "" {
		return policy
	}

	return netboxv1alpha1.DeletionDelete
}
