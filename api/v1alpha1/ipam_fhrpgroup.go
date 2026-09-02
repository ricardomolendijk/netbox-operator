package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FHRPGroupProtocol is one value of NetBox's FHRPGroupProtocolChoices: which first-hop
// redundancy protocol the group speaks.
//
// docs/netbox-schema.md -> ipam.FHRPGroup records the column as `protocol CharField REQ
// len=50 choices=FHRPGroupProtocolChoices` -- the choice *class*, not its members. The seven
// values below are read from `netbox/ipam/choices.py:104-128` (`FHRPGroupProtocolChoices`) in
// the same 4.6.8 tree the digest was taken from.
//
// NetBox renders them in four option groups (`Standard`, `CheckPoint`, `Cisco`, and a bare
// `Other`), which is presentation only: the flat set of *values* is what goes over the wire,
// and the operator sends and compares the value rather than the label
// (docs/concepts/drift.md).
//
// A **closed** enum is safe here, and that is a fact about the source rather than an
// assumption. A ChoiceSet is extensible through `FIELD_CHOICES` only when it declares a
// `key` (`netbox/utilities/choices.py:23-35`), and `FHRPGroupProtocolChoices` declares none
// -- unlike `VLANStatusChoices`, which declares `key = 'VLAN.status'`. So no deployment can
// add a protocol this enum would reject.
//
// +kubebuilder:validation:Enum=vrrp2;vrrp3;carp;clusterxl;hsrp;glbp;other
type FHRPGroupProtocol string

const (
	// FHRPGroupProtocolVRRP2 is VRRPv2 (RFC 3768).
	FHRPGroupProtocolVRRP2 FHRPGroupProtocol = "vrrp2"

	// FHRPGroupProtocolVRRP3 is VRRPv3 (RFC 5798).
	FHRPGroupProtocolVRRP3 FHRPGroupProtocol = "vrrp3"

	// FHRPGroupProtocolCARP is CARP, OpenBSD's redundancy protocol.
	FHRPGroupProtocolCARP FHRPGroupProtocol = "carp"

	// FHRPGroupProtocolClusterXL is Check Point ClusterXL.
	FHRPGroupProtocolClusterXL FHRPGroupProtocol = "clusterxl"

	// FHRPGroupProtocolHSRP is Cisco HSRP.
	FHRPGroupProtocolHSRP FHRPGroupProtocol = "hsrp"

	// FHRPGroupProtocolGLBP is Cisco GLBP.
	FHRPGroupProtocolGLBP FHRPGroupProtocol = "glbp"

	// FHRPGroupProtocolOther is anything else.
	FHRPGroupProtocolOther FHRPGroupProtocol = "other"
)

// FHRPGroupAuthType is one value of NetBox's FHRPGroupAuthTypeChoices.
//
// docs/netbox-schema.md -> ipam.FHRPGroup records `auth_type CharField len=50
// choices=FHRPGroupAuthTypeChoices`; the two values are read from
// `netbox/ipam/choices.py:131-139` in the 4.6.8 tree. That class declares no `key` either,
// so it cannot be extended and the enum is closed.
//
// The column is nullable with no default, so a group with no authentication leaves it unset.
//
// +kubebuilder:validation:Enum=plaintext;md5
type FHRPGroupAuthType string

const (
	// FHRPGroupAuthTypePlaintext sends the authentication string in the clear.
	FHRPGroupAuthTypePlaintext FHRPGroupAuthType = "plaintext"

	// FHRPGroupAuthTypeMD5 authenticates with an MD5 digest.
	FHRPGroupAuthTypeMD5 FHRPGroupAuthType = "md5"
)

// NetBoxFHRPGroupSpec describes one ipam.FHRPGroup: a VRRP, HSRP, GLBP or CARP group that
// several interfaces share a virtual address through.
//
// `docs/netbox-schema.md -> ipam.FHRPGroup` records `group_id
// PositiveSmallIntegerField REQ`, `name CharField len=100`, `protocol CharField REQ len=50`,
// `auth_type CharField len=50` and `auth_key CharField len=255`, plus two GenericRelations
// (`ip_addresses`, `services`) which are reverse accessors and not columns.
//
// **`auth_key` is deliberately absent from this spec.** A pre-shared key may never be inline
// in a spec, so the only permitted shape is `spec.authKeySecretRef` -> a key of a Secret, and
// reading a Secret into a NetBox payload field is
// a capability the engine does not have: there is no `FieldClass` for it and
// `internal/reconciler/payload.go` has nowhere to fetch one from. Shipping it inline to meet
// the field list would put a pre-shared key in an object every namespace reader can `get`, so
// the field waits for the mechanism. Set the key in NetBox by hand until then; the operator
// never writes the column and therefore never clears it. `auth_key` is in
// `internal/netbox/do.go`'s redaction set regardless, because NetBox *returns* it on every
// read of this endpoint.
//
// **No `meta.constraints`.** The table declares `meta.ordering: ['protocol', 'group_id',
// 'pk']` and one non-unique index, so `(protocol, group_id)` is a lookup convention and two
// groups sharing both are a legal server state reported as a `Conflict`.
type NetBoxFHRPGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// GroupID is the protocol's own group number -- the VRID for VRRP, the group number for
	// HSRP (docs/netbox-schema.md -> ipam.FHRPGroup, `group_id
	// PositiveSmallIntegerField REQ`).
	//
	// Not the Kubernetes object's name and not a NetBox id: it is the number that appears in
	// the device configuration. Half of the lookup convention.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	GroupID int32 `json:"groupId"`

	// Protocol is which redundancy protocol the group speaks. The other half of the lookup
	// convention, and required by the column.
	Protocol FHRPGroupProtocol `json:"protocol"`

	// Name is a human label for the group (docs/netbox-schema.md -> ipam.FHRPGroup,
	// `name CharField len=100`).
	//
	// Optional in NetBox and optional here. Not part of the identity: `group_id` and
	// `protocol` are what a device configuration keys on, and a rename should not orphan the
	// object.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Name string `json:"name,omitempty"`

	// AuthType is how the group authenticates its peers: `plaintext` or `md5`.
	//
	// Undefaulted, because the column is nullable with no Django default and a group with no
	// authentication is an ordinary group rather than a misconfigured one.
	//
	// Setting this without a key set in NetBox is accepted by NetBox and does nothing useful;
	// see the type comment for why the key is not a field here.
	// +optional
	AuthType FHRPGroupAuthType `json:"authType,omitempty"`

	// Description is free text shown next to the group. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxFHRPGroup is one ipam.FHRPGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It is the
// Kind `IPAssignment.fhrpGroupRef` has pointed at since NBO-025, so a NetBoxIPAddress
// assigned to a group resolves by name for the first time here.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbfhrp
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Group",type=integer,JSONPath=`.spec.groupId`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxFHRPGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxFHRPGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxFHRPGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxFHRPGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxFHRPGroupList is a list of NetBoxFHRPGroup.
// +kubebuilder:object:root=true
type NetBoxFHRPGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxFHRPGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxFHRPGroup{}, &NetBoxFHRPGroupList{})
}
