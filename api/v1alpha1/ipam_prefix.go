package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PrefixStatus is one value of NetBox's PrefixStatusChoices.
//
// docs/netbox-schema.md -> ipam.Prefix records the column as
// `status CharField len=50 def=UNRESOLVED:PrefixStatusChoices.STATUS_ACTIVE
// choices=PrefixStatusChoices` -- the choice *class*, not its members, and a `def=` the AST
// walk could not evaluate. The four values below are read from `netbox/ipam/choices.py`,
// `PrefixStatusChoices`, in the same 4.6.8 tree the digest was taken from. Note that this
// set is not dcim.Site's: a prefix is `container`, `active`, `reserved` or `deprecated`, and
// has no `planned`, `staging`, `decommissioning` or `retired`.
//
// +kubebuilder:validation:Enum=container;active;reserved;deprecated
type PrefixStatus string

const (
	// PrefixStatusContainer is a prefix that exists to hold child prefixes rather than to
	// be assigned itself.
	PrefixStatusContainer PrefixStatus = "container"

	// PrefixStatusActive is a prefix in service, and NetBox's own default.
	PrefixStatusActive PrefixStatus = "active"

	// PrefixStatusReserved is a prefix set aside for future use.
	PrefixStatusReserved PrefixStatus = "reserved"

	// PrefixStatusDeprecated is a prefix being retired.
	PrefixStatusDeprecated PrefixStatus = "deprecated"
)

// NetBoxPrefixSpec describes one ipam.Prefix.
//
// This is the kind the scope union exists for. Since NetBox 4.2 `ipam.Prefix` has **no
// `site` column at all**: docs/netbox-schema.md -> ipam.Prefix lists
// `bases: ContactsMixin, GetAvailablePrefixesMixin, CachedScopeMixin, PrimaryModel`, and
// dcim.CachedScopeMixin supplies `scope_type` + `scope_id` plus the four read-only caches
// `_region`, `_site_group`, `_site` and `_location`. NetBox drops a field it does not know
// rather than rejecting it, so `POST {"prefix": "...", "site": 3}` returns 201 and creates
// an *unscoped* prefix that then never drifts, because the spec's `site` is compared against
// a column that does not exist. There is therefore no `siteRef` on this kind and no sugar
// that expands into one -- see docs/concepts/generic-refs.md#the-scope-pair.
//
// There is likewise no `parentRef`. A prefix's place in the hierarchy is not stored: NetBox
// computes it from the prefix value itself with a Postgres `inet` GiST index
// (docs/netbox-schema.md -> ipam.Prefix, `meta.indexes: ... GistIndex(fields=['prefix'],
// name='ipam_prefix_gist_idx', opclasses=['inet_ops'])`) and caches the result in `_depth`
// and `_children`, both of which are `_`-prefixed and read-only. `10.0.20.0/24` is a child
// of `10.0.0.0/16` because of what it is, not because anything says so.
//
// `tenantRef` is deliberately absent while NBO-021 is in flight; a field that is accepted
// and writes nothing is worse than a field that is not there.
//
// `deletionPolicy` defaults to `Retain` here, which is decision #176. Deleting a prefix
// destroys the record of who a range of addresses belonged to, and that record is not
// recoverable by re-creating the object: the change log, the journal entries and every child
// row go with it, and a fresh prefix at the same CIDR is a different object with a different
// id. The default is data on the kind's Descriptor (registry.Descriptor.RetainOnDelete) rather
// than a CRD marker, because it cannot be one: `deletionPolicy` is declared once on the
// embedded NetBoxObjectSpec, so a `+kubebuilder:default` there is the same answer for ~120
// kinds, and redeclaring the field on this struct makes controller-gen emit
// `allOf: [{default: Retain}, {default: Delete}]` -- a schema the API server rejects outright.
// So `kubectl explain netboxprefix.spec.deletionPolicy` prints no default, and
// docs/concepts/deletion.md is where the table lives.
type NetBoxPrefixSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Prefix is the network in CIDR notation, IPv4 or IPv6
	// (docs/netbox-schema.md -> ipam.Prefix, `prefix IPNetworkField REQ`).
	//
	// One field for both families, because `IPNetworkField` is one column: `fd00:10::/64`
	// and `::/0` are as ordinary here as `10.0.20.0/24`, which is why the validation is a
	// CEL CIDR check rather than a v4-shaped regex.
	//
	// The host bits must be clear. NetBox canonicalises a prefix to its network address on
	// write, so `10.0.20.5/24` is stored as `10.0.20.0/24` and the drift comparison would be
	// against a value the user never wrote. Rejecting it at admission turns a silent rewrite
	// into an immediate message naming the network address.
	//
	// `/32` and `/128` are legal and are ordinary prefixes: a `10.0.20.10/32` NetBoxPrefix
	// is a different NetBox object from a `10.0.20.10/32` NetBoxIPAddress, and the one you
	// probably want is the address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="prefix must be a CIDR network, IPv4 or IPv6, for example 10.0.20.0/24 or fd00:10::/64"
	// +kubebuilder:validation:XValidation:rule="!isCIDR(self) || cidr(self) == cidr(self).masked()",message="prefix has host bits set; write the network address, which is cidr(self).masked()"
	Prefix string `json:"prefix"`

	// Scope attaches the prefix to a Region, SiteGroup, Site or Location.
	//
	// Omit it for a global prefix, which is a normal state rather than a missing value:
	// neither `scope_type` nor `scope_id` carries a real `REQ`
	// (docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest). An empty `scope: {}`
	// clears the scope by writing both columns as null; an absent one leaves whatever NetBox
	// holds alone.
	//
	// Written as the `(scope_type, scope_id)` pair and diffed as a unit, so moving a prefix
	// from a Region to a Site is one change and one PATCH carrying both keys.
	// +optional
	Scope *ScopeRef `json:"scope,omitempty"`

	// VRFRef puts the prefix in a VRF (docs/netbox-schema.md -> ipam.Prefix,
	// `vrf ForeignKey -> ipam.VRF on_delete=PROTECT`).
	//
	// Part of this kind's identity: the same CIDR may legitimately exist once globally and
	// once in every VRF, so the lookup either matches `vrf_id` against a value or pins it to
	// null, never omits it (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
	// Leaving it unset means the global table, which is a different prefix from the same
	// CIDR inside a VRF rather than the same prefix with a field missing.
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// VLANRef ties the prefix to the VLAN it is carried on
	// (docs/netbox-schema.md -> ipam.Prefix, `vlan ForeignKey -> ipam.VLAN on_delete=PROTECT`).
	//
	// Not part of identity and not a containment parent: a prefix outliving its VLAN is a
	// normal state, so this reference contributes no owner reference
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	VLANRef *VLANRef `json:"vlanRef,omitempty"`

	// RoleRef marks what the prefix is for -- management, storage, guest
	// (docs/netbox-schema.md -> ipam.Prefix, `role ForeignKey -> ipam.Role on_delete=SET_NULL`).
	//
	// An object, not a string. `ipam.Role` is a real NetBox model with its own slug and
	// weight, which is what distinguishes this field from `NetBoxIPAddress.role` -- that one
	// is a choice column of the same name.
	// +optional
	RoleRef *RoleRef `json:"roleRef,omitempty"`

	// Status is the prefix's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status PrefixStatus `json:"status,omitempty"`

	// IsPool makes NetBox treat the whole prefix as a usable pool: the network and broadcast
	// addresses count as assignable and utilisation is computed over the range rather than
	// over child prefixes (docs/netbox-schema.md -> ipam.Prefix, `is_pool BooleanField
	// def=False`).
	//
	// A pointer, and the reason is the column's `def=False`. A plain `bool` cannot tell "not
	// managed" from "managed as false", so adopting a prefix a human had marked as a pool
	// would silently clear it on the first reconcile. Nil leaves NetBox's value alone;
	// `false` writes false.
	// +optional
	IsPool *bool `json:"isPool,omitempty"`

	// MarkUtilized forces NetBox to report the prefix as 100% utilised regardless of what is
	// inside it (docs/netbox-schema.md -> ipam.Prefix, `mark_utilized BooleanField
	// def=False`). A pointer for the same reason as IsPool.
	//
	// Neither this nor `isPool` changes any NetBox data or foreign key -- both change its
	// arithmetic -- so neither participates in the natural key. Both are ordinary drift
	// targets: flipped in the NetBox UI, they are corrected on the next resync.
	// +optional
	MarkUtilized *bool `json:"markUtilized,omitempty"`

	// Description is free text shown next to the prefix. Declared on PrimaryModel rather
	// than on ipam.Prefix (docs/netbox-schema.md -> ipam.Prefix, `description
	// (PrimaryModel) CharField len=200`); an inherited column is as writable as a declared
	// one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the prefix's long-form notes field. Also inherited from PrimaryModel, and
	// a TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxPrefix is one ipam.Prefix in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// The SCOPE printer column reads `.status.naturalKey` rather than the spec, because the
// question it answers is "is this prefix scoped at all in NetBox" -- the observability half
// of the bug this kind exists to fix -- and the spec cannot answer that. `VRF` reads the
// intent, so it is visible side by side with an empty `ID` while the reference is still
// resolving.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbprefix
// +kubebuilder:printcolumn:name="Prefix",type=string,JSONPath=`.spec.prefix`
// +kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.vrfRef.name`
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.status.naturalKey.scope`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxPrefix struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxPrefixSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxPrefix) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxPrefix) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxPrefixList is a list of NetBoxPrefix.
// +kubebuilder:object:root=true
type NetBoxPrefixList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxPrefix `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxPrefix{}, &NetBoxPrefixList{})
}
