package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRouteTargetSpec describes one ipam.RouteTarget.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary
// spec fields that a user writes alongside the rest.
//
// There is no `importedByVRFs` field here, and that is the one thing worth knowing about
// this Kind. `import_targets` and `export_targets` are declared on `ipam.VRF`
// (docs/netbox-schema.md -> ipam.VRF), so the VRF<->RouteTarget relation is written from the
// VRF side only: a route target has nothing to reconcile about its own membership, and two
// VRFs importing one route target do not conflict. A reverse field here would be a second
// writer of one relation, which is a PATCH war rather than a convenience.
//
// `tenant` (docs/netbox-schema.md -> ipam.RouteTarget, `ForeignKey -> tenancy.Tenant
// on_delete=PROTECT`) is deliberately absent: NetBoxTenant is NBO-021, in flight
// concurrently, and a field that is accepted and does nothing is worse than a field that is
// not there -- `kubectl apply` reports success and NetBox never sees the value.
type NetBoxRouteTargetSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the route target itself, in `<asn>:<value>` form -- `65000:10`. It is also
	// this kind's natural key.
	//
	// NetBox enforces uniqueness on the column globally
	// (docs/netbox-schema.md -> ipam.RouteTarget, `name CharField REQ UNIQUE len=21`) while
	// this CRD is namespaced (docs/decisions/0002-crd-scoping.md), so two NetBoxRouteTargets
	// in different namespaces claiming one name is one route target and a Conflict -- not
	// two.
	//
	// The length cap is NetBox's `VRF_RD_MAX_LENGTH`, which is 21 in `ipam/constants.py`.
	// The digest resolves it to a number, so a NetBox release that changes the constant
	// shows up as a schema diff rather than as 400s at runtime.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=21
	Name string `json:"name"`

	// Description is free text shown next to the route target.
	//
	// Declared on PrimaryModel rather than on ipam.RouteTarget, so docs/netbox-schema.md
	// lists it as `description (PrimaryModel)` -- as required and as writable as a declared
	// column.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the route target's long-form notes field.
	//
	// Also inherited from PrimaryModel, and a TextField rather than a CharField: it has no
	// max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRouteTarget is one ipam.RouteTarget in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which is what
// makes a cross-namespace `name` collision possible: NetBox's uniqueness on the column is
// global and a namespace boundary does not partition it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrt
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRouteTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRouteTargetSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRouteTarget) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRouteTarget) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRouteTargetList is a list of NetBoxRouteTarget.
// +kubebuilder:object:root=true
type NetBoxRouteTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRouteTarget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRouteTarget{}, &NetBoxRouteTargetList{})
}
