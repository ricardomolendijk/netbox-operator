package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxServiceTemplateSpec describes one ipam.ServiceTemplate: a reusable
// protocol-and-ports definition a service can be stamped from.
//
// `docs/netbox-schema.md -> ipam.ServiceTemplate` records `name CharField REQ UNIQUE len=100`
// and, inherited from `ipam.ServiceBase`, `protocol CharField REQ len=50`, `ports
// ArrayField REQ` and the `_ports_lowest` cache.
//
// It is `NetBoxService` minus the parent and the addresses, and the difference in identity is
// the whole of what makes it a separate page: `name` here is **column-unique**, so this Kind
// has a real database-backed natural key while `NetBoxService` has a convention. There is no
// relation between a template and the services made from it -- NetBox copies the values at
// creation time and keeps no link -- so editing a template changes nothing that already
// exists.
type NetBoxServiceTemplateSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the template's name, and its natural key -- `ssh`, `https`
	// (docs/netbox-schema.md -> ipam.ServiceTemplate, `name CharField REQ UNIQUE len=100`).
	//
	// Column-unique, so `?name=ssh` matches at most one object. Globally unique in NetBox
	// over namespaced CRDs: two namespaces cannot both own `ssh`, and the loser gets a
	// `Conflict` rather than a second object.
	//
	// Matched exactly rather than case-insensitively. The constraint is a plain `unique=True`
	// on the column, not a `UniqueConstraint` over `Lower('name')` the way
	// `dcim.Device.name` is, so `SSH` and `ssh` are two legal rows and the operator must not
	// adopt one for the other.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Protocol is the layer-four protocol. The same `ServiceProtocol` enum
	// `NetBoxService.protocol` uses, because it is the same column on the same abstract base
	// (`ipam.ServiceBase`).
	Protocol ServiceProtocol `json:"protocol"`

	// Ports are the port numbers the template describes
	// (docs/netbox-schema.md -> ipam.ServiceTemplate, `ports (ServiceBase) ArrayField REQ`).
	//
	// Bounded by `SERVICE_PORT_MIN = 1` and `SERVICE_PORT_MAX = 65535`
	// (`netbox/ipam/constants.py:92-93`). Order is data, exactly as on `NetBoxService`, and
	// not part of the identity -- `name` alone is.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	Ports []int32 `json:"ports"`

	// Description is free text shown next to the template. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the template's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxServiceTemplate is one ipam.ServiceTemplate in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsvctpl
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Ports",type=string,JSONPath=`.spec.ports`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxServiceTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxServiceTemplateSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus        `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxServiceTemplate) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxServiceTemplate) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxServiceTemplateList is a list of NetBoxServiceTemplate.
// +kubebuilder:object:root=true
type NetBoxServiceTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxServiceTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxServiceTemplate{}, &NetBoxServiceTemplateList{})
}
