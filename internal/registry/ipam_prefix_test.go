package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

func prefixDescriptor(t *testing.T) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefix")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in ipam_prefix.go did not run", gvk)
	}

	return d
}

// TestPrefixDescriptorIsRegisteredAndValid is the boot check. Validate is what enforces the
// two rules this kind is most able to get wrong -- every Cached column also in ReadOnly, and
// the union's members agreeing with AllowedTypes -- so a green Validate here is the whole
// scope mechanism being wired correctly rather than a formality.
func TestPrefixDescriptorIsRegisteredAndValid(t *testing.T) {
	d := prefixDescriptor(t)

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "ipam/prefixes" {
		t.Errorf("Endpoint = %q, want ipam/prefixes (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "ipam.prefix" {
		t.Errorf("ObjectType = %q, want ipam.prefix", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	// Patch, not Recreate. Addresses, child prefixes and journal entries hang off a prefix,
	// and NetBox's PROTECT would refuse the delete half anyway.
	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// docs/decisions/0003-ownership-and-references.md rule 4 names scopeRef for this kind by
	// name, and exactly one containment ref: two would make garbage collection wait for both
	// parents.
	if d.ContainmentRef != "scope" {
		t.Errorf("ContainmentRef = %q, want scope", d.ContainmentRef)
	}
}

// TestPrefixCannotExpressSite is the acceptance criterion this kind exists for, asserted
// against the descriptor rather than by reading it.
//
// Two separate claims. `site` and `site_id` must not be writable, because since NetBox 4.2
// there is no such column and a write to one returns 201 and sets nothing. The four scope
// caches and the two hierarchy counters must not be writable *and* must be declared
// read-only, because NetBox returns them on every read: an undeclared one is a difference the
// engine finds again on every resync and PATCHes forever.
func TestPrefixCannotExpressSite(t *testing.T) {
	d := prefixDescriptor(t)

	writable := map[string]bool{}
	for _, f := range d.Fields {
		writable[f.API] = true
	}
	for _, generic := range d.GenericFKs {
		writable[generic.TypeField], writable[generic.IDField] = true, true
	}

	for _, column := range []string{"site", "site_id"} {
		if writable[column] {
			t.Errorf("%q is writable on ipam.Prefix; NetBox 4.2 removed the column and ignores the write "+
				"(docs/concepts/generic-refs.md#the-failure-this-prevents)", column)
		}
	}

	for _, column := range []string{"_site", "_region", "_site_group", "_location", "_depth", "_children"} {
		if writable[column] {
			t.Errorf("%q is writable, but NetBox maintains it and drops the write", column)
		}
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly, so a value NetBox returns would be diffed and PATCHed forever", column)
		}
	}
}

// TestPrefixHasNoHierarchyReference is the other half of "there is no parent". ipam.Prefix
// carries no `parent` foreign key at all (docs/netbox-schema.md -> ipam.Prefix): the tree is
// computed from the prefix value with a Postgres inet GiST index and cached in the read-only
// `_depth` / `_children` pair. A parentRef would therefore be a field with nothing to write
// to -- accepted, silently dropped, and reported as success.
func TestPrefixHasNoHierarchyReference(t *testing.T) {
	d := prefixDescriptor(t)

	for _, f := range d.Fields {
		if f.Spec == "parentRef" || f.API == "parent" {
			t.Errorf("field %q -> %q exists, but ipam.Prefix has no parent column", f.Spec, f.API)
		}
	}
}

// TestPrefixScopeIsTheSharedUnion asserts the kind took the one line rather than restating
// the union. Restating it is how a `dcim.SiteGroup` spelling that NetBox rejects, or a
// forgotten cache column, gets in.
func TestPrefixScopeIsTheSharedUnion(t *testing.T) {
	d := prefixDescriptor(t)

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly the scope pair", d.GenericFKs)
	}

	if want := ScopeFK("scope"); !reflect.DeepEqual(d.GenericFKs[0], want) {
		t.Errorf("the scope pair is not registry.ScopeFK(\"scope\"):\n got %+v\nwant %+v", d.GenericFKs[0], want)
	}
}

// TestPrefixNaturalKeysPinTheVRF is the identity claim.
//
// ipam.Prefix has **no meta.constraints at all** -- its only table-level lines are
// `meta.ordering: (F('vrf').asc(nulls_first=True), 'prefix', 'pk')` and two non-unique
// indexes (docs/netbox-schema.md -> ipam.Prefix). So `(vrf, prefix)` is the ordering tuple
// read as a convention, and the `vrf_id` pin is what keeps the convention safe: the same CIDR
// legitimately exists once globally and once per VRF, so an omitted filter would match all of
// them at once.
func TestPrefixNaturalKeysPinTheVRF(t *testing.T) {
	d := prefixDescriptor(t)

	want := []NaturalKey{
		{Fields: []KeyField{
			{Filter: "prefix", Spec: "prefix"},
			{Filter: "vrf_id", Spec: "vrfRef"},
		}},
		{
			Fields:     []KeyField{{Filter: "prefix", Spec: "prefix"}},
			NullFields: []NullField{{Filter: "vrf_id", Spec: "vrfRef"}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// The pin is sent as an explicit `vrf_id__isnull`, never as an omitted filter
	// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
	if got := d.NaturalKeys[1].NullFields[0].Param(); got != "vrf_id__isnull" {
		t.Errorf("the null pin renders as %q, want vrf_id__isnull", got)
	}
}

// TestPrefixCandidatesKeepTheVRFCasesApart is the behaviour behind the two candidates. A
// prefix in a VRF and the same CIDR in the global table are two different objects, so falling
// through from the first candidate to the second is not a retry -- it is adopting somebody
// else's row and then PATCHing a VRF onto it.
func TestPrefixCandidatesKeepTheVRFCasesApart(t *testing.T) {
	d := prefixDescriptor(t)

	for _, tc := range []struct {
		name  string
		state SpecState
		want  int
	}{
		{
			name:  "vrfRef resolves: the in-VRF identity, and only that one",
			state: SpecState{Declared: []string{"prefix", "vrfRef"}, Resolved: []string{"prefix", "vrfRef"}},
			want:  1,
		},
		{
			name:  "vrfRef never declared: the global identity, and only that one",
			state: SpecState{Declared: []string{"prefix"}, Resolved: []string{"prefix"}},
			want:  1,
		},
		{
			// The case that has to produce nothing. The VRF is wanted but does not exist
			// yet, so the engine must wait rather than adopt the global prefix of the same
			// CIDR (NBO-015).
			name:  "vrfRef declared but unresolved: neither, so the engine waits",
			state: SpecState{Declared: []string{"prefix", "vrfRef"}, Resolved: []string{"prefix"}},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Candidates(tc.state); len(got) != tc.want {
				t.Errorf("Candidates() returned %d candidates (%+v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestPrefixFieldMapCoversEverySpecField guards the defect the explicit table exists to
// prevent: NetBox ignores a field name it does not recognise, so `markUtilized` sent verbatim
// writes nothing and reports success. A foreign key is written without `_id`.
func TestPrefixFieldMapCoversEverySpecField(t *testing.T) {
	d := prefixDescriptor(t)

	want := map[string]string{
		"prefix":       "prefix",
		"status":       "status",
		"isPool":       "is_pool",
		"markUtilized": "mark_utilized",
		"vrfRef":       "vrf",
		"vlanRef":      "vlan",
		"roleRef":      "role",
		"description":  "description",
		"comments":     "comments",
	}

	got := map[string]string{}
	for _, f := range d.Fields {
		got[f.Spec] = f.API
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("field map = %v, want %v", got, want)
	}

	// The three FKs are references so the resolver turns them into ids; nothing else is.
	for _, f := range d.Fields {
		wantRef := f.Spec == "vrfRef" || f.Spec == "vlanRef" || f.Spec == "roleRef"
		if f.Class.Ref() != wantRef {
			t.Errorf("field %q has class %q, ref=%v, want ref=%v", f.Spec, f.Class, f.Class.Ref(), wantRef)
		}
	}
}

// TestPrefixNeedsNoFieldClassesBeyondItsFKs is the "no per-kind engine code" claim. A choice
// column, two nullable booleans and a polymorphic pair are all shapes the existing engine
// normalises, so nothing here is an M2M, an array or a content-type list.
func TestPrefixNeedsNoFieldClassesBeyondItsFKs(t *testing.T) {
	d := prefixDescriptor(t)

	if got := d.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none", got)
	}
	if got := d.ArrayFields(); len(got) != 0 {
		t.Errorf("ArrayFields() = %v, want none", got)
	}
	if got := d.ObjectTypeListFields(); len(got) != 0 {
		t.Errorf("ObjectTypeListFields() = %v, want none", got)
	}
}

// prefixDriftRules are the comparison rules the engine derives from this kind's descriptor.
// Only the scope pair, because the read-only columns need no rule at all: Drift considers
// only fields present in the desired payload, and Descriptor.Validate refuses to let a spec
// field map onto a read-only one, so a cache column cannot reach a diff in the first place.
func prefixDriftRules(t *testing.T) netbox.FieldRules {
	t.Helper()

	pair := prefixDescriptor(t).GenericFKs[0]

	return netbox.FieldRules{GenericFKs: []netbox.GenericFK{
		{TypeField: pair.TypeField, IDField: pair.IDField},
	}}
}

// TestPrefixDriftsCleanlyAgainstNetBoxsReadShape is the anti-hot-loop assertion, and for this
// kind it is the whole point. NetBox returns `status` as {"value","label"}, the FKs as nested
// objects, the scope caches as nested objects the operator never wrote, and `_depth` /
// `_children` as numbers -- and a second reconcile must still find nothing to do.
func TestPrefixDriftsCleanlyAgainstNetBoxsReadShape(t *testing.T) {
	sent := netbox.Object{
		"prefix": "10.0.20.0/24", "status": "active",
		"is_pool": false, "mark_utilized": false,
		"description": "", "comments": "",
		"vrf": float64(7), "vlan": nil, "role": nil,
		"scope_type": "dcim.site", "scope_id": float64(41),
	}
	live := netbox.Object{
		"prefix": "10.0.20.0/24",
		"status": map[string]any{"value": "active", "label": "Active"},
		"vrf":    map[string]any{"id": float64(7), "name": "vrf-home"},
		"vlan":   nil, "role": nil, "tenant": nil,
		"is_pool": false, "mark_utilized": false,
		"description": "", "comments": "",
		"scope_type": "dcim.site",
		"scope_id":   float64(41),
		"scope":      map[string]any{"id": float64(41), "name": "Home"},
		// The read-only cache and counter columns NetBox returns on every read. Each one is
		// a PATCH loop if the descriptor forgot to declare it.
		"_site":        map[string]any{"id": float64(41), "name": "Home"},
		"_region":      nil,
		"_site_group":  nil,
		"_location":    nil,
		"_depth":       float64(1),
		"_children":    float64(3),
		"created":      "2026-08-21T10:00:00Z",
		"last_updated": "2026-08-21T10:00:00Z",
	}

	if drift := netbox.Drift(live, sent, prefixDriftRules(t)); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}

// TestPrefixUnscopedDriftsCleanlyAgainstNull is the empty-union-versus-null case, which is
// exactly where an "always send the scope" implementation starts a PATCH loop. A global prefix
// declares no scope, NetBox returns both columns as null, and there is nothing to do.
func TestPrefixUnscopedDriftsCleanlyAgainstNull(t *testing.T) {
	sent := netbox.Object{"prefix": "fd00:10::/64", "status": "active"}
	live := netbox.Object{
		"prefix":     "fd00:10::/64",
		"status":     map[string]any{"value": "active", "label": "Active"},
		"scope_type": nil, "scope_id": nil, "scope": nil,
		"_site": nil, "_region": nil, "_site_group": nil, "_location": nil,
		"_depth": float64(0), "_children": float64(0),
	}

	if drift := netbox.Drift(live, sent, prefixDriftRules(t)); len(drift) != 0 {
		t.Errorf("an unscoped prefix would PATCH %v against a null scope", drift)
	}
}
