package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestPowerDescriptorsAreRegisteredAndValid is the boot check for the two kinds NBO-052's
// first PR ships.
func TestPowerDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxPowerPanel", "dcim/power-panels", "dcim.powerpanel"},
		{"NetBoxPowerFeed", "dcim/power-feeds", "dcim.powerfeed"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// Looked up in docs/netbox-schema.md's endpoint map rather than derived:
			// `dcim/power-panels` is not the pluralisation of `dcim.PowerPanel`.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// Both are PrimaryModels (docs/netbox-schema.md, bases), so both are stamped in
			// full.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}

			// Power distribution is configuration, not allocated state: deleting a feed frees
			// no allocation and destroys no record of who held one. Neither kind therefore
			// declares DataLossOnDelete, which since #304 is the only per-kind deletion flag
			// left -- every kind defaults to Delete, and this one names the deletes a
			// finalizer refuses outright.
			if d.DataLossOnDelete {
				t.Errorf("DataLossOnDelete = true; deleting a power panel or feed destroys " +
					"no data NetBox would not recreate from the manifest " +
					"(docs/concepts/deletion.md)")
			}
		})
	}
}

// TestPowerNaturalKeysComeFromTheConstraints is the claim a wrong answer to would silently
// adopt somebody else's object, which is the failure behind #206 and #216.
//
// Both keys are checked against two independent committed artefacts rather than against the
// ticket:
//
//   - docs/netbox-schema.md -> dcim.PowerPanel and dcim.PowerFeed, `meta.constraints`, which
//     records `UniqueConstraint(fields=('site', 'name'))` and
//     `UniqueConstraint(fields=('power_panel', 'name'))` respectively;
//   - hack/testdata/ir-4.6.8.json.gz -> `natural_keys`, which resolves each constraint to the
//     *filters* as well as the columns -- `site_id`/`name` and `power_panel_id`/`name` -- with
//     `null_fields: []` and `unusable: null` on both.
//
// That second artefact is what makes the filter names checked rather than guessed. NetBox's
// `BaseFilterSet` drops an unrecognised query parameter and answers with the *unfiltered* set,
// so a wrong filter name is a lookup that matches every panel or feed in the NetBox -- and on
// a kind that adopts what it finds, that is the worst possible failure.
//
// Neither kind gets a second candidate, and neither gets a null pin. That is the contrast with
// dcim.Rack in NBO-051, whose every constraint is keyed on the *optional* `location` so that a
// location-less rack satisfies none of them. Here the constrained columns are `site` and
// `power_panel`, both `REQ`, so every panel and every feed satisfies its constraint and an
// ambiguous match is impossible rather than merely reported: Postgres will not hold two.
func TestPowerNaturalKeysComeFromTheConstraints(t *testing.T) {
	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"a power panel is keyed on (site, name), its one constraint": {
			kind: "NetBoxPowerPanel",
			want: []NaturalKey{{Fields: []KeyField{
				{Filter: "site_id", Spec: "siteRef"},
				{Filter: "name", Spec: "name"},
			}}},
		},
		"a power feed is keyed on (power_panel, name), its one constraint": {
			kind: "NetBoxPowerFeed",
			want: []NaturalKey{{Fields: []KeyField{
				{Filter: "power_panel_id", Spec: "powerPanelRef"},
				{Filter: "name", Spec: "name"},
			}}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !reflect.DeepEqual(d.NaturalKeys, tc.want) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.want)
			}
		})
	}
}

// TestPowerCandidatesByState is the half of the identity that matters operationally: what the
// engine does before the required reference has resolved.
//
// The `want: nil` rows are the point. Both keys read a reference, and neither kind has a
// fallback candidate, so an object whose `siteRef` or `powerPanelRef` has not resolved yet has
// **no** applicable candidate and the engine waits rather than querying. Querying would be the
// bug: `?name=Feed+A1` with the panel dropped matches every feed of that name in the NetBox.
//
// The `location`/`rack`/`tenant` rows assert the other direction -- that an optional reference
// resolving or not cannot change the candidate, because neither is in the key at all.
func TestPowerCandidatesByState(t *testing.T) {
	tests := map[string]struct {
		kind  string
		state SpecState
		want  [][]string
	}{
		"a panel with a resolved site is keyed on the constraint": {
			kind: "NetBoxPowerPanel",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: [][]string{{"site_id", "name"}},
		},
		"a panel's location changes nothing, resolved or not": {
			kind: "NetBoxPowerPanel",
			state: SpecState{
				Declared: []string{"name", "siteRef", "locationRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: [][]string{{"site_id", "name"}},
		},
		"a panel whose site has not been created yet has no candidate": {
			kind: "NetBoxPowerPanel",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
		"a feed with a resolved panel is keyed on the constraint": {
			kind: "NetBoxPowerFeed",
			state: SpecState{
				Declared: []string{"name", "powerPanelRef"},
				Resolved: []string{"name", "powerPanelRef"},
			},
			want: [][]string{{"power_panel_id", "name"}},
		},
		"a feed's rack and tenant change nothing, resolved or not": {
			kind: "NetBoxPowerFeed",
			state: SpecState{
				Declared: []string{"name", "powerPanelRef", "rackRef", "tenantRef"},
				Resolved: []string{"name", "powerPanelRef"},
			},
			want: [][]string{{"power_panel_id", "name"}},
		},
		"a feed whose panel has not been created yet has no candidate": {
			kind: "NetBoxPowerFeed",
			state: SpecState{
				Declared: []string{"name", "powerPanelRef"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			var got [][]string
			for _, key := range d.Candidates(tc.state) {
				got = append(got, params(key))
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Candidates(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestNeitherPowerKindHasAContainmentParent is the cascade reading, and it is the same answer
// dcim.Rack got for the same reason.
//
// Every foreign key on either model is `on_delete=PROTECT` -- `PowerPanel.site`,
// `PowerPanel.location`, `PowerFeed.power_panel`, `PowerFeed.rack` and `PowerFeed.tenant`
// (docs/netbox-schema.md). An owner reference on a PROTECTed foreign key promises a
// cluster-side cascade NetBox refuses to perform, which deletes the CR and leaves the row
// (registry.ErrContainmentNotCascade,
// docs/decisions/0003-ownership-and-references.md rule 4).
//
// `PowerPanel.location` is worth naming on its own: the identically spelled column on
// dcim.Rack is `SET_NULL`. Two kinds in one app pointing at one model with two different
// cascades is why the answer is read per column rather than per target.
func TestNeitherPowerKindHasAContainmentParent(t *testing.T) {
	for _, kind := range []string{"NetBoxPowerPanel", "NetBoxPowerFeed"} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

		if d.ContainmentRef != "" {
			t.Errorf("%s ContainmentRef = %q, want empty: every FK it holds is PROTECT, so "+
				"nothing cascades", kind, d.ContainmentRef)
		}

		for _, field := range d.Fields {
			if field.CascadeOnDelete {
				t.Errorf("%s %q declares CascadeOnDelete; the column is PROTECT",
					kind, field.Spec)
			}
		}
	}
}

// TestPowerFeedServerDefaultsAreOrdinaryFields is the registry half of NBO-052's one novel
// rule, and it asserts an *absence*.
//
// `voltage`, `amperage` and `max_utilization` default to
// `ConfigItem('POWERFEED_DEFAULT_VOLTAGE')` and friends -- read from the target NetBox's own
// configuration rather than from the model (docs/netbox-schema.md -> dcim.PowerFeed;
// `default_unresolved: true` on all three in hack/testdata/ir-4.6.8.json.gz). So the three
// must reach a payload only when the spec sets them.
//
// The mechanism for that is entirely outside the registry: the CRD carries no
// `+kubebuilder:default` and the Go fields are pointers, so an unset one never enters the spec
// map at all. What this test holds is that nothing in the Descriptor undoes it -- the three are
// plain ClassValue entries, with no EmptyIsNull that would turn a zero into a written null and
// no read-only entry that would stop a *set* one from being written.
func TestPowerFeedServerDefaultsAreOrdinaryFields(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerFeed"))

	for spec, api := range map[string]string{
		"voltage": "voltage", "amperage": "amperage", "maxUtilization": "max_utilization",
	} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Errorf("no %q entry in the field map; a ConfigItem default is still a writable "+
				"column when the spec sets one", spec)

			continue
		}

		if field.API != api {
			t.Errorf("%s -> %q, want %q", spec, field.API, api)
		}

		if field.Class != ClassValue {
			t.Errorf("%s Class = %q, want %q", spec, field.Class, ClassValue)
		}

		if field.EmptyIsNull {
			t.Errorf("%s declares EmptyIsNull; the column is NOT NULL with a server-side "+
				"default, so there is no empty state to spell as null", spec)
		}

		if slices.Contains(d.ReadOnly, api) {
			t.Errorf("%q is in ReadOnly; an explicitly set voltage must still be written", api)
		}
	}
}

// TestPowerFeedAvailablePowerIsNeverWritten records what the committed artefacts do and do not
// say about the one field NBO-052 asks for in status.
//
// `available_power` is a real model column that NetBox recomputes in `clean()`. At 4.6.8 it is
// absent from `PowerFeedSerializer.fields` (hack/testdata/api-schema-4.6.8.json.gz) and absent
// from dcim.PowerFeed's `write_path` (hack/testdata/ir-4.6.8.json.gz), so there is no committed
// evidence the REST API returns it at all -- which is why this PR ships no status field for it
// rather than one that would report zero forever.
//
// It is in ReadOnly rather than simply unmapped so that the fact is recorded where the coverage
// audit reads it, and so that a field map ever reaching for it fails Validate at boot
// (ErrFieldReadOnly) instead of PATCHing a column NetBox drops.
//
// If a read against a live NetBox shows the API returning it, this is the test that should
// change.
func TestPowerFeedAvailablePowerIsNeverWritten(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerFeed"))

	if !slices.Contains(d.ReadOnly, "available_power") {
		t.Error("`available_power` is not in ReadOnly; NetBox derives it from voltage, " +
			"amperage and phase and does not accept a write")
	}

	for _, field := range d.Fields {
		if field.API == "available_power" {
			t.Errorf("spec.%s maps onto `available_power`, which is derived", field.Spec)
		}
	}
}

// TestPowerFeedCableColumnsBelongToTheCable is the CabledObjectModel reading, and it is the
// same treatment dcim.Interface gives the same columns off the same base class.
//
// NetBoxCable (NBO-049) owns the cable graph: a cable is created from its own endpoints rather
// than by a feed claiming one. So the cable columns are read-only here rather than absent --
// a feed that adopted an already-cabled row must not PATCH the cable away -- and the path and
// connected-endpoint fields are computed rather than columns at all.
func TestPowerFeedCableColumnsBelongToTheCable(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerFeed"))

	for _, column := range []string{
		"cable", "cable_end", "cable_connector", "cable_positions", "cable_terminations",
		"_path", "_occupied",
		"link_peers", "link_peers_type",
		"connected_endpoints", "connected_endpoints_type", "connected_endpoints_reachable",
	} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly; NetBoxCable owns the cable graph and NetBox "+
				"drops a write to this from the feed's own endpoint", column)
		}
	}

	// `mark_connected` is the one CabledObjectModel column the feed does own: it is how a feed
	// is declared connected *without* a cable, so it belongs to the feed rather than to the
	// graph.
	marked, ok := d.FieldFor("markConnected")
	if !ok {
		t.Fatal("no `markConnected` entry in the field map")
	}

	if marked.API != "mark_connected" {
		t.Errorf("markConnected -> %q, want `mark_connected`", marked.API)
	}
}

// TestPowerFeedClosesTheCableUnionByBeingRegistered is the NBO-049 bullet NBO-052 restates,
// asserted rather than re-implemented.
//
// Every part of it already existed before this PR: `dcim.powerfeed` is in cabledObjectTypes(),
// `powerFeedRef` is a member of both ends of dcim.Cable's termination union, and
// v1alpha1.PowerFeedRef already declared the target Kind. What was missing was the Descriptor
// those declarations point at -- internal/resolver dispatches every mode through
// `Descriptors.Get(Field.Target)`, so a member whose Kind has no Descriptor reports
// RefKindUnavailable and only `id` mode works.
//
// So registering dcim.PowerFeed *is* that bullet, with no edit to dcim_cable.go. This test is
// what says so: it asserts the union member's target resolves to a registered Descriptor whose
// ObjectType is the allowed type the union already lists.
func TestPowerFeedClosesTheCableUnionByBeingRegistered(t *testing.T) {
	feed, ok := Get(netboxv1alpha1.PowerFeedRef{}.TargetGVK())
	if !ok {
		t.Fatal("PowerFeedRef's target Kind has no Descriptor, so a cable terminating on a " +
			"feed by name reports RefKindUnavailable")
	}

	cable, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCable"))

	var found bool

	for _, pair := range cable.GenericFKs {
		if !slices.Contains(pair.AllowedTypes, feed.ObjectType) {
			t.Errorf("%q is not in %q's AllowedTypes; dcim.PowerFeed is a CabledObjectModel",
				feed.ObjectType, pair.Spec)
		}

		for _, member := range pair.Members {
			if member.Target == feed.GVK {
				found = true
			}
		}
	}

	if !found {
		t.Error("no member of dcim.Cable's termination union targets NetBoxPowerFeed")
	}
}
