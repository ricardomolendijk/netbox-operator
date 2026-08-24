package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// TargetUsable admits only the events on a reference target that could change what one of
// its referrers resolves to.
//
// This is the load-bearing half of the ref watches, and the reason they are not a
// self-inflicted thundering herd. Every object in the cluster is reconciled on its own
// resync, and each of those passes can write a status; admitting every update would fan
// each one out to every referrer of that object, at ~120 kinds, forever. The predicate is
// what makes a target event mean "something a referrer wants happened" rather than
// "somebody's status was written".
//
// Deliberately *not* admitted: `status.lastSyncTime`, `status.lastAppliedHash`,
// `status.observedGeneration`, `status.naturalKey`, `status.deferredPending`, and any
// spec-only edit on the target. None of them changes the id a reference resolves to or
// whether it resolves at all -- a referrer woken by one has nothing new to do and writes
// nothing, which is a reconcile per referrer per resync bought for no information.
func TargetUsable() predicate.Predicate {
	return predicate.Funcs{
		// A target may already carry `status.id` when its watch first sees it -- a manager
		// restart replays every object as a Create -- so a Create is always interesting.
		CreateFunc: func(event.CreateEvent) bool { return true },

		// A referrer must learn that its target went away rather than discovering it on the
		// next resync: it has to re-resolve, report RefNotFound and stop claiming the id.
		DeleteFunc: func(event.DeleteEvent) bool { return true },

		UpdateFunc: func(e event.UpdateEvent) bool { return targetBecameUsable(e.ObjectOld, e.ObjectNew) },

		// A Generic event carries no before-and-after to compare, and nothing in this
		// operator emits one.
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// PoolChanged admits everything TargetUsable admits, plus an edit to the target's own spec.
//
// It is the predicate a claim's *pool* watch uses, and it is wider than TargetUsable for a
// reason TargetUsable's own comment spells out: TargetUsable deliberately drops a spec-only
// edit on the target, because for an ordinary reference such an edit cannot change the id
// being resolved. For a claim's pool it changes something else entirely -- widening a prefix
// is a spec edit that turns an exhausted pool into one with room, and an exhausted claim that
// slept through it would sit at PoolExhausted for up to another ten minutes
// (https://github.com/ricardomolendijk/netbox-operator/issues/178).
//
// The fan-out is affordable here in a way it would not be for the general case: pools are
// few, a generation change means a human edited one, and the claims woken by it are only the
// claims of that pool.
//
// It does **not** cover the other fix for an exhausted pool. Freeing an address inside NetBox
// changes no Kubernetes object at all, so no watch can see it and only the requeue timer
// catches it. The two mechanisms cover different fixes rather than the same one twice.
func PoolChanged() predicate.Predicate {
	return predicate.Or(TargetUsable(), predicate.GenerationChangedPredicate{})
}

// refState is everything about a target that a reference's outcome depends on.
//
// Two fields rather than one because which of them decides a reference is still open
// (#142: a ref may come to require its target to be Ready, rather than merely to have an
// id). Watching the pair means the transition that matters is admitted under either rule,
// and settling #142 changes the resolver rather than this predicate.
type refState struct {
	id    int64
	ready metav1.ConditionStatus
}

// targetBecameUsable reports whether this update changed anything a referrer resolves on.
func targetBecameUsable(before, after client.Object) bool {
	if before == nil || after == nil {
		// controller-runtime always sends both halves. If one is missing the comparison is
		// not possible, and a spurious enqueue is a better failure than a lost one.
		return true
	}

	if before.GetDeletionTimestamp().IsZero() && !after.GetDeletionTimestamp().IsZero() {
		// A target under deletion stops resolving (resolver.byName), so its referrers have
		// to hear about it now: the finalizer may hold the object for a long time, and its
		// Delete event will not arrive until it comes off.
		return true
	}

	was, knownBefore := refStateOf(before)
	is, knownAfter := refStateOf(after)

	if !knownBefore || !knownAfter {
		return true
	}

	return was != is
}

// refStateOf reads the pair off any object kind the engine drives, and reports false for
// one it does not.
//
// Through the engine's own Object interface, so there is no switch on Kind and no per-kind
// accessor: every registered kind exposes the same status struct, which is what makes one
// predicate correct for all ~120 of them.
func refStateOf(obj client.Object) (refState, bool) {
	managed, ok := obj.(reconciler.Object)
	if !ok {
		return refState{}, false
	}

	status := managed.NetBoxStatus()

	return refState{id: status.ID, ready: readyStatus(status.Conditions)}, true
}

// readyStatus is the target's Ready condition status, or the empty string when it has none
// yet. Absent is its own value: a target that has never reported and one reporting
// Ready=False are different states, and the move between them is a transition.
func readyStatus(conditions []metav1.Condition) metav1.ConditionStatus {
	for _, condition := range conditions {
		if condition.Type == netboxv1alpha1.ConditionReady {
			return condition.Status
		}
	}

	return ""
}
