package reconciler

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// A natural-key match carrying this CR's own metadata.uid is the other half of recognising your
// own object, and the half that still works when nothing was recorded anywhere.
//
// Until issue #289 the stamp was only consulted on the spec.allowDuplicate path (duplicate.go,
// claimStamped), so a stamped endpoint was no better off on the ordinary path than an unstamped
// one: a CR whose id never reached the API server -- the process died between the POST and the
// status write, say -- accused itself of an object literally labelled with its own uid.
//
// The cases that must *not* change are here too, because this is one shortcut away from
// becoming "adopt anything that matches".

// stampedLiveTag is a NetBox object as this operator leaves one: tagged, and carrying the
// custom fields the stamp writes, for the CR named by uid.
func stampedLiveTag(id int, uid string) netbox.Object {
	live := liveTag(id)
	live["tags"] = []any{map[string]any{"id": float64(7), "name": "k8s-managed"}}
	live["custom_fields"] = map[string]any{
		"k8s_uid": uid, "k8s_cluster": "prod-eu", "k8s_owner": "netboxfake/team-a/managed",
	}

	return live
}

func TestANaturalKeyMatchCarryingThisObjectsStampIsItsOwn(t *testing.T) {
	tests := []struct {
		name       string
		onConflict netboxv1alpha1.ConflictPolicy

		// match is the object the natural key found. Whose stamp it carries is the whole
		// question.
		match netbox.Object

		// liveReads is how many times the API server's copy of the status had to be read. Zero
		// says the stamp answered on its own, which is what makes it a route back for an object
		// whose id was never written anywhere.
		liveReads int

		wantID      int64
		wantAdopted bool
		wantReady   metav1.ConditionStatus
		wantReason  string
		wantEvents  []string
	}{
		{
			name:       "an object stamped with this object's own uid needs no adoption",
			match:      stampedLiveTag(10, "6f1a-uid"),
			liveReads:  0,
			wantID:     10,
			wantReady:  metav1.ConditionTrue,
			wantReason: netboxv1alpha1.ReasonSynced,
		},
		{
			// The refusal the stamp must not weaken: another CR made this one, and says so.
			name:       "an object stamped by another cr is still refused",
			match:      stampedLiveTag(10, "somebody-elses-uid"),
			liveReads:  1,
			wantReady:  metav1.ConditionFalse,
			wantReason: netboxv1alpha1.ReasonConflict,
			wantEvents: []string{"Warning/Conflict"},
		},
		{
			// An object made before the operator, or by another tool. Nothing about it says it
			// is this CR's, so the answer is the one it has always been.
			name:       "an unstamped object on a stamping endpoint is still refused",
			match:      liveTag(10),
			liveReads:  1,
			wantReady:  metav1.ConditionFalse,
			wantReason: netboxv1alpha1.ReasonConflict,
			wantEvents: []string{"Warning/Conflict"},
		},
		{
			// And taking one over is still an adoption, with the Event and the status field
			// that make it visible -- followed by the PATCH that puts this CR's own stamp on
			// the object it has just taken over.
			name:        "adopting an object stamped by another cr is still an adoption",
			onConflict:  netboxv1alpha1.ConflictAdopt,
			match:       stampedLiveTag(10, "somebody-elses-uid"),
			liveReads:   1,
			wantID:      10,
			wantAdopted: true,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantEvents:  []string{"Normal/Adopted", "Normal/Updated"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := stampedObject()
			obj.Spec.OnConflict = tc.onConflict

			nb := &fakeClient{list: []netbox.Object{tc.match}, patched: tc.match}
			live := &fakeLiveStatus{status: &netboxv1alpha1.NetBoxObjectStatus{}}
			events := &fakeRecorder{}

			engine := stampedEngine(t, stampableDescriptor(), nb)
			engine.LiveStatus, engine.Events = live, events

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			if live.reads != tc.liveReads {
				t.Errorf("live status reads = %d, want %d", live.reads, tc.liveReads)
			}

			if obj.Status.ID != tc.wantID {
				t.Errorf("status.id = %d, want %d", obj.Status.ID, tc.wantID)
			}

			if obj.Status.Adopted != tc.wantAdopted {
				t.Errorf("status.adopted = %v, want %v", obj.Status.Adopted, tc.wantAdopted)
			}

			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if ready.Status != tc.wantReady || ready.Reason != tc.wantReason {
				t.Errorf("Ready = %s/%s, want %s/%s",
					ready.Status, ready.Reason, tc.wantReady, tc.wantReason)
			}

			if !slices.Equal(events.events, tc.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, tc.wantEvents)
			}
		})
	}
}
