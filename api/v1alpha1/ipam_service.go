package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceProtocol is one value of NetBox's ServiceProtocolChoices: the layer-four protocol a
// service listens on.
//
// docs/netbox-schema.md records the column as `protocol (ServiceBase) CharField REQ len=50
// choices=ServiceProtocolChoices` on **both** `ipam.Service` and `ipam.ServiceTemplate` --
// one abstract base, `ipam.ServiceBase`, declares it for both. So this is one enum type
// shared by two Kinds, which is the opposite of `VLANStatus` and `PrefixStatus`: those are
// two nearly-identical *different* choice classes and a shared enum would have papered over
// the near-miss. Here the class really is the same one.
//
// The three values are read from `netbox/ipam/choices.py:175-185`
// (`ServiceProtocolChoices`) in the 4.6.8 tree. That class declares no `key`, so it cannot be
// extended through `FIELD_CHOICES` (`netbox/utilities/choices.py:23-35`) and a closed enum
// cannot reject a legitimate value.
//
// +kubebuilder:validation:Enum=tcp;udp;sctp
type ServiceProtocol string

const (
	// ServiceProtocolTCP is TCP.
	ServiceProtocolTCP ServiceProtocol = "tcp"

	// ServiceProtocolUDP is UDP.
	ServiceProtocolUDP ServiceProtocol = "udp"

	// ServiceProtocolSCTP is SCTP.
	ServiceProtocolSCTP ServiceProtocol = "sctp"
)

// NetBoxServiceSpec describes one ipam.Service: a layer-four service running on a device, a
// virtual machine or a first-hop-redundancy group.
//
// `docs/netbox-schema.md -> ipam.Service` records `parent_object_type ForeignKey REQ ->
// contenttypes.ContentType on_delete=PROTECT`, `parent_object_id
// PositiveBigIntegerField REQ`, `name CharField REQ len=100`, `ipaddresses ManyToManyField ->
// ipam.IPAddress`, and -- inherited from `ipam.ServiceBase` -- `protocol CharField REQ
// len=50`, `ports ArrayField REQ` and the `_ports_lowest` cache.
//
// **No `meta.constraints`.** The table declares two non-unique indexes and
// `meta.ordering: ('protocol', '_ports_lowest', 'id')`, so `(parent, name, protocol)` is a
// lookup convention and more than one match is a `Conflict` naming the candidate ids.
//
// **`ports` is not in the lookup, and that is deliberate.** A query parameter carries one
// value, and NetBox's only port filter is `port = NumericArrayFilter(field_name='ports',
// lookup_expr='contains')` (`netbox/ipam/filtersets.py:1282-1285`) -- a single-value
// containment test, which cannot express "these ports and no others". Leaving it out means
// reordering or editing `ports` never produces a *second* object, which is the half of the
// question that matters for correctness.
type NetBoxServiceSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Parent is what the service runs on: a device, a virtual machine or an FHRP group
	// (docs/netbox-schema.md -> ipam.Service, `parent_object_type` / `parent_object_id`).
	//
	// **This Kind's containment parent**, and every member of the union cascades: `dcim.Device`,
	// `virtualization.VirtualMachine` and `ipam.FHRPGroup` each declare a `services`
	// GenericRelation (docs/netbox-schema.md), so deleting any of the three deletes the
	// service server-side and the owner reference is what makes the CR go too
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	//
	// Both halves of the pair are part of the lookup, which the engine can do because
	// `applyGenericFK` writes the resolved pair back into the decoded spec under the two
	// column names.
	Parent ServiceParent `json:"parent"`

	// Name is the service's name -- `ssh`, `https`, `dns`
	// (docs/netbox-schema.md -> ipam.Service, `name CharField REQ len=100`).
	//
	// Part of the lookup convention, together with the parent and the protocol. Not globally
	// unique: `ssh` on every device is the normal shape, which is exactly why the parent is
	// pinned rather than omitted.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Protocol is the layer-four protocol. Required by the column, and part of the lookup.
	Protocol ServiceProtocol `json:"protocol"`

	// Ports are the port numbers the service listens on
	// (docs/netbox-schema.md -> ipam.Service, `ports (ServiceBase) ArrayField REQ`).
	//
	// Bounded by `SERVICE_PORT_MIN = 1` and `SERVICE_PORT_MAX = 65535`
	// (`netbox/ipam/constants.py:92-93`), which NetBox enforces per element with
	// Min/MaxValueValidator on the array's base field.
	//
	// **Order is data.** A Postgres `ArrayField` preserves the order it is given, NetBox does
	// not sort it on save (`netbox/ipam/models/services.py:41-47` recomputes only the
	// `_ports_lowest` cache), and the operator compares it order-sensitively -- the rule
	// `internal/netbox/drift.go` already names `Service.ports` under. So reordering the list
	// is a real edit and produces one PATCH that then converges; it never produces a second
	// object, because `ports` is not in the lookup.
	//
	// `maxItems` is not a NetBox limit. It bounds the API server's cost estimate for the
	// list, the same 256 every bounded list in this API uses.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	Ports []int32 `json:"ports"`

	// IPAddresses restricts the service to specific addresses of its parent
	// (docs/netbox-schema.md -> ipam.Service, `ipaddresses ManyToManyField ->
	// ipam.IPAddress`).
	//
	// A many-to-many, so NetBox does not preserve the order the spec lists them in and the
	// operator compares it as an order-independent id set: reordering this list writes
	// nothing (docs/concepts/drift.md).
	//
	// Absent, empty and set are three different instructions. Omit it to leave NetBox's own
	// list alone; write `[]` to clear it (docs/concepts/field-ownership.md).
	//
	// `maxItems` is not decoration: ObjectRef carries five CEL rules and the API server costs
	// them at the list's maximum length, so an unbounded list makes the whole CRD
	// uninstallable (#185).
	// +kubebuilder:validation:MaxItems=256
	// +optional
	IPAddresses []IPAddressRef `json:"ipAddresses,omitempty"`

	// Description is free text shown next to the service. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the service's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxService is one ipam.Service in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsvc
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Ports",type=string,JSONPath=`.spec.ports`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxServiceSpec  `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (s *NetBoxService) NetBoxSpec() *NetBoxObjectSpec { return &s.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (s *NetBoxService) NetBoxStatus() *NetBoxObjectStatus { return &s.Status }

// NetBoxServiceList is a list of NetBoxService.
// +kubebuilder:object:root=true
type NetBoxServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxService{}, &NetBoxServiceList{})
}
