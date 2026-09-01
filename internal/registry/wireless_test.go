package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// wirelessKinds are the three kinds NBO-050 adds, asserted together for the boilerplate every
// descriptor owes -- registration, Validate, endpoint, object type, scope, update strategy and
// the two provenance mixins. Everything that is interesting about one of them and not the other
// two gets its own test below.
var wirelessKinds = []struct {
	kind       string
	endpoint   string
	objectType string
}{
	{"NetBoxWirelessLANGroup", "wireless/wireless-lan-groups", "wireless.wirelesslangroup"},
	{"NetBoxWirelessLAN", "wireless/wireless-lans", "wireless.wirelesslan"},
	{"NetBoxWirelessLink", "wireless/wireless-links", "wireless.wirelesslink"},
}

func wirelessDescriptor(t *testing.T, kind string) Descriptor {
	t.Helper()

	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the kind's init() did not run", kind)
	}

	return d
}

func TestWirelessDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range wirelessKinds {
		t.Run(tc.kind, func(t *testing.T) {
			d := wirelessDescriptor(t, tc.kind)

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// The endpoint is looked up rather than pluralised from the model name, and the
			// wireless app is why: `wireless-lans` is not what a pluraliser makes of
			// `WirelessLAN`.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q", d.Endpoint, tc.endpoint)
			}

			// Lowercased and unpunctuated, which is Django's `model` attribute -- never
			// `wireless.WirelessLAN` (docs/concepts/generic-refs.md).
			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			// Both endpoints of a wireless link are plain foreign keys, unlike dcim.Cable's
			// terminations, so changing one is an ordinary PATCH for all three kinds.
			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}
			if len(d.RecreateOn) != 0 {
				t.Errorf("RecreateOn = %v, want none", d.RecreateOn)
			}

			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable = %v, CustomFieldable = %v, want both true", d.Taggable, d.CustomFieldable)
			}
		})
	}
}

// TestNoWirelessKindMapsAPreSharedKey is the security invariant of NBO-050, asserted as an
// absence because that is the only way a spec can hold it.
//
// A pre-shared key may never be inline in a spec (plan.md §15), and `authPSKSecretRef` needs
// the engine to source one payload value from a Secret -- a new FieldClass plus a Secret read
// in the payload path, which is shared machinery rather than descriptor data. Until that lands
// the column is mapped by nothing, and a column no spec field maps onto cannot reach a payload
// at all. This test is what stops somebody closing the gap the easy way.
//
// Written over the whole registry rather than the three kinds above, because
// `WirelessAuthenticationBase` is an abstract model and the next kind to mix it in must fail
// here too.
func TestNoWirelessKindMapsAPreSharedKey(t *testing.T) {
	for _, d := range List() {
		for _, field := range d.Fields {
			switch field.API {
			case "auth_psk", "psk", "preshared_key":
				t.Errorf("descriptor %s maps spec.%s onto %q; a pre-shared key may not be inline in a spec",
					d.GVK.Kind, field.Spec, field.API)
			}
		}
	}
}

// TestWirelessLANGroupIdentityIsTheSlugAlone is the assertion the ticket's warning is about,
// and it is deliberately the *opposite* of the nested-group assertion in
// dcim_nestedgroup_test.go.
//
// plan.md §8.1 asserts every MPTT kind needs a `(parent, name)` candidate plus a
// `parent IS NULL` variant. dcim.Region, dcim.SiteGroup and dcim.Location do, because each
// declares constraints with `condition=Q(parent__isnull=True)`
// (netbox/dcim/models/sites.py:62-82). wireless.WirelessLANGroup does not:
//
//   - `name = CharField(max_length=100, unique=True)` (netbox/wireless/models.py:53-58)
//   - `slug = SlugField(max_length=100, unique=True)` (netbox/wireless/models.py:59-63)
//   - `constraints = (UniqueConstraint(fields=('parent', 'name'), name='..._unique_parent_name'),)`
//     (netbox/wireless/models.py:70-75) -- **and no `condition=` clause**
//
// A global unique on `name` already subsumes `(parent, name)`, so `parent` is in no variant of
// the identity and `slug` finds the group whatever its parent is. Adding the pin would make a
// *nested* group's slug unfindable, which is the failure this test pins down: the third case
// below is a child whose parent resolves, and it must still have a usable candidate.
func TestWirelessLANGroupIdentityIsTheSlugAlone(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLANGroup")

	want := []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v -- one candidate, no parent term, no null pin",
			d.NaturalKeys, want)
	}

	// All three states of `parentRef` keep the same single candidate. That is the whole
	// difference from a NetBoxRegion, where the three states select two candidates and none.
	for _, tc := range []struct {
		name  string
		state SpecState
	}{
		{"a top-level group", SpecState{Declared: []string{"slug"}, Resolved: []string{"slug"}}},
		{
			"a nested group whose parent resolves",
			SpecState{Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug", "parentRef"}},
		},
		{
			// The interesting one. On a NetBoxRegion this state has no candidate and the
			// engine waits, correctly -- there, `parent` is part of the identity. Here it is
			// not, so the group is identifiable and the engine creates it top-level and
			// PATCHes the parent on when the reference resolves.
			"a nested group whose parent does not exist yet",
			SpecState{Declared: []string{"slug", "parentRef"}, Resolved: []string{"slug"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != 1 {
				t.Fatalf("Candidates() = %+v (%d), want exactly 1 -- the slug is findable in every state",
					got, len(got))
			}
			if len(got[0].NullFields) != 0 {
				t.Errorf("candidate %+v pins %+v; wireless.WirelessLANGroup declares no conditional constraint",
					got[0], got[0].NullFields)
			}
		})
	}

	// Deferrable precisely because `parent` is outside the natural key, and the containment
	// parent because `parent TreeForeignKey -> self on_delete=CASCADE`
	// (netbox/netbox/models/__init__.py:171-178). The pair matters together: a single
	// slug-only candidate stays applicable when the parent is cascade-deleted, so without the
	// owner reference create-if-absent would resurrect the row -- exactly #203 on
	// NetBoxTenantGroup, which has this same identity shape.
	if want := []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}}; !reflect.DeepEqual(d.Deferred, want) {
		t.Errorf("Deferred = %+v, want %+v", d.Deferred, want)
	}

	if d.ContainmentRef != "parentRef" {
		t.Errorf("ContainmentRef = %q, want parentRef", d.ContainmentRef)
	}
}

// TestWirelessLANScopeCascadesFromEveryMember is the #214/#217 half: the cascade is per union
// member, and this kind is one of the two that needs *both* of NetBox's mechanisms read.
//
//   - dcim.Region and dcim.SiteGroup carry a `wireless_lans` GenericRelation on
//     `scope_type`/`scope_id` (netbox/dcim/models/sites.py:51-56, :122-127).
//   - dcim.Site and dcim.Location carry none -- and need none, because CachedScopeMixin
//     declares `_site` and `_location` `on_delete=CASCADE`
//     (netbox/dcim/models/mixins.py:63-74). The cached column *is* the cascade there.
//
// The other two caches are SET_NULL on the same mixin (netbox/dcim/models/mixins.py:75-89)
// because they cache an *ancestor* of the actual scope. Reading only that half and concluding
// "region and site group do not cascade" is how virtualization.Cluster came to have no
// containment parent at all.
func TestWirelessLANScopeCascadesFromEveryMember(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLAN")

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly one pair", d.GenericFKs)
	}

	pair := d.GenericFKs[0]
	if !reflect.DeepEqual(pair, ScopeFK("scope", ScopeCascadesFromEvery())) {
		t.Errorf("the scope pair is not registry.ScopeFK's: %+v", pair)
	}

	for _, member := range pair.Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("member %s CascadeOnDelete = %v, want true", member.Spec, member.CascadeOnDelete)
		}
	}

	// One cascading reference and one slot, so no tiebreak: `groupRef` is SET_NULL and
	// `vlanRef` and `tenantRef` are PROTECT (netbox/wireless/models.py:88-114), so none of
	// them is eligible and validateContainment would refuse any of them.
	if d.ContainmentRef != "scope" {
		t.Errorf("ContainmentRef = %q, want scope", d.ContainmentRef)
	}

	for _, field := range d.Fields {
		if field.CascadeOnDelete {
			t.Errorf("field %s declares CascadeOnDelete; every ordinary FK on this kind is SET_NULL or PROTECT", field.Spec)
		}
	}

	// Every cached column must also be read-only, which Validate enforces -- but the reason is
	// worth an assertion of its own: writing `_site` is dropped exactly like `site`, so the
	// next read finds it unchanged and the operator PATCHes it again on every resync, forever.
	for _, cache := range ScopeCacheColumns() {
		if !slices.Contains(d.ReadOnly, cache) {
			t.Errorf("ReadOnly = %v, which omits the scope cache %s", d.ReadOnly, cache)
		}
	}

	// And no `site` in the field map at all. NetBox drops a column it does not know rather
	// than rejecting it, so a `site` entry here would return 201, create the SSID unscoped,
	// and compare clean forever -- the netbox-populator bug.
	for _, field := range d.Fields {
		if field.API == "site" || field.API == "site_id" {
			t.Errorf("descriptor maps spec.%s onto %q; wireless.WirelessLAN has no site column since NetBox 4.2",
				field.Spec, field.API)
		}
	}
}

// TestWirelessLANKeySelectionCoversScopeAndTenantIndependently is the four-candidate matrix,
// and why it is four rather than ipam.VLAN's three: `scope` and `tenant` are **independent**
// optional terms, where ipam.VLAN's `group` and `site` are alternatives.
//
// wireless.WirelessLAN declares no meta.constraints at all -- only indexes on `(ssid, id)` and
// `(scope_type, scope_id)` (netbox/wireless/models.py:118-125) -- so `(ssid, scope, tenant)` is
// a lookup convention. Every term still has to be either matched or pinned, never merely
// omitted: `?ssid=Donkersloot` with `tenant_id` left out matches that SSID in every tenant.
func TestWirelessLANKeySelectionCoversScopeAndTenantIndependently(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLAN")

	if len(d.NaturalKeys) != 4 {
		t.Fatalf("NaturalKeys has %d candidates, want 4 (scope x tenant): %+v", len(d.NaturalKeys), d.NaturalKeys)
	}

	// `ssid` is in every candidate: it is the one thing an SSID is always identified by, and
	// it is in WirelessLANFilterSet's Meta.fields (netbox/wireless/filtersets.py:86-88).
	for _, key := range d.NaturalKeys {
		if !slices.Contains(specFiltersOf(key), "ssid") {
			t.Errorf("candidate %+v does not filter on ssid", key)
		}
	}

	// What applyGenericFK puts in the state when the union resolves: the union's own spec
	// field plus the two column names (internal/reconciler/refs.go).
	scoped := []string{"scope", ScopeTypeField, ScopeIDField}

	for _, tc := range []struct {
		name      string
		state     SpecState
		want      int
		wantPins  []string
		wantMatch []string
	}{
		{
			name: "both terms resolved take the narrowest candidate",
			state: SpecState{
				Declared: []string{"ssid", "scope", "tenantRef"},
				Resolved: slices.Concat([]string{"ssid", "tenantRef"}, scoped),
			},
			want:      1,
			wantMatch: []string{ScopeTypeField, ScopeIDField, "tenant_id", "ssid"},
		},
		{
			name: "a scoped SSID with no tenant pins tenant_id",
			state: SpecState{
				Declared: []string{"ssid", "scope"},
				Resolved: slices.Concat([]string{"ssid"}, scoped),
			},
			want:      1,
			wantPins:  []string{"tenant_id"},
			wantMatch: []string{ScopeTypeField, ScopeIDField, "ssid"},
		},
		{
			name: "an unscoped SSID with a tenant pins scope_id",
			state: SpecState{
				Declared: []string{"ssid", "tenantRef"},
				Resolved: []string{"ssid", "tenantRef"},
			},
			want:      1,
			wantPins:  []string{ScopeIDField},
			wantMatch: []string{"tenant_id", "ssid"},
		},
		{
			name:      "neither term declared pins both",
			state:     SpecState{Declared: []string{"ssid"}, Resolved: []string{"ssid"}},
			want:      1,
			wantPins:  []string{ScopeIDField, "tenant_id"},
			wantMatch: []string{"ssid"},
		},
		{
			// A declared scope that has not resolved matches no candidate and pins none: the
			// value candidates need `scope_type` resolved and the pinned ones need `scope`
			// undeclared. With nothing applicable the engine waits, rather than adopting the
			// SSID of the same name in some other scope and PATCHing this scope onto it.
			name: "a declared but unresolved scope takes no candidate, and the engine waits",
			state: SpecState{
				Declared: []string{"ssid", "scope"},
				Resolved: []string{"ssid", "scope"},
			},
			want: 0,
		},
		{
			name: "a declared but unresolved tenant takes no candidate either",
			state: SpecState{
				Declared: []string{"ssid", "scope", "tenantRef"},
				Resolved: slices.Concat([]string{"ssid"}, scoped),
			},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != tc.want {
				t.Fatalf("Candidates() = %+v (%d), want %d", got, len(got), tc.want)
			}
			if tc.want == 0 {
				return
			}

			if !reflect.DeepEqual(specFiltersOf(got[0]), tc.wantMatch) {
				t.Errorf("candidate matches on %v, want %v", specFiltersOf(got[0]), tc.wantMatch)
			}

			pins := make([]string, 0, len(got[0].NullFields))
			for _, pin := range got[0].NullFields {
				pins = append(pins, pin.Filter)
			}
			if len(pins) == 0 {
				pins = nil
			}
			if !reflect.DeepEqual(pins, tc.wantPins) {
				t.Errorf("candidate pins %v, want %v", pins, tc.wantPins)
			}
		})
	}
}

// TestWirelessLANNullPinsFollowTheColumnClass is #206 on a kind that needs both spellings at
// once, which no kind so far has.
//
// `scope_id` is a PositiveBigIntegerField, so the pin is the `__empty=true` suffix -- the
// sentinel `null` fails IntegerField validation and the request is rejected outright.
// `tenant_id` is a foreign key, so it is the sentinel `?tenant_id=null` -- NetBox registers
// only negation on a foreign-key filter, so there is no suffix to use. Getting either backwards
// widens the lookup silently, and `scope_type` may be pinned by neither.
func TestWirelessLANNullPinsFollowTheColumnClass(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLAN")

	pins := make(map[string]NullColumn)
	for _, key := range d.NaturalKeys {
		for _, pin := range key.NullFields {
			pins[pin.Filter] = pin.Column
		}
	}

	for filter, want := range map[string]NullColumn{
		ScopeIDField: NullColumnNumeric,
		"tenant_id":  NullColumnRef,
	} {
		if got := pins[filter]; got != want {
			t.Errorf("%s pin = %q, want %q", filter, got, want)
		}
	}

	if column, pinned := pins[ScopeTypeField]; pinned {
		t.Errorf("%s is pinned as %q; a content-type filter registers no null spelling", ScopeTypeField, column)
	}
}

// TestWirelessLinkIdentityIsTheOrderedPairAndItsReverse is the symmetry question, answered.
//
// NetBox's one constraint is
// `UniqueConstraint(fields=('interface_a', 'interface_b'), name='..._unique_interfaces')`
// (netbox/wireless/models.py:190-195), with no expression and no second conditional form, so
// Postgres stores `(a,b)` and `(b,a)` as two distinct rows. `WirelessLink.clean` does not close
// the gap either -- it checks only that both interfaces are of a wireless type
// (netbox/wireless/models.py:205-220). A link from A to B and a link from B to A are two
// objects to NetBox and one physical link to everybody else.
//
// The second candidate is the same two filters with their spec fields crossed, which is what
// makes the reverse row *findable*. What happens next is the engine's ordinary adoption rule:
// an object this CR did not create is refused under the default `onConflict: Fail` and reported
// as Conflict. That is the acceptance criterion, with no canonicalisation step and no engine
// code.
func TestWirelessLinkIdentityIsTheOrderedPairAndItsReverse(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLink")

	want := []NaturalKey{
		{Fields: []KeyField{
			{Filter: "interface_a_id", Spec: "interfaceARef"},
			{Filter: "interface_b_id", Spec: "interfaceBRef"},
		}},
		{Fields: []KeyField{
			{Filter: "interface_a_id", Spec: "interfaceBRef"},
			{Filter: "interface_b_id", Spec: "interfaceARef"},
		}},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// The second candidate is exactly the first with its spec fields swapped -- asserted as a
	// relation rather than as two literals, so a future edit to one has to be mirrored.
	forward, reverse := d.NaturalKeys[0].Fields, d.NaturalKeys[1].Fields
	for i := range forward {
		if forward[i].Filter != reverse[i].Filter {
			t.Errorf("candidate filters differ at %d: %q vs %q", i, forward[i].Filter, reverse[i].Filter)
		}
		if forward[i].Spec == reverse[i].Spec {
			t.Errorf("candidate %d reads the same spec field %q in both orientations", i, forward[i].Spec)
		}
	}

	// Both fields are required, so there is no state in which one is missing and a narrower
	// identity applies: no null pins, and no fallback candidate.
	for _, key := range d.NaturalKeys {
		if len(key.NullFields) != 0 {
			t.Errorf("candidate %+v pins %+v; both interfaces are required fields", key, key.NullFields)
		}
	}

	// Both candidates are applicable together once both references resolve, which is what
	// makes the reverse row findable at all: the engine tries them in order, so the declared
	// orientation is adopted when it exists and the reverse is only consulted when it does not.
	both := SpecState{
		Declared: []string{"interfaceARef", "interfaceBRef"},
		Resolved: []string{"interfaceARef", "interfaceBRef"},
	}
	if got := d.Candidates(both); len(got) != 2 {
		t.Errorf("Candidates() = %+v (%d), want both orientations", got, len(got))
	}

	// One unresolved endpoint leaves nothing applicable. Every candidate matches on both
	// references, so the engine has no identity and waits -- which is also why this kind needs
	// no containment parent to avoid resurrection.
	half := SpecState{
		Declared: []string{"interfaceARef", "interfaceBRef"},
		Resolved: []string{"interfaceARef"},
	}
	if got := d.Candidates(half); len(got) != 0 {
		t.Errorf("Candidates() = %+v with one endpoint unresolved, want none", got)
	}
}

// TestWirelessLinkHasNoContainmentParentAndCannot is a consequence rather than a gap, and it is
// worth pinning down because the model *does* contain a CASCADE.
//
// All three writable foreign keys are `on_delete=PROTECT`
// (netbox/wireless/models.py:138-167), and validateContainment refuses a containment ref whose
// Field.CascadeOnDelete is false -- so naming one would fail the boot. The cascade that exists
// is on `_interface_a_device` and `_interface_b_device`
// (netbox/wireless/models.py:171-184), which is precisely how deleting a Device collects the
// link instead of hitting the PROTECT on its interfaces. But those are caches NetBox recomputes
// in save() (netbox/wireless/models.py:222-227), they are read-only, and ContainmentRef names a
// *spec field* -- the owner reference is built from a resolved reference's target CR, and there
// is no CR behind a cache column.
//
// Nothing resurrects as a result: see the end of the test above for why.
func TestWirelessLinkHasNoContainmentParentAndCannot(t *testing.T) {
	d := wirelessDescriptor(t, "NetBoxWirelessLink")

	if d.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want empty: every writable FK on this kind is PROTECT", d.ContainmentRef)
	}

	for _, field := range d.Fields {
		if field.CascadeOnDelete {
			t.Errorf("field %s declares CascadeOnDelete; wireless.WirelessLink's FKs are all PROTECT", field.Spec)
		}
	}

	// The three columns NetBox derives and the operator must never write. `_abs_distance` comes
	// from `distance` and `distance_unit` on every save
	// (netbox/netbox/models/mixins.py:108-117); the two device caches come from the interfaces.
	// Writing any of them silently no-ops, so an undeclared one is a PATCH loop rather than an
	// error.
	for _, cache := range []string{"_abs_distance", "_interface_a_device", "_interface_b_device"} {
		if !slices.Contains(d.ReadOnly, cache) {
			t.Errorf("ReadOnly = %v, which omits the derived column %s", d.ReadOnly, cache)
		}
	}

	// Neither endpoint may be deferred, and validateDeferred is what enforces it: a deferred
	// field is by construction unresolved when the lookup runs, and this kind's whole identity
	// is those two references.
	if len(d.Deferred) != 0 {
		t.Errorf("Deferred = %+v, want none: both natural-key candidates match on both endpoints", d.Deferred)
	}

	// `distance` is a nullable non-text column, so it is cleared with null rather than with the
	// empty string: DRF parses `""` as a number and rejects it, which would make `distance: ""`
	// admission-legal and fail on every write (#170). `distance_unit` is a char column and
	// takes the empty string, so it must *not* carry the flag.
	for _, field := range d.Fields {
		switch field.Spec {
		case "distance":
			if !field.EmptyIsNull {
				t.Error("distance does not declare EmptyIsNull; a DecimalField rejects the empty string")
			}
		case "distanceUnit":
			if field.EmptyIsNull {
				t.Error("distanceUnit declares EmptyIsNull; a char column is cleared with the empty string")
			}
		}
	}
}
