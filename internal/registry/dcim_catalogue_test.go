package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestCatalogueDescriptorsAreRegisteredAndValid is the boot check for NBO-027's four kinds.
func TestCatalogueDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxManufacturer", "dcim/manufacturers", "dcim.manufacturer"},
		{"NetBoxDeviceRole", "dcim/device-roles", "dcim.devicerole"},
		{"NetBoxDeviceType", "dcim/device-types", "dcim.devicetype"},
		{"NetBoxPlatform", "dcim/platforms", "dcim.platform"},
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

			// The endpoint is looked up in docs/netbox-schema.md's endpoint map, never
			// derived: `dcim/device-roles` is not the pluralisation of `dcim.DeviceRole`.
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

			// Patch, not Recreate. Every one of these is pointed at by something with
			// on_delete=PROTECT (docs/netbox-schema.md -> dcim.Device, dcim.DeviceType), so
			// delete-then-create to change a description would be refused -- and where it
			// were not, it would take the referring devices with it.
			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// All four carry both mixins, so all four are stamped in full.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}
		})
	}
}

// TestCatalogueNaturalKeysComeFromTheConstraints is the ticket's central claim, and the reason
// it is worth a test of its own: three of these four models are the same *kind* of model and
// have three different identities, so the base class cannot be what decides.
//
//   - dcim.DeviceRole is a NestedGroupModel with `(parent, slug)` plus `(slug)` conditioned on
//     `parent__isnull=True`, so it needs the pin -- on `parent_id`. The Django condition and
//     the query parameter are not the same string: NetBox registers only negation on an FK
//     filter, so the wire spelling is the sentinel `?parent_id=null` (NBO-206).
//   - dcim.Platform is a NestedGroupModel with `(manufacturer, slug)` plus `(slug)` conditioned
//     on `manufacturer__isnull=True`, so it needs the pin -- on `manufacturer_id`, and its
//     `parent` appears in no candidate at all.
//   - dcim.Manufacturer declares no meta.constraints and is column-unique, so it needs no pin,
//     exactly as tenancy.TenantGroup does not.
//   - dcim.DeviceType's two constraints are both unconditional, so it has two candidates and no
//     pin either.
func TestCatalogueNaturalKeysComeFromTheConstraints(t *testing.T) {
	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"a manufacturer is keyed on its column-unique slug alone": {
			kind: "NetBoxManufacturer",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		},
		"a device role pins parent_id when it has no parent": {
			kind: "NetBoxDeviceRole",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "slug", Spec: "slug"},
				}},
				{
					Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
					NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
				},
			},
		},
		"a platform pins manufacturer_id, and never parent_id": {
			kind: "NetBoxPlatform",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "slug", Spec: "slug"},
				}},
				{
					Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
					NullFields: []NullField{{Filter: "manufacturer_id", Spec: "manufacturerRef", Column: NullColumnRef}},
				},
			},
		},
		"a device type is keyed by slug then model, both under its manufacturer": {
			kind: "NetBoxDeviceType",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "slug", Spec: "slug"},
				}},
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "model", Spec: "model"},
				}},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !reflect.DeepEqual(d.NaturalKeys, tc.want) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.want)
			}
		})
	}
}

// TestCatalogueCandidatesByState walks the states each kind can be in, because a candidate list
// only means something together with what makes each entry applicable.
//
// The `want: nil` rows are the ones worth having. A declared-but-unresolved identity reference
// leaves nothing applicable, which is how the engine comes to wait instead of adopting the
// wrong object -- and it is what makes a deferral on such a field dead configuration.
func TestCatalogueCandidatesByState(t *testing.T) {
	tests := map[string]struct {
		kind  string
		state SpecState
		want  [][]string
	}{
		"a top-level device role": {
			kind:  "NetBoxDeviceRole",
			state: SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}},
			want:  [][]string{{"slug", "parent_id=null"}},
		},
		"a nested device role never reaches the null variant": {
			kind: "NetBoxDeviceRole",
			state: SpecState{
				Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug", "parentRef"},
			},
			want: [][]string{{"parent_id", "slug"}},
		},
		"a device role whose parent has not been created yet has no candidate": {
			kind:  "NetBoxDeviceRole",
			state: SpecState{Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug"}},
			want:  nil,
		},
		"a vendor-neutral platform, parent or no parent": {
			kind: "NetBoxPlatform",
			state: SpecState{
				Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug", "parentRef"},
			},
			want: [][]string{{"slug", "manufacturer_id=null"}},
		},
		"a platform whose manufacturer has not been created yet has no candidate": {
			kind:  "NetBoxPlatform",
			state: SpecState{Declared: []string{"slug", "manufacturerRef"}, Resolved: []string{"slug"}},
			want:  nil,
		},
		"a device type keeps both candidates once its manufacturer resolves": {
			kind: "NetBoxDeviceType",
			state: SpecState{
				Declared: []string{"slug", "model", "manufacturerRef"},
				Resolved: []string{"slug", "model", "manufacturerRef"},
			},
			want: [][]string{{"manufacturer_id", "slug"}, {"manufacturer_id", "model"}},
		},
		"a device type with no manufacturer has no candidate at all": {
			kind:  "NetBoxDeviceType",
			state: SpecState{Declared: []string{"slug", "model"}, Resolved: []string{"slug", "model"}},
			want:  nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			var got [][]string
			for _, key := range d.Candidates(tc.state) {
				got = append(got, params(key))
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Candidates(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestDeviceTypeCounterCachesAreReadOnly is the acceptance criterion that every
// CounterCacheField is unwritable, asserted against the digest's own list rather than against a
// count.
//
// dcim.DeviceType declares eleven of them plus WeightMixin's `_abs_weight`
// (docs/netbox-schema.md -> dcim.DeviceType). NetBox ignores a write to any of them instead of
// refusing it, so the guard has to be here: with the column in ReadOnly, a field map that ever
// reaches for one fails Validate at boot (ErrFieldReadOnly) rather than PATCHing forever.
func TestDeviceTypeCounterCachesAreReadOnly(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxDeviceType"))

	for _, column := range []string{
		"console_port_template_count", "console_server_port_template_count",
		"power_port_template_count", "power_outlet_template_count",
		"interface_template_count", "front_port_template_count",
		"rear_port_template_count", "device_bay_template_count",
		"module_bay_template_count", "inventory_item_template_count",
		"device_count", "_abs_weight",
	} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly; NetBox maintains it and drops a write to it", column)
		}
	}
}

// TestOnlyPlatformDefersItsParent is the deferral half of the same constraint reading.
//
// dcim.Platform's `parent` is in no candidate, so a platform whose parent does not exist yet is
// still identifiable and the engine can create it top-level and PATCH `parent` on -- the
// tenancy.TenantGroup shape. dcim.DeviceRole's `parent` is in both candidates, so in that same
// state nothing is applicable and there is nothing to defer: a Deferred entry there would read
// as though a child role were created top-level first, which is what candidate 2's null pin
// exists to prevent.
func TestOnlyPlatformDefersItsParent(t *testing.T) {
	platform, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxPlatform"))

	want := []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}}
	if !reflect.DeepEqual(platform.Deferred, want) {
		t.Errorf("NetBoxPlatform Deferred = %+v, want %+v", platform.Deferred, want)
	}

	for _, kind := range []string{"NetBoxDeviceRole", "NetBoxDeviceType", "NetBoxManufacturer"} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
		if len(d.Deferred) != 0 {
			t.Errorf("%s defers %+v; every reference it holds is part of its identity",
				kind, d.Deferred)
		}
	}
}
