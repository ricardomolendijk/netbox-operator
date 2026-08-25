package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRoleSpec describes one ipam.Role: what a prefix, a VLAN or an IP range is *for* --
// management, guest, IoT, point-to-point.
//
// **`ipam.Role` is not `dcim.DeviceRole`.** They are two separate Django models with two
// separate endpoints (`ipam/roles` and `dcim/device-roles`, docs/netbox-schema.md endpoint
// map) and nothing in common but the word. `RoleRef` targets this Kind;
// `DeviceRoleRef` targets the other one. And neither is `NetBoxIPAddress.role`, which is a
// *choice column* of the same name on `ipam.IPAddress` (docs/netbox-schema.md ->
// ipam.IPAddress, `role CharField len=50 choices=IPAddressRoleChoices`) -- the three-way
// near-miss this comment exists for.
//
// An OrganizationalModel with exactly one column of its own: `weight
// PositiveSmallIntegerField def=1000` (docs/netbox-schema.md -> ipam.Role).
type NetBoxRoleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the role's label -- `Management`, `Guest`, `IoT`.
	//
	// Column-unique, and deliberately not the natural key: `slug` is.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the role's URL-safe identifier, and its natural key.
	//
	// This is the value a `slug`-mode `roleRef` on a NetBoxPrefix, a NetBoxVLAN or a
	// NetBoxIPRange resolves against, which is why it is the identity rather than `name`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Weight orders roles in NetBox's own lists, lowest first
	// (docs/netbox-schema.md -> ipam.Role, `weight PositiveSmallIntegerField def=1000`, and
	// `meta.ordering: ('weight', 'name')`).
	//
	// A pointer rather than a defaulted value, for the reason `isPool` on NetBoxPrefix is
	// one: the column has a Django default, so a plain `int32` cannot tell "not managed"
	// from "managed as 0", and adopting a role a human had ordered would reset it to 0 on
	// the first reconcile. Nil leaves NetBox's value alone.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Weight *int32 `json:"weight,omitempty"`

	// Description is free text shown next to the role. Inherited from OrganizationalModel
	// (docs/netbox-schema.md -> ipam.Role, `description (OrganizationalModel) CharField
	// len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the role's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRole is one ipam.Role in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrole
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Weight",type=integer,JSONPath=`.spec.weight`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRoleSpec     `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRole) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRole) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRoleList is a list of NetBoxRole.
// +kubebuilder:object:root=true
type NetBoxRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRole{}, &NetBoxRoleList{})
}
