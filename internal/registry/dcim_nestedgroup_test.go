package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// nestedGroups are the three NestedGroupModel kinds registered so far, asserted together
// rather than one file per kind: they are the same NetBox model with different names, and the
// property worth testing is that each carries the *pair* of natural keys its constraints
// declare. A per-kind file would restate the same assertions three times and still not say
// that.
var nestedGroups = []struct {
	kind       string
	endpoint   string
	objectType string
	keys       []NaturalKey
	// containment is the spec field whose target gets an owner reference, empty for a kind
	// with no containment parent.
	//
	// Not a preference on any of the three: the containment parent is whichever FK the server
	// cascades (docs/decisions/0003-ownership-and-references.md rule 4, #193), and each entry
	// below records the `on_delete` that decided it.
	containment string

	// otherKeyFields are the spec fields this kind's natural keys read besides `parentRef`
	// and `name` -- `siteRef` on a location, nothing on the two group kinds.
	//
	// Deliberately separate from containment, which they coincide with only on
	// NetBoxLocation. Reusing containment here broke when NetBoxRegion gained
	// `ContainmentRef: "parentRef"`: `parentRef` landed in the always-resolved base and the
	// "undeclared parent" case stopped being undeclared.
	otherKeyFields []string
}{
	{
		kind: "NetBoxRegion", endpoint: "dcim/regions", objectType: "dcim.region",
		keys: []NaturalKey{
			{Fields: []KeyField{
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			}},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},
		// dcim.Region.parent is on_delete=CASCADE and is the only FK this kind has, so the
		// cascade rule selects the self-reference with no tiebreak to make --
		// without it a child CR outlives its row and the create-if-absent step recreates a
		// region NetBox deliberately deleted. This expectation was written before owner
		// references existed and said "" for a while; the two landed in the same hour and
		// each passed CI against a main that did not yet contain the other.
		containment: "parentRef",
	},
	{
		kind: "NetBoxSiteGroup", endpoint: "dcim/site-groups", objectType: "dcim.sitegroup",
		keys: []NaturalKey{
			{Fields: []KeyField{
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			}},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},
		// dcim.SiteGroup.parent is on_delete=CASCADE too, and this kind had no containment
		// parent at all until #198 -- so deleting a parent group in NetBox cascaded to the
		// children server-side while the child CRs stayed, and the create-if-absent step
		// re-created rows NetBox deleted on purpose. The only FK this kind has, so again no
		// tiebreak.
		containment: "parentRef",
	},
	{
		// Every candidate starts at `site`, because every constraint NetBox declares on
		// dcim.Location does (docs/netbox-schema.md -> dcim.Location.meta.constraints).
		kind: "NetBoxLocation", endpoint: "dcim/locations", objectType: "dcim.location",
		keys: []NaturalKey{
			{Fields: []KeyField{
				{Filter: "site_id", Spec: "siteRef"},
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			}},
			{
				Fields: []KeyField{
					{Filter: "site_id", Spec: "siteRef"},
					{Filter: "name", Spec: "name"},
				},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},
		// The one kind here with *two* cascading FKs and one slot: `site` and `parent` are both
		// on_delete=CASCADE. `siteRef` wins because it is the REQ one -- so every location has
		// it, where a containment ref on the optional `parent` would leave every top-level
		// location unowned -- and because deleting the site cascades to a superset of the rows
		// deleting one parent location does. The parent path is covered by identity rather than
		// by ownership: every candidate above reads `parent_id` or pins it null, so a child
		// whose parentRef stops resolving has no applicable candidate and the engine waits
		// instead of re-creating. Full argument in internal/registry/dcim_location.go.
		containment:    "siteRef",
		otherKeyFields: []string{"siteRef"},
	},
}

// TestNestedGroupDescriptorsAreRegisteredAndValid is the boot check for the scope union's
// two missing members. Until these Descriptors existed, `siteGroupRef` and `locationRef`
// resolved to RefKindUnavailable in all four modes -- the endpoint every mode needs is held
// nowhere else (NBO-018, NBO-066).
func TestNestedGroupDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range nestedGroups {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the kind's init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

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

			if !reflect.DeepEqual(d.NaturalKeys, tc.keys) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.keys)
			}

			if d.ContainmentRef != tc.containment {
				t.Errorf("ContainmentRef = %q, want %q", d.ContainmentRef, tc.containment)
			}

			// MPTT keeps its own tree caches. Writing one does not fail, it silently
			// no-ops, so an undeclared cache column is a PATCH loop rather than an error.
			for _, cache := range []string{"_depth", "_children"} {
				if !slices.Contains(d.ReadOnly, cache) {
					t.Errorf("ReadOnly = %v, which omits the MPTT cache %s", d.ReadOnly, cache)
				}
			}

			// Both mixins, so the provenance stamp lands in full: every NestedGroupModel
			// mixes in TagsMixin and CustomFieldsMixin (docs/netbox-schema.md, bases).
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable = %v, CustomFieldable = %v, want both true",
					d.Taggable, d.CustomFieldable)
			}
		})
	}
}

// TestNestedGroupKeySelectionFollowsTheParent is the assertion the `parent IS NULL` variant
// exists for, and the one that would catch a fallback chain being introduced by accident.
//
// The three states of `parentRef` select three different outcomes, and the middle one is the
// point: a child whose parent has not been created yet matches *neither* candidate, so the
// engine waits rather than adopting an unrelated top-level object and reparenting it
// (NBO-015).
func TestNestedGroupKeySelectionFollowsTheParent(t *testing.T) {
	for _, tc := range nestedGroups {
		t.Run(tc.kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			// Every field this kind's keys read other than `parentRef`: `name` always,
			// `siteRef` on a location. They resolve in all three cases below, so the only
			// variable is the parent.
			base := slices.Concat([]string{"name"}, tc.otherKeyFields)
			// slices.Concat and not append: two appends to one base would write the same
			// backing array and the second would silently overwrite the first.
			withParent := slices.Concat(base, []string{"parentRef"})

			for _, state := range []struct {
				name   string
				state  SpecState
				want   int
				pinned bool
			}{
				{
					name:  "a declared and resolved parent takes the (parent, name) candidate",
					state: SpecState{Declared: withParent, Resolved: withParent},
					want:  1,
				},
				{
					name:   "an undeclared parent takes the parent IS NULL candidate",
					state:  SpecState{Declared: base, Resolved: base},
					want:   1,
					pinned: true,
				},
				{
					name:  "a declared but unresolved parent takes neither, and the engine waits",
					state: SpecState{Declared: withParent, Resolved: base},
					want:  0,
				},
			} {
				t.Run(state.name, func(t *testing.T) {
					got := d.Candidates(state.state)
					if len(got) != state.want {
						t.Fatalf("Candidates() = %+v (%d), want %d", got, len(got), state.want)
					}
					if state.want == 0 {
						return
					}
					if pinned := len(got[0].NullFields) == 1; pinned != state.pinned {
						t.Errorf("candidate %+v pins a null filter = %v, want %v",
							got[0], pinned, state.pinned)
					}
				})
			}
		})
	}
}

// TestLocationCannotBeLookedUpWithoutItsSite is dcim.Location's own case, and the reason its
// required reference is not merely a validation marker: `site` is in every candidate, so an
// unresolved `siteRef` leaves the engine with no identity at all and therefore no write.
//
// The failure this prevents is not a missing lookup but a wrong one: a query with `site_id`
// omitted matches a location of that name in *any* site, so it would adopt somebody else's
// and PATCH it into this one.
func TestLocationCannotBeLookedUpWithoutItsSite(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxLocation"))

	for _, key := range d.NaturalKeys {
		if !slices.Contains(specFiltersOf(key), "site_id") {
			t.Errorf("candidate %+v does not filter on site_id", key)
		}
	}

	// Name resolved, site not: nothing is applicable.
	state := SpecState{Declared: []string{"name", "siteRef"}, Resolved: []string{"name"}}
	if got := d.Candidates(state); len(got) != 0 {
		t.Errorf("Candidates() = %+v with an unresolved siteRef, want none", got)
	}
}

// specFiltersOf are the query parameters a candidate matches on, null pins excluded.
func specFiltersOf(key NaturalKey) []string {
	filters := make([]string, 0, len(key.Fields))
	for _, field := range key.Fields {
		filters = append(filters, field.Filter)
	}

	return filters
}
