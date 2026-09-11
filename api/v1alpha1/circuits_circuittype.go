package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxCircuitTypeSpec describes one circuits.CircuitType.
//
// The classification of a circuit -- `Transit`, `Peering`, `DIA`, `Dark Fibre`. The catalogue
// kind `NetBoxCircuit.typeRef` points at, and the reason the circuit's required `type` column
// can be written by name at all.
//
// **The model adds nothing of its own.** `circuits.CircuitType` derives from
// `circuits.BaseCircuitType`, which adds exactly one column -- `color ColorField` -- over
// `OrganizationalModel`, and the digest records the kind as "(no own columns -- every field is
// inherited from BaseCircuitType)" (docs/netbox-schema.md -> circuits.CircuitType). NBO-057's
// ticket says these two type kinds have "no model entry in the schema"; they do, at
// docs/netbox-schema.md -> circuits.CircuitType and -> circuits.VirtualCircuitType, and
// `hack/testdata/ir-4.6.8.json.gz` carries both as full kinds with endpoints, filtersets and
// write paths. The fields below are read from there, not inferred from the base.
//
// The identity is the `OrganizationalModel` derivation: `meta` carries only
// `ordering: ('name',)` and the IR records `natural_keys: []`, so the key comes from the base
// class's column-level `UNIQUE` on `slug`. Exactly what dcim.RackRole, dcim.Manufacturer and
// tenancy.ContactRole do.
type NetBoxCircuitTypeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the type's name, as NetBox displays it.
	//
	// Globally unique (`name (OrganizationalModel) CharField REQ UNIQUE len=100`), and
	// deliberately not this kind's natural key: a kind gets one identity, `slug` is the stable
	// one, and a rename that collides comes back as NetBox's own 409 rather than being adopted
	// under a second candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the type's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (`slug (OrganizationalModel) SlugField REQ UNIQUE
	// len=100`), so two namespaces claiming `transit` are claiming one circuit type and the
	// second reports Ready=False, Reason=Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Color is the type's colour as six hexadecimal digits, without a leading `#`
	// (docs/netbox-schema.md -> circuits.BaseCircuitType, `color ColorField`).
	//
	// **Undefaulted, unlike NetBoxRackRole's**, and the difference is the column's rather than
	// a choice here: `dcim.RackRole.color` reads `def=UNRESOLVED:ColorChoices.COLOR_GREY` in
	// the digest, while `BaseCircuitType.color` carries `blank=True` and no Django default at
	// all (hack/testdata/ir-4.6.8.json.gz -> circuits.CircuitType, field `color`, `sql:
	// {"blank": true}`). Defaulting it here would invent a value NetBox does not have and
	// PATCH it onto every adopted circuit type.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). The pattern admits `""` for that reason -- the
	// column is a CharField and takes the empty string, so no EmptyIsNull is needed.
	// +kubebuilder:validation:Pattern=`^([0-9a-f]{6})?$`
	// +optional
	Color string `json:"color,omitempty"`

	// Description is free text shown next to the type.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the type's long-form notes field.
	//
	// A TextField rather than a CharField (`comments (OrganizationalModel) TextField`): it has
	// no max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxCircuitType is one circuits.CircuitType in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and a catalogue
// kind: a `typeRef` pointing from a team namespace into a shared catalogue namespace needs a
// NetBoxRefGrant there (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - `owner` is `ForeignKey -> users.Owner` and the whole `users` app is deferred, so there is
//     no Kind to point at.
//   - `circuit_count` is a counter NetBox maintains from the circuits pointing here. It is in
//     the serializer's write path and the API refuses it, so writing it silently no-ops and the
//     engine would PATCH it forever. Declared read-only on the Descriptor.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcircuittype
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCircuitType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCircuitTypeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxCircuitType) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxCircuitType) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxCircuitTypeList is a list of NetBoxCircuitType.
// +kubebuilder:object:root=true
type NetBoxCircuitTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCircuitType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCircuitType{}, &NetBoxCircuitTypeList{})
}
