package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRackGroupSpec describes one dcim.RackGroup.
//
// The kind whose name is the whole trap. Every other `*Group` in the catalogue so far --
// dcim.SiteGroup, tenancy.TenantGroup, tenancy.ContactGroup -- is a `NestedGroupModel` with a
// self-referential `parent` and a `(parent, name)` identity. `dcim.RackGroup` is **not**:
//
//	## dcim.RackGroup   (dcim/models/racks.py)
//	   bases: OrganizationalModel
//	   (no own columns — every field is inherited from OrganizationalModel)
//	     name (OrganizationalModel)  CharField  REQ UNIQUE len=100
//	     slug (OrganizationalModel)  SlugField  REQ UNIQUE len=100
//	   meta.ordering: ('name',)
//
// (docs/netbox-schema.md -> dcim.RackGroup.) There is no `parent` column, no MPTT base, no
// `site` column and no `meta.constraints`, and the serializer's write path confirms it --
// `('id', 'url', 'display_url', 'display', 'name', 'slug', 'description', 'owner',
// 'comments', 'tags', 'custom_fields', 'created', 'last_updated', 'rack_count')`, with no
// `parent` in it (hack/testdata/ir-4.6.8.json.gz -> dcim.RackGroup.write_path).
//
// Three consequences, all of them things this kind therefore does *not* have: no `parentRef`,
// no `parent IS NULL` natural-key variant, and no MPTT cycle check for a webhook to make. Its
// identity is `slug` alone, from the base class's column-level `UNIQUE`, exactly as
// dcim.Manufacturer's and tenancy.ContactRole's are.
//
// A rack group is a flat label rather than a tree: `Cage 1`, `DMZ`, `Overflow`.
type NetBoxRackGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's name.
	//
	// Globally unique (`name CharField REQ UNIQUE len=100`), and not this kind's natural key
	// for the reason NetBoxRackRole.Name gives: a kind gets one identity and `slug` is the
	// stable one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the group's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (docs/netbox-schema.md -> dcim.RackGroup, `slug
	// SlugField REQ UNIQUE len=100`) -- which it is *because* the base class is
	// `OrganizationalModel`. `NestedGroupModel.slug` carries no `UNIQUE`, which is why
	// NetBoxContactGroup two files over cannot use `slug` at all.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the group.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRackGroup is one dcim.RackGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// `owner` is absent for the reason it is absent everywhere: `ForeignKey -> users.Owner`, and
// the `users` app has no Kind.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrackgroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRackGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRackGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxRackGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxRackGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxRackGroupList is a list of NetBoxRackGroup.
// +kubebuilder:object:root=true
type NetBoxRackGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRackGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRackGroup{}, &NetBoxRackGroupList{})
}
