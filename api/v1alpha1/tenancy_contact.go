package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxContactSpec describes one tenancy.Contact.
//
// The kind whose identity **no database constraint backs at all**. tenancy.Contact declares
// no `meta.constraints` and only an index on `name` (docs/netbox-schema.md ->
// tenancy.Contact, `meta.indexes: (models.Index(fields=('name',)),)`;
// netbox/tenancy/models/contacts.py:114-120). Two contacts named "NOC" are legal in NetBox,
// so `name` is a *convention* the operator looks up by and an ambiguous match is reported as
// a Conflict rather than resolved by taking the first -- see docs/reference/netboxcontact.md.
//
// It is also the one kind in this group whose group relationship is a **many-to-many**.
// Everywhere else in NetBox a "group" is one foreign key; `Contact.groups` is
// `ManyToManyField -> tenancy.ContactGroup` (netbox/tenancy/models/contacts.py:71-76), so a
// contact may sit in several groups at once and the field is a list.
type NetBoxContactSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the contact's label in the NetBox UI, and this kind's lookup key.
	//
	// Not unique in any sense: no constraint, no column UNIQUE, only an index
	// (docs/netbox-schema.md -> tenancy.Contact, `name CharField REQ len=100`). It is used
	// as the natural key because a contact has nothing else to be identified by -- there is
	// no slug on this model -- and the consequence is explicit: two contacts of one name in
	// NetBox make the lookup ambiguous, and the CR reports Ready=False, Reason=Conflict
	// instead of adopting one of them (docs/concepts/lookups.md).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Groups files this contact under one or more NetBoxContactGroups
	// (docs/netbox-schema.md -> tenancy.Contact, `groups ManyToManyField ->
	// tenancy.ContactGroup`).
	//
	// A to-many reference, so the listed set *is* the set: NetBox replaces a many-to-many
	// wholesale on PATCH and has no add or remove verb. Three states, and all three are
	// instructions (docs/concepts/field-ownership.md) -- omitting the field leaves NetBox's
	// own groups alone, `[]` clears them, and a list replaces them. Order is not data: the
	// ids are sorted and deduplicated and the comparison is order-independent
	// (docs/concepts/drift.md), so reordering the list produces no write.
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of
	// the payload and the object reports RefsResolved=False naming the element that failed.
	// Writing only the ones that resolved would be a full-list replacement with a shorter
	// list -- a deletion, reported as a success.
	//
	// Not part of the identity, unlike `groupRef` on a NetBoxTenant: a many-to-many cannot
	// be, because there is no single value a lookup filter could take. `?group_id=` on
	// tenancy/contacts is a TreeNodeMultipleChoiceFilter over `groups`
	// (netbox/tenancy/filtersets.py:80-85), which matches membership rather than identity.
	//
	// MaxItems is not a NetBox limit: ObjectRef carries five CEL rules
	// (api/v1alpha1/objectref.go) and the API server costs a rule on a list item at the
	// list's maximum length, so an unbounded list of refs is refused outright. 256 is the
	// project standard (docs/concepts/references.md, "A list needs a bound").
	// +optional
	// +kubebuilder:validation:MaxItems=256
	Groups []ContactGroupRef `json:"groups,omitempty"`

	// Title is the contact's job title.
	//
	// `title CharField len=100`, blank-able (docs/netbox-schema.md -> tenancy.Contact).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Title string `json:"title,omitempty"`

	// Phone is the contact's telephone number, as free text.
	//
	// `phone CharField len=50`. NetBox applies no format validation to it and neither does
	// this field: an extension, a country code and a pager number are all legal values, and
	// a pattern here would reject values NetBox accepts.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Phone string `json:"phone,omitempty"`

	// Email is the contact's email address.
	//
	// `email EmailField` (docs/netbox-schema.md -> tenancy.Contact). The digest prints no
	// `len=` because the model passes no `max_length`, so the bound is Django's own
	// `EmailField` default of 254 -- which is the column width, so a longer value is a 400
	// from NetBox rather than something to discover later.
	//
	// No pattern. NetBox validates this server-side with Django's `EmailValidator`, and a
	// second-guessing regex in the CRD would reject addresses NetBox accepts -- while the
	// empty string, which is how the column is cleared, has to stay admissible.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=254
	// +optional
	Email string `json:"email,omitempty"`

	// Address is the contact's postal address.
	//
	// `address CharField len=200`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Address string `json:"address,omitempty"`

	// Link is a URL for the contact -- a directory entry, a ticket queue, a rota.
	//
	// `link URLField`. As with Email the digest prints no `len=`, so the bound is Django's
	// `URLField` default of 200, and validation is left to NetBox's own `URLValidator`
	// rather than duplicated as a pattern that would also have to admit `""`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Link string `json:"link,omitempty"`

	// Description is free text shown next to the contact.
	//
	// Inherited from PrimaryModel (`description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the contact's long-form notes field.
	//
	// Also inherited from PrimaryModel, and a TextField rather than a CharField: it has no
	// max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxContact is one tenancy.Contact in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which matters
// more here than on a kind NetBox keeps unique: `name` is not unique, so two namespaces
// declaring "NOC" produce *two* contacts on a fresh NetBox and an ambiguous lookup
// afterwards. Deciding that is what `onConflict` is for, and it defaults to Fail.
//
// A `contactRef` on a NetBoxContactAssignment is **not** a containment reference
// (docs/decisions/0003-ownership-and-references.md rule 4): `ContactAssignment.contact` is
// `on_delete=PROTECT` (docs/netbox-schema.md -> tenancy.ContactAssignment), so deleting a
// contact does not cascade to its assignments -- NetBox refuses the delete while any
// assignment still names it. Delete the NetBoxContactAssignment CRs first.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcontact
// +kubebuilder:printcolumn:name="Contact",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxContact struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxContactSpec  `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxContact) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxContact) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxContactList is a list of NetBoxContact.
// +kubebuilder:object:root=true
type NetBoxContactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxContact `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxContact{}, &NetBoxContactList{})
}
