package registry

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestTagDescriptorIsRegisteredAndValid is the boot check for the first real kind: the
// descriptor its init() registered is the one the engine will find for a NetBoxTag, and it
// survives the same validation the manager runs before any controller starts.
func TestTagDescriptorIsRegisteredAndValid(t *testing.T) {
	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxTag")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in extras_tag.go did not run", gvk)
	}

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	// Each of these is silently wrong in its own way. A pluralised endpoint 404s; a
	// capitalised object type is what other kinds will point at through a generic FK; and
	// UpdateRecreate would delete and re-create a tag to change its colour.
	if d.Endpoint != "extras/tags" {
		t.Errorf("Endpoint = %q, want extras/tags (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "extras.tag" {
		t.Errorf("ObjectType = %q, want extras.tag", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// The whole natural key, matched by value, with nothing pinned to null: `slug` is
	// column-unique on django-taggit's TagBase, so one candidate identifies at most one
	// tag. A key on `name` instead would still be unique but would not be the field the
	// spec calls the tag's identifier.
	wantKeys := []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, wantKeys) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, wantKeys)
	}

	// No candidate depends on a reference, so identity is establishable from the spec
	// alone. That is what lets this kind reach Ready before internal/resolver exists.
	if got := d.Candidates(SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}}); len(got) != 1 {
		t.Errorf("Candidates() with slug resolved = %d candidates, want 1", len(got))
	}
}

// TestTagFieldMapRoundTrips is the check the explicit field map exists for.
//
// Every spec field must map to a NetBox field and every mapping must have a spec field
// behind it. A *missing* entry fails loudly -- the engine refuses the object with
// Reason=Invalid rather than dropping the field -- but a *wrong* entry does not: NetBox
// ignores a column name it does not recognise instead of rejecting it, so the write
// reports success and changes nothing, forever.
func TestTagFieldMapRoundTrips(t *testing.T) {
	d := extrasTagDescriptor()
	spec := specJSONNames(reflect.TypeFor[netboxv1alpha1.NetBoxTagSpec]())
	envelope := specJSONNames(reflect.TypeFor[netboxv1alpha1.NetBoxObjectSpec]())

	for _, name := range spec {
		if _, mapped := d.FieldFor(name); !mapped {
			t.Errorf("NetBoxTagSpec declares %q and the field map does not", name)
		}
	}

	for _, field := range d.Fields {
		if !slices.Contains(spec, field.Spec) {
			t.Errorf("the field map declares %q, which NetBoxTagSpec does not have", field.Spec)
		}

		if slices.Contains(envelope, field.Spec) {
			t.Errorf("the field map declares the envelope field %q; the engine must never send it", field.Spec)
		}

		// extras.Tag has no foreign keys at all, which is exactly why it is the kind the
		// engine is proved against before internal/resolver lands (NBO-012).
		if field.Ref {
			t.Errorf("field %q is marked as a reference; extras.Tag has none", field.Spec)
		}
	}

	// Spelled out, because it is the pair a camelCase-to-snake_case convention would get
	// right by luck and `wirelessLANs` or `primaryIP4Ref` would not.
	if field, _ := d.FieldFor("objectTypes"); field.API != "object_types" {
		t.Errorf("objectTypes maps to %q, want object_types", field.API)
	}
}

// TestTagObjectTypesIsNotAnM2M pins the field class, not just the field.
//
// Both classes are many-to-many and both compare order-independently, so declaring the
// wrong one produces correct drift detection and a resolver that goes looking for a CR
// named `dcim.device` -- a CR that cannot exist, because a Django ContentType is not a
// NetBox object this operator manages.
func TestTagObjectTypesIsNotAnM2M(t *testing.T) {
	d := extrasTagDescriptor()

	if !slices.Contains(d.ObjectTypeLists, "object_types") {
		t.Errorf("ObjectTypeLists = %v, want it to contain object_types", d.ObjectTypeLists)
	}

	if slices.Contains(d.M2M, "object_types") {
		t.Error("object_types is declared as an M2M; its values are content-type strings, not ids")
	}
}

// TestTagObjectTypesDiffOrderIndependently proves the field class picks the comparison.
//
// internal/netbox is imported here and nowhere else in this package: registry holds
// per-kind facts, netbox builds requests, and neither imports the other. The claim under
// test spans both -- a descriptor's field class is only meaningful through the comparison
// it selects -- and a test is where the two should be made to agree.
func TestTagObjectTypesDiffOrderIndependently(t *testing.T) {
	rules := netbox.FieldRules{ObjectTypeLists: map[string]bool{}}
	for _, field := range extrasTagDescriptor().ObjectTypeLists {
		rules.ObjectTypeLists[field] = true
	}

	cases := []struct {
		name       string
		live, want any
		drift      bool
	}{
		{
			name:  "reordered is not drift",
			live:  []any{"dcim.device", "virtualization.virtualmachine"},
			want:  []any{"virtualization.virtualmachine", "dcim.device"},
			drift: false,
		},
		{
			// NetBox returns the list nested on some endpoints, so both read shapes have
			// to reduce to the same set or the operator PATCHes forever.
			name:  "nested read shape is not drift",
			live:  []any{map[string]any{"app_label": "dcim", "model": "device"}},
			want:  []any{"dcim.device"},
			drift: false,
		},
		{
			name:  "an added type is drift",
			live:  []any{"dcim.device"},
			want:  []any{"dcim.device", "ipam.prefix"},
			drift: true,
		},
		{
			name:  "a removed type is drift",
			live:  []any{"dcim.device", "ipam.prefix"},
			want:  []any{"dcim.device"},
			drift: true,
		},
		{
			name:  "a swapped type is drift",
			live:  []any{"dcim.device"},
			want:  []any{"ipam.prefix"},
			drift: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes := netbox.Changes(
				netbox.Object{"object_types": tc.live},
				netbox.Object{"object_types": tc.want},
				rules)

			if got := len(changes) > 0; got != tc.drift {
				t.Errorf("Changes() reported drift = %v, want %v (changes: %+v)", got, tc.drift, changes)
			}
		})
	}
}

// specJSONNames lists a spec struct's JSON field names, which are the names the engine
// reads a spec by. An inline embedded struct carries no name of its own and is skipped.
func specJSONNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())

	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}

	return names
}
