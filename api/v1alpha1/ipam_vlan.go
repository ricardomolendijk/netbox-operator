package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VLANStatus is one value of NetBox's VLANStatusChoices.
//
// docs/netbox-schema.md -> ipam.VLAN records the column as
// `status CharField len=50 def=UNRESOLVED:VLANStatusChoices.STATUS_ACTIVE
// choices=VLANStatusChoices` -- the choice *class*, not its members, and a `def=` the AST
// walk could not evaluate. The three values below are read from `netbox/ipam/choices.py`,
// `VLANStatusChoices`, in the same 4.6.8 tree the digest was taken from.
//
// Three, not four: a VLAN has no `container` state. `ipam.Prefix` does, and the two sets are
// otherwise identical, which is exactly the sort of near-miss a shared enum would have
// papered over.
//
// +kubebuilder:validation:Enum=active;reserved;deprecated
type VLANStatus string

const (
	// VLANStatusActive is a VLAN in service, and NetBox's own default.
	VLANStatusActive VLANStatus = "active"

	// VLANStatusReserved is a VLAN ID set aside for future use.
	VLANStatusReserved VLANStatus = "reserved"

	// VLANStatusDeprecated is a VLAN being retired.
	VLANStatusDeprecated VLANStatus = "deprecated"
)

// VLANQinQRole is one value of NetBox's VLANQinQRoleChoices: which half of a Q-in-Q
// (802.1ad) pair this VLAN is.
//
// docs/netbox-schema.md -> ipam.VLAN records `qinq_role CharField len=50
// choices=VLANQinQRoleChoices`; the two values are read from `netbox/ipam/choices.py`,
// `VLANQinQRoleChoices`, in the 4.6.8 tree. Note that the wire values are `svlan` and
// `cvlan` while the labels NetBox renders are "Service" and "Customer" -- the operator sends
// and compares the value, never the label (docs/concepts/drift.md).
//
// The column is nullable and has no default, so an ordinary VLAN leaves it unset.
//
// +kubebuilder:validation:Enum=svlan;cvlan
type VLANQinQRole string

const (
	// VLANQinQRoleSVLAN is the outer, service-provider VLAN of a Q-in-Q pair.
	VLANQinQRoleSVLAN VLANQinQRole = "svlan"

	// VLANQinQRoleCVLAN is the inner, customer VLAN of a Q-in-Q pair.
	VLANQinQRoleCVLAN VLANQinQRole = "cvlan"
)

// NetBoxVLANSpec describes one ipam.VLAN.
//
// **This is the one kind in M3 where writing `site` is correct**, and it sits immediately
// next to the kind where it is wrong. `docs/netbox-schema.md -> ipam.VLAN` shows
// `site ForeignKey -> dcim.Site on_delete=PROTECT` -- a genuine foreign key, on the model,
// writable, and part of how the operator finds a VLAN again. `ipam.Prefix` shows no `site`
// column at all, only `CachedScopeMixin` among its bases, so `NetBoxPrefix` has a `scope`
// union instead and writing `site` to it returns 201 and sets nothing. The two are the most
// confusable pair in this API; see docs/reference/netboxvlan.md and
// docs/reference/netboxprefix.md, which cross-link for that reason.
//
// A VLAN group *is* scoped, and `NetBoxVLANGroup` therefore has the `scope` union and no
// `siteRef`. `siteRef` here, `scope` there, in the same API version, is deliberate.
//
// `deletionPolicy` still defaults to `Delete` here, and per
// docs/concepts/deletion.md#the-default-depends-on-the-kind `NetBoxVLAN` should default to
// `Retain` -- deleting a VLAN destroys its change log, its journal entries and every
// termination hanging off it, and a fresh VLAN with the same `vid` is a different object with
// a different id. It is not implemented because it cannot be, per kind, in this API:
// `deletionPolicy` is declared once on the embedded NetBoxObjectSpec, and redeclaring it here
// makes controller-gen emit `allOf: [{default: Retain}, {default: Delete}]`, which the API
// server rejects outright. Giving a kind its own default needs `Descriptor.RetainOnDelete`
// and `deletionPolicyOf(obj, desc)` (#186, #199), neither of which has landed. Until they do,
// every NetBoxVLAN manifest should say `deletionPolicy: Retain` explicitly.
type NetBoxVLANSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// VID is the 802.1Q VLAN ID (docs/netbox-schema.md -> ipam.VLAN,
	// `vid PositiveSmallIntegerField REQ`).
	//
	// 1-4094. 0 and 4095 are reserved by the standard and rejected at admission rather than
	// by NetBox on write, so the message names the field instead of arriving as a 400 three
	// steps later.
	//
	// Part of every one of this kind's identities, and never enough on its own: `vid: 20`
	// exists in every house in `../inventory.yaml`.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	VID int32 `json:"vid"`

	// Name is the VLAN's name (docs/netbox-schema.md -> ipam.VLAN,
	// `name CharField REQ len=64`).
	//
	// Free text, and the real inventory uses it that way: `MGMT (Default)` has a space and
	// parentheses in it. That is a NetBox value, not a Kubernetes name -- the CR's
	// `metadata.name` still has to be a DNS-1123 label (`vlan-1-mgmt-default`), and the two
	// are unrelated strings.
	//
	// Not part of the identity the operator looks up by, even though `unique_group_name` is a
	// real constraint: `vid` is the stable identifier a network engineer keys on and a rename
	// should not orphan the object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// SiteRef ties the VLAN to a site (docs/netbox-schema.md -> ipam.VLAN,
	// `site ForeignKey -> dcim.Site on_delete=PROTECT`).
	//
	// **A real foreign key, not a scope.** It is written under the key `site` as an integer
	// id, and no `scope_type` / `scope_id` appears in a request body for `ipam/vlans` -- the
	// exact opposite of `NetBoxPrefix`. See the type comment.
	//
	// This kind's containment parent (docs/decisions/0003-ownership-and-references.md
	// rule 4), so a NetBoxVLAN in the same namespace as its site acquires a non-controller
	// owner reference on it and `kubectl delete netboxsite` takes the VLAN with it. Across
	// namespaces an owner reference is illegal, so the reference still resolves and the object
	// reports `CascadeUnavailable` naming `siteRef`.
	//
	// Part of the identity when `groupRef` is unset, which is the common case: the lookup then
	// filters `?site_id=<id>&vid=<vid>`, and pins `site_id` to null rather than omitting it
	// when there is no site (docs/concepts/lookups.md).
	// +optional
	SiteRef *SiteRef `json:"siteRef,omitempty"`

	// GroupRef puts the VLAN in a VLAN group (docs/netbox-schema.md -> ipam.VLAN,
	// `group ForeignKey -> ipam.VLANGroup on_delete=PROTECT`).
	//
	// The only identity of this kind that a database constraint actually backs:
	// `unique_group_vid` (docs/netbox-schema.md -> ipam.VLAN, meta.constraints). With it set
	// the lookup is `?group_id=<id>&vid=<vid>` and can match at most one VLAN. Without it,
	// `group` is null, Postgres treats the NULLs as distinct and the constraint does not fire
	// -- so nothing in NetBox stops two VLANs with `vid: 20` on one site, and more than one
	// match is reported as a Conflict naming both candidates rather than resolved by taking
	// the first.
	//
	// Not a containment reference: a VLAN outliving its group is a normal state.
	// +optional
	GroupRef *VLANGroupRef `json:"groupRef,omitempty"`

	// TenantRef assigns the VLAN to a tenant (docs/netbox-schema.md -> ipam.VLAN,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// `PROTECT`, so a VLAN holding this reference blocks deletion of that tenant in NetBox and
	// the refusal is reported as `Deleting=False, Reason=Protected` naming this object
	// (docs/concepts/deletion.md). Not a containment reference and contributes no owner
	// reference (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// RoleRef marks what the VLAN is for -- management, guest, IoT
	// (docs/netbox-schema.md -> ipam.VLAN, `role ForeignKey -> ipam.Role
	// on_delete=SET_NULL`).
	//
	// An object, not a string. `ipam.Role` is a real NetBox model with its own slug and
	// weight, and it is the same model `NetBoxPrefix.roleRef` points at -- which is what
	// distinguishes both from `NetBoxIPAddress.role`, a choice column of the same name.
	//
	// The Kind is not delivered until NBO-055, and the field is here anyway rather than
	// omitted, because a field the API accepts and silently drops is worse than one that says
	// why it cannot resolve. A `name`-mode ref -- the sibling-CR mode -- reports
	// `RefsResolved=False, Reason=RefKindUnavailable` naming the field and `ipam.Role`;
	// `slug`, `lookup` and `id` resolve against NetBox directly and work today.
	// +optional
	RoleRef *RoleRef `json:"roleRef,omitempty"`

	// QinQSVLANRef is the outer service VLAN of a Q-in-Q pair
	// (docs/netbox-schema.md -> ipam.VLAN, `qinq_svlan ForeignKey -> ipam.VLAN
	// on_delete=PROTECT`).
	//
	// A self-reference, and therefore **deferred**: it is left out of the create payload and
	// applied by a follow-up PATCH, so a pair of VLANs that point at each other converges
	// instead of deadlocking. In between the object reports
	// `Ready=False, Reason=DeferredFieldPending` (docs/concepts/references.md, NBO-015).
	//
	// `(qinq_svlan, vid)` and `(qinq_svlan, name)` are real constraints on this model, but
	// neither can be a natural key here: a deferred reference is by construction unresolved
	// when the lookup runs.
	// +optional
	QinQSVLANRef *VLANRef `json:"qinqSVLANRef,omitempty"`

	// QinQRole is which half of a Q-in-Q pair this VLAN is: `svlan` (outer, service) or
	// `cvlan` (inner, customer).
	//
	// Undefaulted, unlike `status`. The column is nullable with no Django default and an
	// ordinary VLAN is neither half of a Q-in-Q pair, so defaulting it would assert a topology
	// nobody described.
	// +optional
	QinQRole VLANQinQRole `json:"qinqRole,omitempty"`

	// Status is the VLAN's lifecycle state: active, reserved or deprecated.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct. NetBox returns it as `{"value":"active","label":"Active"}` and accepts
	// the bare value, and the differ compares the value (docs/concepts/drift.md).
	// +kubebuilder:default=active
	// +optional
	Status VLANStatus `json:"status,omitempty"`

	// Description is free text shown next to the VLAN. Declared on PrimaryModel rather than
	// on ipam.VLAN (docs/netbox-schema.md -> ipam.VLAN,
	// `description (PrimaryModel) CharField len=200`); an inherited column is as writable as
	// a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the VLAN's long-form notes field. Also inherited from PrimaryModel, and a
	// TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxVLAN is one ipam.VLAN in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// SITE and GROUP read the spec rather than the status, because both are visible intent and
// both are worth seeing next to an empty ID while a reference is still resolving.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvlan
// +kubebuilder:printcolumn:name="VID",type=integer,JSONPath=`.spec.vid`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.siteRef.name`
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.groupRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVLAN struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVLANSpec     `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (v *NetBoxVLAN) NetBoxSpec() *NetBoxObjectSpec { return &v.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (v *NetBoxVLAN) NetBoxStatus() *NetBoxObjectStatus { return &v.Status }

// NetBoxVLANList is a list of NetBoxVLAN.
// +kubebuilder:object:root=true
type NetBoxVLANList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVLAN `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVLAN{}, &NetBoxVLANList{})
}
