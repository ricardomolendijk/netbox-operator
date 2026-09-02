package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TunnelStatus is one value of NetBox's TunnelStatusChoices: the tunnel's lifecycle state.
//
// Three members, `netbox/vpn/choices.py:10` at 4.6.8. The column is
// `status CharField len=50 def=UNRESOLVED:TunnelStatusChoices.STATUS_ACTIVE
// choices=TunnelStatusChoices` (docs/netbox-schema.md -> vpn.Tunnel).
//
// Unlike the crypto ChoiceSets, this one **is** extensible: it declares `key = 'Tunnel.status'`
// (hack/testdata/ir-4.6.8.json.gz -> enums.TunnelStatusChoices), so a deployment's
// `FIELD_CHOICES` can add a value. The enum is still closed here -- the ipam.VLAN and dcim.Rack
// derivation -- because a CRD cannot read a NetBox setting: a deployment that extends the
// ChoiceSet needs this enum widened, and docs/reference/netboxtunnel.md says so where a user
// will find it after `kubectl apply` refuses the value.
//
// +kubebuilder:validation:Enum=planned;active;disabled
type TunnelStatus string

const (
	// TunnelStatusPlanned is a tunnel that is designed but not yet configured.
	TunnelStatusPlanned TunnelStatus = "planned"

	// TunnelStatusActive is a tunnel in service, and NetBox's own default.
	TunnelStatusActive TunnelStatus = "active"

	// TunnelStatusDisabled is a tunnel that is configured but administratively down.
	TunnelStatusDisabled TunnelStatus = "disabled"
)

// TunnelEncapsulation is one value of NetBox's TunnelEncapsulationChoices: how the tunnel
// wraps the traffic it carries.
//
// Eight members, `netbox/vpn/choices.py:24` at 4.6.8. No empty member, because the column is
// `encapsulation CharField REQ len=50` (docs/netbox-schema.md -> vpn.Tunnel). The class
// declares no `key`, so the set is closed
// (hack/testdata/ir-4.6.8.json.gz -> enums.TunnelEncapsulationChoices).
//
// +kubebuilder:validation:Enum="ipsec-transport";"ipsec-tunnel";"ip-ip";gre;wireguard;openvpn;l2tp;pptp
type TunnelEncapsulation string

const (
	// TunnelEncapsulationIPSecTransport is IPSec transport mode: the payload is protected and
	// the original IP header is kept.
	TunnelEncapsulationIPSecTransport TunnelEncapsulation = "ipsec-transport"

	// TunnelEncapsulationIPSecTunnel is IPSec tunnel mode: the whole packet is encapsulated.
	TunnelEncapsulationIPSecTunnel TunnelEncapsulation = "ipsec-tunnel"

	// TunnelEncapsulationIPIP is IP-in-IP.
	TunnelEncapsulationIPIP TunnelEncapsulation = "ip-ip"

	// TunnelEncapsulationGRE is GRE.
	TunnelEncapsulationGRE TunnelEncapsulation = "gre"

	// TunnelEncapsulationWireGuard is WireGuard.
	TunnelEncapsulationWireGuard TunnelEncapsulation = "wireguard"

	// TunnelEncapsulationOpenVPN is OpenVPN.
	TunnelEncapsulationOpenVPN TunnelEncapsulation = "openvpn"

	// TunnelEncapsulationL2TP is L2TP.
	TunnelEncapsulationL2TP TunnelEncapsulation = "l2tp"

	// TunnelEncapsulationPPTP is PPTP.
	TunnelEncapsulationPPTP TunnelEncapsulation = "pptp"
)

// NetBoxTunnelSpec describes one vpn.Tunnel.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// # Identity: two constraints, and the second one needs a null pin
//
// docs/netbox-schema.md -> vpn.Tunnel.meta.constraints:
//
//	UniqueConstraint(fields=('group', 'name'), name='..._group_name')
//	UniqueConstraint(fields=('name',),         name='..._name', condition=Q(group__isnull=True))
//
// The MPTT shape, on a model that is not a tree: a tunnel in a group is identified by
// `(group, name)`, and a tunnel in no group by `name` alone -- but only among the tunnels that
// are in no group. The lookup for the second one must therefore send `?group_id=null` rather
// than omitting the filter; an omitted filter would match a tunnel of that name inside
// somebody else's group and the follow-up PATCH would move it out (#206, #216). The pin and
// the two candidates are declared on the Descriptor
// (internal/registry/vpn_tunnel.go).
//
// `name` also carries a column-level `UNIQUE` in 4.6.8 (docs/netbox-schema.md -> vpn.Tunnel,
// `name CharField REQ UNIQUE len=100`; hack/testdata/ir-4.6.8.json.gz -> vpn.Tunnel, `"sql":
// {"unique": true}`), which makes the two constraints belt-and-braces rather than the whole
// story: neither candidate can match more than one row, and a tunnel renamed into an existing
// name comes back as NetBox's own 409 rather than adopting anything.
//
// # Terminations are not here
//
// `vpn.TunnelTermination` is a separate model and a separate Kind, and it is **not** part of
// this change -- neither as its own Kind nor as an inline list on this one. Its identity is
// `(termination_type, termination_id)` over a generic foreign key, which is a different piece
// of machinery from anything on this spec; see the PR for #59 for what is deferred and why. A
// tunnel declared here is a complete, adoptable `vpn.Tunnel`; what terminates on it is set in
// NetBox until the termination Kind ships.
type NetBoxTunnelSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the tunnel's name, and half of its identity (docs/netbox-schema.md ->
	// vpn.Tunnel, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Status is the tunnel's lifecycle state: planned, active or disabled.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct. NetBox returns it as `{"value":"active","label":"Active"}` and accepts
	// the bare value, and the differ compares the value (docs/concepts/drift.md).
	// +kubebuilder:default=active
	// +optional
	Status TunnelStatus `json:"status,omitempty"`

	// GroupRef is the tunnel group this tunnel is filed under (docs/netbox-schema.md ->
	// vpn.Tunnel, `group ForeignKey -> vpn.TunnelGroup on_delete=PROTECT`).
	//
	// Optional, and the other half of the identity. **Declaring it changes which natural key
	// applies**: with a group the tunnel is looked up as `(group_id, name)`, and without one
	// as `name` with `?group_id=null` pinned. A group that is declared but does not exist yet
	// makes neither candidate applicable, so the tunnel waits rather than adopting a
	// groupless tunnel of the same name and PATCHing this group onto it
	// (docs/concepts/lookups.md).
	//
	// PROTECT rather than CASCADE, so this is an ordinary reference and not a containment
	// parent: NetBox refuses to delete a group that still has tunnels, and a cluster-side
	// cascade would delete the CR and leave the row
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	GroupRef *TunnelGroupRef `json:"groupRef,omitempty"`

	// Encapsulation is how the tunnel wraps the traffic it carries
	// (docs/netbox-schema.md -> vpn.Tunnel, `encapsulation CharField REQ len=50`).
	//
	// Required by the column, and undefaulted: NetBox declares no Django default, and
	// choosing one here would put an encapsulation into every tunnel the operator adopted.
	// The enum carries no empty member, so `""` is refused by the enum itself.
	Encapsulation TunnelEncapsulation `json:"encapsulation"`

	// IPSecProfileRef is the IPSec profile this tunnel is protected by
	// (docs/netbox-schema.md -> vpn.Tunnel, `ipsec_profile ForeignKey -> vpn.IPSecProfile
	// on_delete=PROTECT`).
	//
	// Optional: a GRE or WireGuard tunnel has no IPSec profile, and NetBox's own `clean()` is
	// the authority on which encapsulations require one.
	// +optional
	IPSecProfileRef *IPSecProfileRef `json:"ipsecProfileRef,omitempty"`

	// TenantRef is the tenant this tunnel belongs to (docs/netbox-schema.md -> vpn.Tunnel,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// Not part of the identity: neither constraint names `tenant`, so two tenants cannot both
	// hold a tunnel of the same name and the lookup does not filter on it.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// TunnelID is the tunnel's numeric identifier as configured on the devices -- a VNI, a
	// key, an ifindex (docs/netbox-schema.md -> vpn.Tunnel, `tunnel_id
	// PositiveBigIntegerField`).
	//
	// A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so
	// that `0` is distinguishable from unset. An int64 because the column is a
	// `PositiveBigInteger`: NetBox's choice of column width says not to rely on a VNI fitting
	// an int32.
	//
	// Not part of the identity, and deliberately: no constraint names it, so two tunnels may
	// legitimately carry the same number.
	// +kubebuilder:validation:Minimum=0
	// +optional
	TunnelID *int64 `json:"tunnelId,omitempty"`

	// Description is free text shown next to the tunnel. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the tunnel's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxTunnel is one vpn.Tunnel in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbtunnel
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="Encapsulation",type=string,JSONPath=`.spec.encapsulation`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxTunnel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxTunnelSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxTunnel) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxTunnel) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxTunnelList is a list of NetBoxTunnel.
// +kubebuilder:object:root=true
type NetBoxTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxTunnel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxTunnel{}, &NetBoxTunnelList{})
}
