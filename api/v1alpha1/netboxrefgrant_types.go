package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EndpointKind is the one Kind an empty `to[].kinds` never covers.
//
// A catalogue reference hands over a NetBox *id*. An `endpointRef` hands over use of
// another namespace's **token Secret**: the referring namespace gets to make authenticated
// writes against that NetBox, with that token's NetBox permissions, without ever being able
// to read -- or appearing in any RBAC review of -- the Secret itself. That is a capability,
// not a lookup, and it is the one thing in this API that can be escalated by reference.
//
// So it is excluded from the ergonomic default on purpose. "Readable by everything" is a
// sentence a catalogue owner will write without thinking hard, and it must not be the same
// sentence as "and anyone may borrow my credentials". Lending an endpoint costs one more
// line -- `kinds: [NetBoxEndpoint]` -- and that line is the audit trail.
const EndpointKind = "NetBoxEndpoint"

// FromNamespaces is which namespaces one grant entry admits.
//
// An enum rather than an optional selector whose absence means "everything": the permissive
// case has to be a word somebody typed and a reviewer can grep for, not a field somebody
// forgot. There is no `Same` value -- a same-namespace reference needs no grant and never
// consults one.
//
// +kubebuilder:validation:Enum=All;Selector
type FromNamespaces string

const (
	// NamespacesAll admits every namespace in the cluster. This is the form ADR-0002
	// requires: one object makes a catalogue namespace readable cluster-wide.
	NamespacesAll FromNamespaces = "All"

	// NamespacesSelector admits the namespaces whose *labels* match `selector`.
	NamespacesSelector FromNamespaces = "Selector"
)

// RefGrantFrom is one audience: the namespaces that may refer into this one.
//
// +kubebuilder:validation:XValidation:rule="self.namespaces != 'All' || !has(self.selector)",message="selector may only be set with namespaces: Selector"
// +kubebuilder:validation:XValidation:rule="self.namespaces != 'Selector' || (has(self.selector) && ((has(self.selector.matchLabels) && size(self.selector.matchLabels) > 0) || (has(self.selector.matchExpressions) && size(self.selector.matchExpressions) > 0)))",message="namespaces: Selector requires a non-empty selector; write namespaces: All to admit every namespace"
type RefGrantFrom struct {
	// Namespaces is how the referring namespaces are chosen.
	Namespaces FromNamespaces `json:"namespaces"`

	// Selector matches the **labels of the referring Namespace object**, and is required by
	// `namespaces: Selector`.
	//
	// A label selector rather than a list of namespace names, because names are what does
	// not scale: a list has to be edited by the catalogue owner every time a team is
	// onboarded, which is the "grant per (namespace, kind) pair" failure ADR-0002 warns
	// about. Selecting by name is still available and needs no extra field, because the API
	// server labels every Namespace with `kubernetes.io/metadata.name` -- so
	// `matchLabels: {kubernetes.io/metadata.name: team-a}` is the one-namespace form and a
	// `matchExpressions` `In` is the several-namespaces form.
	//
	// An empty selector is refused by the CEL rule above even though Kubernetes would read
	// it as "everything": that meaning already has a spelling, and in a default-deny feature
	// the broad case must not be reachable by leaving a field blank.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// RefGrantTo is one set of objects in this namespace that the audience may reference.
type RefGrantTo struct {
	// Kinds is the Kinds that may be referenced. **Empty means every Kind except
	// NetBoxEndpoint** -- see EndpointKind for why that one is not included.
	//
	// Empty-means-all is the ergonomic default and it is deliberate: a catalogue namespace
	// holds `NetBoxManufacturer`, `NetBoxDeviceType`, `NetBoxTag` and a few dozen more, and
	// a grant that had to enumerate them would go stale on every kind this operator adds.
	//
	// A Kind this build does not know is inert rather than an error, so a grant may be
	// written before the kind it names exists -- the same reason a typed ref alias may point
	// at a Kind with no CRD yet.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^NetBox[A-Za-z0-9]+$`
	Kinds []string `json:"kinds,omitempty"`

	// Names is the object names that may be referenced. Empty, or the single entry `*`,
	// means every name.
	//
	// `*` is a whole entry and never a prefix. A prefix glob invites `web-*`, which makes a
	// grant's meaning depend on a naming discipline nobody enforces and turns a rename into
	// a silent permission change.
	// +optional
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^(\*|[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)$`
	Names []string `json:"names,omitempty"`
}

// NetBoxRefGrantSpec is who may refer into this grant's namespace, and at what.
//
// Permitted is the cross product: a reference is allowed when some `from` entry covers the
// referring namespace **and** some `to` entry covers the target's Kind and name. Two lists
// rather than one flat rule list because the two axes are the thing that gets confused --
// `from` is the referrer, `to` is the object in this namespace -- and a single `kinds` field
// sitting next to `namespaces` reads as the referrer's Kind, which is the one misreading
// that matters here.
type NetBoxRefGrantSpec struct {
	// From is the audiences this grant admits. At least one entry, and no default: a grant
	// that admits nobody is not a grant.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	From []RefGrantFrom `json:"from"`

	// To is what those audiences may reference here. **Omit it entirely** for every Kind
	// except NetBoxEndpoint, under every name -- which makes the whole of "this catalogue
	// namespace is readable by every namespace" three lines of YAML, as ADR-0002 requires.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	To []RefGrantTo `json:"to,omitempty"`
}

// NetBoxRefGrant permits references from other namespaces into the namespace it lives in.
//
// **It lives in the namespace being referenced, and that direction is the whole design.** A
// grant is a capability the target namespace hands out about itself; the same object in the
// referring namespace would be a claim anybody could write about somebody else's objects,
// which authorises nothing. Read it as "this namespace is readable by ...".
//
// Cross-namespace references are the ordinary case rather than an exotic one -- every kind
// is namespaced (ADR-0002), so `deviceTypeRef`, `manufacturerRef` and `tags` from a team
// namespace into a shared `netbox-catalog` namespace all cross one. That is why the wildcard
// and selector forms above are not a convenience: without them a cluster needs a grant per
// (namespace, kind) pair, and ADR-0002 records that as the design not surviving contact with
// more than three teams.
//
// What it is **not**: NetBox authorisation. A grant protects the Kubernetes reference graph,
// which is only the `name` mode. A `slug`, `lookup` or `id` reference reaches NetBox
// directly, with the referring namespace's own endpoint and token, and no grant anywhere can
// gate it -- NetBox's own permissions are the only thing that can.
//
// There is no status and no controller. Nothing here is reconciled into NetBox, and a status
// would only cache a selector evaluation the resolver has to redo per reference anyway.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=nbgrant
// +kubebuilder:printcolumn:name="From",type=string,JSONPath=`.spec.from[*].namespaces`
// +kubebuilder:printcolumn:name="Kinds",type=string,JSONPath=`.spec.to[*].kinds`
// +kubebuilder:printcolumn:name="Names",type=string,JSONPath=`.spec.to[*].names`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRefGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec NetBoxRefGrantSpec `json:"spec,omitempty"`
}

// NetBoxRefGrantList is a list of NetBoxRefGrant.
// +kubebuilder:object:root=true
type NetBoxRefGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRefGrant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRefGrant{}, &NetBoxRefGrantList{})
}
