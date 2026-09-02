package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimModuleBayDescriptor()) }

// dcimModuleBayDescriptor is dcim.ModuleBay as data.
//
// The fourth kind whose natural key NetBox does not enforce where it matters, after
// ipam.IPAddress, tenancy.Contact and dcim.Rack -- and it gets there by a different route
// from any of them. The constraint exists and names three columns
// (docs/netbox-schema.md -> dcim.ModuleBay.meta.constraints):
//
//	UniqueConstraint(fields=('device', 'module', 'name'), name='..._unique_device_module_name')
//
// but `module` is nullable (`module ForeignKey -> dcim.Module on_delete=CASCADE`, no `REQ`),
// and Postgres treats NULLs as distinct. So the constraint holds for a bay *on a module* and
// does nothing at all for a bay on the chassis, which is the common case. The null-pinned
// second candidate is what covers it, and it is the dcim.Rack derivation applied to a
// different column.
//
// **The MPTT parent is not written and cannot be.** `dcim.ModuleBay` is an `MPTTModel` and
// carries `parent TreeForeignKey -> dcim.ModuleBay`, but the column is absent from the
// serializer's write path (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.write_path, which
// lists `device`, `module`, `name`, `label`, `position`, `enabled`, `description`,
// `installed_module` and `_occupied`). NetBox derives it from `module.module_bay`. So there is
// no `parentRef` on this kind, no cycle check to webhook and no `parent IS NULL` variant: the
// tree edge is a consequence of `moduleRef`, and declaring it twice would be two writers for
// one fact where only one of them reaches the database.
func dcimModuleBayDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleBay"),
		Endpoint:   "dcim/module-bays",
		ObjectType: "dcim.modulebay",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.ComponentModel is a NetBoxModel, not a PrimaryModel (docs/netbox-schema.md ->
		// dcim.ComponentModel, bases) -- so there is no `comments` column here -- and
		// NetBoxModel still mixes in both TagsMixin and CustomFieldsMixin, so the provenance
		// stamp applies in full. The dcim.Interface reading, unchanged.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimModuleBayFields(),

		NaturalKeys: dcimModuleBayKeys(),

		UpdateStrategy: UpdatePatch,

		// No Deferred entry for `moduleRef`, and that is the decision rather than an omission.
		// It is matched on by candidate 1 and pinned null by candidate 2, so a bay whose module
		// is declared and not yet created has *no* applicable candidate and the engine waits --
		// which is the correct outcome and the one DeferIfUnresolved would take away. Deferring
		// the reference out of the create would mean creating the bay on the chassis and then
		// PATCHing it onto the module, and between the two writes the `(device, name)` pair it
		// occupies is the chassis bay's (NBO-015).

		ReadOnly: dcimModuleBayReadOnly(),

		// The device is the containment parent, which is the same thing `on_delete=CASCADE`
		// says on the NetBox side: `dcim.ComponentModel.device` is `REQ` and cascading
		// (docs/netbox-schema.md -> dcim.ComponentModel), so `kubectl delete nbdev` takes its
		// hand-written module bays with it in the same namespace
		// (docs/decisions/0003-ownership-and-references.md rule 4).
		//
		// `module` cascades too, and exactly one containment parent is permitted -- Kubernetes
		// garbage collection waits for every owner, so two would turn "delete the device or the
		// module" into "delete both". `device` wins because it is the required one: every bay
		// has a device and only a nested bay has a module, so choosing `module` would leave the
		// common case with no containment parent at all. The dcim.Interface precedent, which
		// records the same pair and makes the same choice.
		ContainmentRef: "deviceRef",
	}
}

// dcimModuleBayFields is this kind's spec-to-column map.
//
// Small enough not to need extracting and extracted anyway, for the dcimRackFields shape.
//
// `moduleRef` -> `module` is the entry worth reading twice, because the neighbouring column is
// spelled almost the same and means the opposite: `module` is the module that *provides* this
// bay, and `installed_module` -- read-only below -- is the module installed *in* it. Writing
// the wrong one would attach a line card's bay to the card sitting in it.
//
// CascadeOnDelete is true on both references, and truthfully: `dcim.ComponentModel.device` and
// `dcim.ModularComponentModel.module` are both `on_delete=CASCADE` (docs/netbox-schema.md).
// Declaring both is what makes ContainmentRef's single choice a choice rather than the only
// option validateContainment would accept.
//
// `enabled` needs no EmptyIsNull: the column is `BooleanField def=True` with no null, and the
// spec field is a pointer, so an unmanaged value is absent from the payload rather than empty
// in it. `position` and `label` are plain CharFields where `""` is how NetBox spells empty.
func dcimModuleBayFields() []Field {
	return []Field{
		{Spec: "name", API: "name"},
		{Spec: "label", API: "label"},
		{Spec: "position", API: "position"},
		{Spec: "enabled", API: "enabled"},
		{Spec: "description", API: "description"},
		{
			Spec: "deviceRef", API: "device", Class: ClassRefOne,
			Target: netboxv1alpha1.DeviceRef{}.TargetGVK(), CascadeOnDelete: true,
		},
		{
			Spec: "moduleRef", API: "module", Class: ClassRefOne,
			Target: netboxv1alpha1.ModuleRef{}.TargetGVK(), CascadeOnDelete: true,
		},
	}
}

// dcimModuleBayKeys are the lookup candidates, in priority order.
//
// Two, and never both applicable to one object, because they disagree about whether `moduleRef`
// is declared:
//
//  1. `(device_id, module_id, name)` -- the database constraint verbatim, used by a bay that a
//     module provides. This is the candidate the committed IR supplies
//     (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.natural_keys, with `device_id`,
//     `module_id` and `name` as its filters).
//  2. `(device_id, name)` with `module_id` pinned null -- the chassis bay. NetBox's constraint
//     covers this case on paper and not in the database, because a UNIQUE over a NULL column
//     does not constrain rows where it is NULL. The pin is what makes the candidate safe:
//     without it the lookup would also match a bay of that name on some module of the same
//     device and adopt it, and the follow-up PATCH would move it off the card and onto the
//     chassis. `?module_id=null` is the wire spelling of NullColumnRef, and `module_id` is a
//     `ModelMultipleChoiceFilter` -- the class #216 established the spelling against
//     (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.filters.module_id, declared on
//     `ModularDeviceComponentFilterSet`).
//
// NaturalKey.Applicable offers candidate 2 only while `moduleRef` is *undeclared*, so a bay
// whose module has not been created yet waits rather than falling through and adopting the
// chassis bay of the same name (NBO-015).
//
// `device_id` is never omitted from either. The pair is unique per device and `Slot 1` is the
// most-reused bay name there is, so a lookup without it would adopt another device's bay on the
// first reconcile -- the dcim.Interface argument, and the failure class behind #206 and #216.
//
// There is no case-insensitive lookup here. Both of dcim.Device's own constraints are declared
// over `Lower('name')` and `dcim.ComponentModel`'s is not, so `Slot1` and `slot1` are two bays
// to NetBox and must be two to the operator.
//
// Every filter is registered: `device_id` on `DeviceComponentFilterSet`, `module_id` on
// `ModularDeviceComponentFilterSet`, and `name` in `ModuleBayFilterSet`'s `meta_fields`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.filters).
func dcimModuleBayKeys() []NaturalKey {
	return []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "device_id", Spec: "deviceRef"},
				{Filter: "module_id", Spec: "moduleRef"},
				{Filter: "name", Spec: "name"},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "device_id", Spec: "deviceRef"},
				{Filter: "name", Spec: "name"},
			},
			NullFields: []NullField{
				{Filter: "module_id", Spec: "moduleRef", Column: NullColumnRef},
			},
		},
	}
}

// dcimModuleBayReadOnly are the columns the operator must never write, in four groups.
//
// The four every ChangeLoggedModel carries, plus:
//
// `parent` -- the MPTT self-reference. Absent from the serializer's write path entirely
// (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.write_path) because NetBox derives it from
// `module.module_bay`. It is listed here rather than merely left out of the field map so that a
// later edit reaching for it fails the boot with ErrFieldReadOnly instead of writing a key
// NetBox drops in silence.
//
// `installed_module` -- the reverse accessor of `dcim.Module.module_bay`'s
// `related_name='installed_module'`. It is in the write path and it is the wrong half of a
// one-to-one: `NetBoxModule.moduleBayRef` is the writable side and the one the operator owns.
// Read-only here rather than absent, for the reason dcim.Interface gives about `cable`: a bay
// that adopted an occupied slot must not PATCH the module out of it.
//
// `_occupied` -- a BooleanField the serializer computes and declares `read_only`
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleBaySerializer, `declared._occupied`).
//
// `_site`, `_location` and `_rack` -- the underscore-prefixed ComponentModel caches NetBox
// denormalises from the device (docs/netbox-schema.md -> dcim.ComponentModel). The IR records
// all three as absent from the write path, and `inventory_items` beside them is a
// GenericRelation.
func dcimModuleBayReadOnly() []string {
	return []string{
		"created", "last_updated", "url", "display",
		"parent", "installed_module", "_occupied",
		"_site", "_location", "_rack", "inventory_items",
	}
}
