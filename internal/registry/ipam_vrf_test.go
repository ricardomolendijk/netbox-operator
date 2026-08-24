package registry

import (
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func registeredVRF(t *testing.T) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxVRF")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in ipam_vrf.go did not run", gvk)
	}

	return d
}

// TestVRFDescriptorIsRegisteredAndValid is the boot check for ipam.VRF.
func TestVRFDescriptorIsRegisteredAndValid(t *testing.T) {
	d := registeredVRF(t)

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "ipam/vrfs" {
		t.Errorf("Endpoint = %q, want ipam/vrfs (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "ipam.vrf" {
		t.Errorf("ObjectType = %q, want ipam.vrf", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	// A VRF accumulates prefixes and addresses that key on it, so delete-then-create to
	// change a description would reparent all of them -- and NetBox's PROTECT would refuse
	// it anyway, which is the loud version of the same mistake.
	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}
}

// TestVRFToManyFieldsAreDeclaredOnce is this ticket's central registry claim: the two
// many-to-many relations are one declaration each, and the comparison set is derived from
// them rather than listed beside them (NBO-088).
func TestVRFToManyFieldsAreDeclaredOnce(t *testing.T) {
	d := registeredVRF(t)

	want := []string{"import_targets", "export_targets"}
	if got := d.M2MFields(); !reflect.DeepEqual(got, want) {
		t.Errorf("M2MFields() = %v, want %v", got, want)
	}

	target := netboxv1alpha1.RouteTargetRef{}.TargetGVK()

	for _, spec := range []string{"importTargets", "exportTargets"} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Fatalf("FieldFor(%q) found nothing", spec)
		}

		if field.Class != ClassRefMany {
			t.Errorf("%s class = %q, want %q", spec, field.Class, ClassRefMany)
		}

		if field.Target != target {
			t.Errorf("%s target = %s, want %s", spec, field.Target, target)
		}
	}

	// Neither is an array or an object-type list. All three arrive as JSON lists and are
	// compared by three different rules, so a class landing in the wrong bucket is a hot
	// loop rather than a type error.
	if got := d.ArrayFields(); len(got) != 0 {
		t.Errorf("ArrayFields() = %v, want none", got)
	}

	if got := d.ObjectTypeListFields(); len(got) != 0 {
		t.Errorf("ObjectTypeListFields() = %v, want none", got)
	}
}

// TestVRFNaturalKeysFallFromRDToName pins both candidates and, more importantly, when each
// one applies.
//
// The second candidate pins `rd` to null rather than merely leaving it out. Without the pin a
// VRF that declares an `rd` whose NetBox object does not exist yet would fall through to a
// name-only lookup, adopt an unrelated VRF of the same name, and PATCH its own `rd` onto it
// -- reparenting every prefix and address keyed on that VRF.
func TestVRFNaturalKeysFallFromRDToName(t *testing.T) {
	d := registeredVRF(t)

	wantKeys := []NaturalKey{
		{Fields: []KeyField{{Filter: "rd", Spec: "rd"}}},
		{
			Fields:     []KeyField{{Filter: "name", Spec: "name"}},
			NullFields: []NullField{{Filter: "rd", Spec: "rd", Column: NullColumnChar}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, wantKeys) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, wantKeys)
	}

	tests := map[string]struct {
		state SpecState
		want  []NaturalKey
	}{
		"rd set is looked up by rd alone": {
			state: SpecState{
				Declared: []string{"name", "rd"}, Resolved: []string{"name", "rd"},
			},
			want: wantKeys[:1],
		},
		"rd absent is looked up by name and a null rd": {
			state: SpecState{Declared: []string{"name"}, Resolved: []string{"name"}},
			want:  wantKeys[1:],
		},
		// `rd: ""` is a user asking for the route distinguisher to be cleared, and it leaves
		// this object with no identity at all: the first candidate has no value to filter on
		// and the second asserts the field was never declared. The engine waits, which is the
		// safe answer -- the alternative is adopting a VRF chosen by name alone.
		"an explicitly emptied rd is no identity": {
			state: SpecState{Declared: []string{"name", "rd"}, Resolved: []string{"name"}},
			want:  nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != len(tc.want) {
				t.Fatalf("Candidates() = %+v, want %+v", got, tc.want)
			}

			for i := range got {
				if !reflect.DeepEqual(got[i], tc.want[i]) {
					t.Errorf("Candidates()[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRouteTargetDescriptorHasNoToManyField is the relation-direction assertion, and it is
// the one thing about ipam.RouteTarget worth a test.
//
// `import_targets` and `export_targets` are declared on ipam.VRF, so a NetBoxRouteTarget has
// no many-to-many field of its own and every write to the relation goes through the VRF. A
// reverse field here would make two objects writers of one relation, which is a PATCH war.
func TestRouteTargetDescriptorHasNoToManyField(t *testing.T) {
	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxRouteTarget")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in ipam_routetarget.go did not run", gvk)
	}

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "ipam/route-targets" {
		t.Errorf("Endpoint = %q, want ipam/route-targets", d.Endpoint)
	}

	if d.ObjectType != "ipam.routetarget" {
		t.Errorf("ObjectType = %q, want ipam.routetarget", d.ObjectType)
	}

	if got := d.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none: the relation is declared on ipam.VRF", got)
	}

	for _, field := range d.Fields {
		if field.Class.ToMany() {
			t.Errorf("field %q is to-many; ipam.RouteTarget declares no ManyToManyField", field.Spec)
		}
	}

	// `name` is column-unique on ipam.RouteTarget and the model declares no meta.constraints,
	// so one candidate identifies at most one route target.
	wantKeys := []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, wantKeys) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, wantKeys)
	}
}
