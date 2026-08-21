package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	protectedRetryBase = 10 * time.Second

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
	protectedEventAfter = 3
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
	if controllerutil.ContainsFinalizer(obj, netboxv1alpha1.Finalizer) {
		return nil
	}

	controllerutil.AddFinalizer(obj, netboxv1alpha1.Finalizer)

	if err := e.Finalizers.UpdateFinalizers(ctx, obj); err != nil {
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

	endpoint, ok := p.engine.Endpoints.Endpoint(p.obj.GetNamespace(), p.obj.NetBoxSpec().EndpointRef)
	if !ok {
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

	return p.deleteObject(ctx)
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

	if deletionPolicyOf(p.obj) == netboxv1alpha1.DeletionRetain {
		return release{
			event: netboxv1alpha1.EventRetained,
			message: fmt.Sprintf("spec.deletionPolicy is Retain: netbox %s/%d is left in place",
				p.desc.Endpoint, id),
		}, true
	}

	// status.id is the operator's claim on an object. Without one there is nothing it can
	// prove it owns, and it will not go looking: a natural-key lookup at deletion time
	// would find whatever happens to match right now, which is how an operator deletes
	// somebody else's data. An object adopted under onConflict: Adopt has an id and is
	// deleted; one this CR never wrote does not and is not.
	//
	// Two things produce an unset id -- nothing was ever created, or a create succeeded
	// and the status write recording its id did not -- and the operator genuinely cannot
	// tell them apart, because the id and the hash that would have distinguished them are
	// written together in the update that failed. So it names both rather than picking the
	// flattering one. This is the only place the operator can leave an orphan it will
	// never find again.
	if id == 0 {
		return release{
			event: netboxv1alpha1.EventNothingToDelete,
			message: "no netbox object is recorded in status.id, so nothing was deleted; " +
				"either nothing was ever created, or a create succeeded and the status write " +
				"recording its id did not" + lookedFor(p.obj.NetBoxStatus().NaturalKey, p.desc.Endpoint),
		}, true
	}

	return release{}, false
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

	wait := protectedBackoff(status.DeletionAttempts)

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

// deletionPolicyOf returns the object's deletion policy, defaulting to the one that leaves
// nothing behind. The CRD defaults it as well; this is the guard for an object stored
// before that default existed.
func deletionPolicyOf(obj Object) netboxv1alpha1.DeletionPolicy {
	if policy := obj.NetBoxSpec().DeletionPolicy; policy != "" {
		return policy
	}

	return netboxv1alpha1.DeletionDelete
}
