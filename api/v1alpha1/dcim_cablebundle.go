package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxCableBundleSpec describes one dcim.CableBundle.
//
// A named grouping of cables that are pulled together -- a trunk, a riser, a patch bundle.
// `docs/netbox-schema.md -> dcim.CableBundle` records exactly one column of its own,
// `name CharField REQ UNIQUE len=100`, on top of PrimaryModel's `description` and `comments`.
//
// It is the simplest identity in the catalogue after NetBoxClusterType's, and it is a
// different simplest: a PrimaryModel rather than an OrganizationalModel, so there is **no
// `slug`** to key on and `name` is the natural key. That is legal here where it would not be
// on tenancy.Contact -- whose `name` is backed by an index and no constraint
// (docs/reference/netboxcontact.md) -- because this column carries a real column-level
// `UNIQUE`, so two bundles of one name is server state the database refuses rather than state
// the operator has to arbitrate.
//
// The kind ships with NetBoxCable because `dcim.Cable.bundle` is the field that needs it, and
// nothing else in NetBox points at a CableBundle at all.
type NetBoxCableBundleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the bundle's label, and its natural key.
	//
	// Globally unique in NetBox over namespaced CRDs, exactly as a Site's slug is: two
	// namespaces cannot both own `riser-a`, and the loser gets a Conflict rather than a
	// second bundle.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Description is free text shown next to the bundle
	// (docs/netbox-schema.md -> dcim.CableBundle, `description (PrimaryModel) CharField
	// len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the bundle's long-form note
	// (docs/netbox-schema.md -> dcim.CableBundle, `comments (PrimaryModel) TextField`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	//
	// Unbounded on purpose: the column is a `TextField`, so NetBox declares no length and a
	// `MaxLength` here would be a limit the operator invented.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxCableBundle is one dcim.CableBundle in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// No containment parent, and none available: the model has no foreign keys of its own beyond
// `owner`, which this operator does not manage (docs/netbox-schema.md -> dcim.CableBundle).
// The relation runs the other way -- `dcim.Cable.bundle` points here, `on_delete=SET_NULL` --
// so deleting a bundle clears the column on every cable in it and destroys none of them. That
// is also why `deletionPolicy` defaults to `Delete` here: a bundle is a label, and losing one
// loses no record of a connection.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcablebundle
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCableBundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCableBundleSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (b *NetBoxCableBundle) NetBoxSpec() *NetBoxObjectSpec { return &b.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (b *NetBoxCableBundle) NetBoxStatus() *NetBoxObjectStatus { return &b.Status }

// NetBoxCableBundleList is a list of NetBoxCableBundle.
// +kubebuilder:object:root=true
type NetBoxCableBundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCableBundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCableBundle{}, &NetBoxCableBundleList{})
}
