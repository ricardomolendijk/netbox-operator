package reconciler

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// The engine's Events go to events.k8s.io/v1, which asks for two things the old API did
// not: a non-empty action, and a note of at most 1024 bytes. Neither is visible to a test
// that only reads back "Type/Reason" -- an Event the API server refuses looks exactly like
// one it accepted -- so these read the whole call.

// TestEventCarriesTheActionForItsReason is the one that would have caught the migration
// landing without an action at all: every Event the engine emits would still have compiled,
// still have shown up in every fake, and been rejected by every real API server.
func TestEventCarriesTheActionForItsReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		emit      func(e *Engine, obj Object)
		eventtype string
		reason    string
		action    string
	}{
		{
			name:      "a write names what was written",
			emit:      func(e *Engine, obj Object) { e.event(obj, netboxv1alpha1.EventCreated, "netbox extras/tags/7") },
			eventtype: "Normal", reason: netboxv1alpha1.EventCreated, action: netboxv1alpha1.ActionCreate,
		},
		{
			name:      "a refusal names the operation that was refused",
			emit:      func(e *Engine, obj Object) { e.warn(obj, netboxv1alpha1.EventConflict, "two matches") },
			eventtype: "Warning", reason: netboxv1alpha1.EventConflict, action: netboxv1alpha1.ActionClaim,
		},
		{
			name:      "every deletion outcome is the same action",
			emit:      func(e *Engine, obj Object) { e.event(obj, netboxv1alpha1.EventRetained, "left in netbox") },
			eventtype: "Normal", reason: netboxv1alpha1.EventRetained, action: netboxv1alpha1.ActionDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := &fakeRecorder{}
			obj := fakeObject()
			tt.emit(&Engine{Events: recorder}, obj)

			if len(recorder.recorded) != 1 {
				t.Fatalf("recorded %d Events, want 1", len(recorder.recorded))
			}

			got := recorder.recorded[0]
			if got.eventtype != tt.eventtype || got.reason != tt.reason {
				t.Errorf("Event = %s/%s, want %s/%s",
					got.eventtype, got.reason, tt.eventtype, tt.reason)
			}
			if got.action != tt.action {
				t.Errorf("action = %q, want %q", got.action, tt.action)
			}
			if got.regarding != Object(obj) {
				t.Errorf("regarding = %v, want the object being reconciled", got.regarding)
			}
			if got.related != nil {
				t.Errorf("related = %v, want nil: this Event is about one object", got.related)
			}
		})
	}
}

// TestEventNoteIsFormattedAndCapped covers the other half the API server checks. A note
// over 1024 bytes takes the whole Event down with it, and the engine's `Updated` note is a
// diff whose length is a function of how wide the kind is.
func TestEventNoteIsFormattedAndCapped(t *testing.T) {
	t.Parallel()

	recorder := &fakeRecorder{}
	engine := &Engine{Events: recorder}

	engine.event(fakeObject(), netboxv1alpha1.EventUpdated,
		"netbox %s/%d: %s", "extras/tags", 7, strings.Repeat("description: a -> b; ", 200))

	got := recorder.recorded[0]
	if len(got.note) > 1024 {
		t.Errorf("note = %d bytes; the API server refuses anything over 1024", len(got.note))
	}
	if !strings.HasPrefix(got.note, "netbox extras/tags/7: ") {
		t.Errorf("note = %q; the format arguments were not applied", got.note)
	}
}

// TestEventNoteWithAPercentSignIsNotAFormatDirective is why emit() formats the note itself
// and hands the recorder a bare "%s". NetBox error bodies and user-chosen names contain
// per-cent signs, and the recorder would otherwise render one as %!s(MISSING).
func TestEventNoteWithAPercentSignIsNotAFormatDirective(t *testing.T) {
	t.Parallel()

	recorder := &fakeRecorder{}
	engine := &Engine{Events: recorder}

	engine.warn(fakeObject(), netboxv1alpha1.EventInvalid, "%s", "utilisation must be 100% or less")

	if got := recorder.recorded[0].note; got != "utilisation must be 100% or less" {
		t.Errorf("note = %q, want the message verbatim", got)
	}
}

// TestChildEventNamesTheChild is not cosmetic. events.k8s.io/v1 aggregates on
// (type, reason, action, regarding, related) and *not* on the note, so without the child in
// `related` a parent that materialises two children in one pass produces one Event carrying
// the first child's note and a count of two -- and the second child is never named anywhere
// a user looks.
func TestChildEventNamesTheChild(t *testing.T) {
	t.Parallel()

	recorder := &fakeRecorder{}
	engine := &Engine{Events: recorder}
	parent := fakeObject()

	first := &unstructured.Unstructured{}
	first.SetName("dns-eth0")
	second := &unstructured.Unstructured{}
	second.SetName("dns-eth1")

	engine.eventAbout(parent, first, netboxv1alpha1.EventChildMaterialised, "created eth0")
	engine.eventAbout(parent, second, netboxv1alpha1.EventChildMaterialised, "created eth1")

	if len(recorder.recorded) != 2 {
		t.Fatalf("recorded %d Events, want 2", len(recorder.recorded))
	}

	for i, want := range []string{"dns-eth0", "dns-eth1"} {
		got := recorder.recorded[i]
		if got.action != netboxv1alpha1.ActionMaterialise {
			t.Errorf("Event %d action = %q, want %q", i, got.action, netboxv1alpha1.ActionMaterialise)
		}

		related, ok := got.related.(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("Event %d related = %T, want the child", i, got.related)
		}
		if related.GetName() != want {
			t.Errorf("Event %d related = %q, want %q", i, related.GetName(), want)
		}
	}
}

// TestEventWithoutARecorderIsDropped: the field is documented as optional, and a test that
// does not care about Events wires nothing.
func TestEventWithoutARecorderIsDropped(t *testing.T) {
	t.Parallel()

	engine := &Engine{}
	engine.event(fakeObject(), netboxv1alpha1.EventCreated, "netbox extras/tags/7")
	engine.warn(fakeObject(), netboxv1alpha1.EventInvalid, "refused")
	engine.eventAbout(fakeObject(), nil, netboxv1alpha1.EventChildPruned, "gone")
	engine.warnAbout(fakeObject(), nil, netboxv1alpha1.EventChildFieldReverted, "taken back")
}
