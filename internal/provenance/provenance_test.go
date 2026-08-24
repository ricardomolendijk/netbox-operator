package provenance

import (
	"reflect"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

func TestFromSpec(t *testing.T) {
	off := false

	cases := []struct {
		name string
		spec *netboxv1alpha1.ManagedBy
		want Config
	}{
		{
			name: "nil spec stamps nothing",
			spec: nil,
			want: Config{},
		},
		{
			// The case an endpoint stored before the CRD defaults existed lands in: only
			// clusterID is set, and every name has to come out the same as the marker
			// would have produced.
			name: "defaults match the crd markers",
			spec: &netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"},
			want: Config{
				ClusterID: "prod-eu", Tag: DefaultTag,
				UIDField: DefaultUIDField, ClusterField: DefaultClusterField,
				OwnerField: DefaultOwnerField, AllocationIdentityField: DefaultAllocationIdentityField,
				Bootstrap: true,
			},
		},
		{
			name: "explicit false bootstrap is honoured",
			spec: &netboxv1alpha1.ManagedBy{ClusterID: "prod-eu", Bootstrap: &off},
			want: Config{
				ClusterID: "prod-eu", Tag: DefaultTag,
				UIDField: DefaultUIDField, ClusterField: DefaultClusterField,
				OwnerField: DefaultOwnerField, AllocationIdentityField: DefaultAllocationIdentityField,
				Bootstrap: false,
			},
		},
		{
			name: "every name overridden",
			spec: &netboxv1alpha1.ManagedBy{
				ClusterID: "lab", Tag: "from-k8s", UIDField: "cr_uid",
				ClusterField: "cluster", OwnerField: "owner", AllocationIdentityField: "alloc",
			},
			want: Config{
				ClusterID: "lab", Tag: "from-k8s", UIDField: "cr_uid",
				ClusterField: "cluster", OwnerField: "owner", AllocationIdentityField: "alloc",
				Bootstrap: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromSpec(tc.spec)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FromSpec() = %+v, want %+v", got, tc.want)
			}
			if got.Enabled() != (tc.want.ClusterID != "") {
				t.Errorf("Enabled() = %v for clusterID %q", got.Enabled(), got.ClusterID)
			}
		})
	}
}

func TestCustomFieldNames(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "sorted",
			cfg:  FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "c"}),
			want: []string{"k8s_allocation_identity", "k8s_cluster", "k8s_owner", "k8s_uid"},
		},
		{
			// An empty name switches one field off, which is the documented way to keep the
			// allocation-identity definition out of a NetBox that will never serve a claim.
			name: "an empty name is dropped",
			cfg: Config{
				ClusterID: "c", UIDField: "k8s_uid", ClusterField: "k8s_cluster",
				OwnerField: "", AllocationIdentityField: "",
			},
			want: []string{"k8s_cluster", "k8s_uid"},
		},
		{
			// Two names configured the same is a typo, and POSTing the definition twice
			// would make the second call a 400 on a unique column.
			name: "duplicates collapse",
			cfg: Config{
				ClusterID: "c", UIDField: "same", ClusterField: "same", OwnerField: "same",
			},
			want: []string{"same"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.CustomFieldNames(); !slices.Equal(got, tc.want) {
				t.Errorf("CustomFieldNames() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOwnerRef(t *testing.T) {
	owner := Owner{Kind: "NetBoxSite", Namespace: "homelab", Name: "dc1", UID: "u-1"}
	if got, want := owner.Ref(), "netboxsite/homelab/dc1"; got != want {
		t.Errorf("Ref() = %q, want %q", got, want)
	}
}

// stamp is a resolved stamp with every definition present, as a bootstrap against a NetBox
// that has them would produce.
func stamp() Stamp {
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})

	return Stamp{Config: cfg, TagID: 7, Fields: cfg.CustomFieldNames()}
}

func TestStampApply(t *testing.T) {
	owner := Owner{Kind: "NetBoxSite", Namespace: "homelab", Name: "dc1", UID: "u-1"}
	both := Target{Taggable: true, CustomFields: true}

	cases := []struct {
		name    string
		stamp   Stamp
		target  Target
		desired netbox.Object
		live    netbox.Object
		// owner overrides the shared one, for the case where a value is missing.
		owner  *Owner
		wantOK bool
		want   netbox.Object
	}{
		{
			name:    "create with no live object carries only our tag",
			stamp:   stamp(),
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  true,
			want: netbox.Object{
				"slug": "dc1",
				"tags": []any{7},
				"custom_fields": map[string]any{
					"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
				},
			},
		},
		{
			// The case that makes the live object an argument at all: `tags` is a
			// full-replacement list, so a stamp that ignored what is there would strip
			// every tag a human applied in the UI.
			name:    "adopt unions our tag into the live list",
			stamp:   stamp(),
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			live: netbox.Object{"tags": []any{
				map[string]any{"id": float64(3), "name": "by-hand"},
				map[string]any{"id": float64(9), "name": "audited"},
			}},
			wantOK: true,
			want: netbox.Object{
				"slug": "dc1",
				"tags": []any{3, 7, 9},
				"custom_fields": map[string]any{
					"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
				},
			},
		},
		{
			name:    "our tag is not added twice",
			stamp:   stamp(),
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			live:    netbox.Object{"tags": []any{map[string]any{"id": float64(7)}}},
			wantOK:  true,
			want: netbox.Object{
				"slug": "dc1",
				"tags": []any{7},
				"custom_fields": map[string]any{
					"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
				},
			},
		},
		{
			// A spec that declares tags owns the list, or a tag dropped from the manifest
			// could never be removed. The live list is deliberately ignored here.
			name:    "a spec-declared tag list wins over the live one",
			stamp:   stamp(),
			target:  both,
			desired: netbox.Object{"slug": "dc1", "tags": []any{4}},
			live:    netbox.Object{"tags": []any{map[string]any{"id": float64(99)}}},
			wantOK:  true,
			want: netbox.Object{
				"slug": "dc1",
				"tags": []any{4, 7},
				"custom_fields": map[string]any{
					"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
				},
			},
		},
		{
			// extras.Tag: neither column exists on the model, so writing either would be a
			// value NetBox drops and the engine re-sends on every resync forever.
			name:    "a kind with neither column is not stamped",
			stamp:   stamp(),
			target:  Target{},
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  false,
			want:    netbox.Object{"slug": "dc1"},
		},
		{
			name:    "a taggable kind with no custom fields gets only the tag",
			stamp:   stamp(),
			target:  Target{Taggable: true},
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  true,
			want:    netbox.Object{"slug": "dc1", "tags": []any{7}},
		},
		{
			name:    "no clusterID stamps nothing",
			stamp:   Stamp{},
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  false,
			want:    netbox.Object{"slug": "dc1"},
		},
		{
			// The state a suppressed bootstrap leaves: configured, but with no tag id to
			// write. Half a stamp is worse than none, so nothing is written.
			name:    "an unresolved tag id stamps nothing",
			stamp:   Stamp{Config: FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "c"})},
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  false,
			want:    netbox.Object{"slug": "dc1"},
		},
		{
			// A definition the bootstrap could not create is not in Fields, and must not be
			// written: that write is exactly the 400 the bootstrap exists to prevent.
			name: "only fields the bootstrap confirmed are written",
			stamp: Stamp{
				Config: FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"}),
				TagID:  7, Fields: []string{"k8s_cluster"},
			},
			target:  both,
			desired: netbox.Object{"slug": "dc1"},
			wantOK:  true,
			want: netbox.Object{
				"slug": "dc1", "tags": []any{7},
				"custom_fields": map[string]any{"k8s_cluster": "prod-eu"},
			},
		},
		{
			// An object built by hand has no UID. Writing null into a field somebody may be
			// filling by other means is not the operator's business.
			name:    "an empty value is not written",
			stamp:   stamp(),
			target:  Target{CustomFields: true},
			desired: netbox.Object{"slug": "dc1"},
			owner:   &Owner{Kind: "NetBoxSite", Namespace: "homelab", Name: "dc1"},
			wantOK:  true,
			want: netbox.Object{
				"slug": "dc1",
				"custom_fields": map[string]any{
					"k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ownerFor := owner
			if tc.owner != nil {
				ownerFor = *tc.owner
			}

			_, ok := tc.stamp.Apply(tc.desired, tc.live, ownerFor, tc.target)
			if ok != tc.wantOK {
				t.Fatalf("Apply() ok = %v, want %v", ok, tc.wantOK)
			}
			if !reflect.DeepEqual(tc.desired, tc.want) {
				t.Errorf("payload =\n  %#v\nwant\n  %#v", tc.desired, tc.want)
			}
		})
	}
}

// TestStampApplyStatus asserts the record the engine writes to status.provenance, which is
// what NetBoxSweep reads to decide what it may touch.
func TestStampApplyStatus(t *testing.T) {
	owner := Owner{Kind: "NetBoxSite", Namespace: "homelab", Name: "dc1", UID: "u-1"}

	applied, ok := stamp().Apply(netbox.Object{}, nil, owner, Target{Taggable: true, CustomFields: true})
	if !ok {
		t.Fatal("Apply() reported nothing stamped")
	}

	want := netboxv1alpha1.ProvenanceStatus{
		ClusterID: "prod-eu", Tag: "k8s-managed",
		CustomFields: map[string]string{
			"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
		},
	}
	if !reflect.DeepEqual(applied, want) {
		t.Errorf("applied = %+v, want %+v", applied, want)
	}
}

// TestApplyIsIdempotent is the regression guard for the failure mode that does not announce
// itself: a stamp that is re-derived on every pass and never equals what NetBox returns is a
// PATCH loop for the lifetime of the object.
func TestApplyIsIdempotent(t *testing.T) {
	owner := Owner{Kind: "NetBoxSite", Namespace: "homelab", Name: "dc1", UID: "u-1"}
	target := Target{Taggable: true, CustomFields: true}

	first := netbox.Object{"slug": "dc1"}
	stamp().Apply(first, nil, owner, target)

	// NetBox's read shape for what was just written: tags as nested objects, custom fields
	// as a map that also carries the definitions the operator does not own.
	live := netbox.Object{
		"slug": "dc1",
		"tags": []any{map[string]any{"id": float64(7), "name": "k8s-managed"}},
		"custom_fields": map[string]any{
			"k8s_uid": "u-1", "k8s_cluster": "prod-eu", "k8s_owner": "netboxsite/homelab/dc1",
			"k8s_allocation_identity": nil, "somebody_elses_field": "keep me",
		},
	}

	second := netbox.Object{"slug": "dc1"}
	stamp().Apply(second, live, owner, target)

	rules := netbox.FieldRules{M2M: map[string]bool{TagsField: true}}
	if changes := netbox.Changes(live, second, rules); len(changes) != 0 {
		t.Errorf("a re-applied stamp drifts against what netbox returned: %+v", changes)
	}
}

func TestObjectTypes(t *testing.T) {
	descriptors := []registry.Descriptor{
		{ObjectType: "dcim.site", CustomFieldable: true},
		{ObjectType: "extras.tag"},
		{ObjectType: "dcim.region", CustomFieldable: true},
		// A second descriptor for one NetBox model is not a case today, but two CRDs onto
		// one model is a v1beta1 possibility and object_types must not list it twice.
		{ObjectType: "dcim.site", CustomFieldable: true},
	}

	want := []string{"dcim.region", "dcim.site"}
	if got := ObjectTypes(descriptors); !slices.Equal(got, want) {
		t.Errorf("ObjectTypes() = %v, want %v", got, want)
	}
}

// TestObjectTypesFromTheRealRegistry is the check that matters: object_types is derived from
// whatever kinds are registered, so this fails when a kind lands with the flag unset --
// which is the mistake that surfaces as a 400 on that kind and nowhere else.
func TestObjectTypesFromTheRealRegistry(t *testing.T) {
	got := ObjectTypes(registry.List())
	if len(got) == 0 {
		t.Fatal("no registered kind declares CustomFieldable")
	}

	for _, d := range registry.List() {
		stamped := slices.Contains(got, d.ObjectType)
		if stamped != d.CustomFieldable {
			t.Errorf("%s: in object_types = %v, CustomFieldable = %v",
				d.GVK.Kind, stamped, d.CustomFieldable)
		}
	}
}
