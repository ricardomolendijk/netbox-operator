package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimPowerFeedDescriptor()) }

// dcimPowerFeedDescriptor is dcim.PowerFeed as data.
//
// Identity is a real database constraint, like its panel's and unlike dcim.Rack's:
//
//	meta.constraints: (models.UniqueConstraint(fields=('power_panel', 'name'),
//	   name='%(app_label)s_%(class)s_unique_power_panel_name'),)
//
// (docs/netbox-schema.md -> dcim.PowerFeed.) The committed IR resolves it to filters as well
// as columns -- `dcim.PowerFeed.natural_keys` is one candidate over
// `{column: power_panel, filter: power_panel_id}` and `{column: name, filter: name}`, with
// `null_fields: []` and `unusable: null` (hack/testdata/ir-4.6.8.json.gz) -- and both filters
// are registered on `PowerFeedFilterSet` (same file, `dcim.PowerFeed.filters`).
//
// `rack` and `tenant` are optional and unconstrained, so neither is a candidate and neither is
// a pin. One candidate, no fallback.
//
// **This Descriptor is the whole of NBO-052's cable bullet.** `dcim.powerfeed` is already in
// cabledObjectTypes(), `powerFeedRef` is already a member of both ends of dcim.Cable's
// termination union, and v1alpha1.PowerFeedRef already exists -- all declared ahead of the
// Kind by NBO-049, deliberately. internal/resolver dispatches every mode through
// Descriptors.Get(Field.Target), so until this init() ran a cable terminating on a feed by
// *name* reported RefKindUnavailable and only `id` mode worked. Registering here closes that
// with no edit to dcim_cable.go, and registry.Validate's ErrMemberTypeNotAllowed cross-check
// now has a Kind to check the member against.
//
// **No containment parent.** `power_panel`, `rack` and `tenant` are all `on_delete=PROTECT`
// (docs/netbox-schema.md -> dcim.PowerFeed), so validateContainment would refuse any of them
// (ErrContainmentNotCascade): NetBox declines to delete a panel that still has feeds, so a
// cluster-side cascade would garbage-collect the CR and leave the row.
//
// **Nothing here defaults voltage, amperage or max_utilization**, and that absence is the
// point of the ticket. Their column defaults are `ConfigItem(...)` lookups against the target
// NetBox's configuration rather than model constants (docs/netbox-schema.md -> dcim.PowerFeed;
// `default_unresolved: true` on all three in hack/testdata/ir-4.6.8.json.gz), so the CRD
// carries no `+kubebuilder:default` for them and they reach the payload only when set. A
// Descriptor cannot express a default at all, which is exactly right here: the three are
// ordinary field-map entries, and "unset means the server's value" is a property of the
// engine -- payload.desired skips a spec key with no value and netbox.Drift considers only
// fields present in desired. See internal/reconciler/dcim_powerfeed_test.go.
func dcimPowerFeedDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerFeed"),
		Endpoint:   "dcim/power-feeds",
		ObjectType: "dcim.powerfeed",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.PowerFeed is a PrimaryModel (docs/netbox-schema.md -> dcim.PowerFeed, bases),
		// so it mixes in both TagsMixin and CustomFieldsMixin. PathEndpoint and
		// CabledObjectModel contribute the cable graph, which this kind does not own.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimPowerFeedFields(),

		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "power_panel_id", Spec: "powerPanelRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef, and no DataLossOnDelete -- see the doc comment, and
		// internal/registry/dcim_powerpanel.go for the deletion reasoning both kinds share.

		ReadOnly: dcimPowerFeedReadOnly(),
	}
}

// dcimPowerFeedFields is this kind's spec-to-column map.
//
// Three entries earn the explicit table on their own. `powerPanelRef` -> `power_panel`,
// `maxUtilization` -> `max_utilization` and `markConnected` -> `mark_connected` are pairs a
// camelCase-to-snake_case convention gets wrong or gets right only by luck, and NetBox ignores
// a field name it does not know rather than rejecting it -- so a wrong spelling would write
// nothing and report success.
//
// `status`, `type`, `supply` and `phase` need no field class: NetBox returns a choice as
// {"value","label"} and takes the bare value, which internal/netbox/drift.go's unwrapNested
// already reduces by the absence of an "id" key. None of the four needs EmptyIsNull either --
// all four columns are NOT NULL with a default (docs/netbox-schema.md -> dcim.PowerFeed, and
// `nullable: false` on each in hack/testdata/ir-4.6.8.json.gz), so there is no empty state to
// spell as null and the CRD enums carry no `""` member.
//
// `voltage`, `amperage` and `max_utilization` are ordinary entries and carry nothing special.
// That is deliberate: their "unset means the server's configured value" behaviour is the
// engine's, not a per-kind rule, and a Descriptor flag for it would be a second place the fact
// could be wrong.
func dcimPowerFeedFields() []Field {
	return []Field{
		{Spec: "name", API: "name"},
		{Spec: "status", API: "status"},
		{Spec: "type", API: "type"},
		{Spec: "supply", API: "supply"},
		{Spec: "phase", API: "phase"},
		{Spec: "voltage", API: "voltage"},
		{Spec: "amperage", API: "amperage"},
		{Spec: "maxUtilization", API: "max_utilization"},
		{Spec: "markConnected", API: "mark_connected"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		// Written as `power_panel`, filtered as `power_panel_id`. Required and PROTECT, so no
		// cascade to declare -- which is what leaves this Kind without a containment parent.
		{
			Spec: "powerPanelRef", API: "power_panel", Class: ClassRefOne,
			Target: netboxv1alpha1.PowerPanelRef{}.TargetGVK(),
		},
		{
			Spec: "rackRef", API: "rack", Class: ClassRefOne,
			Target: netboxv1alpha1.RackRef{}.TargetGVK(),
		},
		{
			Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
			Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
		},
	}
}

// dcimPowerFeedReadOnly is every API field the operator must never write.
//
// The four every ChangeLoggedModel carries, plus:
//
// `available_power` -- the column NBO-052 asks to expose in status. NetBox recomputes it in
// `clean()` from voltage, amperage and phase, and it is **not in the serializer at all** at
// 4.6.8: `PowerFeedSerializer.fields` does not list it
// (hack/testdata/api-schema-4.6.8.json.gz), and neither does dcim.PowerFeed's `write_path`
// (hack/testdata/ir-4.6.8.json.gz). It is listed here rather than omitted because it is a real
// model column and the read-only list is where "a column exists and the operator must not
// touch it" is recorded -- the coverage audit reads this list, and leaving it out would report
// the column as an unexplained gap. There is no status field for it either; see
// api/v1alpha1/dcim_powerfeed.go.
//
// `_path` -- the cable path NetBox recomputes from the cable graph (dcim.PathEndpoint), one of
// the `_`-prefixed caches that are maintained server-side and dropped on write.
//
// `cable`, `cable_end`, `cable_connector`, `cable_positions` and `cable_terminations` --
// writable columns that belong to another Kind. NetBoxCable (NBO-049) owns the cable graph and
// a cable is created from its own endpoints rather than by a feed claiming one, so these are
// read-only here rather than absent: a feed that adopted a cabled row must not PATCH the cable
// away. Exactly the treatment dcim.Interface gives the same columns off the same base class.
//
// `link_peers`, `link_peers_type`, `connected_endpoints`, `connected_endpoints_type`,
// `connected_endpoints_reachable` and `_occupied` -- computed serializer fields over the cable
// path rather than columns of anything (hack/testdata/api-schema-4.6.8.json.gz ->
// serializers.PowerFeedSerializer, bases CabledObjectSerializer and
// ConnectedEndpointsSerializer). They are in the write path and refused there, which is the
// failure this list exists for.
func dcimPowerFeedReadOnly() []string {
	return []string{
		"created", "last_updated", "url", "display",
		"available_power",
		"_path", "_occupied",
		"cable", "cable_end", "cable_connector", "cable_positions", "cable_terminations",
		"link_peers", "link_peers_type",
		"connected_endpoints", "connected_endpoints_type", "connected_endpoints_reachable",
	}
}
