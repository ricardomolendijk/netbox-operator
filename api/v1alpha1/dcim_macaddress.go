package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MACAssignment selects what a MAC address is attached to.
//
// At most one member may be set; none means the address is unattached, which NetBox permits
// -- `assigned_object_type` and `assigned_object_id` are both `blank=True, null=True`
// (netbox/dcim/models/devices.py:1364-1374). The `GenericForeignKey` row above them carries
// `REQ` in the digest and that is the extractor artefact, not a column
// (docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest).
//
// Two members and not three, which is the whole reason this is its own union rather than a
// reuse of IPAssignment. NetBox restricts the column to
// `MACADDRESS_ASSIGNMENT_MODELS = Q(app_label='dcim', model='interface') |
// Q(app_label='virtualization', model='vminterface')`
// (netbox/dcim/constants.py:156-159), enforced on the serializer field
// (netbox/dcim/api/serializers_/devices.py:318). `ipam.fhrpgroup` is legal for an IP address
// and illegal for a MAC.
//
// Reusing IPAssignment here would not merely widen `kubectl explain` past what NetBox
// accepts -- it would be a latent boot failure. Registry.validateUnionTypes checks every
// union member *whose Kind is registered* against the pair's AllowedTypes and refuses to
// start the manager on a mismatch (internal/registry/registry.go, ErrMemberTypeNotAllowed),
// so the day NetBoxFHRPGroup is registered the operator would stop booting for every kind.
// The two typed refs themselves are shared with IPAssignment; only the member set differs.
//
// +kubebuilder:validation:XValidation:rule="[has(self.interfaceRef), has(self.vmInterfaceRef)].filter(x, x).size() <= 1",message="at most one of interfaceRef or vmInterfaceRef may be set"
type MACAssignment struct {
	// InterfaceRef attaches the address to a device interface -> `dcim.interface`.
	// +optional
	InterfaceRef *InterfaceRef `json:"interfaceRef,omitempty"`

	// VMInterfaceRef attaches the address to a virtual-machine interface ->
	// `virtualization.vminterface`.
	// +optional
	VMInterfaceRef *VMInterfaceRef `json:"vmInterfaceRef,omitempty"`
}

// NetBoxMACAddressSpec describes one dcim.MACAddress.
//
// NetBox 4.2 moved the MAC off the interface and into its own model:
// `BaseInterface.primary_mac_address` is a OneToOneField *to* this model while
// `MACAddress.assigned_object` points back at the interface, so an interface may hold many
// MACs and designate one as primary.
//
// The reverse half is deliberately absent from this spec. Modelling both directions as
// required references is the unresolvable cycle NBO-016 rejects; the MAC is created first
// with its `assignedObject`, and `NetBoxInterface.primaryMACAddressRef` is a deferred field
// on the interface side (NBO-053). Same shape as `Device.primary_ip4`.
//
// **This kind's identity is not enforced by NetBox.** dcim.MACAddress declares no
// `meta.constraints` at all -- only two indexes, on `(mac_address, id)` and on
// `(assigned_object_type, assigned_object_id)` (netbox/dcim/models/devices.py:1380-1385).
// Duplicate MACs are legal, so `(mac_address, assigned_object)` is a lookup convention
// rather than a key. A lookup matching more than one row is an
// `*netbox.AmbiguousError` and becomes `Conflict` with nothing written, which is the
// existing engine-wide rule for a natural key the server does not police -- the same one
// ipam.IPAddress and ipam.VLANGroup rely on (internal/netbox/client.go, docs/concepts/lookups.md).
type NetBoxMACAddressSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// MACAddress is the EUI-48 address, in NetBox's own canonical spelling: six uppercase
	// hex octets separated by colons, as in `AA:BB:CC:DD:EE:FF`.
	//
	// The pattern is narrower than what NetBox *accepts* on write, and that is deliberate.
	// `dcim.MACAddressField.to_python` parses whatever netaddr can parse and stores
	// `EUI(value, version=48, dialect=mac_unix_expanded_uppercase)`
	// (netbox/dcim/fields.py:40-48), so every read comes back uppercase and colon-separated
	// whatever was sent. The differ compares strings and normalises no case
	// (internal/netbox/drift.go), so a spec holding `aa:bb:cc:dd:ee:ff` would differ from
	// NetBox's `AA:BB:CC:DD:EE:FF` on every single pass and PATCH forever without ever
	// converging. Rejecting the other spellings at `kubectl apply` turns an invisible hot
	// loop into an error message.
	// +kubebuilder:validation:Pattern=`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`
	MACAddress string `json:"macAddress"`

	// AssignedObject attaches the address to a device interface or a VM interface -- the two
	// models deriving from `dcim.BaseInterface`, which is what owns `primary_mac_address`.
	//
	// Omit it for an unattached MAC, which NetBox permits. An empty `assignedObject: {}`
	// clears the attachment by writing both columns null; an absent one leaves whatever
	// NetBox holds alone. NetBox refuses to unassign a MAC that is still some interface's
	// `primary_mac_address` (netbox/dcim/models/devices.py:1406-1424), so clearing one
	// surfaces as `Invalid` naming that interface rather than as a silent no-op.
	//
	// Written as the `(assigned_object_type, assigned_object_id)` pair and diffed as a unit,
	// so moving a MAC from one interface to another is one change and one PATCH carrying
	// both keys.
	// +optional
	AssignedObject *MACAssignment `json:"assignedObject,omitempty"`

	// Description is free text shown next to the address
	// (`description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the address's long-form notes field, inherited from PrimaryModel. A
	// TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxMACAddress is one dcim.MACAddress in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// The ASSIGNED printer column reads `.status.naturalKey` rather than the spec, because the
// spec's answer is a union member name while the question a human is asking is which object
// the lookup actually resolved to.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmac
// +kubebuilder:printcolumn:name="MAC",type=string,JSONPath=`.spec.macAddress`
// +kubebuilder:printcolumn:name="Assigned",type=string,JSONPath=`.status.naturalKey.assigned_object_id`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxMACAddress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxMACAddressSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (m *NetBoxMACAddress) NetBoxSpec() *NetBoxObjectSpec { return &m.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (m *NetBoxMACAddress) NetBoxStatus() *NetBoxObjectStatus { return &m.Status }

// NetBoxMACAddressList is a list of NetBoxMACAddress.
// +kubebuilder:object:root=true
type NetBoxMACAddressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxMACAddress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxMACAddress{}, &NetBoxMACAddressList{})
}
