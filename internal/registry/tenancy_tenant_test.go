package registry

import (
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestTenancyDescriptorsAreRegisteredAndValid is the boot check for both tenancy kinds.
func TestTenancyDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxTenantGroup", "tenancy/tenant-groups", "tenancy.tenantgroup"},
		{"NetBoxTenant", "tenancy/tenants", "tenancy.tenant"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			// Patch, not Recreate. Almost every IPAM model points at a tenant with
			// on_delete=PROTECT (docs/netbox-schema.md), so delete-then-create to change a
			// description would be refused by NetBox -- and where it were not, it would
			// take the referring objects with it.
			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}
		})
	}
}

// TestTenantGroupIsKeyedOnSlugAloneWithNoParentFilter is the divergence this kind exists to
// pin down.
//
// plan.md §8.1 claims every MPTT kind is keyed on `(parent, name)` with a `parent IS NULL`
// variant. That is true of dcim.Region and false here: tenancy.TenantGroup declares no
// meta.constraints at all and carries column-level UNIQUE on `name` and `slug`
// (docs/netbox-schema.md -> tenancy.TenantGroup), so its uniqueness is global. A
// `parent_id` filter of either sort would make a nested group's slug unfindable.
func TestTenantGroupIsKeyedOnSlugAloneWithNoParentFilter(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTenantGroup"))

	want := []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	for _, key := range d.NaturalKeys {
		for _, field := range key.NullFields {
			t.Errorf("candidate pins %q to null; tenancy.TenantGroup has no conditional "+
				"constraint to express", field.Filter)
		}
	}

	// Identity holds whether or not the group is nested, which is the whole point: the
	// candidate does not read `parentRef` at all.
	for _, state := range []SpecState{
		{Declared: []string{"slug"}, Resolved: []string{"slug"}},
		{Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug"}},
		{Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug", "parentRef"}},
	} {
		if got := d.Candidates(state); len(got) != 1 {
			t.Errorf("Candidates(%+v) returned %d candidates, want 1", state, len(got))
		}
	}
}

// TestTenantGroupDefersItsSelfReference checks the deferral is the conditional kind.
//
// DeferAlways would strip a resolved `parent` from the create, leaving the group top-level
// in NetBox for one pass. DeferIfUnresolved sends it when it is in hand and PATCHes it on
// later when it is not, which is what makes a parent and child applied in one batch
// converge (internal/reconciler/deferred.go).
func TestTenantGroupDefersItsSelfReference(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTenantGroup"))

	want := []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}}
	if !reflect.DeepEqual(d.Deferred, want) {
		t.Errorf("Deferred = %+v, want %+v", d.Deferred, want)
	}
}

// TestTenantNaturalKeysPinGrouplessnessRatherThanOmittingIt is NBO-005's null-filter
// requirement on its first non-MPTT kind.
//
// The two candidates come from tenancy.Tenant.meta.constraints: `unique_group_slug` on
// `(group, slug)` and `unique_slug` on `(slug)` conditioned on `group IS NULL`. The second
// must pin `group_id__isnull=true`; with `group_id` merely omitted the query means "this
// slug in any group", so every groupless tenant adopts an unrelated grouped one.
func TestTenantNaturalKeysPinGrouplessnessRatherThanOmittingIt(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTenant"))

	want := []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "group_id", Spec: "groupRef"},
				{Filter: "slug", Spec: "slug"},
			},
		},
		{
			Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
			NullFields: []NullField{{Filter: "group_id", Spec: "groupRef"}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// The pin renders as a filter rather than as an absence. If this ever became an
	// omission the query would be indistinguishable from "any group".
	if got := want[1].NullFields[0].Param(); got != "group_id__isnull" {
		t.Errorf("null pin renders as %q, want group_id__isnull", got)
	}
}

// TestTenantCandidatesFollowTheGroup walks the three states `groupRef` can be in, because
// which candidate applies is the whole of this kind's identity logic.
func TestTenantCandidatesFollowTheGroup(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTenant"))

	for _, tc := range []struct {
		name  string
		state SpecState
		// want is the number of applicable candidates, and which one leads.
		want      int
		wantFirst string
	}{
		{
			name:      "grouped and resolved uses (group, slug)",
			state:     SpecState{Declared: []string{"slug", "groupRef"}, Resolved: []string{"slug", "groupRef"}},
			want:      1,
			wantFirst: "group_id",
		},
		{
			name:      "groupless uses slug with the group pinned null",
			state:     SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}},
			want:      1,
			wantFirst: "slug",
		},
		{
			// The regression the ticket names: a group declared but not created yet must
			// not fall through to the groupless candidate, which would adopt an unrelated
			// tenant of the same slug and then PATCH the group off it.
			name:  "group declared but unresolved matches nothing and waits",
			state: SpecState{Declared: []string{"slug", "groupRef"}, Resolved: []string{"slug"}},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != tc.want {
				t.Fatalf("Candidates() returned %d candidates (%+v), want %d", len(got), got, tc.want)
			}

			if tc.want > 0 && got[0].Fields[0].Filter != tc.wantFirst {
				t.Errorf("leading candidate filters on %q, want %q",
					got[0].Fields[0].Filter, tc.wantFirst)
			}
		})
	}
}

// TestTenantsWithDifferentSlugsDoNotAdoptEachOther is the acceptance criterion spelled out
// as the query it turns into: two groupless tenants differ in the one filter that is
// matched, so the pin cannot make them collide.
func TestTenantsWithDifferentSlugsDoNotAdoptEachOther(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTenant"))

	groupless := d.Candidates(SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}})
	if len(groupless) != 1 {
		t.Fatalf("Candidates() returned %d candidates, want 1", len(groupless))
	}

	key := groupless[0]
	if len(key.Fields) != 1 || key.Fields[0].Spec != "slug" {
		t.Fatalf("groupless candidate matches on %+v, want slug alone", key.Fields)
	}

	if len(key.NullFields) != 1 || key.NullFields[0].Filter != "group_id" {
		t.Fatalf("groupless candidate pins %+v, want group_id", key.NullFields)
	}
}

// TestTenancyKindsNeedNoFieldClassesBeyondTheirReference keeps the descriptors honest about
// what is special here: one to-one reference each, and nothing else. A class that stops
// carrying its weight shows up as a failure rather than as dead data.
func TestTenancyKindsNeedNoFieldClassesBeyondTheirReference(t *testing.T) {
	for kind, ref := range map[string]string{
		"NetBoxTenantGroup": "parent",
		"NetBoxTenant":      "group",
	} {
		t.Run(kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

			if got := d.M2MFields(); len(got) != 0 {
				t.Errorf("M2MFields() = %v, want none", got)
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

			// A tenant is an attribute of an object, not its container, and neither of
			// these kinds is contained by anything either
			// (docs/decisions/0003-ownership-and-references.md rule 4).
			if d.ContainmentRef != "" {
				t.Errorf("ContainmentRef = %q, want empty: a catalogue kind has no container",
					d.ContainmentRef)
			}

			refs := make([]string, 0, 1)
			for _, f := range d.Fields {
				if f.Class == ClassRefOne {
					refs = append(refs, f.API)
				}
			}

			if !reflect.DeepEqual(refs, []string{ref}) {
				t.Errorf("to-one references = %v, want [%s]", refs, ref)
			}
		})
	}
}

// TestTenantDriftsCleanly pairs the payload the operator sends with the shape NetBox
// returns, and asserts the second reconcile finds nothing to do.
//
// The reference is what earns the test: `group` is written as an id and comes back as a
// nested object, so a naive comparison would find a difference on every pass and PATCH
// forever.
func TestTenantDriftsCleanly(t *testing.T) {
	sent := netbox.Object{
		"name": "Donkerslootstraat (RTM)", "slug": "donkerslootstraat",
		"description": "Donkerslootstraat 155B", "comments": "", "group": 3,
	}
	live := netbox.Object{
		"name": "Donkerslootstraat (RTM)", "slug": "donkerslootstraat",
		"description": "Donkerslootstraat 155B", "comments": "",
		"group":        map[string]any{"id": float64(3), "name": "Houses", "slug": "houses"},
		"created":      "2026-08-21T10:00:00Z",
		"last_updated": "2026-08-21T10:00:00Z",
		"prefix_count": float64(4), "vlan_count": float64(6),
	}

	if drift := netbox.Drift(live, sent, netbox.FieldRules{}); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}
