package registry

import (
	"errors"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestConfigContextSetsAreAllToManyReferences is the claim NBO-059's widest kind makes: the
// thirteen assignment sets are data, not engine code.
//
// It asserts the list rather than the count, because the count is the uninteresting half. A set
// declared ClassValue by mistake would be written as a nested object and compared as a scalar
// -- NetBox returns a many-to-many as a list of objects and takes it as a list of ids, so the
// comparison would never settle and the operator would PATCH the same list forever. A set
// declared ClassArray would compare order-sensitively, which is the same loop with a different
// cause: NetBox does not preserve M2M order.
func TestConfigContextSetsAreAllToManyReferences(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigContext"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxConfigContext")
	}

	// The thirteen columns docs/netbox-schema.md -> extras.ConfigContext lists as
	// ManyToManyField, in the digest's own order.
	want := []string{
		"regions", "site_groups", "sites", "locations", "device_types", "roles", "platforms",
		"cluster_types", "cluster_groups", "clusters", "tenant_groups", "tenants", "tags",
	}

	got := d.M2MFields()
	for _, column := range want {
		if !slices.Contains(got, column) {
			t.Errorf("%s is a ManyToManyField on extras.ConfigContext and is not compared as one; "+
				"M2MFields() = %v", column, got)
		}
	}

	for _, field := range d.Fields {
		if !slices.Contains(want, field.API) {
			continue
		}

		if field.Class != ClassRefMany {
			t.Errorf("field %s -> %s is %q, want %q", field.Spec, field.API, field.Class, ClassRefMany)
		}

		if field.Target.Empty() {
			t.Errorf("field %s -> %s declares no target kind, so the resolver has nothing to "+
				"resolve its elements against", field.Spec, field.API)
		}
	}
}

// TestConfigContextIsNotTaggable is the one boolean on this kind that is dangerous to get
// wrong, plus the guard that makes getting it wrong impossible.
//
// `tags` on extras.ConfigContext is not TagsMixin: it is a plain M2M selecting *which tagged
// objects the context applies to* (docs/netbox-schema.md -> extras.ConfigContext, bases -- no
// TagsMixin). Taggable would make the provenance stamp append `k8s-managed` to that selector,
// because `tags` is a full-replacement list and the stamp appends to whatever the payload
// carries -- silently changing which objects in NetBox receive the configuration. Nothing else
// in the tree would report that.
func TestConfigContextIsNotTaggable(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigContext"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxConfigContext")
	}

	if d.Taggable {
		t.Error("Taggable = true: `tags` here selects which tagged objects the context applies " +
			"to, so the provenance stamp would change the selector rather than mark the object")
	}

	if d.CustomFieldable {
		t.Error("CustomFieldable = true: extras.ConfigContext mixes in no CustomFieldsMixin, so " +
			"netbox would ignore the key and the operator would report a stamp that is not there")
	}

	// The guard, asserted on a copy rather than by mutating a registered descriptor: the
	// combination has to fail Validate, or the next kind with a `tags` column that is not
	// TagsMixin can still be declared taggable.
	bad := d
	bad.Taggable = true

	if err := bad.Validate(); !errors.Is(err, ErrTagsFieldOnTaggableKind) {
		t.Errorf("Validate() on a taggable descriptor mapping a spec field onto `tags` = %v, "+
			"want %v", err, ErrTagsFieldOnTaggableKind)
	}
}

// TestConfigContextProfileFollowsTheAPIAndNotTheBases pins the one flag in the catalogue that
// contradicts its own model's bases.
//
// The AST digest has extras.ConfigContextProfile as a PrimaryModel, which mixes in
// CustomFieldsMixin; the REST serializer's write path carries no `custom_fields` key at all
// (hack/testdata/ir-4.6.8.json.gz). NetBox ignores a column it does not know rather than
// rejecting it, so CustomFieldable here would build a stamp into every payload, have it dropped
// server-side, and report Ready=True with status.provenance claiming a stamp that is not there.
// TestCoverage checks both flags against the IR; this states the intent next to the kind, so
// that a reader who "fixes" the flag to match the bases finds out why it does not.
func TestConfigContextProfileFollowsTheAPIAndNotTheBases(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigContextProfile"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxConfigContextProfile")
	}

	if !d.Taggable {
		t.Error("Taggable = false: `tags` is in this endpoint's write path and comes from TagsMixin")
	}

	if d.CustomFieldable {
		t.Error("CustomFieldable = true: the REST write path for extras/config-context-profiles " +
			"has no `custom_fields` key, so the stamp would be dropped silently (docs/regenerating.md)")
	}
}
