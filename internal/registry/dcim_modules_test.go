package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestModuleDescriptorsAreRegisteredAndValid is the boot check for the four module kinds.
func TestModuleDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxModuleTypeProfile", "dcim/module-type-profiles", "dcim.moduletypeprofile"},
		{"NetBoxModuleType", "dcim/module-types", "dcim.moduletype"},
		{"NetBoxModuleBay", "dcim/module-bays", "dcim.modulebay"},
		{"NetBoxModule", "dcim/modules", "dcim.module"},
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
			// derived: `dcim/module-type-profiles` is not the pluralisation of
			// `dcim.ModuleTypeProfile`, and `dcim/module-bays` is not `dcim/modulebays`.
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

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// All four carry both mixins, so all four are stamped in full: three are
			// PrimaryModels and dcim.ModuleBay is a ComponentModel, which is a NetBoxModel
			// (docs/netbox-schema.md, bases). NetBoxModel mixes in TagsMixin and
			// CustomFieldsMixin just as PrimaryModel does; what it lacks is `comments`.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}
		})
	}
}

// TestModuleNaturalKeysAndWhereTheyComeFrom is #54's central claim, and the four kinds derive
// their identity four different ways over two NetBox source files:
//
//   - dcim.ModuleType is the only one the committed IR supplies directly: its
//     `meta.constraints` is `(manufacturer, model)` and `natural_keys` carries the pair.
//   - dcim.ModuleTypeProfile declares no meta.constraints at all, so the IR reports no
//     candidate and the key is hand-declared from `name CharField REQ UNIQUE` -- a
//     column-level unique, the dcim.Manufacturer derivation with `name` for `slug`.
//   - dcim.ModuleBay has a three-column constraint whose middle column is nullable, so the
//     null-pinned `(device, name)` variant is a convention rather than a constraint -- the
//     dcim.Rack shape.
//   - dcim.Module has no constraint and does not need one: `module_bay` is a OneToOneField,
//     which is a UNIQUE index by another name.
func TestModuleNaturalKeysAndWhereTheyComeFrom(t *testing.T) {
	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"a profile is keyed on its column-unique name, because it has no slug": {
			kind: "NetBoxModuleTypeProfile",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},
		},
		"a module type is keyed on the constraint, and has no second candidate to fall to": {
			kind: "NetBoxModuleType",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "model", Spec: "model"},
				}},
			},
		},
		"a module bay pins module_id when the bay is on the chassis": {
			kind: "NetBoxModuleBay",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "device_id", Spec: "deviceRef"},
					{Filter: "module_id", Spec: "moduleRef"},
					{Filter: "name", Spec: "name"},
				}},
				{
					Fields: []KeyField{
						{Filter: "device_id", Spec: "deviceRef"},
						{Filter: "name", Spec: "name"},
					},
					NullFields: []NullField{
						{Filter: "module_id", Spec: "moduleRef", Column: NullColumnRef},
					},
				},
			},
		},
		"a module is keyed on its bay alone, because the column is one-to-one": {
			kind: "NetBoxModule",
			want: []NaturalKey{
				{Fields: []KeyField{{Filter: "module_bay_id", Spec: "moduleBayRef"}}},
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

// TestModuleCandidatesByState is the table #54's acceptance criteria ask for: a bay on a
// module, a bay on the chassis, and a bay whose module is declared but not yet created.
//
// The two `want: nil` rows are the ones worth having. A bay whose `moduleRef` names a
// NetBoxModule that has not reconciled yet must **not** fall through to the null-pinned
// `(device, name)` variant -- that candidate would find the chassis bay of the same name on
// the same device and adopt it, and the follow-up PATCH would move it off the card. With
// nothing applicable the engine waits, which is the correct outcome (NBO-015).
func TestModuleCandidatesByState(t *testing.T) {
	tests := map[string]struct {
		kind  string
		state SpecState
		want  [][]string
	}{
		"a bay on the chassis uses the null-pinned convention": {
			kind: "NetBoxModuleBay",
			state: SpecState{
				Declared: []string{"deviceRef", "name"},
				Resolved: []string{"deviceRef", "name"},
			},
			want: [][]string{{"device_id", "name", "module_id=null"}},
		},
		"a bay a module provides uses the constraint verbatim": {
			kind: "NetBoxModuleBay",
			state: SpecState{
				Declared: []string{"deviceRef", "name", "moduleRef"},
				Resolved: []string{"deviceRef", "name", "moduleRef"},
			},
			want: [][]string{{"device_id", "module_id", "name"}},
		},
		"a bay whose module has not been created yet has no candidate": {
			kind: "NetBoxModuleBay",
			state: SpecState{
				Declared: []string{"deviceRef", "name", "moduleRef"},
				Resolved: []string{"deviceRef", "name"},
			},
			want: nil,
		},
		"a bay whose device has not been created yet has no candidate either": {
			kind: "NetBoxModuleBay",
			state: SpecState{
				Declared: []string{"deviceRef", "name"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
		"a module waits for its bay and asks about nothing else": {
			kind: "NetBoxModule",
			state: SpecState{
				Declared: []string{"deviceRef", "moduleBayRef", "moduleTypeRef"},
				Resolved: []string{"deviceRef", "moduleTypeRef"},
			},
			want: nil,
		},
		"a module with a resolved bay is keyed on the bay and not on the device": {
			kind: "NetBoxModule",
			state: SpecState{
				Declared: []string{"deviceRef", "moduleBayRef", "moduleTypeRef"},
				Resolved: []string{"deviceRef", "moduleBayRef", "moduleTypeRef"},
			},
			want: [][]string{{"module_bay_id"}},
		},
		"a module type with no manufacturer has no candidate at all": {
			kind: "NetBoxModuleType",
			state: SpecState{
				Declared: []string{"model"}, Resolved: []string{"model"},
			},
			want: nil,
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

// TestModuleContainmentFollowsTheCascade asserts the two owner references and the two
// absences, against `on_delete` rather than against a sense of which kind feels like a parent.
//
// `dcim.ModuleBay.device` and `dcim.Module.module_bay` are `CASCADE`, so both qualify. The two
// catalogue kinds hold nothing but `PROTECT` foreign keys, so neither gets one: an owner
// reference on a PROTECTed foreign key promises a cluster-side cascade NetBox refuses to
// perform, which deletes the CR and leaves the row (registry.ErrContainmentNotCascade,
// docs/decisions/0003-ownership-and-references.md rule 4).
//
// The choice on each of the two is between two cascading keys rather than between cascade and
// no cascade, which is why the *unchosen* one is asserted too: dcim.ModuleBay could name
// `moduleRef` and dcim.Module could name `deviceRef`, and both would pass validateContainment.
func TestModuleContainmentFollowsTheCascade(t *testing.T) {
	for kind, want := range map[string]string{
		"NetBoxModuleBay": "deviceRef",
		"NetBoxModule":    "moduleBayRef",
	} {
		t.Run(kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

			if d.ContainmentRef != want {
				t.Errorf("ContainmentRef = %q, want %q", d.ContainmentRef, want)
			}

			field, ok := d.FieldFor(want)
			if !ok {
				t.Fatalf("no %q entry in the field map", want)
			}

			if !field.CascadeOnDelete {
				t.Errorf("%s.CascadeOnDelete = false; a containment parent has to be the FK "+
					"NetBox itself cascades", want)
			}
		})
	}

	// The bay's other cascading key, declared truthfully and deliberately not chosen: every
	// bay has a device and only a nested one has a module, so naming `moduleRef` would leave
	// the common case with no containment parent at all.
	bay, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleBay"))

	module, ok := bay.FieldFor("moduleRef")
	if !ok {
		t.Fatal("no `moduleRef` entry in NetBoxModuleBay's field map")
	}

	if !module.CascadeOnDelete {
		t.Error("NetBoxModuleBay.moduleRef CascadeOnDelete = false; " +
			"dcim.ModularComponentModel.module is on_delete=CASCADE")
	}

	for _, kind := range []string{"NetBoxModuleTypeProfile", "NetBoxModuleType"} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
		if d.ContainmentRef != "" {
			t.Errorf("%s ContainmentRef = %q, want empty: every FK it holds is PROTECT",
				kind, d.ContainmentRef)
		}
	}

	// The module's chain reaches the device without naming it: the bay owns the module and
	// the device owns the bay.
	moduleDesc, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModule"))

	targets := moduleDesc.ContainmentTargets()

	got := make([]string, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.Kind)
	}

	if want := []string{"NetBoxModuleBay"}; !reflect.DeepEqual(got, want) {
		t.Errorf("NetBoxModule ContainmentTargets() = %v, want %v", got, want)
	}
}

// TestModuleTypeWritesAttributesAndNotAttributeData is the rename that would otherwise fail
// silently, asserted rather than commented.
//
// The model column is `attribute_data` and `ModuleTypeSerializer` exposes it as `attributes`
// through an `AttributesField` (hack/testdata/api-schema-4.6.8.json.gz -> ModuleTypeSerializer),
// and the IR in hack/testdata/ir-4.6.8.json.gz records `attribute_data` as absent from the
// write path and `attributes` as present in it. NetBox drops a field name it does not know
// rather than
// rejecting it, so writing `attribute_data` would report 200 and set nothing, and the next
// reconcile would find the same difference forever.
func TestModuleTypeWritesAttributesAndNotAttributeData(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleType"))

	attributes, ok := d.FieldFor("attributes")
	if !ok {
		t.Fatal("no `attributes` entry in the field map")
	}

	if attributes.API != "attributes" {
		t.Errorf("attributes API = %q, want `attributes`: `attribute_data` is the model "+
			"column and NetBox's serializer does not accept it", attributes.API)
	}

	// ClassJSON, not ClassValue. The scalar comparison unwraps any JSON object carrying an
	// `id` or a `value` key, because that is how NetBox renders a foreign key and a choice on
	// read, so an attribute document containing a `value` property would never settle
	// (registry.ClassJSON, netbox.FieldRules.JSON).
	if attributes.Class != ClassJSON {
		t.Errorf("attributes Class = %q, want %q", attributes.Class, ClassJSON)
	}

	if got := d.JSONFields(); !slices.Contains(got, "attributes") {
		t.Errorf("JSONFields() = %v, want it to contain `attributes`", got)
	}

	profile, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleTypeProfile"))
	if got := profile.JSONFields(); !slices.Contains(got, "schema") {
		t.Errorf("NetBoxModuleTypeProfile JSONFields() = %v, want it to contain `schema`", got)
	}
}

// TestModuleCountersAndDerivedColumnsAreReadOnly asserts the unwritable columns against the
// digest's own lists rather than against a count.
//
// NetBox ignores a write to any of them instead of refusing it, so the guard has to be here:
// with the column in ReadOnly, a field map that ever reaches for one fails Validate at boot
// (ErrFieldReadOnly) rather than PATCHing forever.
//
// The two on dcim.ModuleBay are not counters and are the more interesting half. `parent` is
// the MPTT self-reference NetBox derives from `module.module_bay` and leaves out of the
// serializer's write path entirely, and `installed_module` is the *reverse* accessor of
// `dcim.Module.module_bay`'s `related_name` -- the wrong half of a one-to-one whose writable
// side is NetBoxModule.moduleBayRef.
func TestModuleCountersAndDerivedColumnsAreReadOnly(t *testing.T) {
	for kind, columns := range map[string][]string{
		// The nine CounterCacheFields and WeightMixin's normalised-grams cache.
		"NetBoxModuleType": {
			"module_count",
			"console_port_template_count", "console_server_port_template_count",
			"power_port_template_count", "power_outlet_template_count",
			"interface_template_count", "front_port_template_count",
			"rear_port_template_count", "module_bay_template_count",
			"_abs_weight",
		},
		"NetBoxModuleBay": {
			"parent", "installed_module", "_occupied", "_site", "_location", "_rack",
		},
	} {
		t.Run(kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

			for _, column := range columns {
				if !slices.Contains(d.ReadOnly, column) {
					t.Errorf("%q is not in ReadOnly; NetBox maintains it and drops a write to it",
						column)
				}
			}
		})
	}
}

// TestModuleDoesNotWriteTheWriteOnlyActionFlags is an assertion in the negative, and the one
// that keeps a plausible mistake out of the payload.
//
// `replicate_components` and `adopt_components` are `write_only` BooleanFields declared on
// `ModuleSerializer` rather than columns on the model
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleSerializer, `declared`). A write-only field
// never comes back on read, so a field-map entry for either would put a key in every payload
// that is absent from every response, and the drift comparison would never settle. They are
// also actions rather than state: `replicate_components` defaults to true and instantiates the
// module type's component templates, which is the rest of #54.
func TestModuleDoesNotWriteTheWriteOnlyActionFlags(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModule"))

	for _, field := range d.Fields {
		if field.API == "replicate_components" || field.API == "adopt_components" {
			t.Errorf("%q is mapped from %q; it is write-only and cannot be diffed",
				field.API, field.Spec)
		}
	}

	// And no reference on this kind is a to-many or a generic FK: every column dcim.Module
	// holds is a plain scalar or a single foreign key.
	if got := d.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none", got)
	}

	if len(d.GenericFKs) != 0 {
		t.Errorf("GenericFKs = %+v, want none: dcim.Module has no polymorphic column", d.GenericFKs)
	}
}

// TestModuleAssetTagIsClearedWithNullAndIsNotAKey pins two decisions about one column.
//
// `asset_tag` is `UNIQUE` *and* `null=True` (hack/testdata/ir-4.6.8.json.gz ->
// dcim.Module.asset_tag). EmptyIsNull follows from the pair rather than from taste: two modules
// whose asset tag was cleared to `""` collide on the unique index, where two NULLs do not.
//
// And it is deliberately not a natural-key candidate, the dcim.Rack argument unchanged: an
// asset tag identifies the hardware and this CR describes the slot, so adopting by asset tag
// would let moving a card between chassis rewrite the device and bay of somebody else's module.
func TestModuleAssetTagIsClearedWithNullAndIsNotAKey(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxModule"))

	assetTag, ok := d.FieldFor("assetTag")
	if !ok {
		t.Fatal("no `assetTag` entry in the field map")
	}

	if assetTag.API != "asset_tag" {
		t.Errorf("assetTag API = %q, want `asset_tag`", assetTag.API)
	}

	if !assetTag.EmptyIsNull {
		t.Error("assetTag EmptyIsNull = false; the column is UNIQUE and null=True, so an " +
			"emptied one has to be sent as null rather than as \"\"")
	}

	for _, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			if field.Filter == "asset_tag" {
				t.Error("asset_tag is a natural-key filter; it identifies the hardware, not " +
					"the slot the CR describes")
			}
		}
	}
}
