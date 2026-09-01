package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxAggregateSpec describes one ipam.Aggregate: a top-level block of address space, as
// allocated to you by a registry, that the prefixes underneath it are carved from.
//
// `docs/netbox-schema.md -> ipam.Aggregate` records `prefix IPNetworkField REQ`,
// `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`, `tenant ForeignKey ->
// tenancy.Tenant on_delete=PROTECT` and `date_added DateField`, with `description` and
// `comments` inherited from PrimaryModel.
//
// **No `meta.constraints` at all.** The table declares only `meta.ordering: ('prefix', 'pk')`
// and one non-unique index on `('prefix', 'id')`, so `(prefix, rir)` is a lookup *convention*
// rather than a database guarantee: two aggregates with the same prefix under the same
// registry are a legal server state, and more than one match is reported as a `Conflict`
// naming the candidate ids rather than resolved by taking the first.
type NetBoxAggregateSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Prefix is the aggregate in CIDR notation -- `10.0.0.0/8`, `2001:db8::/32`
	// (docs/netbox-schema.md -> ipam.Aggregate, `prefix IPNetworkField REQ`).
	//
	// NetBox masks host bits on an aggregate exactly as it does on a prefix, so
	// `10.0.0.1/8` is stored as `10.0.0.0/8` and the differ would then see a change it
	// cannot fix. Write the network address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=43
	Prefix string `json:"prefix"`

	// RIRRef is the registry that allocated the block (docs/netbox-schema.md ->
	// ipam.Aggregate, `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`).
	//
	// **Required, and half of the identity.** An unresolved reference writes nothing at all
	// and makes no candidate applicable, so the engine waits rather than adopting the same
	// prefix under a different registry (docs/concepts/lookups.md).
	//
	// `PROTECT` and therefore not a containment parent: NetBox refuses to delete an RIR that
	// still has aggregates.
	RIRRef RIRRef `json:"rirRef"`

	// TenantRef assigns the aggregate to a tenant (docs/netbox-schema.md -> ipam.Aggregate,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// `PROTECT`, so an aggregate holding this reference blocks that tenant's deletion and
	// the refusal is reported as `Deleting=False, Reason=Protected` naming this object.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// DateAdded is when the block was allocated to you, as `YYYY-MM-DD`
	// (docs/netbox-schema.md -> ipam.Aggregate, `date_added DateField`).
	//
	// The pattern admits the empty string on purpose. The column is nullable and a
	// `DateField` rejects `""` outright, so an emptied value has to go over the wire as
	// `null` to clear rather than to fail -- which is what the descriptor's `EmptyIsNull`
	// does, the same handling `NetBoxSite`'s coordinates get (#170).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:Pattern=`^(\d{4}-\d{2}-\d{2})?$`
	// +optional
	DateAdded string `json:"dateAdded,omitempty"`

	// Description is free text shown next to the aggregate. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the aggregate's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxAggregate is one ipam.Aggregate in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbagg
// +kubebuilder:printcolumn:name="Prefix",type=string,JSONPath=`.spec.prefix`
// +kubebuilder:printcolumn:name="RIR",type=string,JSONPath=`.spec.rirRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxAggregate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxAggregateSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxAggregate) NetBoxSpec() *NetBoxObjectSpec { return &a.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxAggregate) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxAggregateList is a list of NetBoxAggregate.
// +kubebuilder:object:root=true
type NetBoxAggregateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxAggregate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxAggregate{}, &NetBoxAggregateList{})
}
