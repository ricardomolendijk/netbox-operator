package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxModuleBaySpec describes one dcim.ModuleBay.
//
// The slot a module goes into. A `dcim.ModularComponentModel` like every other device
// component, which is what gives it both `device` and `module`
// (docs/netbox-schema.md -> dcim.ModuleBay, bases) -- a bay belongs to a device directly, or
// to a module installed in that device, and the second is how NetBox represents a line card
// that provides slots of its own.
//
// Its `meta.constraints` is `UniqueConstraint(fields=('device', 'module', 'name'))`, and
// `module` in it is nullable. Postgres treats NULLs as distinct, so the constraint does not
// in fact stop two identically named bays on one device with no module -- the identity
// therefore falls back to `(device, name)` with `module_id` pinned null, a convention rather
// than a constraint. See internal/registry/dcim_modulebay.go.
type NetBoxModuleBaySpec struct {
	NetBoxObjectSpec `json:",inline"`

	// DeviceRef is the device this bay is on. Required, because NetBox's column is
	// (`device (ComponentModel) ForeignKey REQ -> dcim.Device on_delete=CASCADE`).
	//
	// It is part of both natural keys, so until it resolves no candidate applies and the
	// object writes nothing at all.
	//
	// CASCADE, so this is the containment parent: deleting the NetBoxDevice deletes the bay's
	// CR with it (docs/decisions/0003-ownership-and-references.md rule 4).
	DeviceRef DeviceRef `json:"deviceRef"`

	// Name is the bay's name, unique per `(device, module)`
	// (docs/netbox-schema.md -> dcim.ComponentModel, `name CharField REQ len=64`).
	//
	// Matched exactly rather than case-insensitively. Unlike dcim.Device, whose constraints
	// are declared over `Lower('name')`, `dcim.ComponentModel` declares a plain column, so
	// `Slot1` and `slot1` are two bays to NetBox and must be two to the operator.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// ModuleRef is the module that *provides* this bay, when the bay is a slot on a line card
	// rather than on the chassis (docs/netbox-schema.md -> dcim.ModularComponentModel,
	// `module ForeignKey -> dcim.Module on_delete=CASCADE`).
	//
	// Not the module *installed in* this bay -- that is the other direction, and it is
	// `NetBoxModule.moduleBayRef`. The two are easy to swap and the schema tells them apart:
	// `Module.module_bay` is a `OneToOneField` with `related_name='installed_module'`, so a
	// bay's occupant is a reverse accessor the operator never writes, while `module` here is
	// a forward foreign key it does.
	//
	// Optional, and load-bearing on the identity: it is the third column of NetBox's unique
	// constraint. Declared and unresolved, it makes *both* candidates inapplicable and the
	// engine waits -- which is the point. Falling through to the `module_id IS NULL`
	// convention would find a chassis bay of the same name on the same device and adopt it,
	// and the follow-up PATCH would move somebody else's bay onto this module (NBO-015).
	// +optional
	ModuleRef *ModuleRef `json:"moduleRef,omitempty"`

	// Label is a physical label on the bay, distinct from its name
	// (docs/netbox-schema.md -> dcim.ComponentModel, `label CharField len=64`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Label string `json:"label,omitempty"`

	// Position is the slot identifier, and NetBox uses it for more than display: it is what
	// the `{module}` token in a module type's component templates is substituted with when a
	// module installed here has its components instantiated
	// (`ModuleBay.position`'s help text, NetBox 4.6.8
	// `netbox/dcim/models/device_components.py`; the column itself is
	// `position CharField len=30`, docs/netbox-schema.md -> dcim.ModuleBay).
	//
	// A string and not a number: NetBox's column is a CharField, and slot identifiers are
	// routinely `A1` or `0/1`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear it.
	// +kubebuilder:validation:MaxLength=30
	// +optional
	Position string `json:"position,omitempty"`

	// Enabled is whether the bay may take a module
	// (docs/netbox-schema.md -> dcim.ModuleBay, `enabled BooleanField def=True`).
	//
	// A pointer, and the reason is the column's `def=True`. A plain bool cannot tell "not
	// managed" from "managed as false", so adopting a bay a human had disabled would silently
	// re-enable it on the first reconcile. Nil leaves NetBox's value alone; `false` writes
	// false (docs/concepts/field-ownership.md).
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Description is free text shown next to the bay.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxModuleBay is one dcim.ModuleBay in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// It has no `comments`: `dcim.ComponentModel` is a plain `NetBoxModel` rather than a
// `PrimaryModel` (docs/netbox-schema.md -> dcim.ComponentModel, bases), so the writable
// long-form column dcim.ModuleType has does not exist here.
//
// Absent deliberately:
//
//   - `parent`, the MPTT self-reference. It is **not in the serializer's write path**
//     (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleBay.write_path lists `device`, `module`,
//     `name`, `label`, `position`, `enabled`, `description`, `installed_module` and no
//     `parent`), because NetBox derives it from `module.module_bay` rather than accepting
//     it. A `parentRef` here would be a field the API drops in silence. It is read-only on
//     the Descriptor rather than merely unmapped, so a later edit that reaches for it fails
//     the boot (registry.ErrFieldReadOnly).
//   - `installed_module`, the reverse accessor of `dcim.Module.module_bay`'s
//     `related_name`. The forward half is `NetBoxModule.moduleBayRef` and it is the writable
//     one; two writers for one relation is the mutual-FK mistake, and here the schema already
//     says which side owns it.
//   - `_occupied`, a read-only BooleanField the serializer computes.
//   - `_site`, `_location` and `_rack`, ComponentModel caches denormalised from the device.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind.
//   - `inventory_items`, `bookmarks`, `journal_entries` and `subscriptions` are
//     GenericRelations, not columns.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmodulebay
// +kubebuilder:printcolumn:name="Device",type=string,JSONPath=`.spec.deviceRef.name`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Position",type=string,JSONPath=`.spec.position`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxModuleBay struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxModuleBaySpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (b *NetBoxModuleBay) NetBoxSpec() *NetBoxObjectSpec { return &b.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (b *NetBoxModuleBay) NetBoxStatus() *NetBoxObjectStatus { return &b.Status }

// NetBoxModuleBayList is a list of NetBoxModuleBay.
// +kubebuilder:object:root=true
type NetBoxModuleBayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxModuleBay `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxModuleBay{}, &NetBoxModuleBayList{})
}
