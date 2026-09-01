package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CableType is one value of NetBox's CableTypeChoices: what the cable physically is.
//
// docs/netbox-schema.md -> dcim.Cable records the column as
// `type CharField len=50 choices=CableTypeChoices` -- the choice *class* and no members,
// because the AST walk behind the digest cannot evaluate a Django ChoiceSet. The 33 values
// below are `netbox/dcim/choices.py:1840`, `CableTypeChoices`, in the same 4.6.8 tree, and
// they are checked against the machine-extracted choice list by TestCableEnumsMatchTheSchema
// rather than transcribed and trusted.
//
// No per-value Go constants, for the reason InterfaceType has none: nothing in this
// repository branches on a cable type, so 33 exported identifiers would be 33 lines nobody
// reads. The list that matters is the one the API server enforces, and that is the marker.
//
// The empty value is a member on purpose. `type` is `blank=True, null=True`, so `""` is how a
// cable of unknown type is spelled and the only way to clear a type that has been set. It
// carries no tri-state note and cannot: an `enum` is exactly the validation
// TestClearableFieldsDocumentBothStatesInTheSchema treats as forbidding the empty value, so
// the note and the enum would contradict each other in the generated schema. The empty member
// is the statement instead -- and the descriptor sends it as `null` rather than as `""`
// (registry.Field.EmptyIsNull), because NetBox's ChoiceField renders an empty choice as
// `null` on read and a `""` compared against that `null` is drift that never settles.
//
// +kubebuilder:validation:Enum="";"cat3";"cat5";"cat5e";"cat6";"cat6a";"cat7";"cat7a";"cat8";"mrj21-trunk";"dac-active";"dac-passive";"coaxial";"rg-6";"rg-8";"rg-11";"rg-59";"rg-62";"rg-213";"lmr-100";"lmr-200";"lmr-400";"mmf";"mmf-om1";"mmf-om2";"mmf-om3";"mmf-om4";"mmf-om5";"smf";"smf-os1";"smf-os2";"aoc";"power";"usb"
type CableType string

// LinkStatus is one value of NetBox's LinkStatusChoices: whether a link carries traffic.
//
// docs/netbox-schema.md -> dcim.Cable records the column as `status CharField len=50
// def=UNRESOLVED:LinkStatusChoices.STATUS_CONNECTED choices=LinkStatusChoices`. The three
// values are `netbox/dcim/choices.py:1965`, `LinkStatusChoices`.
//
// No empty member: the column is neither `blank` nor `null` and carries a default, so there
// is no such thing as a cable with no status.
//
// +kubebuilder:validation:Enum=connected;planned;decommissioning
type LinkStatus string

const (
	// LinkStatusConnected is a live link. NetBox's own default.
	LinkStatusConnected LinkStatus = "connected"

	// LinkStatusPlanned is a link that is documented and not yet installed. It contributes
	// no active CablePath.
	LinkStatusPlanned LinkStatus = "planned"

	// LinkStatusDecommissioning is a link on its way out, still physically present.
	LinkStatusDecommissioning LinkStatus = "decommissioning"
)

// CableProfile is one value of NetBox's CableProfileChoices: the connector-and-position
// geometry of a multi-strand cable.
//
// docs/netbox-schema.md -> dcim.Cable records the column as `profile CharField len=50
// choices=CableProfileChoices`. The 26 values are `netbox/dcim/choices.py:1764`,
// `CableProfileChoices`, and they read as `<connectors>C<positions>P`: `trunk-8c4p` is eight
// connectors of four positions each.
//
// **No empty member, so a profile cannot be cleared through the operator** -- the same
// answer, for the same reason, that `InterfaceMode` gives. The column is `blank=True` and
// *not* `null=True` (docs/netbox-schema.md -> dcim.Cable; the IR records no `allow_null` on
// its ChoiceField either), so the empty value NetBox stores is `""` while the value it
// returns for it is `null` -- and a `""` sent against a `null` read is a diff that never
// settles. On this kind that is worse than on any other: a permanent diff on a
// `UpdateStrategy: Recreate` kind is a cable deleted and re-created on every resync. Set a
// profile or never set one; to remove one, delete the cable.
//
// +kubebuilder:validation:Enum="single-1c1p";"single-1c2p";"single-1c4p";"single-1c6p";"single-1c8p";"single-1c12p";"single-1c16p";"trunk-2c1p";"trunk-2c2p";"trunk-2c4p";"trunk-2c4p-shuffle";"trunk-2c6p";"trunk-2c8p";"trunk-2c12p";"trunk-4c1p";"trunk-4c2p";"trunk-4c4p";"trunk-4c4p-shuffle";"trunk-4c6p";"trunk-4c8p";"trunk-8c4p";"breakout-1c2p-2c1p";"breakout-1c4p-4c1p";"breakout-1c6p-6c1p";"breakout-1c8p-8c1p";"breakout-2c4p-8c1p-shuffle"
type CableProfile string

// CableLengthUnit is one value of NetBox's CableLengthUnitChoices: the unit `length` is
// expressed in.
//
// docs/netbox-schema.md -> dcim.Cable records the column as `length_unit CharField len=50
// choices=CableLengthUnitChoices`. The six values are `netbox/dcim/choices.py:1978`,
// `CableLengthUnitChoices`.
//
// The empty value is a member, and the unit is nullable where the profile is not: NetBox
// declares the ChoiceField `allow_null=True`
// (netbox/dcim/api/serializers_/cables.py:40, `length_unit`), so the cleared state goes over
// the wire as `null` and settles. No tri-state note, for the reason CableType gives.
//
// +kubebuilder:validation:Enum="";"km";"m";"cm";"mi";"ft";"in"
type CableLengthUnit string

const (
	// CableLengthUnitKilometers is `km`.
	CableLengthUnitKilometers CableLengthUnit = "km"

	// CableLengthUnitMeters is `m`.
	CableLengthUnitMeters CableLengthUnit = "m"

	// CableLengthUnitCentimeters is `cm`.
	CableLengthUnitCentimeters CableLengthUnit = "cm"

	// CableLengthUnitMiles is `mi`.
	CableLengthUnitMiles CableLengthUnit = "mi"

	// CableLengthUnitFeet is `ft`.
	CableLengthUnitFeet CableLengthUnit = "ft"

	// CableLengthUnitInches is `in`.
	CableLengthUnitInches CableLengthUnit = "in"
)

// CableTerminationTarget selects one object a cable end terminates on.
//
// The union `docs/reference/genericref.md` has listed as "lands with NBO-049", and the
// **first to-many polymorphic reference in the catalogue**: `aTerminations` and
// `bTerminations` are *lists* of this, because NetBox 4.x permits several terminations per
// cable end.
//
// It is also the union that does not sit on two columns. `dcim.CableTermination` carries
// `termination_type` / `termination_id` (docs/netbox-schema.md -> dcim.CableTermination), but
// those live on the termination *row* and that whole endpoint is read-only:
// `CableTerminationSerializer.Meta.read_only_fields = fields`
// (netbox/dcim/api/serializers_/cables.py:71). The writable form is the cable's own
// `a_terminations` / `b_terminations`, declared
// `GenericObjectSerializer(many=True, required=False)`
// (netbox/dcim/api/serializers_/cables.py:40) over `{object_type, object_id}`
// (netbox/netbox/api/serializers/generic.py:15) -- so the pair is nested inside a list
// element rather than being two columns of the cable. See registry.GenericFKList and
// docs/concepts/generic-refs.md, "A to-many pair".
//
// Exactly one member must be set, and the field itself is required by the enclosing list
// being required: `termination_type ForeignKey REQ` and `termination_id
// PositiveBigIntegerField REQ` (docs/netbox-schema.md -> dcim.CableTermination), so there is
// no such thing as a termination that terminates on nothing and the empty union is not an
// instruction here.
//
// The members are every model that mixes in `dcim.CabledObjectModel` -- the nine classes
// whose `bases:` line names it in docs/netbox-schema.md, which is what
// `CableTermination.termination` may point at. Only `interfaceRef` has a registered Kind in
// this build; the other eight report
// `RefsResolved=False, Reason=RefKindUnavailable` in **all four** ref modes until their Kinds
// land, which is the correct answer and is reported rather than worked around
// (docs/concepts/generic-refs.md, "Kinds that do not exist yet"). The issue's claim that they
// are "accepted by `id` and rejected by name" is not what the engine does and not what it
// should do: `slug`, `lookup` and `id` all need the target's NetBox endpoint, and only a
// Descriptor holds one.
//
// The type strings in the comments below are not written down against the members. Each is
// the target Kind's own `Descriptor.ObjectType`, so `circuits.circuittermination` is spelled
// once in the codebase -- lowercased and unpunctuated.
//
// +kubebuilder:validation:XValidation:rule="[has(self.interfaceRef), has(self.consolePortRef), has(self.consoleServerPortRef), has(self.powerPortRef), has(self.powerOutletRef), has(self.frontPortRef), has(self.rearPortRef), has(self.powerFeedRef), has(self.circuitTerminationRef)].filter(x, x).size() == 1",message="exactly one of interfaceRef, consolePortRef, consoleServerPortRef, powerPortRef, powerOutletRef, frontPortRef, rearPortRef, powerFeedRef or circuitTerminationRef must be set"
type CableTerminationTarget struct {
	// InterfaceRef terminates the cable on a device interface -> `dcim.interface`. The one
	// member whose Kind this build carries.
	// +optional
	InterfaceRef *InterfaceRef `json:"interfaceRef,omitempty"`

	// ConsolePortRef terminates the cable on a console port -> `dcim.consoleport`.
	// +optional
	ConsolePortRef *ConsolePortRef `json:"consolePortRef,omitempty"`

	// ConsoleServerPortRef terminates the cable on a console server port ->
	// `dcim.consoleserverport`.
	// +optional
	ConsoleServerPortRef *ConsoleServerPortRef `json:"consoleServerPortRef,omitempty"`

	// PowerPortRef terminates the cable on a power port -> `dcim.powerport`.
	// +optional
	PowerPortRef *PowerPortRef `json:"powerPortRef,omitempty"`

	// PowerOutletRef terminates the cable on a power outlet -> `dcim.poweroutlet`.
	// +optional
	PowerOutletRef *PowerOutletRef `json:"powerOutletRef,omitempty"`

	// FrontPortRef terminates the cable on a patch panel's front port -> `dcim.frontport`.
	// +optional
	FrontPortRef *FrontPortRef `json:"frontPortRef,omitempty"`

	// RearPortRef terminates the cable on a patch panel's rear port -> `dcim.rearport`.
	// +optional
	RearPortRef *RearPortRef `json:"rearPortRef,omitempty"`

	// PowerFeedRef terminates the cable on a power feed -> `dcim.powerfeed`.
	// +optional
	PowerFeedRef *PowerFeedRef `json:"powerFeedRef,omitempty"`

	// CircuitTerminationRef terminates the cable on a circuit ->
	// `circuits.circuittermination`.
	// +optional
	CircuitTerminationRef *CircuitTerminationRef `json:"circuitTerminationRef,omitempty"`
}

// NetBoxCableSpec describes one dcim.Cable.
//
// Three facts about `dcim.Cable` shape this whole kind, and all three are in
// docs/netbox-schema.md.
//
// **It has no `meta.constraints` at all.** `meta.ordering: ('pk',)` is the entire Meta. There
// is no natural key on the cable row, so identity lives in the terminations -- and the only
// question NetBox answers about them from the cable's own endpoint is
// `CableFilterSet.termination_a_type` / `termination_a_id` / `termination_b_type` /
// `termination_b_id` (netbox/dcim/filtersets.py:2637). Those four filters *are* this kind's
// natural key. What makes them identity rather than a search is
// `unique(termination_type, termination_id)` on dcim.CableTermination: an object is terminated
// by **at most one cable, globally**, so a cable's A-end termination names one cable or none.
//
// **The termination lists are effectively immutable.** They are not columns of the cable;
// they are CableTermination rows, and that unique constraint keeps the wanted endpoint
// occupied by the *old* cable while a replacement is created. So the replacement cannot be
// created first, and `UpdateStrategy: Recreate` with `RecreateOn: [a_terminations,
// b_terminations]` is the only legal order: DELETE, then POST. Everything else here --
// `type`, `status`, `profile`, `tenantRef`, `bundleRef`, `label`, `color`, `length`,
// `lengthUnit`, description, comments, tags, custom fields -- is an ordinary PATCH. See
// docs/reference/netboxcable.md for what the window between the delete and the create costs.
//
// **`_abs_length` and `dcim.CablePath` are NetBox's.** `_abs_length` is a denormalised cache
// (docs/netbox-schema.md preamble: every `_`-prefixed column is), and NetBox rebuilds every
// CablePath traversing a cable whenever the cable changes. The operator writes neither, and
// there is no spec field for either.
type NetBoxCableSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ATerminations is what the cable's A end is plugged into (`a_terminations`).
	//
	// Required and non-empty, which is stricter than NetBox: an unterminated cable is legal
	// server state -- `CableFilterSet.unterminated` exists to find them -- and it is state
	// this operator cannot manage, because a cable with no terminations has no natural key
	// and therefore no identity to find itself by on the next reconcile. Refusing it at
	// admission is more useful than creating an anonymous row and then reporting
	// `Ready=False, Reason=WaitingForKey` about it forever.
	//
	// A **set**, not a list: order inside it is not data. NetBox stores the elements as
	// CableTermination rows and returns them in `('cable', 'cable_end', 'connector', 'pk')`
	// order (docs/netbox-schema.md -> dcim.CableTermination, meta.ordering), so the operator
	// sorts and deduplicates before comparing and before writing. Reordering entries
	// produces zero API writes.
	//
	// Changing the *membership* deletes the cable and creates a new one -- see the type
	// comment, and `Recreated` in docs/reference/netboxcable.md.
	//
	// Bounded at 16. `unique(cable, cable_end, connector)` on dcim.CableTermination allows one
	// termination row per connector per end, and the widest geometry
	// `CableProfileChoices` knows is `trunk-8c4p`: eight connectors
	// (netbox/dcim/choices.py:1764). Sixteen is twice that, so a cable that declares no
	// profile still has room, and a manifest above it is a modelling mistake rather than a
	// cable. The marker is not optional either way -- a list whose items carry CEL rules and
	// declares no `maxItems` makes the whole CRD refused at install
	// (docs/concepts/references.md#a-list-needs-a-bound).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	ATerminations []CableTerminationTarget `json:"aTerminations"`

	// BTerminations is what the cable's B end is plugged into (`b_terminations`). Everything
	// ATerminations says applies here unchanged.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	BTerminations []CableTerminationTarget `json:"bTerminations"`

	// Type is what the cable physically is (`type CharField len=50
	// choices=CableTypeChoices`).
	//
	// Not part of the identity: changing it is a PATCH, not a recreate.
	// +optional
	Type CableType `json:"type,omitempty"`

	// Status is whether the link carries traffic.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct (docs/netbox-schema.md -> dcim.Cable, `status ... def=
	// UNRESOLVED:LinkStatusChoices.STATUS_CONNECTED`, which is `connected`).
	// +kubebuilder:default=connected
	// +optional
	Status LinkStatus `json:"status,omitempty"`

	// Profile is the connector-and-position geometry of a multi-strand cable
	// (`profile CharField len=50 choices=CableProfileChoices`).
	//
	// There is no way to clear it once set -- see CableProfile for why, and it is a real
	// limitation rather than an oversight.
	// +optional
	Profile CableProfile `json:"profile,omitempty"`

	// TenantRef is the tenant the cable belongs to (docs/netbox-schema.md -> dcim.Cable,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// Not the containment reference and not part of the identity. PROTECT means deleting the
	// tenant is *refused* while a cable names it rather than cascading, so an owner reference
	// here would garbage-collect the CR while NetBox still held the row
	// (docs/concepts/ownership.md).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// BundleRef is the bundle this cable is pulled with (docs/netbox-schema.md -> dcim.Cable,
	// `bundle ForeignKey -> dcim.CableBundle on_delete=SET_NULL`).
	//
	// SET_NULL, so deleting the bundle clears the column and the next reconcile PATCHes it
	// back -- which is why this is an ordinary reference and not a containment one.
	// +optional
	BundleRef *CableBundleRef `json:"bundleRef,omitempty"`

	// Label is the cable's printed label (docs/netbox-schema.md -> dcim.Cable,
	// `label CharField len=100`).
	//
	// Not part of the identity, deliberately: two cables may carry one label, NetBox has no
	// constraint saying otherwise, and changing a label is a PATCH rather than a recreate.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Label string `json:"label,omitempty"`

	// Color is the cable's colour as six hexadecimal digits, without a leading `#`
	// (docs/netbox-schema.md -> dcim.Cable, `color ColorField`).
	//
	// Not defaulted, unlike a tag's: `dcim.Cable.color` carries no `def=` at all, so an
	// uncoloured cable is a real state and defaulting one would make the operator paint every
	// cable it adopts.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:Pattern=`^$|^[0-9a-f]{6}$`
	// +optional
	Color string `json:"color,omitempty"`

	// Length is how long the cable is, in LengthUnit, as a string.
	//
	// A string and not a float64: NetBox stores it as `length DecimalField decimal(8,2)` and
	// returns it padded, `"3.50"` for a spec that said `"3.5"`. An OpenAPI `number`
	// round-trips through IEEE-754 on the way in and out of the API server, and the engine
	// compares two numeric strings numerically (internal/netbox/drift.go, scalarEqual), so
	// `"3.5"` and NetBox's `"3.50"` produce no PATCH.
	//
	// The pattern is the numeric-format rule read straight off `decimal(8,2)`: eight digits,
	// two of them after the point, so up to 999999.99. The `^$` alternative is the clear.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). A cleared length is written as `null` rather than as
	// an empty string, which is what NetBox's nullable DecimalField takes -- see
	// registry.Field.EmptyIsNull.
	//
	// `_abs_length` is NetBox's own normalisation of this value into a single unit and is a
	// read-only cache: the operator never writes it.
	// +kubebuilder:validation:Pattern=`^$|^[0-9]{1,6}(\.[0-9]{1,2})?$`
	// +optional
	Length string `json:"length,omitempty"`

	// LengthUnit is the unit Length is expressed in (`length_unit CharField len=50
	// choices=CableLengthUnitChoices`).
	//
	// NetBox's own `Cable.clean()` requires a unit whenever a length is set and clears the
	// length when the unit is removed; the operator does not duplicate that rule, so a length
	// without a unit is a `400` surfaced as `Ready=False, Reason=Invalid` carrying NetBox's
	// message.
	// +optional
	LengthUnit CableLengthUnit `json:"lengthUnit,omitempty"`

	// Description is free text shown next to the cable (docs/netbox-schema.md -> dcim.Cable,
	// `description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the cable's long-form note (docs/netbox-schema.md -> dcim.Cable,
	// `comments (PrimaryModel) TextField`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	//
	// Unbounded on purpose: the column is a `TextField`, so NetBox declares no length and a
	// `MaxLength` here would be a limit the operator invented.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxCable is one dcim.Cable in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// **No containment parent, and none possible.** ADR-0003 rule 4 makes the containment parent
// whichever foreign key NetBox cascades, and the cable has none: `tenant` is PROTECT, `bundle`
// is SET_NULL, and the termination union -- the reference that really does describe what the
// cable belongs to -- cascades in the *wrong direction*. `dcim.CabledObjectModel.cable` is
// `on_delete=SET_NULL` (docs/netbox-schema.md), so deleting an interface does **not** delete
// the cable plugged into it; it clears the interface's own denormalised `cable` column while
// the cable and its CableTermination rows survive. An owner reference on `aTerminations` would
// therefore garbage-collect the CR while NetBox still held the cable, which is exactly the
// mistake docs/concepts/ownership.md refuses to make. `CascadeOnDelete` is stated as `false`
// on every member of the union rather than left unstated, so this is a recorded answer instead
// of an accident.
//
// `deletionPolicy` defaults to `Delete`. A cable is a statement about a physical connection
// that the manifest is the record of, and re-creating one loses nothing that was not in Git --
// unlike an IPAM allocation, which is the exception decision #176 carved out.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcable
// +kubebuilder:printcolumn:name="Label",type=string,JSONPath=`.spec.label`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCableSpec    `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCable) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCable) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxCableList is a list of NetBoxCable.
// +kubebuilder:object:root=true
type NetBoxCableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCable `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCable{}, &NetBoxCableList{})
}
