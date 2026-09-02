package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxPowerPanelSpec describes one dcim.PowerPanel.
//
// The upstream end of the power path: a distribution panel in a site, from which
// NetBoxPowerFeed hangs. Four writable columns of substance and one real database-backed
// identity, which makes it the plainest kind in NBO-052 and the reason it leads the block.
//
//	meta.constraints: (models.UniqueConstraint(fields=('site', 'name'),
//	   name='%(app_label)s_%(class)s_unique_site_name'),)
//
// (docs/netbox-schema.md -> dcim.PowerPanel; hack/testdata/ir-4.6.8.json.gz ->
// dcim.PowerPanel.natural_keys, which records the same pair with the filters `site_id` and
// `name`.) So `(site, name)` is a constraint the database enforces rather than a convention,
// unlike dcim.Rack's second candidate in NBO-051 -- there is no null to pin and no ambiguous
// match to report, because Postgres will not let a second row exist.
//
// **`location` is not in the key.** It is optional and NetBox constrains nothing on it, so two
// panels of one name in one site are refused however their locations differ, and moving a panel
// between rooms does not change what this CR is looking for. That is the opposite of dcim.Rack,
// whose constraints are all keyed on the optional column; the two kinds sit one source file
// apart and the difference is worth reading twice.
type NetBoxPowerPanelSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the panel's name.
	//
	// Unique per *site* (docs/netbox-schema.md -> dcim.PowerPanel.meta.constraints), so two
	// panels called `Panel A` in two sites are legitimate NetBox state and two in one site are
	// not, whatever their locations.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// SiteRef is the site this panel stands in. Required, because NetBox's column is
	// (`site ForeignKey REQ -> dcim.Site on_delete=PROTECT`) and there is no such thing as a
	// panel outside a site.
	//
	// The natural key reads it, so until it resolves the object reports RefsResolved=False
	// naming this field and makes no NetBox write at all -- rather than creating a panel in
	// the wrong site, or adopting a same-named panel in one.
	//
	// It is **not** this kind's containment reference, and cannot be: `PROTECT` means NetBox
	// refuses to delete a site while a panel points at it, so there is no server-side deletion
	// for an owner reference to mirror (docs/decisions/0003-ownership-and-references.md rule
	// 4). Deleting the site CR is refused on the *site*, as `Deleting=False, Reason=Protected`.
	SiteRef SiteRef `json:"siteRef"`

	// LocationRef is the room or row within the site the panel stands in
	// (docs/netbox-schema.md -> dcim.PowerPanel, `location ForeignKey -> dcim.Location
	// on_delete=PROTECT`).
	//
	// Optional, and -- unlike the identically spelled field on NetBoxRack -- it carries no
	// weight in the identity at all: `dcim.PowerPanel`'s one constraint is `(site, name)`, so
	// this is a label rather than half a key. Setting it, clearing it or never declaring it
	// changes nothing about which NetBox row this CR resolves to.
	//
	// `PROTECT` here where dcim.Rack's `location` is `SET_NULL`, so it is not a containment
	// parent either, for the reason SiteRef gives.
	//
	// A pointer to the typed alias, so it has two states rather than three: absent means
	// unmanaged, and a value claims the column. Clearing the column from a manifest needs
	// registry.EmptyIsNull and a v1alpha1.OptionalRef -- a third state no shipped kind uses
	// yet (#185). Until then the way to move a panel out of a location is to clear it in
	// NetBox and stop declaring the field.
	// +optional
	LocationRef *LocationRef `json:"locationRef,omitempty"`

	// Description is free text shown next to the panel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the panel's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Clearable on the same three-state terms as Description.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxPowerPanel is one dcim.PowerPanel in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Absent deliberately:
//
//   - **No containment parent.** `site` and `location` are both `PROTECT`
//     (docs/netbox-schema.md -> dcim.PowerPanel), so nothing on the server side disappears
//     when either goes and there is nothing for an owner reference to mirror
//     (docs/decisions/0003-ownership-and-references.md rule 4).
//   - `powerfeed_count` is a counter the serializer returns and the API refuses
//     (hack/testdata/ir-4.6.8.json.gz -> dcim.PowerPanel.write_path, where it sits beside a
//     `RelatedObjectCountField` declaration). Declared read-only on the Descriptor.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind
//     (hack/coverage-exclusions.yaml).
//   - `contacts` and `images` are GenericRelations -- the far end of somebody else's foreign
//     key. A panel is a legal `ContactAssignment` target through the union on
//     NetBoxContactAssignment, written from the *other* object.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbpowerpanel
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.siteRef.name`
// +kubebuilder:printcolumn:name="Location",type=string,JSONPath=`.spec.locationRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxPowerPanel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxPowerPanelSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxPowerPanel) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxPowerPanel) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxPowerPanelList is a list of NetBoxPowerPanel.
// +kubebuilder:object:root=true
type NetBoxPowerPanelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxPowerPanel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxPowerPanel{}, &NetBoxPowerPanelList{})
}
