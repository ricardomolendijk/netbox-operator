package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxManufacturerSpec describes one dcim.Manufacturer.
//
// The plainest kind in the catalogue, and the one every other kind in NBO-027 leans on:
// `dcim.DeviceType.manufacturer` is a required foreign key and `dcim.Platform.manufacturer`
// is what that model's uniqueness is scoped by (docs/netbox-schema.md -> dcim.DeviceType,
// dcim.Platform), so nothing else here reconciles until a manufacturer does.
//
// Every column is inherited. `dcim.Manufacturer` declares none of its own -- the digest
// entry says so in as many words, `(no own columns -- every field is inherited from
// ContactsMixin, OrganizationalModel)` -- and `OrganizationalModel` gives `name`, `slug`
// and `description`. ContactsMixin contributes only reverse relations.
type NetBoxManufacturerSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the manufacturer's name, as NetBox displays it.
	//
	// Column-unique, unlike the nested-group kinds: `name (OrganizationalModel) CharField
	// REQ UNIQUE len=100` (docs/netbox-schema.md -> dcim.Manufacturer). It is a candidate
	// key and deliberately not the lookup key -- a kind gets one identity and `slug` is the
	// stable one -- so a rename that collides comes back as NetBox's own 409 reported as
	// Ready=False/Invalid rather than being adopted under the other candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the manufacturer's URL-safe identifier, and this kind's natural key.
	//
	// `slug (OrganizationalModel) SlugField REQ UNIQUE len=100`, and `dcim.Manufacturer`
	// declares no `meta.constraints` at all, so uniqueness is global: one candidate, no
	// conditional variant, nothing to pin to null.
	//
	// Global uniqueness over a namespaced CRD is what makes a cross-namespace collision
	// routine on a catalogue kind (docs/decisions/0002-crd-scoping.md): two NetBoxManufacturers
	// in two namespaces claiming `ubiquiti` are one NetBox manufacturer and a Conflict, not two.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the manufacturer.
	//
	// Inherited from OrganizationalModel (docs/netbox-schema.md -> dcim.Manufacturer,
	// `description (OrganizationalModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxManufacturer is one dcim.Manufacturer in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which for a
// catalogue kind means the everyday deployment is a shared namespace holding manufacturers,
// device types, roles and platforms, with team namespaces referencing into it. A
// `manufacturerRef` from a team namespace is therefore a cross-namespace reference and needs
// a NetBoxRefGrant here -- see docs/reference/netboxmanufacturer.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmfr
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxManufacturer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxManufacturerSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus     `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (m *NetBoxManufacturer) NetBoxSpec() *NetBoxObjectSpec { return &m.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (m *NetBoxManufacturer) NetBoxStatus() *NetBoxObjectStatus { return &m.Status }

// NetBoxManufacturerList is a list of NetBoxManufacturer.
// +kubebuilder:object:root=true
type NetBoxManufacturerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxManufacturer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxManufacturer{}, &NetBoxManufacturerList{})
}
