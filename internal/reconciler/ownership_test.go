package reconciler

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// shape is one field shape the tri-state question has to be answered for, together with
// everything the four cases need in order to ask it.
//
// One table rather than four tests per shape, because the four cases are the same four
// questions every time and a shape that answers three of them is the bug NBO-079 is about.
type shape struct {
	// name is the shape, as the acceptance criteria list it.
	name string

	// spec is the CR spec field, and api the NetBox column it is written as.
	spec, api string

	// set fills the field with a non-empty value, which is the state before the user
	// clears it.
	set func(*fakeSpec)

	// live is the value NetBox already holds, non-empty.
	live any

	// wantEmpty is the value the operator must send to clear the NetBox one. Not `null`:
	// NetBox takes an empty string for a CharField and an empty list for a
	// many-to-many, and a null on a non-nullable column is a 400.
	wantEmpty any
}

// shapes is one field per shape the engine can be handed. The fake kind carries them all,
// with `omitempty` -- which is what makes each of them invisible when empty, and therefore
// what the four cases below are actually testing.
func shapes() []shape {
	return []shape{
		{
			name: "string", spec: "description", api: "description",
			set:  func(s *fakeSpec) { s.Description = "written by hand" },
			live: "written by hand", wantEmpty: "",
		},
		{
			name: "int", spec: "priority", api: "priority",
			set:  func(s *fakeSpec) { s.Priority = 40 },
			live: float64(40), wantEmpty: float64(0),
		},
		{
			name: "bool", spec: "enabled", api: "enabled",
			set:  func(s *fakeSpec) { s.Enabled = true },
			live: true, wantEmpty: false,
		},
		{
			name: "slice", spec: "objectTypes", api: "object_types",
			set:  func(s *fakeSpec) { s.ObjectTypes = []string{"dcim.device"} },
			live: []any{"dcim.device"}, wantEmpty: []any{},
		},
		{
			// Not `custom_fields`: NetBox merges a partial custom-field PATCH and the
			// comparison deliberately only looks at the keys the spec sets
			// (internal/netbox/drift.go, customFieldsEqual), so an empty map there means
			// "manage nothing" rather than "clear everything". `local_context_data` is an
			// ordinary JSON column and is the shape this case is about.
			name: "map", spec: "localContextData", api: "local_context_data",
			set:  func(s *fakeSpec) { s.LocalContext = map[string]string{"role": "spine"} },
			live: map[string]any{"role": "spine"}, wantEmpty: map[string]any{},
		},
	}
}

// TestFieldShapeClearsWhenExplicitlyEmpty is the first acceptance criterion, per shape: a
// field the user set to its empty value reaches NetBox as that empty value.
//
// The field is absent from the marshalled spec in every one of these cases -- `omitempty`
// dropped it -- so the only thing that says the user meant it is the managed-fields entry.
func TestFieldShapeClearsWhenExplicitlyEmpty(t *testing.T) {
	for _, sh := range shapes() {
		t.Run(sh.name, func(t *testing.T) {
			obj := fakeObject()
			ownedBy(obj, "flux", "slug", sh.spec)

			payload := payloadFor(t, obj)

			if got, ok := payload[sh.api]; !ok || !reflect.DeepEqual(got, sh.wantEmpty) {
				t.Fatalf("payload[%q] = %#v, present %v; want the empty %#v",
					sh.api, got, ok, sh.wantEmpty)
			}

			// The half that matters to NetBox: against a live object that holds a value,
			// the empty one is a change and the change is what clears it.
			changes := netbox.Changes(netbox.Object{sh.api: sh.live}, payload, fieldRules(fakeDescriptor()))
			if got, ok := payloadOf(changes)[sh.api]; !ok || !reflect.DeepEqual(got, sh.wantEmpty) {
				t.Errorf("the patch for %q = %#v, present %v; want the empty %#v",
					sh.api, got, ok, sh.wantEmpty)
			}
		})
	}
}

// TestFieldShapeIsUntouchedWhenAbsent is the second acceptance criterion, per shape: a field
// nobody set is left as NetBox has it.
//
// The spec is byte-for-byte the one above. The only difference is that no manager claims the
// field, which is the whole point: intent comes from managedFields and not from the value.
func TestFieldShapeIsUntouchedWhenAbsent(t *testing.T) {
	for _, sh := range shapes() {
		t.Run(sh.name, func(t *testing.T) {
			obj := fakeObject()
			ownedBy(obj, "flux", "slug")

			payload := payloadFor(t, obj)

			if got, ok := payload[sh.api]; ok {
				t.Fatalf("payload[%q] = %#v on a field nobody set; want it left out", sh.api, got)
			}

			// The same assertion where it counts: with NetBox holding a value nobody
			// claimed, the patch the engine would send does not mention the column.
			live := maps.Clone(netbox.Object(payload))
			live[sh.api] = sh.live

			changes := netbox.Changes(live, payload, fieldRules(fakeDescriptor()))
			if _, mentioned := payloadOf(changes)[sh.api]; mentioned {
				t.Errorf("the patch mentions %q; want it left as netbox has it: %v", sh.api, changes)
			}
		})
	}
}

// TestFieldShapeEmptyAndAbsentAreDistinguishable is the third acceptance criterion, per
// shape: the two states are not the same state.
//
// Asserted on both sides of the CRD boundary. Inside, the two produce different payloads and
// different declared-field sets from an identical spec struct, which is the thing that was
// impossible before NBO-079. Outside, the schema keeps the field optional and its
// description says which is which, so `kubectl explain` tells the truth -- checked against
// the generated CRDs in internal/controller/manifests_test.go, which is where they are.
func TestFieldShapeEmptyAndAbsentAreDistinguishable(t *testing.T) {
	for _, sh := range shapes() {
		t.Run(sh.name, func(t *testing.T) {
			empty, absent := fakeObject(), fakeObject()
			ownedBy(empty, "flux", "slug", sh.spec)
			ownedBy(absent, "flux", "slug")

			if !reflect.DeepEqual(empty.Spec, absent.Spec) {
				t.Fatal("the two specs differ; this test would prove nothing about the metadata")
			}

			emptyState, absentState := declaredFor(t, empty), declaredFor(t, absent)

			if !slices.Contains(emptyState, sh.spec) {
				t.Errorf("declared = %v with %s claimed; want it declared", emptyState, sh.spec)
			}

			if slices.Contains(absentState, sh.spec) {
				t.Errorf("declared = %v with %s claimed by nobody; want it undeclared",
					absentState, sh.spec)
			}
		})
	}
}

// TestFieldShapeAdoptionDoesNotWipe is the fourth acceptance criterion, per shape, and the
// one that rules out the obvious wrong fix: dropping `omitempty` would make every field
// always managed, so adopting a NetBox object somebody else made would empty every field the
// user had not restated.
//
// A whole engine pass rather than a payload comparison, because adoption is a decision the
// engine makes and the assertion is on the wire: the PATCH must not mention the field.
func TestFieldShapeAdoptionDoesNotWipe(t *testing.T) {
	for _, sh := range shapes() {
		t.Run(sh.name, func(t *testing.T) {
			obj := fakeObject()
			obj.Spec.OnConflict = "Adopt"
			// Only the natural key is claimed: the user wrote a manifest naming the tag
			// and said nothing at all about this field.
			ownedBy(obj, "flux", "slug", "name", "color", "endpointRef", "onConflict")

			live := netbox.Object{
				"id": float64(7), "slug": "managed", "name": "Managed", "color": "9e9e9e",
				sh.api: sh.live,
			}
			client := &fakeClient{list: []netbox.Object{live}}

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: client, Resync: testResync}, ready: true,
				},
				Status: &fakeStatus{}, Finalizers: &fakeFinalizers{}, Scheme: fakeScheme(t),
			}

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			if !obj.Status.Adopted {
				t.Fatalf("status.adopted = false; the object was not adopted and this proves nothing")
			}

			for _, c := range client.calls {
				if _, mentioned := c.payload[sh.api]; mentioned {
					t.Errorf("%s payload = %v; want %s left as netbox has it",
						c.method, c.payload, sh.api)
				}
			}
		})
	}
}

// TestOwnershipIgnoresTheOperatorsOwnEntries is what makes the field manager load-bearing.
//
// The operator's writes go to `status` and `metadata.finalizers`, so its own entry can never
// name a spec field -- but a rule that trusted every manager would read a future entry of
// its own as a user's intent and manage a field nobody asked for. Excluding it by name is
// also the assertion ADR-0005 §1 turns into: this manager owning anything under `f:spec`
// means the operator wrote a spec.
func TestOwnershipIgnoresTheOperatorsOwnEntries(t *testing.T) {
	obj := fakeObject()
	ownedBy(obj, "flux", "slug")
	ownedBy(obj, FieldManager, "description")
	obj.ManagedFields = append(obj.ManagedFields, metav1.ManagedFieldsEntry{
		Manager: "flux", Operation: metav1.ManagedFieldsOperationUpdate, Subresource: "status",
		FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:color":{}}}`)},
	})

	owned := ownershipOf(obj)

	if owned.fields["description"] {
		t.Error("the operator's own entry was read as a user's intent")
	}

	if owned.fields["color"] {
		t.Error("a status subresource entry was read as spec ownership")
	}

	if !owned.fields["slug"] || !owned.tracked {
		t.Errorf("ownership = %+v, want slug tracked", owned)
	}
}

// TestUntrackedOwnershipFallsBackToNonEmptyFields is the fallback, and it is the case that
// decides whether the operator keeps working for a client that erases field ownership.
//
// Reading managedFields as the only truth would mean an object with none is an object the
// operator manages nothing on -- so it stops writing, quietly, for a whole class of users.
// Instead an object with no ownership metadata behaves exactly as it did before NBO-079:
// every non-empty field is managed, and only the empty ones are invisible.
func TestUntrackedOwnershipFallsBackToNonEmptyFields(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Description = "set in git"

	owned := ownershipOf(obj)
	if owned.tracked {
		t.Fatalf("ownership = %+v on an object with no managedFields, want untracked", owned)
	}

	payload := payloadFor(t, obj)

	if payload["description"] != "set in git" {
		t.Errorf("payload[description] = %v, want the value to be managed anyway", payload["description"])
	}

	// And the ceiling of the fallback, stated as a test so it cannot drift into a surprise:
	// with nothing to read, an empty value is still indistinguishable from an absent one.
	// docs/concepts/field-ownership.md says so, and metrics.SpecOwnershipUntracked counts it.
	obj.Spec.Description = ""
	if _, present := payloadFor(t, obj)["description"]; present {
		t.Error("an empty description reached the payload with no ownership to justify it")
	}
}

// TestEmptyValueOfCoversEveryShapeAndNothingElse guards the reflection the fill depends on.
//
// A shape with no empty form is a field that cannot be cleared, and it would fail silently:
// the value is simply left out and NetBox keeps what it had. A pointer and a struct are the
// two deliberate absences -- a nil pointer is a state of its own, and a reference is an
// object rather than a value.
func TestEmptyValueOfCoversEveryShapeAndNothingElse(t *testing.T) {
	tests := []struct {
		name  string
		typ   reflect.Type
		want  any
		wants bool
	}{
		{name: "string", typ: reflect.TypeFor[string](), want: "", wants: true},
		{name: "int32", typ: reflect.TypeFor[int32](), want: float64(0), wants: true},
		{name: "int64", typ: reflect.TypeFor[int64](), want: float64(0), wants: true},
		{name: "bool", typ: reflect.TypeFor[bool](), want: false, wants: true},
		{name: "slice", typ: reflect.TypeFor[[]string](), want: []any{}, wants: true},
		{name: "map", typ: reflect.TypeFor[map[string]string](), want: map[string]any{}, wants: true},
		{name: "pointer has a nil of its own", typ: reflect.TypeFor[*int32]()},
		{name: "struct is a reference, not a value", typ: reflect.TypeFor[fakeRef]()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := emptyValueOf(tc.typ)

			if ok != tc.wants || (tc.wants && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("emptyValueOf(%s) = %#v, %v; want %#v, %v", tc.typ, got, ok, tc.want, tc.wants)
			}
		})
	}
}

// TestEmptyValuesReachThroughAnInlinedEnvelope is why the reflection descends. Every kind
// inlines NetBoxObjectSpec, so a walk that stopped at the outer struct would give the fields
// of any inlined struct no empty form -- and a field with no empty form cannot be cleared.
func TestEmptyValuesReachThroughAnInlinedEnvelope(t *testing.T) {
	empties := emptyValues(fakeObject())

	for _, name := range []string{"description", "endpointRef", "onConflict"} {
		if _, ok := empties[name]; !ok {
			t.Errorf("emptyValues() has no entry for %q; got %v", name, empties)
		}
	}
}

// ownedBy adds a managed-fields entry claiming fields for manager, the way a server-side
// apply leaves one.
func ownedBy(obj *fakeKind, manager string, fields ...string) {
	claimed := make(map[string]map[string]any, len(fields))
	for _, field := range fields {
		claimed["f:"+field] = map[string]any{}
	}

	raw, err := json.Marshal(map[string]any{"f:spec": claimed})
	if err != nil {
		panic(err)
	}

	obj.ManagedFields = append(obj.ManagedFields, metav1.ManagedFieldsEntry{
		Manager:   manager,
		Operation: metav1.ManagedFieldsOperationApply,
		FieldsV1:  &metav1.FieldsV1{Raw: raw},
	})
}

// payloadFor renders obj the way a reconcile pass does: spec, ownership fill, payload.
func payloadFor(t *testing.T, obj Object) netbox.Object {
	t.Helper()

	payload, _ := renderFor(t, obj)

	return payload
}

// declaredFor renders obj and returns which spec fields the pass considered declared.
func declaredFor(t *testing.T, obj Object) []string {
	t.Helper()

	_, declared := renderFor(t, obj)

	return declared
}

func renderFor(t *testing.T, obj Object) (netbox.Object, []string) {
	t.Helper()

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	spec.restoreEmpty(obj, ownershipOf(obj))

	payload, state, _, err := spec.desired(fakeDescriptor())
	if err != nil {
		t.Fatalf("desired() = %v", err)
	}

	return payload, state.Declared
}

// TestEveryRegisteredKindCanExpressAnEmptyValue closes the hole the reflection could leave.
//
// restoreEmpty finds the spec struct by its JSON name and reads the empty form off each
// field's Go type. Both steps fail quietly: a kind the reflection cannot reach simply has no
// field it can clear, and nothing says so -- the operator reports the object synced while
// NetBox keeps the value. So it is asserted over the registry rather than over the fake kind,
// which is the only version that stays true as the catalogue grows.
func TestEveryRegisteredKindCanExpressAnEmptyValue(t *testing.T) {
	descriptors := registry.List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	for _, d := range descriptors {
		t.Run(d.GVK.Kind, func(t *testing.T) {
			built, err := apiScheme(t).New(d.GVK)
			if err != nil {
				t.Fatalf("%s is registered but not in the scheme: %v", d.GVK, err)
			}

			obj, ok := built.(Object)
			if !ok {
				t.Fatalf("%T does not implement reconciler.Object", built)
			}

			empties := emptyValues(obj)
			if len(empties) == 0 {
				t.Fatalf("no field of %s has an empty form; the spec struct was not reached", d.GVK.Kind)
			}

			// Every clearable field has to be one the payload can carry. An empty value
			// restored for a spec field the descriptor does not map turns "the user
			// mentioned this field" into errUnmappedField, so a missing mapping stops being
			// a latent bug and starts failing the object -- which is the right outcome, and
			// this is where it is caught instead.
			for name := range empties {
				if envelopeFields[name] {
					continue
				}

				if _, mapped := d.FieldFor(name); mapped {
					continue
				}

				if _, generic := d.GenericFKFor(name); !generic {
					t.Errorf("spec field %q has no entry in %s's field map", name, d.GVK.Kind)
				}
			}
		})
	}
}
