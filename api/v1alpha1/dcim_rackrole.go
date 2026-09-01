package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRackRoleSpec describes one dcim.RackRole.
//
// The functional classification of a rack -- `Compute`, `Network`, `Storage` -- and the
// plainest kind NBO-051 ships. An `OrganizationalModel` with exactly one column of its own,
// `color ColorField def=UNRESOLVED:ColorChoices.COLOR_GREY`
// (docs/netbox-schema.md -> dcim.RackRole), so it is dcim.DeviceRole minus the self-reference
// and minus `vm_role`.
//
// `dcim.RackRole` declares **no** `meta.constraints`, so the identity comes from the base
// class instead: `OrganizationalModel.slug` carries a column-level `UNIQUE`. That is the same
// derivation tenancy.ContactRole and dcim.Manufacturer use, and the opposite of what a
// `NestedGroupModel` gets -- see docs/reference/netboxcontactrole.md, which is the page that
// spells out why the base class and not the app decides.
type NetBoxRackRoleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the role's name.
	//
	// Globally unique (`name CharField REQ UNIQUE len=100`), and deliberately not this
	// kind's natural key: a kind gets one identity and `slug` is the stable one, so a rename
	// that collides comes back as NetBox's own 409 rather than being adopted under a second
	// candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the role's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (docs/netbox-schema.md -> dcim.RackRole, `slug
	// SlugField REQ UNIQUE len=100`), so two namespaces claiming `compute` are claiming one
	// role and the second reports Ready=False, Reason=Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Color is the role's colour as six hexadecimal digits, without a leading `#`.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct (docs/netbox-schema.md -> dcim.RackRole, `color ColorField
	// def=UNRESOLVED:ColorChoices.COLOR_GREY`, which is `9e9e9e`).
	// +kubebuilder:default="9e9e9e"
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{6}$`
	// +optional
	Color string `json:"color,omitempty"`

	// Description is free text shown next to the role.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the role's long-form notes field.
	//
	// A TextField rather than a CharField (docs/netbox-schema.md -> dcim.RackRole, `comments
	// (OrganizationalModel) TextField`): it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRackRole is one dcim.RackRole in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// `owner` is absent, as it is on every kind so far: it is `ForeignKey -> users.Owner`
// (docs/netbox-schema.md -> dcim.RackRole) and the whole `users` app is deferred, so there is
// no Kind to point at and a field that resolved to nothing would report success while writing
// nothing.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrackrole
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Color",type=string,JSONPath=`.spec.color`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRackRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRackRoleSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRackRole) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRackRole) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRackRoleList is a list of NetBoxRackRole.
// +kubebuilder:object:root=true
type NetBoxRackRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRackRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRackRole{}, &NetBoxRackRoleList{})
}
