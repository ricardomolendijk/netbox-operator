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

	// No Ref entries: dcim.Site's foreign keys are out of scope this milestone and are
	// absent from the CRD, so the map must not declare them either.
	for _, f := range d.Fields {
		if f.Ref {
			t.Errorf("field %q is marked as a reference, but no dcim.Site FK is in scope yet", f.Spec)
		}
	}
}

// TestSiteNeedsNoFieldClasses is the substantive claim of the second kind. A choice column
// and two decimals are exactly the shapes that look like they need special handling and do
// not: the engine's existing normalisation covers both, so the descriptor declares no field
// class at all. If this test starts failing, either the normalisation regressed or someone
// added a class that is not carrying its weight.
func TestSiteNeedsNoFieldClasses(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxSite"))

	if len(d.M2M) != 0 {
		t.Errorf("M2M = %v, want none", d.M2M)
	}
	if len(d.Arrays) != 0 {
		t.Errorf("Arrays = %v, want none", d.Arrays)
	}
	if len(d.ObjectTypeLists) != 0 {
		t.Errorf("ObjectTypeLists = %v, want none", d.ObjectTypeLists)
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
