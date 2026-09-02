package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxProviderAccountSpec describes one circuits.ProviderAccount.
//
// The billing account a circuit is bought under. One provider may sell you several, which is
// why `Circuit` carries both `provider` and `provider_account` and why both appear in its
// uniqueness constraints.
//
// Two `meta.constraints`, and **only the first is usable as an identity**
// (docs/netbox-schema.md -> circuits.ProviderAccount):
//
//	UniqueConstraint(fields=('provider', 'account'), name='..._unique_provider_account')
//	UniqueConstraint(fields=('provider', 'name'),    name='..._unique_provider_name',
//	                 condition=~Q(name=''))
//
// The second is conditional on `name` being non-empty, which is not a null pin: the extractor
// records it as `unusable: "constraint condition is more than a null pin: ['name']"`
// (hack/testdata/ir-4.6.8.json.gz, and the unusable-constraint table in docs/coverage.md). A
// candidate the operator cannot reproduce as a filter is a candidate that matches the wrong
// set, so it is not declared -- see the Descriptor
// (internal/registry/circuits_provideraccount.go).
type NetBoxProviderAccountSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ProviderRef is the provider this account is held with. Required, because NetBox's
	// column is (`provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT`).
	//
	// It is the leading half of the only natural key, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all.
	//
	// PROTECT, so NetBox refuses to delete a provider while any account points at it; that
	// surfaces on the *provider* as Deleting=False, Reason=Protected. Not a containment
	// parent for exactly that reason: nothing cascades server-side, so there is no deletion
	// for an owner reference to mirror (docs/decisions/0003-ownership-and-references.md
	// rule 4).
	ProviderRef ProviderRef `json:"providerRef"`

	// Account is the account number or identifier the provider bills you under
	// (docs/netbox-schema.md -> circuits.ProviderAccount, `account CharField REQ len=100`).
	//
	// Required, and the trailing half of this kind's natural key: `(provider, account)` is the
	// one unconditional UniqueConstraint on the model.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Account string `json:"account"`

	// Name is a human label for the account (`name CharField len=100`).
	//
	// Optional, and **not** part of the identity even though `(provider, name)` is a
	// UniqueConstraint: that constraint carries `condition=~Q(name='')`, so it constrains only
	// the rows where `name` is set. The operator cannot express "and name is not empty" as a
	// NetBox filter, and a candidate that drops the condition matches the *unconstrained* set
	// -- which on a kind that adopts what it finds is how #206 and #216 happened.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Name string `json:"name,omitempty"`

	// Description is free text shown next to the account. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the account's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxProviderAccount is one circuits.ProviderAccount in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Absent deliberately: `owner` (`ForeignKey -> users.Owner`, and the `users` app has no Kind)
// and `contacts` (a ContactsMixin GenericRelation, which is the reverse of NBO-056's
// NetBoxContactAssignment rather than a column here).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbprovideracct
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerRef.name`
// +kubebuilder:printcolumn:name="Account",type=string,JSONPath=`.spec.account`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxProviderAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxProviderAccountSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus        `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxProviderAccount) NetBoxSpec() *NetBoxObjectSpec { return &a.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxProviderAccount) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxProviderAccountList is a list of NetBoxProviderAccount.
// +kubebuilder:object:root=true
type NetBoxProviderAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxProviderAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxProviderAccount{}, &NetBoxProviderAccountList{})
}
