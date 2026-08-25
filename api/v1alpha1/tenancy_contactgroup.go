package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxContactGroupSpec describes one tenancy.ContactGroup.
//
// The third NestedGroupModel to ship, and the one that shows the family has three different
// identities rather than two. dcim.Region and dcim.SiteGroup declare `(parent, name)` plus a
// `name WHERE parent IS NULL` variant; tenancy.TenantGroup declares no `meta.constraints` at
// all and puts column-level UNIQUE on `name` and `slug`. tenancy.ContactGroup is neither: it
// declares **one** constraint, `(parent, name)`, and no conditional variant at all
// (docs/netbox-schema.md -> tenancy.ContactGroup, `meta.constraints`;
// netbox/tenancy/models/contacts.py:53-58). Its `slug` carries no UNIQUE, because
// NestedGroupModel's does not (netbox/netbox/models/__init__.py:183-186) -- so `slug` cannot
// be this kind's identity the way it is tenancy.TenantGroup's.
//
// What follows from the missing conditional constraint is the interesting half, and it is
// stated on ParentRef below: two *top-level* contact groups with the same name are legal in
// NetBox, so the second lookup candidate is a convention rather than a constraint and an
// ambiguous match is a Conflict.
type NetBoxContactGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's label in the NetBox UI, and this kind's natural key.
	//
	// Unique *per parent* rather than globally (docs/netbox-schema.md ->
	// tenancy.ContactGroup, `meta.constraints`: `unique_parent_name` on `(parent, name)`).
	// It is the identity here and `slug` is not, which is the opposite of every
	// OrganizationalModel in the catalogue: NestedGroupModel's `slug` has no UNIQUE on it
	// and this model adds none, so two contact groups may share a slug and `slug` could
	// only ever adopt the wrong one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the group's URL-safe identifier.
	//
	// Required by NetBox (`slug SlugField REQ len=100`) and deliberately **not** a natural
	// key: it carries no UNIQUE, on this model or on NestedGroupModel. A rename that
	// collides with another group's slug under the same parent is NetBox's own 409, reported
	// as Ready=False/Invalid, rather than something the operator looks objects up by.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this group under another NetBoxContactGroup
	// (docs/netbox-schema.md -> tenancy.ContactGroup, `parent (NestedGroupModel)
	// TreeForeignKey -> tenancy.ContactGroup on_delete=CASCADE`).
	//
	// Part of this kind's identity, not an attribute of it: leaving it unset makes a
	// *top-level* group, which is a different lookup rather than the same one with a filter
	// dropped, so the second candidate pins `?parent_id=null` instead of omitting it
	// (docs/concepts/lookups.md). Declaring it and having it not resolve yet makes neither
	// candidate applicable, and the engine waits rather than adopting an unrelated top-level
	// group of the same name and reparenting it.
	//
	// Unlike dcim.Region and dcim.SiteGroup, **no conditional constraint backs that second
	// candidate**: NetBox has `(parent, name)` and nothing else, and Postgres treats NULLs
	// as distinct, so the constraint does not fire between two top-level groups. Two of them
	// may legitimately share a name, and more than one match is therefore a real server
	// state reported as a Conflict rather than resolved by taking the first.
	//
	// CASCADE, so deleting a group in NetBox deletes its descendants server-side -- which is
	// why this is the containment reference and a nested group carries an owner reference to
	// its parent (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	ParentRef *ContactGroupRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the group.
	//
	// Inherited from NestedGroupModel (docs/netbox-schema.md -> tenancy.ContactGroup,
	// `description (NestedGroupModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field.
	//
	// Also inherited from NestedGroupModel, and a TextField rather than a CharField: it has
	// no max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxContactGroup is one tenancy.ContactGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). A contact
// group is a shared catalogue object, so a cross-namespace name collision under one parent
// is one group and a Conflict rather than two -- `onConflict` already defaults to Fail.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcontactgroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxContactGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxContactGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus     `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxContactGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxContactGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxContactGroupList is a list of NetBoxContactGroup.
// +kubebuilder:object:root=true
type NetBoxContactGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxContactGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxContactGroup{}, &NetBoxContactGroupList{})
}
