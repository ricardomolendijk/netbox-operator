package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SiteStatus is one value of NetBox's SiteStatusChoices.
//
// docs/netbox-schema.md -> dcim.Site records the column as
// `status CharField len=50 def='SiteStatusChoices.STATUS_ACTIVE' choices=SiteStatusChoices`
// -- the choice *class*, not its members. The AST walk cannot evaluate a Django choice
// class, so the digest never lists the values behind one (NBO-041, which ingests NetBox's
// OpenAPI schema precisely to settle them); these five are read from
// `netbox/dcim/choices.py`, `SiteStatusChoices`, in the 4.6.8 tree the digest was taken
// from.
//
// +kubebuilder:validation:Enum=planned;staging;active;decommissioning;retired
type SiteStatus string

const (
	// SiteStatusPlanned is a site that does not physically exist yet.
	SiteStatusPlanned SiteStatus = "planned"

	// SiteStatusStaging is a site being built out.
	SiteStatusStaging SiteStatus = "staging"

	// SiteStatusActive is a site in service, and NetBox's own default.
	SiteStatusActive SiteStatus = "active"

	// SiteStatusDecommissioning is a site being retired.
	SiteStatusDecommissioning SiteStatus = "decommissioning"

	// SiteStatusRetired is a site no longer in service.
	SiteStatusRetired SiteStatus = "retired"
)

// NetBoxSiteSpec describes one dcim.Site.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary
// spec fields that a user writes alongside the rest -- and the engine excludes exactly
// those from the NetBox payload by reflecting over that struct.
//
// Every optional foreign key dcim.Site has -- `region`, `group`, `tenant` and the `asns`
// many-to-many (docs/netbox-schema.md -> dcim.Site) -- is still absent from this Kind, but
// no longer for want of a resolver: internal/resolver landed in M2 and resolves references,
// including to-many ones. What is left is per-field work on this Kind, and it waits on the
// Kinds each field points at -- `group` on NetBoxSiteGroup (NBO-066), `tenant` on
// NetBoxTenant (NBO-021), `asns` on NetBoxASN (NBO-055) -- all of which now ship, so what is
// left is the per-field work on this Kind and nothing blocks it. A field that is accepted and
// does nothing is worse than a field that is not there: `kubectl apply` reports success and
// NetBox never sees the value, which is why each one waits for its own change rather than
// arriving with the Kind it points at (docs/coverage.md records all four as gaps).
//
// `tags` is the exception that is only waiting on this Kind: NetBoxTag exists, and the
// digest now carries the row to cite it against (`tags (TagsMixin) NetBoxTaggableManagerField
// M2M -> extras.Tag`), which it did not until the extractor was fixed to match NetBox's real
// manager class.
type NetBoxSiteSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the site's label in the NetBox UI. Column-unique across NetBox
	// (docs/netbox-schema.md -> dcim.Site, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the site's URL-safe identifier, and this kind's natural key.
	//
	// NetBox enforces uniqueness on it globally (docs/netbox-schema.md -> dcim.Site,
	// `slug SlugField REQ UNIQUE len=100`) while this CRD is namespaced
	// (docs/decisions/0002-crd-scoping.md), so two NetBoxSites in different namespaces
	// claiming one slug is one site and a Conflict -- not two sites.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Status is the site's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status SiteStatus `json:"status,omitempty"`

	// Facility is the data-centre or campus designation within the site
	// (docs/netbox-schema.md -> dcim.Site, `facility CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Facility string `json:"facility,omitempty"`

	// PhysicalAddress is the site's street address
	// (docs/netbox-schema.md -> dcim.Site, `physical_address CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	PhysicalAddress string `json:"physicalAddress,omitempty"`

	// ShippingAddress is where equipment for this site is delivered
	// (docs/netbox-schema.md -> dcim.Site, `shipping_address CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	ShippingAddress string `json:"shippingAddress,omitempty"`

	// Latitude is the site's GPS latitude in decimal degrees, as a string.
	//
	// A string and not a float64: NetBox stores it as a DecimalField and returns it as a
	// string, and an OpenAPI `number` round-trips through IEEE-754 on its way in and out of
	// the API server, so the sixth decimal place -- roughly a tenth of a metre -- is not
	// reliably what was written. The engine compares it numerically
	// (internal/netbox/drift.go, scalarEqual), so `"51.9244"` and NetBox's `"51.924400"`
	// are the same value and produce no PATCH.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). A cleared coordinate is written as `null` rather
	// than as an empty string, which is what NetBox's nullable DecimalField takes -- see
	// registry.Field.EmptyIsNull.
	//
	// The pattern caps the fraction at six digits and the integer part at two, both read
	// straight off docs/netbox-schema.md -> dcim.Site: `latitude DecimalField decimal(8,6)`,
	// so eight digits total with six after the point. (`longitude` is decimal(9,6), which is
	// why its pattern allows three integer digits and this one does not.) The `^$` alternative
	// is the clear, and the CEL rule has to admit it too: `double("")` is an error, so a rule
	// that did not short-circuit would reject at admission the one value clearing uses (#170).
	// The -90..90 range is the tighter constraint on top.
	// +kubebuilder:validation:Pattern=`^$|^-?[0-9]{1,2}(\.[0-9]{1,6})?$`
	// +kubebuilder:validation:XValidation:rule="self == \"\" || (double(self) >= -90.0 && double(self) <= 90.0)",message="latitude must be between -90 and 90 degrees"
	// +optional
	Latitude string `json:"latitude,omitempty"`

	// Longitude is the site's GPS longitude in decimal degrees, as a string. A string for
	// the same reason as Latitude.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). Cleared as `null`, for the reason Latitude gives.
	//
	// Three integer digits, not two: longitude runs to +-180. NBO-009 states both columns
	// as `decimal(8,6)`, which would cap this at 99.999999 and reject every longitude east
	// of Taipei; docs/netbox-schema.md records no precision either way (NBO-071), so the
	// permissive reading is taken deliberately. If NetBox really does refuse three integer
	// digits here it says so, and the site reports Ready=False, Reason=Invalid carrying
	// NetBox's own message -- which is a great deal easier to diagnose than an admission
	// rejection of a correct value.
	// +kubebuilder:validation:Pattern=`^$|^-?[0-9]{1,3}(\.[0-9]{1,6})?$`
	// +kubebuilder:validation:XValidation:rule="self == \"\" || (double(self) >= -180.0 && double(self) <= 180.0)",message="longitude must be between -180 and 180 degrees"
	// +optional
	Longitude string `json:"longitude,omitempty"`

	// Description is free text shown next to the site.
	//
	// Declared on PrimaryModel rather than on dcim.Site, so docs/netbox-schema.md does not
	// list it under `dcim.Site` -- see that file's preamble on inherited columns, which are
	// as required and as writable as declared ones.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the site's long-form notes field.
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

// NetBoxSite is one dcim.Site in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which is
// what makes a cross-namespace `slug` collision possible: NetBox's uniqueness on the column
// is global and a namespace boundary does not partition it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsite
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxSite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxSiteSpec     `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec. One of the two methods that are
// the whole of the per-kind code the engine needs.
func (s *NetBoxSite) NetBoxSpec() *NetBoxObjectSpec { return &s.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (s *NetBoxSite) NetBoxStatus() *NetBoxObjectStatus { return &s.Status }

// NetBoxSiteList is a list of NetBoxSite.
// +kubebuilder:object:root=true
type NetBoxSiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxSite `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxSite{}, &NetBoxSiteList{})
}
