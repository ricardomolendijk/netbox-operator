package registry

import (
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestSiteDescriptorIsRegisteredAndValid is the boot check for the second kind. It is worth
// having twice: the first kind proved the engine accepts a descriptor, and this one proves
// the descriptor is per-kind data rather than a shape that happened to fit one example.
func TestSiteDescriptorIsRegisteredAndValid(t *testing.T) {
	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxSite")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in dcim_site.go did not run", gvk)
	}

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "dcim/sites" {
		t.Errorf("Endpoint = %q, want dcim/sites (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "dcim.site" {
		t.Errorf("ObjectType = %q, want dcim.site", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	// Patch, not Recreate. A site accumulates devices, racks and prefixes that point at it,
	// so delete-then-create to change a description would be a catastrophic way to spell
	// PATCH -- and NetBox's PROTECT would refuse it anyway, which is the loud version of
	// the same mistake.
	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// `slug` is column-unique on dcim.Site, so one candidate identifies at most one site.
	// `name` is column-unique too and deliberately is not a candidate: a kind gets one
	// identity.
	wantKeys := []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, wantKeys) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, wantKeys)
	}

	// Identity is establishable from the spec alone, which is what lets this kind reach
	// Ready before internal/resolver exists.
	if got := d.Candidates(SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}}); len(got) != 1 {
		t.Errorf("Candidates() returned %d candidates, want 1", len(got))
	}
}

// TestSiteFieldMapCoversEverySpecField guards the defect the field table exists to prevent:
// NetBox ignores a field name it does not recognise rather than rejecting it, so a missing
// or misspelled entry produces a write that reports success and changes nothing, forever.
func TestSiteFieldMapCoversEverySpecField(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxSite"))

	// The two entries that earn the table rather than a camelCase-to-snake_case convention.
	want := map[string]string{
		"physicalAddress": "physical_address",
		"shippingAddress": "shipping_address",
		"latitude":        "latitude",
		"status":          "status",
	}
	got := map[string]string{}
	for _, f := range d.Fields {
		got[f.Spec] = f.API
	}
	for spec, api := range want {
		if got[spec] != api {
			t.Errorf("field %q maps to %q, want %q", spec, got[spec], api)
		}
	}

	// `asns` is the one reference in scope, and it is a to-many:
	// `asns ManyToManyField -> ipam.ASN` (docs/netbox-schema.md -> dcim.Site), recorded in
	// the schema IR as `class: M2M` with `api.many: true`. dcim.Site's three remaining
	// foreign keys -- `region`, `group`, `tenant` -- are absent from the CRD, so the map
	// must not declare them either.
	refs := map[string]FieldClass{}
	for _, f := range d.Fields {
		if f.Class.Ref() {
			refs[f.Spec] = f.Class
		}
	}
	wantRefs := map[string]FieldClass{"asns": ClassRefMany}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("reference fields = %v, want %v", refs, wantRefs)
	}

	// A reference that resolves to nothing is a reference that writes nothing, so the
	// target has to be the Kind that owns ipam.ASN and not merely non-empty.
	wantTarget := netboxv1alpha1.ASNRef{}.TargetGVK()
	for _, f := range d.Fields {
		if f.Spec == "asns" && f.Target != wantTarget {
			t.Errorf("asns targets %s, want %s", f.Target, wantTarget)
		}
	}
}

// TestSiteNeedsOnlyTheOneFieldClass is the substantive claim of the second kind. A choice
// column and two decimals are exactly the shapes that look like they need special handling
// and do not: the engine's existing normalisation covers both, so the descriptor declares no
// class for any of them. The single class it does declare is `asns`, and it is there because
// no normalisation can infer set semantics from a JSON list -- an order-sensitive array
// arrives in exactly the same shape. If this test starts failing, either the normalisation
// regressed or someone added a class that is not carrying its weight.
func TestSiteNeedsOnlyTheOneFieldClass(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxSite"))

	// M2MFields() is what internal/netbox reads to compare a to-many as an unordered id
	// set, so `asns` being in it is the whole of what stops a reordered list PATCHing.
	if got := d.M2MFields(); !reflect.DeepEqual(got, []string{"asns"}) {
		t.Errorf("M2MFields() = %v, want [asns]", got)
	}
	if got := d.ArrayFields(); len(got) != 0 {
		t.Errorf("ArrayFields() = %v, want none", got)
	}
	if got := d.ObjectTypeListFields(); len(got) != 0 {
		t.Errorf("ObjectTypeListFields() = %v, want none", got)
	}
	if len(d.GenericFKs) != 0 {
		t.Errorf("GenericFKs = %v, want none", d.GenericFKs)
	}
}

// TestSiteChoiceAndDecimalDriftCleanly pairs the payload the operator sends with the shape
// NetBox returns for it, and asserts the second reconcile finds nothing to do.
//
// This is the anti-hot-loop assertion at the level of a real kind. `status` comes back as
// {"value","label"} and `latitude` comes back as a string padded to its decimal_places, so
// a naive comparison would find a difference on every pass and PATCH forever.
func TestSiteChoiceAndDecimalDriftCleanly(t *testing.T) {
	sent := netbox.Object{
		"name": "Home", "slug": "home", "status": "active",
		"latitude": 51.9244, "longitude": 4.4777, "description": "",
	}
	live := netbox.Object{
		"name": "Home", "slug": "home",
		"status":   map[string]any{"value": "active", "label": "Active"},
		"latitude": "51.924400", "longitude": "4.477700",
		"description": "", "facility": "", "comments": "",
		"region": nil, "group": nil, "tenant": nil,
		"created": "2026-08-21T10:00:00Z", "last_updated": "2026-08-21T10:00:00Z",
		"device_count": float64(9), "rack_count": float64(0),
	}

	if drift := netbox.Drift(live, sent, netbox.FieldRules{}); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}
