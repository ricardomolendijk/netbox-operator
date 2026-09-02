package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxProviderSpec describes one circuits.Provider.
//
// The company at the far end of a circuit -- a carrier, a transit provider, an exchange
// operator. The root of the `circuits` app: every other kind in it reaches a provider either
// directly (`ProviderAccount.provider`, `ProviderNetwork.provider`, `Circuit.provider`) or
// through one of those, so this is the kind that has to exist before any of the rest can be
// declared by name.
//
// A `PrimaryModel` with exactly two columns of its own plus one many-to-many
// (docs/netbox-schema.md -> circuits.Provider):
//
//	name   CharField        REQ UNIQUE len=100
//	slug   SlugField        REQ UNIQUE len=100
//	asns   ManyToManyField      -> ipam.ASN
//
// `circuits.Provider` declares **no `meta.constraints`** -- its `meta` carries only
// `ordering: ['name']`, and `hack/testdata/ir-4.6.8.json.gz` records `natural_keys: []` for it.
// The identity therefore comes from the two column-level `UNIQUE`s, the same derivation
// dcim.Manufacturer, dcim.Site and NBO-051's NetBoxRackRole use, and `slug` is the one that
// wins for the reason those kinds give: a kind gets one identity and `slug` is the stable half
// of the pair.
type NetBoxProviderSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the provider's name, as NetBox displays it.
	//
	// Globally unique (`name CharField REQ UNIQUE len=100`), and deliberately not this kind's
	// natural key: a kind gets one identity, `slug` is the stable one, and a rename that
	// collides comes back as NetBox's own 409 rather than being adopted under a second
	// candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the provider's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (docs/netbox-schema.md -> circuits.Provider, `slug
	// SlugField REQ UNIQUE len=100`), so two namespaces claiming `ntt` are claiming one
	// provider and the second reports Ready=False, Reason=Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ASNs are the autonomous system numbers this provider announces
	// (docs/netbox-schema.md -> circuits.Provider, `asns ManyToManyField -> ipam.ASN`).
	//
	// A to-many reference with the three states every optional list field has: omitting it
	// leaves NetBox's own list alone, `[]` clears it, and a list replaces it. The order is not
	// data -- NetBox does not preserve M2M order -- so the ids are sent sorted and deduplicated
	// and the comparison is order-independent (docs/concepts/drift.md).
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of the
	// payload and the object reports `RefsResolved=False` naming the element that failed.
	// Writing only the ones that resolved would be a full-list replacement with a shorter list
	// -- a deletion, reported as a success.
	//
	// MaxItems is not a NetBox limit and is not decoration: ObjectRef carries five CEL rules
	// (objectref.go) and the API server costs each one at the list's maximum length, so an
	// unbounded list of refs is rejected outright with "estimated rule cost exceeds budget".
	// 256 is the project's standard bound for a to-many reference
	// (docs/concepts/references.md, "A list needs a bound").
	// +kubebuilder:validation:MaxItems=256
	// +optional
	ASNs []ASNRef `json:"asns,omitempty"`

	// Description is free text shown next to the provider. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the provider's long-form notes field.
	//
	// A TextField rather than a CharField (docs/netbox-schema.md -> circuits.Provider,
	// `comments (PrimaryModel) TextField`): it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxProvider is one circuits.Provider in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and a catalogue
// kind in practice: a provider is shared infrastructure that circuits in many team namespaces
// point at, so `providerRef` crossing a namespace needs a NetBoxRefGrant in the provider's
// namespace (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - `owner` is `ForeignKey -> users.Owner` (docs/netbox-schema.md -> circuits.Provider) and
//     the whole `users` app is deferred, so there is no Kind to point at and a field that
//     resolved to nothing would report success while writing nothing.
//   - `contacts` is a ContactsMixin GenericRelation: the reverse of somebody else's foreign
//     key, not a column. Contact assignments are NBO-056's NetBoxContactAssignment, which
//     writes the pair from the assignment's side.
//   - `circuit_count` is a counter NetBox maintains from the circuits pointing here. It is in
//     the serializer's write path and the API refuses it, so writing it silently no-ops and
//     the engine would PATCH it forever. Declared read-only on the Descriptor.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbprovider
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxProviderSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxProvider) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxProvider) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxProviderList is a list of NetBoxProvider.
// +kubebuilder:object:root=true
type NetBoxProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxProvider{}, &NetBoxProviderList{})
}
