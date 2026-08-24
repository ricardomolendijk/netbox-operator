package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxDeviceRoleSpec describes one dcim.DeviceRole.
//
// The role both `dcim.Device.role` and `virtualization.VirtualMachine.role` point at
// (docs/netbox-schema.md): there is no virtualization-specific role model, which is what
// `vm_role` on this one is for.
//
// A `NestedGroupModel` in 4.6.8 -- it gained a `parent` -- and one whose `meta.constraints`
// really do scope uniqueness by that parent, unlike tenancy.TenantGroup's, which has no
// constraints at all. Both halves matter and they are read off the constraint list rather
// than off the base class; see Slug.
type NetBoxDeviceRoleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the role's name, as NetBox displays it.
	//
	// Inherited from NestedGroupModel (docs/netbox-schema.md -> dcim.DeviceRole, `name
	// (NestedGroupModel) CharField REQ len=100`) and **not** column-unique: two of the
	// model's four constraints are on it, `(parent, name)` and `(name) WHERE parent IS
	// NULL`, so two roles may share a name under different parents.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the role's URL-safe identifier, and this kind's natural key.
	//
	// Unique *per parent* rather than globally (docs/netbox-schema.md -> dcim.DeviceRole,
	// `meta.constraints`: `..._parent_slug` on `(parent, slug)`, plus `..._slug` on `(slug)`
	// with `condition=Q(parent__isnull=True)`). That pair is why this kind has two
	// natural-key candidates and why the second pins `parent_id__isnull=true` instead of
	// dropping the filter (docs/concepts/lookups.md).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this role under another one
	// (docs/netbox-schema.md -> dcim.DeviceRole, `parent (NestedGroupModel) TreeForeignKey
	// -> dcim.DeviceRole on_delete=CASCADE`).
	//
	// Part of this kind's identity, not just an attribute of it: leaving it unset makes a
	// *top-level* role, which is a different natural key rather than the same key with a
	// filter omitted. Declaring it and having it not resolve yet makes neither candidate
	// applicable, and the engine waits rather than adopting an unrelated top-level role of
	// the same slug and then reparenting it.
	// +optional
	ParentRef *DeviceRoleRef `json:"parentRef,omitempty"`

	// Color is the role's colour as six hexadecimal digits, without a leading `#`.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct (docs/netbox-schema.md -> dcim.DeviceRole, `color ColorField
	// def=UNRESOLVED:ColorChoices.COLOR_GREY`, which is `9e9e9e`).
	// +kubebuilder:default="9e9e9e"
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{6}$`
	// +optional
	Color string `json:"color,omitempty"`

	// VMRole makes this role assignable to virtual machines as well as to devices
	// (docs/netbox-schema.md -> dcim.DeviceRole, `vm_role BooleanField def=True`).
	//
	// A pointer, and the reason is the column's default. NetBox defaults it to true, so a
	// plain `bool` cannot tell "not managed" from "managed as false" and adopting a
	// hand-made role would silently take it away from every VM using it. Nil leaves
	// NetBox's value -- and therefore its `true` -- alone; `false` writes false.
	// +optional
	VMRole *bool `json:"vmRole,omitempty"`

	// Description is free text shown next to the role.
	//
	// Inherited from NestedGroupModel (docs/netbox-schema.md -> dcim.DeviceRole,
	// `description (NestedGroupModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxDeviceRole is one dcim.DeviceRole in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). A `roleRef`
// from a team namespace into the catalogue namespace is a cross-namespace reference and needs
// a NetBoxRefGrant -- see docs/reference/netboxdevicerole.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbdrole
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxDeviceRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxDeviceRoleSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxDeviceRole) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxDeviceRole) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxDeviceRoleList is a list of NetBoxDeviceRole.
// +kubebuilder:object:root=true
type NetBoxDeviceRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxDeviceRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxDeviceRole{}, &NetBoxDeviceRoleList{})
}
