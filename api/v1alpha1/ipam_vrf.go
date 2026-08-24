package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxVRFSpec describes one ipam.VRF.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary
// spec fields that a user writes alongside the rest.
//
// This is the first Kind carrying a real to-many reference. `importTargets` and
// `exportTargets` are `ManyToManyField -> ipam.RouteTarget` (docs/netbox-schema.md ->
// ipam.VRF), and both are declared *here* rather than on NetBoxRouteTarget: the relation is
// written from the VRF side only, so one object owns it and there is no second writer to
// fight with.
//
// `tenant` (docs/netbox-schema.md -> ipam.VRF, `ForeignKey -> tenancy.Tenant
// on_delete=PROTECT`) is deliberately absent: NetBoxTenant is NBO-021, in flight
// concurrently, and a field that is accepted and does nothing is worse than a field that is
// not there.
type NetBoxVRFSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the VRF's label in the NetBox UI.
	//
	// **Not unique.** docs/netbox-schema.md -> ipam.VRF gives `name CharField REQ len=100`
	// with no `UNIQUE`, and the model declares no `meta.constraints` at all, so NetBox
	// happily holds two VRFs called `Donkerslootstraat (RTM)`. That is why `rd` is the first
	// natural-key candidate and why a name-only lookup that matches twice is reported as a
	// Conflict rather than resolved by taking the first row -- adopting the wrong VRF
	// silently reparents every prefix and address keyed on it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// RD is the VRF's route distinguisher, in `<asn>:<value>` form -- `65000:10`.
	//
	// Column-unique in NetBox (docs/netbox-schema.md -> ipam.VRF,
	// `rd CharField UNIQUE len=21`), which makes it the only field on this Kind that
	// identifies a VRF on its own -- and therefore the natural key whenever it is set.
	// Globally unique over namespaced CRDs, exactly like a Site's `slug`: two namespaces
	// cannot both own `65000:10`, and the loser gets a Conflict.
	//
	// The length cap is NetBox's `VRF_RD_MAX_LENGTH`, 21 in `ipam/constants.py`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). Note that an explicitly-emptied `rd` leaves this
	// object with no applicable natural key -- see docs/reference/netboxvrf.md.
	// +kubebuilder:validation:MaxLength=21
	// +optional
	RD string `json:"rd,omitempty"`

	// EnforceUnique asks NetBox to refuse duplicate prefixes and addresses inside this VRF.
	//
	// A pointer, and that is load-bearing rather than fussy: docs/netbox-schema.md ->
	// ipam.VRF gives `enforce_unique BooleanField def=True`, so a plain bool would make
	// "omitted" indistinguishable from "false" and the operator would silently turn NetBox's
	// own default off on every VRF it adopted. The same rule applies to every defaulted
	// boolean in the catalogue -- ipam.Prefix's `is_pool` and `mark_utilized`,
	// ipam.IPRange's `mark_populated`.
	//
	// Undefaulted here on purpose: defaulting it to NetBox's `true` would write the field on
	// every VRF the operator touches, which is the opposite of "spec omission means do not
	// manage".
	//
	// The operator models no uniqueness logic of its own; the flag is written and NetBox
	// decides (issue #177).
	// +optional
	EnforceUnique *bool `json:"enforceUnique,omitempty"`

	// ImportTargets is the set of route targets imported into this VRF.
	//
	// A to-many reference: every element is resolved to a NetBox id and the field is written
	// as the whole list, because NetBox replaces a many-to-many wholesale on PATCH -- there
	// is no add or remove verb. So the listed set *is* the set.
	//
	// Three states, and all three are instructions (docs/concepts/field-ownership.md):
	// omitting the field leaves NetBox's own route targets alone, `[]` clears them, and a
	// list replaces them. The order you write them in is not data: NetBox does not preserve
	// it, so the ids are sent sorted and deduplicated and the comparison is
	// order-independent (docs/concepts/drift.md). Reordering the list produces no write.
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of
	// the payload and the object reports RefsResolved=False naming the element that failed.
	// Writing the ones that did resolve would be a full-list replacement with a shorter list
	// -- a deletion, reported as a success.
	//
	// MaxItems is not a NetBox limit and is not decoration. ObjectRef carries five CEL rules
	// (api/v1alpha1/objectref.go), and the API server costs a rule on a list item at the
	// list's maximum length -- so an unbounded list of refs is rejected outright with
	// "estimated rule cost exceeds budget". Every to-many reference field therefore needs a
	// bound, and the project standard is 256 (docs/concepts/references.md, "A list needs a
	// bound"). This field shipped at 32, chosen because it cleared the budget -- which was
	// measured afterwards at ~57 800 for one such list, so 32 was roughly 1800x too
	// conservative and would have refused a cluster with 40 route targets on one VRF for no
	// reason a user could act on.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ImportTargets []RouteTargetRef `json:"importTargets,omitempty"`

	// ExportTargets is the set of route targets exported from this VRF. It behaves exactly
	// like ImportTargets, and it is a separate relation: the same route target may appear in
	// both, and each resolves and is written independently.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ExportTargets []RouteTargetRef `json:"exportTargets,omitempty"`

	// Description is free text shown next to the VRF.
	//
	// Declared on PrimaryModel rather than on ipam.VRF, so docs/netbox-schema.md lists it as
	// `description (PrimaryModel)` -- as required and as writable as a declared column.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the VRF's long-form notes field.
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

// NetBoxVRF is one ipam.VRF in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvrf
// +kubebuilder:printcolumn:name="RD",type=string,JSONPath=`.spec.rd`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVRF struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVRFSpec      `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (v *NetBoxVRF) NetBoxSpec() *NetBoxObjectSpec { return &v.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (v *NetBoxVRF) NetBoxStatus() *NetBoxObjectStatus { return &v.Status }

// NetBoxVRFList is a list of NetBoxVRF.
// +kubebuilder:object:root=true
type NetBoxVRFList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVRF `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVRF{}, &NetBoxVRFList{})
}
