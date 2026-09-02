package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxProviderNetworkSpec describes one circuits.ProviderNetwork.
//
// The provider's own network, on the far side of the demarcation point: the thing a circuit
// terminates *into* when the other end is not a site you own. NetBox models it as an object so
// that a `CircuitTermination` can point at it through its generic foreign key, and so that
// several circuits can be recorded as landing on the same provider cloud.
//
// One unconditional constraint, so one candidate and no pin
// (docs/netbox-schema.md -> circuits.ProviderNetwork):
//
//	UniqueConstraint(fields=('provider', 'name'), name='..._unique_provider_name')
//
// No `condition=` clause, unlike circuits.ProviderAccount's second constraint, so this one is
// reproducible as a filter pair exactly as written and `hack/testdata/ir-4.6.8.json.gz` records
// it with `unusable: null`.
type NetBoxProviderNetworkSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ProviderRef is the provider whose network this is. Required, because NetBox's column is
	// (`provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT`).
	//
	// It is the leading half of the natural key, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all.
	//
	// PROTECT, so it is not a containment parent: NetBox refuses to delete a provider while a
	// provider network points at it, so nothing cascades server-side and there is no
	// server-side deletion for an owner reference to mirror
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	ProviderRef ProviderRef `json:"providerRef"`

	// Name is the network's name (docs/netbox-schema.md -> circuits.ProviderNetwork,
	// `name CharField REQ len=100`).
	//
	// Unique per provider rather than globally: the UNIQUE is on the pair and there is no
	// column-level unique here, so two providers may both have a `Backbone`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// ServiceID is the provider's own identifier for the service on this network
	// (`service_id CharField len=100`).
	//
	// Not part of the identity: it carries no UNIQUE of any kind, and a filter on it can
	// match any number of rows.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +optional
	ServiceID string `json:"serviceId,omitempty"`

	// Description is free text shown next to the network. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the network's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxProviderNetwork is one circuits.ProviderNetwork in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Absent deliberately: `owner` (`ForeignKey -> users.Owner`, and the `users` app has no Kind).
// This is the one kind in the provider family with no ContactsMixin, so there is not even a
// `contacts` GenericRelation to explain away (docs/netbox-schema.md ->
// circuits.ProviderNetwork, `bases: PrimaryModel`).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbprovidernet
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxProviderNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxProviderNetworkSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus        `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (n *NetBoxProviderNetwork) NetBoxSpec() *NetBoxObjectSpec { return &n.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (n *NetBoxProviderNetwork) NetBoxStatus() *NetBoxObjectStatus { return &n.Status }

// NetBoxProviderNetworkList is a list of NetBoxProviderNetwork.
// +kubebuilder:object:root=true
type NetBoxProviderNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxProviderNetwork `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxProviderNetwork{}, &NetBoxProviderNetworkList{})
}
