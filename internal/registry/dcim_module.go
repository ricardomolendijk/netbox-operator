package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimModuleDescriptor()) }

// dcimModuleDescriptor is dcim.Module as data.
//
// The first kind in the tree whose identity comes from a `OneToOneField` rather than from a
// constraint or a convention. `dcim.Module` declares no `meta.constraints`
// (docs/netbox-schema.md -> dcim.Module; the IR's `natural_keys` is `[]` to match), and it does
// not need one:
//
//	module_bay OneToOneField REQ -> dcim.ModuleBay on_delete=CASCADE
//
// A `OneToOneField` is a `ForeignKey` Django declares `unique=True` on, so `module_bay_id`
// carries a UNIQUE index and one bay holds at most one module. That is a database guarantee of
// exactly the kind a natural key needs, read off a committed artefact, and it is the whole key:
// adding `device_id` to it would narrow the lookup below what the database enforces, and
// `asset_tag` -- globally UNIQUE and optional -- is deliberately not a candidate for the reason
// dcim.Rack gives about its own, that it identifies the hardware rather than the slot.
//
// **One thing the filter does that the column does not.** `module_bay_id` is declared on
// `ModuleFilterSet` as a `TreeNodeMultipleChoiceFilter` with `lookup_expr='in'`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.Module.filters.module_bay_id), because
// `dcim.ModuleBay` is an MPTTModel. So `?module_bay_id=N` matches modules in bay N *and in
// every bay descended from it*, and a bay's descendants are the bays provided by the module
// installed in it. Three cases, and none of them adopts the wrong object:
//
//   - Bay N is empty. It can have no descendants -- a descendant bay's parent is derived from
//     `module.module_bay`, which requires a module in N -- so the lookup matches nothing and
//     the engine creates.
//   - Bay N holds a module with no sub-modules. Exactly one match, and it is the right one.
//   - Bay N holds a module that itself has modules in its sub-bays. Two or more matches, which
//     internal/netbox answers with an AmbiguousError and the engine reports as `Conflict`
//     naming every id (reconciler.lookupFailure). No write is made and nothing is adopted.
//
// The third case is a real limitation and it is a refusal rather than a corruption: the engine
// consults the natural key only while `status.id` is unset (reconciler.pass.find), so a module
// that converged before its sub-modules existed keeps working, and a fresh cluster adopting a
// populated chassis reports the conflict instead of guessing. Narrowing it needs a filter
// NetBox does not offer in 4.6.8 -- there is no exact-match parameter for `module_bay`.
func dcimModuleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxModule"),
		Endpoint:   "dcim/modules",
		ObjectType: "dcim.module",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A PrimaryModel (docs/netbox-schema.md -> dcim.Module, bases), so it mixes in both
		// TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		// TrackingModelMixin, the other base, adds no column.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimModuleFields(),

		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "module_bay_id", Spec: "moduleBayRef"}}},
		},

		UpdateStrategy: UpdatePatch,

		// The bay is the containment parent, and it is the tighter of the two cascading
		// foreign keys this model has: `device` and `module_bay` are both `REQ` and both
		// `on_delete=CASCADE` (docs/netbox-schema.md -> dcim.Module), and exactly one
		// containment parent is permitted, because Kubernetes garbage collection waits for
		// every owner and two would turn "delete the device or the bay" into "delete both".
		//
		// `moduleBayRef` wins over `deviceRef` because the chain already reaches the device:
		// NetBoxModuleBay's own containment parent is its device, so deleting the device CR
		// collects the bay CRs and deleting those collects their modules. Naming `deviceRef`
		// here would give the same reach with less precision -- removing a single bay would
		// leave its module's CR behind to be recreated into a slot that no longer exists.
		//
		// It is also the reference this kind's identity is, which keeps the two facts in
		// agreement: the object the CR is named by is the object whose deletion removes it.
		ContainmentRef: "moduleBayRef",

		// The four columns every ChangeLoggedModel carries, and no others: dcim.Module
		// declares no CounterCacheField and no underscore-prefixed cache
		// (hack/testdata/ir-4.6.8.json.gz -> dcim.Module.fields).
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}

// dcimModuleFields is this kind's spec-to-column map.
//
// `moduleBayRef` -> `module_bay` is written as `module_bay` and filtered as `module_bay_id`:
// the field map carries the write name and the natural key carries the filter name. NetBox
// takes an id there through `NestedModuleBaySerializer`, a `WritableNestedSerializer`
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleSerializer, `declared.module_bay`).
//
// `assetTag` -> `asset_tag` carries EmptyIsNull, and that flag is load-bearing rather than
// tidy: the column is `UNIQUE` *and* `null=True`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.Module.asset_tag), so two modules whose asset tag was
// cleared to `""` would collide on the unique index where two NULLs do not.
//
// `status` needs no field class: NetBox returns a choice as `{"value","label"}` and takes the
// bare value, which internal/netbox/drift.go's unwrapNested already reduces by the absence of
// an `id` key. It needs no EmptyIsNull either -- the column is NOT NULL with a default, and the
// enum has no empty member to send.
//
// CascadeOnDelete is true on `deviceRef` and `moduleBayRef` and false on `moduleTypeRef`, which
// is `on_delete=PROTECT`. Declaring all three truthfully is what makes ContainmentRef
// enforceable rather than a convention (validateContainment).
//
// Not here, deliberately: `replicate_components` and `adopt_components`. Both are `write_only`
// BooleanFields on `ModuleSerializer` rather than columns
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleSerializer, `declared`), so neither can be
// read back and a field map entry for either would put a key in every payload that never
// appears in the response -- drift that cannot settle. They are also actions rather than state,
// and the action they take is the component instantiation the rest of #54 is about.
func dcimModuleFields() []Field {
	return []Field{
		{Spec: "status", API: "status"},
		{Spec: "serial", API: "serial"},
		{Spec: "assetTag", API: "asset_tag", EmptyIsNull: true},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		{
			Spec: "deviceRef", API: "device", Class: ClassRefOne,
			Target: netboxv1alpha1.DeviceRef{}.TargetGVK(), CascadeOnDelete: true,
		},
		{
			Spec: "moduleBayRef", API: "module_bay", Class: ClassRefOne,
			Target: netboxv1alpha1.ModuleBayRef{}.TargetGVK(), CascadeOnDelete: true,
		},
		{
			Spec: "moduleTypeRef", API: "module_type", Class: ClassRefOne,
			Target: netboxv1alpha1.ModuleTypeRef{}.TargetGVK(),
		},
	}
}
