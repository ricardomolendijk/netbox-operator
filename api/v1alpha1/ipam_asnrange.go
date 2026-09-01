package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxASNRangeSpec describes one ipam.ASNRange: a span of autonomous system numbers set
// aside for allocation.
//
// `docs/netbox-schema.md -> ipam.ASNRange` is the one entry in this app that says
// `shadows inherited: name (OrganizationalModel), slug (OrganizationalModel)` -- the model
// redeclares both columns rather than inheriting them, and both carry `REQ UNIQUE len=100`.
// It adds `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`, `start ASNField REQ`,
// `end ASNField REQ` and `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`.
//
// The range is a *declaration of intent*, not an allocation source the operator draws from:
// nothing in this Kind hands out numbers. `NetBoxASN` is where an allocated number is
// recorded.
type NetBoxASNRangeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the range's name -- `Private 16-bit`, `Customer allocations`.
	//
	// Column-unique here, exactly as `slug` is, and deliberately not the natural key: `slug`
	// is, for the reason every catalogue kind in this operator prefers it -- a rename should
	// not orphan the object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the range's URL-safe identifier, and its natural key.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// RIRRef is the registry the range belongs to (docs/netbox-schema.md -> ipam.ASNRange,
	// `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`).
	//
	// **Required**, so an unresolved reference writes nothing at all rather than a partial
	// object. `PROTECT` and therefore not a containment parent.
	RIRRef RIRRef `json:"rirRef"`

	// Start is the first ASN in the range, inclusive (docs/netbox-schema.md ->
	// ipam.ASNRange, `start ASNField REQ`).
	//
	// Bounded by `BGP_ASN_MIN = 1` and `BGP_ASN_MAX = 2**32 - 1`
	// (`netbox/ipam/fields.py:17-18,120-125`), and `int64` for the same reason
	// NetBoxASN.asn is: a 4-byte ASN overflows a signed 32-bit field.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	Start int64 `json:"start"`

	// End is the last ASN in the range, inclusive (docs/netbox-schema.md -> ipam.ASNRange,
	// `end ASNField REQ`).
	//
	// NetBox's own `clean()` rejects `end < start`; that check is server-side and arrives as
	// a 400 reported on the object, because the CRD schema cannot compare two fields without
	// a CEL rule on the whole spec and the message NetBox gives is already the right one.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	End int64 `json:"end"`

	// TenantRef assigns the range to a tenant (docs/netbox-schema.md -> ipam.ASNRange,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// An ordinary reference, same- or cross-namespace through the normal NetBoxRefGrant
	// path. The `PROTECT` consequence is the confusing one and is worth stating plainly: a
	// NetBoxASNRange holding a tenant **blocks that tenant's deletion in NetBox**, and the
	// tenant reports `Deleting=False, Reason=Protected` naming this range's namespace and
	// name (docs/concepts/deletion.md).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Description is free text shown next to the range. Inherited from OrganizationalModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the range's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxASNRange is one ipam.ASNRange in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbasnrange
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Start",type=integer,JSONPath=`.spec.start`
// +kubebuilder:printcolumn:name="End",type=integer,JSONPath=`.spec.end`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxASNRange struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxASNRangeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxASNRange) NetBoxSpec() *NetBoxObjectSpec { return &a.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxASNRange) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxASNRangeList is a list of NetBoxASNRange.
// +kubebuilder:object:root=true
type NetBoxASNRangeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxASNRange `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxASNRange{}, &NetBoxASNRangeList{})
}
