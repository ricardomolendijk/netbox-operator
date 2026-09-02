package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PowerFeedStatus is one value of NetBox's PowerFeedStatusChoices.
//
// Four values, `netbox/dcim/choices.py:2020` at 4.6.8
// (hack/testdata/ir-4.6.8.json.gz -> enums.PowerFeedStatusChoices, which records the members
// where docs/netbox-schema.md records only the choice *class* -- the AST walk cannot evaluate
// one).
//
// Its own Go type rather than a reuse of RackStatus or SiteStatus: the members differ
// (`failed` is here and nowhere else; `reserved`, `available`, `staging` and `retired` are
// not), and this ChoiceSet declares `key = 'PowerFeed.status'`, so a deployment can add
// values through FIELD_CHOICES. Sharing one Go enum would make a value added to one model
// silently legal on another. Enumerated anyway, following SiteStatus and RackStatus, because
// a typo caught by `kubectl apply` is worth more than an extension nobody has made.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=offline;active;planned;failed
type PowerFeedStatus string

const (
	// PowerFeedStatusOffline is a feed that is not energised.
	PowerFeedStatusOffline PowerFeedStatus = "offline"

	// PowerFeedStatusActive is a feed in service, and NetBox's own default.
	PowerFeedStatusActive PowerFeedStatus = "active"

	// PowerFeedStatusPlanned is a feed that does not physically exist yet.
	PowerFeedStatusPlanned PowerFeedStatus = "planned"

	// PowerFeedStatusFailed is a feed that has failed.
	PowerFeedStatusFailed PowerFeedStatus = "failed"
)

// PowerFeedType is one value of NetBox's PowerFeedTypeChoices.
//
// Two values, `netbox/dcim/choices.py:2036` at 4.6.8, and `extendable: false` -- unlike
// PowerFeedStatusChoices this ChoiceSet declares no `key`, so FIELD_CHOICES cannot widen it
// and the enum is closed on both sides (hack/testdata/ir-4.6.8.json.gz ->
// enums.PowerFeedTypeChoices).
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=primary;redundant
type PowerFeedType string

const (
	// PowerFeedTypePrimary is the A feed, and NetBox's own default.
	PowerFeedTypePrimary PowerFeedType = "primary"

	// PowerFeedTypeRedundant is the B feed of a redundant pair.
	PowerFeedTypeRedundant PowerFeedType = "redundant"
)

// PowerFeedSupply is one value of NetBox's PowerFeedSupplyChoices.
//
// Two values, `netbox/dcim/choices.py:2047` at 4.6.8, `extendable: false`.
//
// It is what makes a negative Voltage meaningful: a DC feed's voltage may be signed, which is
// why the column is a `SmallIntegerField` and not a `PositiveSmallIntegerField` like its two
// neighbours (docs/netbox-schema.md -> dcim.PowerFeed).
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=ac;dc
type PowerFeedSupply string

const (
	// PowerFeedSupplyAC is alternating current, and NetBox's own default.
	PowerFeedSupplyAC PowerFeedSupply = "ac"

	// PowerFeedSupplyDC is direct current.
	PowerFeedSupplyDC PowerFeedSupply = "dc"
)

// PowerFeedPhase is one value of NetBox's PowerFeedPhaseChoices.
//
// Two values, `netbox/dcim/choices.py:2058` at 4.6.8, `extendable: false`. The wire spellings
// are `single-phase` and `three-phase`, which are not the labels -- NetBox renders them as
// "Single phase" and "Three-phase" -- and the value is what a manifest writes.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=single-phase;three-phase
type PowerFeedPhase string

const (
	// PowerFeedPhaseSingle is a single-phase feed, and NetBox's own default.
	PowerFeedPhaseSingle PowerFeedPhase = "single-phase"

	// PowerFeedPhaseThree is a three-phase feed.
	PowerFeedPhaseThree PowerFeedPhase = "three-phase"
)

// NetBoxPowerFeedSpec describes one dcim.PowerFeed.
//
// One circuit from a NetBoxPowerPanel, and the kind NBO-052 exists for: it is the first in the
// catalogue whose defaults are **not the model's own**.
//
//	voltage          SmallIntegerField          def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_VOLTAGE')
//	amperage         PositiveSmallIntegerField  def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_AMPERAGE')
//	max_utilization  PositiveSmallIntegerField  def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')
//
// (docs/netbox-schema.md -> dcim.PowerFeed; the same three carry `default_unresolved: true`
// in the committed IR, hack/testdata/ir-4.6.8.json.gz -> dcim.PowerFeed.)
//
// A `ConfigItem` is read from the *target
// NetBox's* configuration at write time, not from the model, so there is no value the CRD
// could carry that would be right on every instance. Baking NetBox's shipped 120/15/80 into
// the CRD would silently reconfigure every feed on an installation configured for 230 V.
//
// So Voltage, Amperage and MaxUtilization are optional pointers with **no
// `+kubebuilder:default`**, and the whole of the rule falls out of that one decision: a nil
// pointer marshals to nothing, `payload.desired` skips a spec key with no value, `netbox.Drift`
// only considers fields present in desired, and `restoreEmpty` has no empty form for a pointer
// so field ownership never puts one back. Omitted therefore means "whatever this NetBox is
// configured for" end to end, and a subsequent reconcile reports no drift against whatever
// NetBox defaulted to. See internal/reconciler/dcim_powerfeed_test.go, which pins each of those
// four steps, and docs/reference/netboxpowerfeed.md#server-side-defaults.
//
// The four enums below are the contrast, and they *are* defaulted. Their column defaults are
// model-level constants -- `PowerFeedStatusChoices.STATUS_ACTIVE` and friends -- not
// `ConfigItem` lookups, so the value is the same on every NetBox and a defaulted field the
// operator manages from the first reconcile is strictly better than one it can never correct.
//
// Identity is `(power_panel, name)`:
//
//	meta.constraints: (models.UniqueConstraint(fields=('power_panel', 'name'),
//	   name='%(app_label)s_%(class)s_unique_power_panel_name'),)
//
// (docs/netbox-schema.md -> dcim.PowerFeed; hack/testdata/ir-4.6.8.json.gz ->
// dcim.PowerFeed.natural_keys, filters `power_panel_id` and `name`.) `rack` and `tenant` are
// both optional and neither is constrained, so neither is in the key.
type NetBoxPowerFeedSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the feed's name.
	//
	// Unique per *power panel* (docs/netbox-schema.md ->
	// dcim.PowerFeed.meta.constraints), so `Feed A` on two panels is legitimate NetBox state
	// and two on one panel is not.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// PowerPanelRef is the panel this feed comes off. Required, because NetBox's column is
	// (`power_panel ForeignKey REQ -> dcim.PowerPanel on_delete=PROTECT`).
	//
	// The natural key reads it, so until it resolves the object reports RefsResolved=False
	// naming this field and makes no NetBox write at all. That matters more here than on most
	// kinds: `name` alone is not unique anywhere, so a lookup that dropped the panel would
	// match every feed of that name in the whole NetBox and adopt one (#206, #216).
	//
	// **Not a containment reference.** `PROTECT`, so NetBox refuses to delete a panel while a
	// feed points at it and there is no server-side cascade for an owner reference to mirror
	// (docs/decisions/0003-ownership-and-references.md rule 4). Deleting the
	// NetBoxPowerPanel CR reports `Deleting=False, Reason=Protected` on the *panel*; delete
	// the feeds first.
	PowerPanelRef PowerPanelRef `json:"powerPanelRef"`

	// RackRef is the rack this feed serves
	// (docs/netbox-schema.md -> dcim.PowerFeed, `rack ForeignKey -> dcim.Rack
	// on_delete=PROTECT`).
	//
	// Optional, and in no natural-key candidate: NetBox constrains nothing on it, so two feeds
	// of one name on one panel are refused however their racks differ.
	//
	// A pointer to the typed alias, so it has two states rather than three: absent means
	// unmanaged, and a value claims the column (#185).
	// +optional
	RackRef *RackRef `json:"rackRef,omitempty"`

	// TenantRef is who the feed belongs to
	// (docs/netbox-schema.md -> dcim.PowerFeed, `tenant ForeignKey -> tenancy.Tenant
	// on_delete=PROTECT`).
	//
	// Not a containment parent and never a cascade -- see docs/reference/netboxtenant.md on
	// why `tenantRef` does not cascade, and docs/concepts/references.md on why a namespace
	// does not imply a tenant.
	//
	// Two states, as RackRef explains.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Status is the feed's lifecycle state.
	//
	// Defaulted to NetBox's own default, which is a model-level constant rather than a
	// `ConfigItem` -- so unlike Voltage below, the value is the same on every NetBox and the
	// operator can manage the field from the first reconcile. A defaulted field that never
	// reaches a payload is a field the operator can never correct.
	// +kubebuilder:default=active
	// +optional
	Status PowerFeedStatus `json:"status,omitempty"`

	// Type is whether this is the primary feed or the redundant one.
	//
	// Defaulted for the reason Status is.
	// +kubebuilder:default=primary
	// +optional
	Type PowerFeedType `json:"type,omitempty"`

	// Supply is whether the feed is AC or DC.
	//
	// Defaulted for the reason Status is. It is also what decides whether a negative Voltage
	// makes sense.
	// +kubebuilder:default=ac
	// +optional
	Supply PowerFeedSupply `json:"supply,omitempty"`

	// Phase is whether the feed is single- or three-phase.
	//
	// Defaulted for the reason Status is.
	// +kubebuilder:default="single-phase"
	// +optional
	Phase PowerFeedPhase `json:"phase,omitempty"`

	// Voltage is the feed's voltage.
	//
	// **Deliberately not defaulted.** The column's default is
	// `ConfigItem('POWERFEED_DEFAULT_VOLTAGE')` (docs/netbox-schema.md -> dcim.PowerFeed),
	// resolved from the target NetBox's own configuration rather than from the model, so
	// there is no value this CRD could carry that would be right on every instance --
	// NetBox ships 120 and an installation in Europe is configured for 230. Omit the field and
	// the POST body carries no `voltage` key at all, NetBox applies its own configured value,
	// and no later reconcile reports drift against it. Set it and it is written and
	// drift-corrected like any other column.
	//
	// A pointer, and that is what makes the above true rather than merely intended: a nil
	// pointer marshals to nothing, and specFields.restoreEmpty deliberately has no empty form
	// for a pointer type, so field ownership cannot put a zero back
	// (internal/reconciler/ownership.go, docs/concepts/field-ownership.md). Removing the field
	// from a manifest therefore stops managing the voltage; it does not clear it.
	//
	// The bounds are the Django field's own -- `SmallIntegerField`, so a Postgres `smallint`.
	// **Signed**, unlike Amperage and MaxUtilization, because a DC feed's voltage may be
	// negative. NetBox's model carries validators beyond the column type; none of them are
	// recorded in any committed artefact (hack/testdata/ir-4.6.8.json.gz records `sql` and
	// `api` metadata and no `validators` key at all), so they are not restated here as bounds
	// that could be wrong. A value NetBox's own `clean()` refuses comes back as a 400,
	// reported as `Ready=False, Reason=Invalid`.
	// +kubebuilder:validation:Minimum=-32768
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Voltage *int32 `json:"voltage,omitempty"`

	// Amperage is the feed's rated current, in amps.
	//
	// **Deliberately not defaulted**, for the reason Voltage gives: the column's default is
	// `ConfigItem('POWERFEED_DEFAULT_AMPERAGE')`.
	//
	// `PositiveSmallIntegerField`, so the bounds are 0 to 32767 -- the Django field's own, not
	// a restatement of a validator no committed artefact records.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Amperage *int32 `json:"amperage,omitempty"`

	// MaxUtilization is the maximum permissible draw, as a percentage.
	//
	// **Deliberately not defaulted**, for the reason Voltage gives: the column's default is
	// `ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')`.
	//
	// `PositiveSmallIntegerField`, so the bounds are 0 to 32767. Wider than the percentage
	// the field means, and deliberately so: NetBox's model narrows it further and the
	// committed artefacts do not record by how much, so the CRD bound is the one that is
	// checkable. A value outside NetBox's own range comes back as a 400, reported as
	// `Ready=False, Reason=Invalid`.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	MaxUtilization *int32 `json:"maxUtilization,omitempty"`

	// MarkConnected marks the feed as connected without a cable
	// (docs/netbox-schema.md -> dcim.PowerFeed,
	// `mark_connected (CabledObjectModel) BooleanField def=False`).
	//
	// A pointer, and for a different reason from Voltage's: the default here is the model's
	// own literal `False`, so the risk is not a wrong default but an unmanaged one. A plain
	// bool cannot tell "not managed" from "managed as false", so adopting a feed a human had
	// marked connected would silently unmark it on the first reconcile. Nil leaves NetBox's
	// value alone; `false` writes false (docs/concepts/field-ownership.md). The same choice
	// NetBoxInterface makes for the same column on the same base class.
	// +optional
	MarkConnected *bool `json:"markConnected,omitempty"`

	// Description is free text shown next to the feed.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the feed's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Clearable on the same three-state terms as Description.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxPowerFeed is one dcim.PowerFeed in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// **A legal cable termination.** `dcim.PowerFeed` mixes in `CabledObjectModel`
// (docs/netbox-schema.md -> dcim.PowerFeed, bases), and `dcim.powerfeed` was already in
// registry.cabledObjectTypes with a `powerFeedRef` member on both ends of NetBoxCable's
// termination union and a PowerFeedRef alias to point at -- all of it declared ahead of this
// Kind by NBO-049. Registering this Descriptor is what makes that member *resolvable*:
// internal/resolver dispatches every mode through Descriptors.Get(Field.Target), so until now
// a cable terminating on a feed by name reported RefKindUnavailable and only `id` mode worked.
// Nothing in dcim_cable.go changes.
//
// Absent deliberately:
//
//   - **`available_power` is not here, and not in `status` either.** NetBox recomputes it from
//     voltage, amperage and phase, and at 4.6.8 the serializer does not expose it at all:
//     it is absent from `PowerFeedSerializer.fields`
//     (hack/testdata/api-schema-4.6.8.json.gz -> serializers.PowerFeedSerializer) and from the
//     kind's `write_path` (hack/testdata/ir-4.6.8.json.gz -> dcim.PowerFeed.write_path). So
//     there is no committed evidence the REST API returns it, and a status field promising a
//     value the API never sends would report zero forever. NBO-052 asks for it in status;
//     that needs a read against a live NetBox first, and is reported rather than guessed.
//   - **No containment parent.** `power_panel`, `rack` and `tenant` are all `PROTECT`, so
//     nothing on the server side disappears when a parent does
//     (docs/decisions/0003-ownership-and-references.md rule 4).
//   - `_path`, `cable`, `cable_end`, `cable_connector`, `cable_positions` and
//     `cable_terminations` belong to the cable graph. NetBoxCable owns it, so they are
//     read-only here rather than absent: a feed that adopted a cabled row must not PATCH the
//     cable away. The dcim.Interface treatment, for the same columns off the same base class.
//   - `link_peers`, `connected_endpoints` and `_occupied` are computed serializer fields over
//     the cable path, not columns.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind
//     (hack/coverage-exclusions.yaml).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbpowerfeed
// +kubebuilder:printcolumn:name="Panel",type=string,JSONPath=`.spec.powerPanelRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxPowerFeed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxPowerFeedSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (f *NetBoxPowerFeed) NetBoxSpec() *NetBoxObjectSpec { return &f.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (f *NetBoxPowerFeed) NetBoxStatus() *NetBoxObjectStatus { return &f.Status }

// NetBoxPowerFeedList is a list of NetBoxPowerFeed.
// +kubebuilder:object:root=true
type NetBoxPowerFeedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxPowerFeed `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxPowerFeed{}, &NetBoxPowerFeedList{})
}
