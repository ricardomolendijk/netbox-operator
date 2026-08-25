package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxASNSpec describes one ipam.ASN: an autonomous system number allocated by a registry.
//
// `docs/netbox-schema.md -> ipam.ASN` records `asn ASNField REQ UNIQUE`, `rir ForeignKey REQ
// -> ipam.RIR on_delete=PROTECT`, `role ForeignKey -> ipam.Role on_delete=SET_NULL` and
// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`, with `description` and `comments`
// inherited from PrimaryModel.
//
// **This Kind has no `name` and no `slug`.** The number *is* the identity, which is why
// `ASNRef` documents `slug` mode as matching nothing.
type NetBoxASNSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ASN is the autonomous system number, and this Kind's whole identity
	// (docs/netbox-schema.md -> ipam.ASN, `asn ASNField REQ UNIQUE`).
	//
	// `int64` and not `int32`: an ASNField is a `BigIntegerField` bounded by
	// `BGP_ASN_MIN = 1` and `BGP_ASN_MAX = 2**32 - 1` (`netbox/ipam/fields.py:17-18,120-125`
	// in the 4.6.8 tree), so a 4-byte ASN such as `4200000000` overflows a signed 32-bit
	// field. The bounds are enforced at admission rather than arriving as a 400 three steps
	// later.
	//
	// Column-unique, so the natural key is `?asn=<n>` and it matches at most one object.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	ASN int64 `json:"asn"`

	// RIRRef is the registry that allocated the number (docs/netbox-schema.md -> ipam.ASN,
	// `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`).
	//
	// **Required.** An unresolved reference on a required column writes nothing at all
	// rather than a partial object, and the CR reports
	// `RefsResolved=False, Reason=RefNotFound` naming this field.
	//
	// `PROTECT`, not `CASCADE`, so it is *not* a containment parent: NetBox refuses to
	// delete an RIR that still has ASNs, and an owner reference would promise a cascade the
	// server declines (docs/decisions/0003-ownership-and-references.md rule 4).
	RIRRef RIRRef `json:"rirRef"`

	// RoleRef marks what the ASN is used for (docs/netbox-schema.md -> ipam.ASN,
	// `role ForeignKey -> ipam.Role on_delete=SET_NULL`).
	//
	// `ipam.Role`, the same model NetBoxPrefix and NetBoxVLAN point at -- not
	// `dcim.DeviceRole` and not `ipam.IPAddress.role`, which is a choice column. See
	// NetBoxRole.
	// +optional
	RoleRef *RoleRef `json:"roleRef,omitempty"`

	// TenantRef assigns the ASN to a tenant (docs/netbox-schema.md -> ipam.ASN,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// `PROTECT`, so an ASN holding this reference blocks deletion of that tenant in NetBox
	// and the refusal is reported as `Deleting=False, Reason=Protected` naming this object
	// (docs/concepts/deletion.md).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Description is free text shown next to the ASN. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the ASN's long-form notes field. Also inherited, and a TextField, so there
	// is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxASN is one ipam.ASN in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbasn
// +kubebuilder:printcolumn:name="ASN",type=integer,JSONPath=`.spec.asn`
// +kubebuilder:printcolumn:name="RIR",type=string,JSONPath=`.spec.rirRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxASN struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxASNSpec      `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxASN) NetBoxSpec() *NetBoxObjectSpec { return &a.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxASN) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxASNList is a list of NetBoxASN.
// +kubebuilder:object:root=true
type NetBoxASNList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxASN `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxASN{}, &NetBoxASNList{})
}
