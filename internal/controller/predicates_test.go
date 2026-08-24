package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestTargetUsable is the cost assertion of the whole ticket, at the unit level: every row
// that wants `false` is a fan-out to every referrer of that object which this predicate
// refuses to pay for.
func TestTargetUsable(t *testing.T) {
	tests := []struct {
		name   string
		before *netboxv1alpha1.NetBoxRegion
		after  *netboxv1alpha1.NetBoxRegion
		want   bool
	}{
		{
			// The case the whole ticket exists for: the target has just been written to
			// NetBox, so a referrer waiting on RefNotReady can now resolve.
			name:   "status.id appears",
			before: region(0, ""),
			after:  region(12, metav1.ConditionTrue),
			want:   true,
		},
		{
			// A recreate (dcim.Cable's strategy) gives the same object a new id, and a
			// referrer holding the old one has to write the new one.
			name:   "status.id changes after a recreate",
			before: region(12, metav1.ConditionTrue),
			after:  region(19, metav1.ConditionTrue),
			want:   true,
		},
		{
			// #142 is open on whether a reference requires its target to be Ready or merely
			// to have an id. Admitting this transition means the watch is correct under
			// either answer.
			name:   "Ready flips False to True",
			before: region(12, metav1.ConditionFalse),
			after:  region(12, metav1.ConditionTrue),
			want:   true,
		},
		{
			name:   "Ready flips True to False",
			before: region(12, metav1.ConditionTrue),
			after:  region(12, metav1.ConditionFalse),
			want:   true,
		},
		{
			// "Has not reported yet" and "reported False" are different states, and the move
			// between them is what a referrer quotes in its own message.
			name:   "Ready appears for the first time",
			before: region(12, ""),
			after:  region(12, metav1.ConditionFalse),
			want:   true,
		},
		{
			// A finalizer can hold a terminating object for a long time, and the resolver
			// already refuses to resolve through one, so the Delete event is too late.
			name:   "the target starts deleting",
			before: region(12, metav1.ConditionTrue),
			after:  deleting(region(12, metav1.ConditionTrue)),
			want:   true,
		},
		{
			// The storm this predicate exists to prevent: every object in the cluster writes
			// this on every pass that touches NetBox.
			name:   "only lastSyncTime moved",
			before: region(12, metav1.ConditionTrue),
			after:  synced(region(12, metav1.ConditionTrue)),
			want:   false,
		},
		{
			name:   "only lastAppliedHash moved",
			before: region(12, metav1.ConditionTrue),
			after:  hashed(region(12, metav1.ConditionTrue)),
			want:   false,
		},
		{
			name:   "only observedGeneration moved",
			before: region(12, metav1.ConditionTrue),
			after:  observed(region(12, metav1.ConditionTrue)),
			want:   false,
		},
		{
			// A referrer resolves off the target's id, not off its description. A spec edit
			// on the target changes nothing for it.
			name:   "only the target's own spec changed",
			before: region(12, metav1.ConditionTrue),
			after:  described(region(12, metav1.ConditionTrue)),
			want:   false,
		},
		{
			// The resync's steady state, which is what most events in a healthy cluster are.
			name:   "nothing changed",
			before: region(12, metav1.ConditionTrue),
			after:  region(12, metav1.ConditionTrue),
			want:   false,
		},
		{
			// A condition other than Ready: RefsResolved and Synced flap on the target
			// without changing what a referrer gets.
			name:   "another condition changed",
			before: region(12, metav1.ConditionTrue),
			after:  withSynced(region(12, metav1.ConditionTrue), metav1.ConditionFalse),
			want:   false,
		},
	}

	predicate := TargetUsable()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := predicate.Update(event.UpdateEvent{ObjectOld: tc.before, ObjectNew: tc.after})
			if got != tc.want {
				t.Errorf("Update = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTargetUsableNonUpdateEvents covers the three events that are decided without a
// comparison.
func TestTargetUsableNonUpdateEvents(t *testing.T) {
	predicate := TargetUsable()

	if !predicate.Create(event.CreateEvent{Object: region(12, metav1.ConditionTrue)}) {
		t.Error("a Create was rejected; a manager restart replays every object as one, " +
			"and the target may already carry an id")
	}
	if !predicate.Delete(event.DeleteEvent{Object: region(12, metav1.ConditionTrue)}) {
		t.Error("a Delete was rejected; a referrer has to learn its target went away")
	}
	if predicate.Generic(event.GenericEvent{Object: region(12, metav1.ConditionTrue)}) {
		t.Error("a Generic event was admitted; it carries no before-and-after to compare")
	}
}

// TestTargetUsableAdmitsAnUncomparableObject covers an object that is not one of the engine's
// kinds. Nothing registers such a watch, and if one ever does, a spurious enqueue is a far
// better failure than a lost one.
func TestTargetUsableAdmitsAnUncomparableObject(t *testing.T) {
	before := &netboxv1alpha1.NetBoxRefGrant{}
	after := &netboxv1alpha1.NetBoxRefGrant{}

	if !TargetUsable().Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Error("an object with no NetBox status was rejected; the comparison cannot be made, " +
			"so the event has to be admitted")
	}
}

// region is a target carrying an id and a Ready condition, which is the pair a reference's
// outcome depends on.
func region(id int64, ready metav1.ConditionStatus) *netboxv1alpha1.NetBoxRegion {
	target := &netboxv1alpha1.NetBoxRegion{
		Spec:   netboxv1alpha1.NetBoxRegionSpec{Name: "EMEA", Slug: "emea"},
		Status: netboxv1alpha1.NetBoxObjectStatus{ID: id},
	}
	if ready != "" {
		target.Status.Conditions = []metav1.Condition{{
			Type: netboxv1alpha1.ConditionReady, Status: ready,
			Reason: netboxv1alpha1.ReasonSynced, LastTransitionTime: metav1.Now(),
		}}
	}

	return target
}

func deleting(target *netboxv1alpha1.NetBoxRegion) *netboxv1alpha1.NetBoxRegion {
	now := metav1.NewTime(time.Now())
	target.DeletionTimestamp = &now
	target.Finalizers = []string{netboxv1alpha1.Finalizer}

	return target
}

func synced(target *netboxv1alpha1.NetBoxRegion) *netboxv1alpha1.NetBoxRegion {
	now := metav1.NewTime(time.Now())
	target.Status.LastSyncTime = &now

	return target
}

func hashed(target *netboxv1alpha1.NetBoxRegion) *netboxv1alpha1.NetBoxRegion {
	target.Status.LastAppliedHash = "sha256:9c1185a5c5e9fc54612808977ee8f548b2258d31"

	return target
}

func observed(target *netboxv1alpha1.NetBoxRegion) *netboxv1alpha1.NetBoxRegion {
	target.Status.ObservedGeneration = 4

	return target
}

func described(target *netboxv1alpha1.NetBoxRegion) *netboxv1alpha1.NetBoxRegion {
	target.Spec.Description = "the whole of europe"
	target.Generation = 2

	return target
}

func withSynced(target *netboxv1alpha1.NetBoxRegion, status metav1.ConditionStatus) *netboxv1alpha1.NetBoxRegion {
	target.Status.Conditions = append(target.Status.Conditions, metav1.Condition{
		Type: netboxv1alpha1.ConditionSynced, Status: status,
		Reason: netboxv1alpha1.ReasonNoDrift, LastTransitionTime: metav1.Now(),
	})

	return target
}
