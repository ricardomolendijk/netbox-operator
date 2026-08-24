package reconciler

import (
	"context"
	"maps"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestNoRegisteredKindWritesOutsideStatus is the never-write-spec invariant, checked over
// the whole registry rather than over one kind
// (docs/decisions/0005-gitops-coexistence.md §1, NBO-065).
//
// The invariant is already structural -- the engine holds a StatusWriter and a
// FinalizerWriter and no client, so there is nothing it could call to write a spec -- and
// this is what makes it *checked*. It runs over registry.List() rather than a fixed list of
// kinds so that the ~120 still to land are covered by having been registered, which is the
// only version of this test that stays true: NBO-065 exists now, on two kinds, because
// retrofitting it across a full catalogue is not a thing anybody does.
//
// What "non-status write" means here is every route the engine has to the API server that
// is not StatusWriter: a finalizer write, and metadata.generation moving. A generation bump
// is the signature of a spec write, because the API server only ever increments it for a
// change outside metadata and status -- which is also the assertion the envtest half of
// this makes against a real API server (internal/controller/gitops_test.go).
func TestNoRegisteredKindWritesOutsideStatus(t *testing.T) {
	descriptors := registry.List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	for _, descriptor := range descriptors {
		t.Run(descriptor.GVK.Kind, func(t *testing.T) {
			obj, live := unchangedObject(t, descriptor)
			client := &fakeClient{get: live}
			status, finalizers := &fakeStatus{}, &fakeFinalizers{}
			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: client, Resync: testResync},
					ready:    true,
				},
				Status:     status,
				Finalizers: finalizers,
				Scheme:     apiScheme(t),
			}

			generation := obj.GetGeneration()

			// Twice: the first pass settles the conditions and the second is the resync
			// that a steady-state cluster spends all of its time in. Both must leave the
			// spec alone, and the second must write nothing at all.
			for pass := range 2 {
				if _, err := engine.Reconcile(context.Background(), obj); err != nil {
					t.Fatalf("Reconcile() pass %d = %v", pass, err)
				}
			}

			for _, method := range client.methods() {
				if method != "GET" {
					t.Errorf("netbox saw %v on an unchanged object, want reads only", client.methods())

					break
				}
			}

			if len(finalizers.writes) != 0 {
				t.Errorf("finalizer writes = %v on an object that already carries it", finalizers.writes)
			}

			if status.writes != 1 {
				t.Errorf("status writes over two passes = %d, want the one that settled the conditions",
					status.writes)
			}

			if obj.GetGeneration() != generation {
				t.Errorf("metadata.generation = %d, want the unchanged %d; a bump is a spec write",
					obj.GetGeneration(), generation)
			}
		})
	}
}

// unchangedObject builds a CR of a registered kind together with the NetBox object that
// already matches it exactly.
//
// The live object is the payload the engine itself renders from the spec, so "matches
// exactly" is not this test's opinion about a kind's field map -- it is the field map. A
// descriptor whose comparison rules disagree with its own payload shows up here as a write,
// which is the second thing this test catches.
//
// status.id is pre-set so the engine locates by id and never has to build a natural key.
// That keeps the fixture free of per-kind knowledge: a kind whose key needs a parent
// reference would otherwise be unreconcilable here for reasons that have nothing to do with
// the invariant.
func unchangedObject(t *testing.T, d registry.Descriptor) (Object, netbox.Object) {
	t.Helper()

	built, err := apiScheme(t).New(d.GVK)
	if err != nil {
		t.Fatalf("%s is registered but not in the scheme: %v", d.GVK, err)
	}

	obj, ok := built.(Object)
	if !ok {
		t.Fatalf("%T does not implement reconciler.Object; every registered kind must embed the envelope", built)
	}

	obj.SetNamespace("gitops")
	obj.SetName(strings.ToLower(d.GVK.Kind))
	obj.SetGeneration(4)
	// Already claimed, so the pass has no finalizer to add: adding one is a legitimate
	// non-status write, and it would drown the writes this test is looking for.
	obj.SetFinalizers([]string{netboxv1alpha1.Finalizer})
	obj.NetBoxSpec().EndpointRef = "homelab"
	obj.NetBoxStatus().ID = 11

	fillSpec(t, obj, d)

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("reading the spec of %s: %v", d.GVK.Kind, err)
	}

	desired, _, _, err := spec.desired(d)
	if err != nil {
		t.Fatalf("rendering the payload for %s: %v", d.GVK.Kind, err)
	}

	live := maps.Clone(desired)
	live["id"] = float64(11)
	live["url"] = "https://netbox.invalid/api/" + d.Endpoint + "/11/"

	return obj, live
}

// apiScheme is the real scheme, since this test is about real kinds.
func apiScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}

	return scheme
}

// fillSpec sets every plainly-valued field the descriptor maps, so the payload under test
// is the kind's real one rather than an empty map.
//
// References are skipped because the engine cannot render one yet (NBO-012) and would leave
// it out of the payload anyway, and so is any shape plausible() will not invent a value
// for. Skipping is safe in a way that guessing is not: an unset field is simply not
// exercised, whereas a made-up nested object is a comparison this test would be asserting
// about itself.
func fillSpec(t *testing.T, obj Object, d registry.Descriptor) {
	t.Helper()

	spec := reflect.ValueOf(obj).Elem().FieldByName("Spec")
	if !spec.IsValid() || spec.Kind() != reflect.Struct {
		t.Fatalf("%T has no Spec struct", obj)
	}

	for _, field := range d.Fields {
		if field.Class.Ref() {
			continue
		}

		target, found := specField(spec, field.Spec)
		if !found {
			// The spec field named in the field map does not exist on the Go type. The
			// engine would never write it, and NetBox ignores a column it does not know
			// rather than rejecting it, so this is exactly the silent omission the
			// explicit field map exists to prevent (internal/registry/fields.go).
			t.Errorf("descriptor maps spec field %q, which %T does not have", field.Spec, obj)

			continue
		}

		if value, ok := plausible(target.Type()); ok {
			target.Set(value)
		}
	}
}

// specField finds the struct field carrying a JSON name, descending into embedded structs
// the way encoding/json does -- which is how the shared envelope's fields are reached.
func specField(spec reflect.Value, name string) (reflect.Value, bool) {
	for i := range spec.NumField() {
		field := spec.Type().Field(i)

		if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag == name {
			return spec.Field(i), true
		}

		if field.Anonymous && spec.Field(i).Kind() == reflect.Struct {
			if inner, ok := specField(spec.Field(i), name); ok {
				return inner, true
			}
		}
	}

	return reflect.Value{}, false
}

// plausible returns a non-zero value of type t, or false for a shape it will not invent
// one for. Values are arbitrary: the live object is built from the rendered payload, so
// what matters is that a value round-trips through JSON and compares equal to itself.
func plausible(t reflect.Type) (reflect.Value, bool) {
	value := reflect.New(t).Elem()

	switch t.Kind() {
	case reflect.String:
		value.SetString("gitops")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Pointer:
		inner, ok := plausible(t.Elem())
		if !ok {
			return reflect.Value{}, false
		}

		value.Set(reflect.New(t.Elem()))
		value.Elem().Set(inner)
	case reflect.Slice:
		inner, ok := plausible(t.Elem())
		if !ok {
			return reflect.Value{}, false
		}

		value.Set(reflect.Append(value, inner))
	default:
		return reflect.Value{}, false
	}

	return value, true
}

// TestEveryRegisteredKindEmbedsTheEnvelope is the compile-time-shaped half of the
// invariant: the engine writes status through NetBoxStatus(), so a kind that does not offer
// one cannot be reconciled at all -- and a kind that offered a whole-object writer instead
// is how a spec write would get in.
func TestEveryRegisteredKindEmbedsTheEnvelope(t *testing.T) {
	scheme := apiScheme(t)

	for _, descriptor := range registry.List() {
		built, err := scheme.New(descriptor.GVK)
		if err != nil {
			t.Fatalf("%s is registered but not in the scheme: %v", descriptor.GVK, err)
		}

		obj, ok := built.(Object)
		if !ok {
			t.Errorf("%T does not implement reconciler.Object", built)

			continue
		}

		if obj.NetBoxSpec() == nil || obj.NetBoxStatus() == nil {
			t.Errorf("%T returns a nil envelope", built)
		}
	}
}

// TestStatusWriterIsTheOnlyObjectWriter states the shape of the engine that makes the
// invariant structural, so that widening it is a deliberate edit to this list rather than a
// field somebody adds to Engine.
//
// It is a reflection test rather than prose in a comment because prose does not fail CI.
func TestStatusWriterIsTheOnlyObjectWriter(t *testing.T) {
	// Every collaborator the engine may hold, and what it is allowed to reach. Anything
	// else -- a client.Client, a client.Writer, a whole manager -- can write a spec.
	allowed := map[string]bool{
		"Descriptors": true,
		"Endpoints":   true,
		"Refs":        true,
		"Status":      true,
		"Finalizers":  true,
		"Events":      true,
		"Scheme":      true,
	}

	engine := reflect.TypeFor[Engine]()
	for i := range engine.NumField() {
		if name := engine.Field(i).Name; !allowed[name] {
			t.Errorf("Engine.%s is new; if it can write a CR it breaks the never-write-spec "+
				"invariant (docs/decisions/0005-gitops-coexistence.md)", name)
		}
	}

	// The status writer's one method takes a client.Object and returns an error, so there
	// is no route through it to anything but the status subresource.
	writer := reflect.TypeFor[StatusWriter]()
	if writer.NumMethod() != 1 || writer.Method(0).Name != "UpdateStatus" {
		t.Errorf("StatusWriter has %d methods, want only UpdateStatus", writer.NumMethod())
	}

	// The resolver is the one collaborator handed whole CRs, so it is the one that has to be
	// narrow: reading a reference target must not come with a route to writing one. One
	// method, and it returns a Resolution rather than accepting a writer.
	refs := reflect.TypeFor[RefResolver]()
	if refs.NumMethod() != 1 || refs.Method(0).Name != "ResolveAll" {
		t.Errorf("RefResolver has %d methods, want only ResolveAll", refs.NumMethod())
	}
}

// TestConditionsCarryObservedGeneration guards the other half of the generation story: the
// engine stamps every condition with the generation it observed, and a status whose
// observedGeneration lags is indistinguishable from one the operator never processed.
func TestConditionsCarryObservedGeneration(t *testing.T) {
	descriptor := fakeDescriptor()
	obj := fakeObject()
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: &fakeClient{created: liveTag(7)}, Resync: testResync},
			ready:    true,
		},
		Status:     &fakeStatus{},
		Finalizers: &fakeFinalizers{},
		Scheme:     fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.ObservedGeneration != obj.Generation {
		t.Errorf("observedGeneration = %d, generation = %d",
			obj.Status.ObservedGeneration, obj.Generation)
	}

	for _, condition := range obj.Status.Conditions {
		if condition.ObservedGeneration != obj.Generation {
			t.Errorf("condition %s observed generation %d, want %d",
				condition.Type, condition.ObservedGeneration, obj.Generation)
		}

		if condition.Status != metav1.ConditionTrue && condition.Status != metav1.ConditionFalse {
			t.Errorf("condition %s = %q, want True or False", condition.Type, condition.Status)
		}
	}
}
