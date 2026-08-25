package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxContactRoleSpec describes one tenancy.ContactRole.
//
// An OrganizationalModel with no columns of its own (docs/netbox-schema.md ->
// tenancy.ContactRole, "no own columns"), so every field here is inherited and the whole
// kind is `name`, `slug`, `description` and `comments`. Both `name` and `slug` carry
// column-level UNIQUE (netbox/netbox/models/__init__.py:227-236), which is what makes the
// identity a single global slug -- the opposite of NetBoxContactGroup next door, whose slug
// is not unique at all.
//
// What the role means is the third column of a contact assignment: NetBox's own
// `ContactAssignment.__str__` renders `contact (priority) -> object`, and the role is what
// lets the *same* contact be attached to the same object twice -- once as "Technical" and
// once as "Billing" (netbox/tenancy/models/contacts.py:160-163, the `unique_object_contact_role`
// constraint).
type NetBoxContactRoleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the role's label in the NetBox UI.
	//
	// Column-unique (docs/netbox-schema.md -> tenancy.ContactRole, `name
	// (OrganizationalModel) CharField REQ UNIQUE len=100`) and deliberately not the natural
	// key: a kind gets one identity and `slug` is the stable one, so a rename that collides
	// comes back as NetBox's own 409 reported as Ready=False/Invalid rather than being
	// adopted under the other candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the role's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique (`slug (OrganizationalModel) SlugField REQ UNIQUE len=100`), so one
	// slug is one role for the whole NetBox instance. Two NetBoxContactRoles in different
	// namespaces claiming one slug is one role and a Conflict, not two
	// (docs/decisions/0002-crd-scoping.md). `onConflict` defaults to Fail; on a shared
	// catalogue kind, leave it there.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the role.
	//
	// Inherited from OrganizationalModel (`description (OrganizationalModel) CharField
	// len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the role's long-form notes field.
	//
	// A TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxContactRole is one tenancy.ContactRole in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// A `roleRef` on a NetBoxContactAssignment is **not** a containment reference
// (docs/decisions/0003-ownership-and-references.md rule 4): `ContactAssignment.role` is
// `on_delete=PROTECT` (docs/netbox-schema.md -> tenancy.ContactAssignment), so deleting a
// role in NetBox does not cascade to the assignments that use it -- NetBox refuses the
// delete instead.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcontactrole
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxContactRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxContactRoleSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxContactRole) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxContactRole) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxContactRoleList is a list of NetBoxContactRole.
// +kubebuilder:object:root=true
type NetBoxContactRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxContactRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxContactRole{}, &NetBoxContactRoleList{})
}
