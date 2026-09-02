package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CircuitStatus is one value of NetBox's CircuitStatusChoices.
//
// Six values, read from `circuits/choices.py:10` in the same NetBox 4.6.8 tree
// docs/netbox-schema.md was taken from -- the digest records the choice *class* and not its
// members, because the AST walk cannot evaluate one (docs/netbox-schema.md -> circuits.Circuit,
// `status CharField len=50 def=UNRESOLVED:CircuitStatusChoices.STATUS_ACTIVE
// choices=CircuitStatusChoices`). The members are carried in the committed IR
// (hack/testdata/ir-4.6.8.json.gz -> enums -> CircuitStatusChoices), which is where these six
// come from rather than from memory.
//
// Its own Go type rather than a reuse of RackStatus or SiteStatus, even though `planned` and
// `active` appear in all three: `provisioning`, `offline`, `deprovisioning` and
// `decommissioned` are this ChoiceSet's, and NetBox extends each set independently through
// FIELD_CHOICES, so one shared enum would make a value added to a rack legal on a circuit.
//
// This ChoiceSet declares `key = 'Circuit.status'`, so a deployment *can* add members through
// FIELD_CHOICES and a closed enum would reject one at admission. Enumerated anyway, following
// SiteStatus, PrefixStatus and RackStatus: a typo caught by `kubectl apply` is worth more than
// an extension nobody has made, and widening the enum is a one-line change.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=planned;provisioning;active;offline;deprovisioning;decommissioned
type CircuitStatus string

const (
	// CircuitStatusPlanned is a circuit that has been designed but not ordered.
	CircuitStatusPlanned CircuitStatus = "planned"

	// CircuitStatusProvisioning is a circuit the provider is currently turning up.
	CircuitStatusProvisioning CircuitStatus = "provisioning"

	// CircuitStatusActive is a circuit carrying traffic, and NetBox's own default.
	CircuitStatusActive CircuitStatus = "active"

	// CircuitStatusOffline is a circuit that exists but is down.
	CircuitStatusOffline CircuitStatus = "offline"

	// CircuitStatusDeprovisioning is a circuit being torn down by the provider.
	CircuitStatusDeprovisioning CircuitStatus = "deprovisioning"

	// CircuitStatusDecommissioned is a circuit that has been cancelled.
	CircuitStatusDecommissioned CircuitStatus = "decommissioned"
)

// NetBoxCircuitSpec describes one circuits.Circuit.
//
// A single service bought from a provider, identified by the circuit ID the provider gave you.
// The kind the whole `circuits` app is arranged around: `CircuitTermination` hangs off it,
// `CircuitGroupAssignment` points at it, and `Provider`, `ProviderAccount` and `CircuitType`
// exist so that it can be declared by name.
//
// Two unconditional `meta.constraints` (docs/netbox-schema.md -> circuits.Circuit):
//
//	UniqueConstraint(fields=('provider', 'cid'),         name='..._unique_provider_cid')
//	UniqueConstraint(fields=('provider_account', 'cid'), name='..._unique_provideraccount_cid')
//
// Only the first is a natural-key candidate here, and that is a deliberate narrowing rather
// than an oversight -- the argument is in the Descriptor
// (internal/registry/circuits_circuit.go). The short version: the two constraints are keyed on
// *different* references, so the second can only ever fire when the first matched nothing, and
// the object it would then find is by construction a circuit belonging to a **different
// provider**. Adopting that and PATCHing `provider` is a worse outcome than the 409 NetBox
// returns for the create.
//
// **`termination_a` and `termination_z` are absent, and stay absent.** They are real foreign
// keys back to `circuits.CircuitTermination`, and the extractor records both as `read_only`
// with `on_delete=SET_NULL` (hack/testdata/ir-4.6.8.json.gz -> circuits.Circuit). The
// authoritative relationship is the other way round -- `CircuitTermination.circuit` plus
// `term_side`, which is what carries `unique(circuit, term_side)` -- so the operator writes
// the termination side only. Two writers for one relationship is how you get flapping. Nothing
// here can express them, so no request body this kind produces can contain them.
type NetBoxCircuitSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// CID is the provider's circuit ID (docs/netbox-schema.md -> circuits.Circuit,
	// `cid CharField REQ len=100`).
	//
	// Required, and the trailing half of this kind's natural key. Not unique on its own: two
	// providers may both use `100000123`, which is exactly why the constraint is a pair.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	CID string `json:"cid"`

	// ProviderRef is who sells the circuit. Required, because NetBox's column is
	// (`provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT`).
	//
	// It is half of the natural key, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all -- not even a
	// create without the column, which Postgres would refuse anyway.
	//
	// PROTECT, so NetBox refuses to delete a provider while a circuit points at it; that
	// surfaces on the *provider* as Deleting=False, Reason=Protected, and it is why this is
	// not a containment parent (docs/decisions/0003-ownership-and-references.md rule 4).
	ProviderRef ProviderRef `json:"providerRef"`

	// TypeRef is the circuit's classification. Required, because NetBox's column is
	// (`type ForeignKey REQ -> circuits.CircuitType on_delete=PROTECT`).
	//
	// Not part of the identity -- neither constraint names it -- so unlike `providerRef` an
	// unresolved `typeRef` does not make the lookup impossible. It still blocks the write: the
	// column is `REQ`, so a create without it is a 400 from DRF or a NOT NULL violation from
	// Postgres, and the engine reports RefsResolved=False rather than sending it.
	TypeRef CircuitTypeRef `json:"typeRef"`

	// ProviderAccountRef is the billing account the circuit is bought under
	// (`provider_account ForeignKey -> circuits.ProviderAccount on_delete=PROTECT`).
	//
	// Optional, and **not** a natural-key candidate here even though
	// `(provider_account, cid)` is a UniqueConstraint. See the type comment and the
	// Descriptor: the two constraints are keyed on different references, so a second candidate
	// could only ever adopt a circuit sold by another provider.
	// +optional
	ProviderAccountRef *ProviderAccountRef `json:"providerAccountRef,omitempty"`

	// Status is the circuit's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct (docs/netbox-schema.md -> circuits.Circuit, `status ... def=UNRESOLVED:
	// CircuitStatusChoices.STATUS_ACTIVE`).
	// +kubebuilder:default=active
	// +optional
	Status CircuitStatus `json:"status,omitempty"`

	// TenantRef assigns the circuit to a tenant
	// (`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// Not part of the natural key: this kind has a real uniqueness constraint and
	// `(provider, cid)` is the whole of it, so there is nothing for a tenant term to
	// disambiguate.
	//
	// PROTECT, so a circuit holding this reference blocks that tenant's deletion and the
	// refusal is reported as `Deleting=False, Reason=Protected` naming this object.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// InstallDate is when the circuit was turned up, as `YYYY-MM-DD`
	// (docs/netbox-schema.md -> circuits.Circuit, `install_date DateField`).
	//
	// The pattern admits the empty string on purpose. The column is nullable and a `DateField`
	// rejects `""` outright, so an emptied value has to go over the wire as `null` to clear
	// rather than to fail -- which is what the descriptor's `EmptyIsNull` does, the same
	// handling `NetBoxAggregate.dateAdded` gets (#170).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:Pattern=`^(\d{4}-\d{2}-\d{2})?$`
	// +optional
	InstallDate string `json:"installDate,omitempty"`

	// TerminationDate is when the circuit is due to be cancelled, as `YYYY-MM-DD`
	// (`termination_date DateField`). Cleared as `null`, as InstallDate explains.
	//
	// Deliberately unvalidated against InstallDate. NetBox itself does not order the two, and
	// a CEL rule comparing them would reject data NetBox holds today.
	// +kubebuilder:validation:Pattern=`^(\d{4}-\d{2}-\d{2})?$`
	// +optional
	TerminationDate string `json:"terminationDate,omitempty"`

	// CommitRate is the committed information rate in Kbps
	// (`commit_rate PositiveIntegerField`).
	//
	// A pointer with **two** states rather than three, and that is a statement about the
	// column rather than an omission here: it is `blank=True, null=True` and every value it
	// can hold is a real rate, so there is no empty *value* to write. Nil leaves NetBox's own
	// value alone; a number claims and sets it. Clearing the column back to null is NBO-060's
	// audit item, not a state this field can express -- the same two-state shape
	// `NetBoxRackType.outerWidth` has.
	//
	// The bound is Django's `PositiveIntegerField`, which is a 32-bit signed column with a
	// non-negative check.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	CommitRate *int32 `json:"commitRate,omitempty"`

	// Distance is the circuit's length, as a string in the unit DistanceUnit names
	// (docs/netbox-schema.md -> circuits.Circuit, `distance (DistanceMixin) DecimalField
	// decimal(8,2)`).
	//
	// A string and not a float64, for the reason dcim.Site.latitude and
	// NetBoxWirelessLink.distance are: NetBox stores it as `DecimalField(max_digits=8,
	// decimal_places=2)` and returns it as a string, and an OpenAPI `number` round-trips
	// through IEEE-754 on its way in and out of the API server. The engine compares it
	// numerically (internal/netbox/drift.go, scalarEqual), so `"1.5"` and NetBox's `"1.50"`
	// are the same value and produce no PATCH.
	//
	// The pattern caps the fraction at two digits and the integer part at six, which is
	// `decimal(8,2)` written out, and admits no sign.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox.
	// A cleared distance is written as `null` rather than as an empty string, which is what
	// NetBox's nullable DecimalField takes -- see registry.Field.EmptyIsNull. NetBox clears
	// `distance_unit` by itself whenever `distance` is null
	// (netbox/netbox/models/mixins.py:115-117).
	// +kubebuilder:validation:Pattern=`^$|^[0-9]{1,6}(\.[0-9]{1,2})?$`
	// +optional
	Distance string `json:"distance,omitempty"`

	// DistanceUnit is the unit Distance is expressed in: km, m, mi or ft
	// (`distance_unit (DistanceMixin) CharField len=50 choices=DistanceUnitChoices`).
	//
	// The same Go type NetBoxWirelessLink uses, and shared on purpose: both columns come from
	// the one `DistanceMixin` and `DistanceUnitChoices` is not extensible
	// (hack/testdata/ir-4.6.8.json.gz -> enums -> DistanceUnitChoices, `extendable: false`),
	// so there is no FIELD_CHOICES divergence for two enums to protect against.
	//
	// Meaningless without Distance, and NetBox enforces that from its side by nulling the unit
	// on save whenever the distance is null. Undefaulted: the column is nullable with no
	// Django default, and there is no unit that is right by default.
	// +optional
	DistanceUnit DistanceUnit `json:"distanceUnit,omitempty"`

	// Description is free text shown next to the circuit. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the circuit's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxCircuit is one circuits.Circuit in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Its three
// catalogue references -- `providerRef`, `typeRef` and `providerAccountRef` -- are the ordinary
// shared-namespace shape, so each one crossing a namespace needs a NetBoxRefGrant in the target
// namespace (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - `termination_a` and `termination_z`: read-only on the model, and written by NetBox from
//     the termination side. See the spec type comment.
//   - `owner` is `ForeignKey -> users.Owner` and the whole `users` app is deferred.
//   - `_abs_distance` is a DistanceMixin cache the IR records as absent from the write path.
//   - `contacts`, `images` and `group_assignments` are GenericRelations: the reverse of
//     somebody else's foreign key, not columns. `group_assignments` in particular is
//     `circuits.CircuitGroupAssignment`'s side of the relationship, which NBO-057 defers.
//
// **No `terminations:` inline list.** NBO-057's ticket asks for one, and it cannot be written
// yet: an inline child set materialises CRs of a child Kind, and `NetBoxCircuitTermination`
// has no Descriptor in this build. It is deferred with the termination kind itself.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcircuit
// +kubebuilder:printcolumn:name="CID",type=string,JSONPath=`.spec.cid`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCircuit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCircuitSpec  `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCircuit) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCircuit) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxCircuitList is a list of NetBoxCircuit.
// +kubebuilder:object:root=true
type NetBoxCircuitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCircuit `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCircuit{}, &NetBoxCircuitList{})
}
