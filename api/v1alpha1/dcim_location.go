package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocationStatus is one value of NetBox's LocationStatusChoices.
//
// docs/netbox-schema.md -> dcim.Location records the column as
// `status CharField len=50 def=UNRESOLVED:LocationStatusChoices.STATUS_ACTIVE
// choices=LocationStatusChoices` -- the choice *class*, not its members, because the AST walk
// cannot evaluate one (NBO-041 ingests NetBox's OpenAPI schema precisely to settle them).
// These five are read from `netbox/dcim/choices.py`, `LocationStatusChoices`, in the same
// 4.6.8 tree the digest was taken from, and are the set the ticket states.
//
// The same five values as SiteStatus and deliberately not the same Go type: they are two
// separate ChoiceSets in NetBox, both user-extensible through `FIELD_CHOICES`, so sharing
// one enum would make a value added to one of them silently legal on the other.
//
// +kubebuilder:validation:Enum=planned;staging;active;decommissioning;retired
type LocationStatus string

const (
	// LocationStatusPlanned is a location that does not physically exist yet.
	LocationStatusPlanned LocationStatus = "planned"

	// LocationStatusStaging is a location being built out.
	LocationStatusStaging LocationStatus = "staging"

	// LocationStatusActive is a location in service, and NetBox's own default.
	LocationStatusActive LocationStatus = "active"

	// LocationStatusDecommissioning is a location being retired.
	LocationStatusDecommissioning LocationStatus = "decommissioning"

	// LocationStatusRetired is a location no longer in service.
	LocationStatusRetired LocationStatus = "retired"
)

// NetBoxLocationSpec describes one dcim.Location.
//
// A NestedGroupModel like dcim.Region and dcim.SiteGroup, and the one with a **required**
// foreign key: `site ForeignKey REQ -> dcim.Site on_delete=CASCADE`
// (docs/netbox-schema.md -> dcim.Location). That is what makes its identity a pair of
// references rather than one -- both natural keys start at `site`, so a location cannot be
// looked up at all until its site resolves.
//
// dcim.Location also carries a `tenant` foreign key, which is deliberately absent here:
// TenantRef's Kind is NBO-021 and the field belongs to that ticket rather than this one. A
// field that is accepted and does nothing is worse than a field that is not there, because
// `kubectl apply` reports success and NetBox never sees the value.
type NetBoxLocationSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the location's name.
	//
	// Unique per (site, parent) rather than globally
	// (docs/netbox-schema.md -> dcim.Location.meta.constraints), with a separate constraint
	// for top-level locations within a site. Two rooms called "Ground floor" in two different
	// sites are a legitimate NetBox state, not a collision.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the location's URL-safe identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// SiteRef is the site this location is part of. Required, because NetBox's column is
	// (`site ForeignKey REQ`) and there is no such thing as a location outside a site.
	//
	// It is also this kind's containment reference, declared as such on the Descriptor
	// (internal/registry/dcim_location.go, ContainmentRef): NetBox deletes a site's locations
	// with it (`on_delete=CASCADE`), so the site is the containment parent under
	// docs/decisions/0003-ownership-and-references.md rule 4, and exactly one field may be --
	// which is why `parentRef` is not, even though `parent` is `CASCADE` too. The Descriptor
	// carries the whole tiebreak; the short version is that `site` is the required FK, so it
	// is the one every location has.
	//
	// Until it resolves, no natural-key candidate is applicable -- both start at `site` --
	// so the object reports RefsResolved=False naming this field and makes no NetBox write
	// at all, rather than creating a location in the wrong site.
	SiteRef SiteRef `json:"siteRef"`

	// ParentRef nests this location under another one in the same site.
	//
	// Self-referential (`parent -> self`). Leaving it unset makes a top-level location
	// within the site, which is a different natural key rather than the same key with a
	// field omitted -- see docs/concepts/lookups.md on why a null filter is pinned.
	// +optional
	ParentRef *LocationRef `json:"parentRef,omitempty"`

	// Status is the location's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status LocationStatus `json:"status,omitempty"`

	// Facility is the building or room designation within the site
	// (docs/netbox-schema.md -> dcim.Location, `facility CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Facility string `json:"facility,omitempty"`

	// Description is free text shown next to the location.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxLocation is one dcim.Location in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and the kind
// ADR-0002 singles out: it reads like a catalogue object, so a cluster-scoped CRD looks
// right, but `Location.site` is required and a cluster-scoped location pointing at a
// namespaced site would be a reference nobody could authorise.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nblocation
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.siteRef.name`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxLocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxLocationSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (l *NetBoxLocation) NetBoxSpec() *NetBoxObjectSpec { return &l.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (l *NetBoxLocation) NetBoxStatus() *NetBoxObjectStatus { return &l.Status }

// NetBoxLocationList is a list of NetBoxLocation.
// +kubebuilder:object:root=true
type NetBoxLocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxLocation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxLocation{}, &NetBoxLocationList{})
}
