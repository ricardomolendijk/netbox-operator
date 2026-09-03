package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// L2VPNType is one value of NetBox's L2VPNTypeChoices: which layer 2 VPN technology this is.
//
// Fourteen members, `netbox/vpn/choices.py:217` at 4.6.8. NetBox renders them in two option
// groups (VPLS/VPWS/EVPN family, then the MEF service types), which is presentation only: the
// flat set of *values* is what goes over the wire, and the operator sends and compares the
// value rather than the label (docs/concepts/drift.md).
//
// Closed: the class declares no `key`, so no deployment's `FIELD_CHOICES` can add a member
// this enum would reject (hack/testdata/ir-4.6.8.json.gz -> enums.L2VPNTypeChoices).
//
// No empty member: the column is `type CharField REQ len=50`
// (docs/netbox-schema.md -> vpn.L2VPN).
//
// +kubebuilder:validation:Enum=vpws;vpls;vxlan;"vxlan-evpn";"mpls-evpn";"pbb-evpn";"evpn-vpws";epl;evpl;"ep-lan";"evp-lan";"ep-tree";"evp-tree";spb
type L2VPNType string

const (
	// L2VPNTypeVPWS is a Virtual Private Wire Service: a point-to-point pseudowire.
	L2VPNTypeVPWS L2VPNType = "vpws"

	// L2VPNTypeVPLS is a Virtual Private LAN Service.
	L2VPNTypeVPLS L2VPNType = "vpls"

	// L2VPNTypeVXLAN is VXLAN.
	L2VPNTypeVXLAN L2VPNType = "vxlan"

	// L2VPNTypeVXLANEVPN is VXLAN with an EVPN control plane.
	L2VPNTypeVXLANEVPN L2VPNType = "vxlan-evpn"

	// L2VPNTypeMPLSEVPN is MPLS EVPN.
	L2VPNTypeMPLSEVPN L2VPNType = "mpls-evpn"

	// L2VPNTypePBBEVPN is PBB EVPN.
	L2VPNTypePBBEVPN L2VPNType = "pbb-evpn"

	// L2VPNTypeEVPNVPWS is EVPN VPWS.
	L2VPNTypeEVPNVPWS L2VPNType = "evpn-vpws"

	// L2VPNTypeEPL is an Ethernet Private Line.
	L2VPNTypeEPL L2VPNType = "epl"

	// L2VPNTypeEVPL is an Ethernet Virtual Private Line.
	L2VPNTypeEVPL L2VPNType = "evpl"

	// L2VPNTypeEPLAN is an Ethernet Private LAN.
	L2VPNTypeEPLAN L2VPNType = "ep-lan"

	// L2VPNTypeEVPLAN is an Ethernet Virtual Private LAN.
	L2VPNTypeEVPLAN L2VPNType = "evp-lan"

	// L2VPNTypeEPTree is an Ethernet Private Tree.
	L2VPNTypeEPTree L2VPNType = "ep-tree"

	// L2VPNTypeEVPTree is an Ethernet Virtual Private Tree.
	L2VPNTypeEVPTree L2VPNType = "evp-tree"

	// L2VPNTypeSPB is Shortest Path Bridging.
	L2VPNTypeSPB L2VPNType = "spb"
)

// L2VPNStatus is one value of NetBox's L2VPNStatusChoices: the L2VPN's lifecycle state.
//
// Three members, `netbox/vpn/choices.py:272` at 4.6.8. The column is
// `status CharField len=50 def=UNRESOLVED:L2VPNStatusChoices.STATUS_ACTIVE`
// (docs/netbox-schema.md -> vpn.L2VPN).
//
// Extensible in NetBox -- the class declares `key = 'L2VPN.status'`
// (hack/testdata/ir-4.6.8.json.gz -> enums.L2VPNStatusChoices) -- and closed here, the
// TunnelStatus and ipam.VLAN derivation: a deployment that extends the ChoiceSet needs this
// enum widened, and docs/reference/netboxl2vpn.md says so.
//
// Three members and not TunnelStatus's three: `decommissioning` is not `disabled` and
// `planned` is the only value the two sets share, which is exactly the near-miss one shared
// enum would have papered over.
//
// +kubebuilder:validation:Enum=active;planned;decommissioning
type L2VPNStatus string

const (
	// L2VPNStatusActive is an L2VPN in service, and NetBox's own default.
	L2VPNStatusActive L2VPNStatus = "active"

	// L2VPNStatusPlanned is an L2VPN that is designed but not yet configured.
	L2VPNStatusPlanned L2VPNStatus = "planned"

	// L2VPNStatusDecommissioning is an L2VPN being retired.
	L2VPNStatusDecommissioning L2VPNStatus = "decommissioning"
)

// NetBoxL2VPNSpec describes one vpn.L2VPN: a layer 2 overlay and the route targets that
// import and export it.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// **The second kind in the catalogue with real to-many references**, after ipam.VRF, and they
// are the same relation to the same model: `import_targets` and `export_targets` are
// `ManyToManyField -> ipam.RouteTarget` (docs/netbox-schema.md -> vpn.L2VPN), declared *here*
// rather than on NetBoxRouteTarget, because the relation is written from this side only and
// one object owns it. Two `ClassRefMany` entries on the Descriptor and nothing else -- no
// engine change, which is the claim NBO-022 made and this kind tests a second time.
//
// **Terminations are not here.** `vpn.L2VPNTermination` is a separate model and a separate
// Kind, and it is not part of this change: its identity is
// `(assigned_object_type, assigned_object_id)` over a generic foreign key. An L2VPN declared
// here is a complete, adoptable `vpn.L2VPN`; what terminates on it is set in NetBox until that
// Kind ships.
//
// **Identity is `slug`.** Both `name` and `slug` carry a column-level `UNIQUE`
// (docs/netbox-schema.md -> vpn.L2VPN) and the model declares no `meta.constraints`, so either
// would identify an L2VPN on its own. `slug` is the candidate for the reason it is on every
// OrganizationalModel: a kind gets one identity and the slug is the stable one, so renaming an
// L2VPN updates the object NetBox already holds rather than orphaning it. There is
// deliberately no `name` fallback -- reaching it would mean adopting an object whose slug
// disagrees and PATCHing this slug onto it, which is a rename of somebody else's L2VPN.
type NetBoxL2VPNSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the L2VPN's name.
	//
	// Globally unique (docs/netbox-schema.md -> vpn.L2VPN, `name CharField REQ UNIQUE
	// len=100`), and not this kind's natural key -- see the type comment.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the L2VPN's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (docs/netbox-schema.md -> vpn.L2VPN, `slug
	// SlugField REQ UNIQUE len=100`): two namespaces cannot both own `campus-evpn`, and the
	// loser gets a Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Type is which layer 2 VPN technology this is (docs/netbox-schema.md -> vpn.L2VPN,
	// `type CharField REQ len=50`).
	//
	// Required by the column and undefaulted: NetBox declares no Django default, and every
	// value here describes a different service.
	//
	// The serializer marks the field `required=False` (hack/testdata/ir-4.6.8.json.gz ->
	// vpn.L2VPN.type, `"api": {"required": false}`) while the column is `REQ`
	// (`"sql_required": true`), so an omitted type is accepted by DRF and then refused by the
	// database. Required here, because the stricter of the two readings is the one that
	// turns it into a `kubectl apply` error instead of a 500. The enum carries no empty
	// member, so `""` is refused by the enum itself.
	Type L2VPNType `json:"type"`

	// Status is the L2VPN's lifecycle state: active, planned or decommissioning.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status L2VPNStatus `json:"status,omitempty"`

	// Identifier is the L2VPN's numeric identifier -- a VNI for VXLAN, a VC ID for VPLS
	// (docs/netbox-schema.md -> vpn.L2VPN, `identifier BigIntegerField`).
	//
	// A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so
	// that `0` is distinguishable from unset. A signed `BigIntegerField` rather than a
	// positive one, so no `Minimum` marker is derived: NetBox accepts a negative identifier
	// and a bound this schema invented would refuse a value NetBox stores.
	//
	// Not part of the identity: no constraint names it, and `meta.ordering: ('name',
	// 'identifier')` is an ordering rather than a uniqueness claim.
	// +optional
	Identifier *int64 `json:"identifier,omitempty"`

	// ImportTargets is the set of route targets imported into this L2VPN.
	//
	// A to-many reference: every element is resolved to a NetBox id and the field is written
	// as the whole list, because NetBox replaces a many-to-many wholesale on PATCH -- there is
	// no add or remove verb. So the listed set *is* the set.
	//
	// Three states, and all three are instructions (docs/concepts/field-ownership.md):
	// omitting the field leaves NetBox's own route targets alone, `[]` clears them, and a list
	// replaces them. The order you write them in is not data: NetBox does not preserve it, so
	// the ids are sent sorted and deduplicated and the comparison is order-independent
	// (docs/concepts/drift.md). Reordering the list produces no write.
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of the
	// payload and the object reports RefsResolved=False naming the element that failed.
	// Writing the ones that did resolve would be a full-list replacement with a shorter list
	// -- a deletion, reported as a success.
	//
	// Optional, unlike a policy's `proposals`: the column is `blank=True`
	// (hack/testdata/ir-4.6.8.json.gz -> vpn.L2VPN.import_targets), so an L2VPN with no
	// route targets is an ordinary L2VPN.
	//
	// MaxItems is the project standard 256 (docs/concepts/references.md, "A list needs a
	// bound").
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ImportTargets []RouteTargetRef `json:"importTargets,omitempty"`

	// ExportTargets is the set of route targets exported from this L2VPN. It behaves exactly
	// like ImportTargets, and it is a separate relation: the same route target may appear in
	// both, and each resolves and is written independently.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ExportTargets []RouteTargetRef `json:"exportTargets,omitempty"`

	// TenantRef is the tenant this L2VPN belongs to (docs/netbox-schema.md -> vpn.L2VPN,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// Not part of the identity: `slug` is unique across the whole install, so a tenant filter
	// would narrow a lookup that already matches at most one row.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Description is free text shown next to the L2VPN. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the L2VPN's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxL2VPN is one vpn.L2VPN in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbl2vpn
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxL2VPN struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxL2VPNSpec    `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (v *NetBoxL2VPN) NetBoxSpec() *NetBoxObjectSpec { return &v.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (v *NetBoxL2VPN) NetBoxStatus() *NetBoxObjectStatus { return &v.Status }

// NetBoxL2VPNList is a list of NetBoxL2VPN.
// +kubebuilder:object:root=true
type NetBoxL2VPNList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxL2VPN `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxL2VPN{}, &NetBoxL2VPNList{})
}
