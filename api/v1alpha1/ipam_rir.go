package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRIRSpec describes one ipam.RIR: a regional internet registry, or a private
// allocation authority standing in for one.
//
// An OrganizationalModel with exactly one column of its own. `docs/netbox-schema.md ->
// ipam.RIR` records `is_private BooleanField def=False`; `name` (`REQ UNIQUE len=100`),
// `slug` (`REQ UNIQUE len=100`), `description` (`len=200`) and `comments` (TextField) are
// inherited, and an inherited column is as writable as a declared one.
//
// It ships ahead of the three Kinds that need it. `ipam.ASN`, `ipam.ASNRange` and
// `ipam.Aggregate` each declare `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`
// (docs/netbox-schema.md), so none of them can be created without one and none of them can
// be deleted out from under one.
type NetBoxRIRSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the registry's name -- `RFC 1918`, `ARIN`, `RIPE NCC`.
	//
	// Column-unique, and deliberately not the natural key: `slug` is, for the reason every
	// catalogue kind in this operator prefers it -- a slug is the spelling a reference from
	// another namespace uses.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the registry's URL-safe identifier, and its natural key.
	//
	// Globally unique in NetBox over namespaced CRDs: two namespaces cannot both own
	// `rfc-1918`, and the loser gets a Conflict rather than a second object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// IsPrivate marks the registry as holding space that is not globally routable --
	// RFC 1918, RFC 4193, RFC 6598 (docs/netbox-schema.md -> ipam.RIR, `is_private
	// BooleanField def=False`).
	//
	// A pointer, and the reason is the column's `def=False`. A plain `bool` cannot tell "not
	// managed" from "managed as false", so adopting an RIR a human had marked private would
	// silently clear the flag on the first reconcile. Nil leaves NetBox's value alone;
	// `false` writes false.
	// +optional
	IsPrivate *bool `json:"isPrivate,omitempty"`

	// Description is free text shown next to the registry. Inherited from
	// OrganizationalModel (docs/netbox-schema.md -> ipam.RIR, `description
	// (OrganizationalModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the registry's long-form notes field. Also inherited, and a TextField
	// rather than a CharField: it has no max_length, so there is no MaxLength marker to
	// derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRIR is one ipam.RIR in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). A shared
// catalogue namespace plus a NetBoxRefGrant is how a team namespace points at one.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrir
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Private",type=boolean,JSONPath=`.spec.isPrivate`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRIR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRIRSpec      `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRIR) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRIR) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRIRList is a list of NetBoxRIR.
// +kubebuilder:object:root=true
type NetBoxRIRList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRIR `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRIR{}, &NetBoxRIRList{})
}
