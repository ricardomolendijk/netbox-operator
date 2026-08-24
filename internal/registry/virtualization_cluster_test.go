package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

func virtualizationDescriptor(t *testing.T, kind string) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind(kind)

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in internal/registry did not run", gvk)
	}

	return d
}

// TestVirtualizationDescriptorsAreRegisteredAndValid is the boot check for all three kinds.
//
// Validate is what enforces the two rules the Cluster is most able to get wrong -- every
// Cached column also in ReadOnly, and the scope union's members agreeing with AllowedTypes --
// so a green Validate here is the scope mechanism being wired correctly rather than a
// formality.
func TestVirtualizationDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxClusterType", "virtualization/cluster-types", "virtualization.clustertype"},
		{"NetBoxClusterGroup", "virtualization/cluster-groups", "virtualization.clustergroup"},
		{"NetBoxCluster", "virtualization/clusters", "virtualization.cluster"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d := virtualizationDescriptor(t, tc.kind)

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// Looked up, never pluralised: `virtualization/cluster-types` is hyphenated and
			// `virtualization/interfaces` is not `virtualization/vm-interfaces`
			// (docs/netbox-schema.md, endpoint map).
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			// Immutable once shipped, which is why the ticket settles it rather than the
			// implementation: v1alpha1 has no cluster-scoped object kinds
			// (docs/decisions/0002-crd-scoping.md).
			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			// Patch, not Recreate. Devices and virtual machines hang off a cluster and
			// NetBox's PROTECT would refuse the delete half of a recreate anyway.
			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// Both mixins, on all three: the two catalogue kinds are OrganizationalModels
			// and the cluster is a PrimaryModel, and all three mix in TagsMixin and
			// CustomFieldsMixin, so all three carry the provenance stamp.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable = %v, CustomFieldable = %v, want both true",
					d.Taggable, d.CustomFieldable)
			}
		})
	}
}

// TestClusterCannotExpressSite is the acceptance criterion this kind exists for, asserted
// against the descriptor rather than by reading it.
//
// Two separate claims. `site` and `site_id` must not be writable: NetBox's ClusterSerializer
// has no `site` member at all, so DRF discards the key, returns 201, and creates an unscoped
// cluster -- the netbox-populator bug at ../reconcile.go:270. And the four scope caches must be
// declared read-only, because NetBox maintains them from `(scope_type, scope_id)`: an
// undeclared one is a difference the engine finds again on every resync and PATCHes forever.
//
// `site` is additionally required to be in ReadOnly, not merely absent from Fields. An
// omission is satisfied by forgetting; the explicit entry makes a `siteRef -> site` field a
// boot failure.
func TestClusterCannotExpressSite(t *testing.T) {
	d := virtualizationDescriptor(t, "NetBoxCluster")

	writable := map[string]bool{}
	for _, f := range d.Fields {
		writable[f.API] = true
	}
	for _, generic := range d.GenericFKs {
		writable[generic.TypeField], writable[generic.IDField] = true, true
	}

	for _, column := range []string{"site", "site_id"} {
		if writable[column] {
			t.Errorf("%q is writable on virtualization.Cluster; NetBox 4.2 removed the column and "+
				"discards the write (docs/concepts/generic-refs.md#the-failure-this-prevents)", column)
		}
	}

	if !slices.Contains(d.ReadOnly, "site") {
		t.Error("`site` is not in ReadOnly; the populator's write is accepted and discarded, " +
			"so the deny has to be explicit rather than an omission")
	}

	for _, column := range ScopeCacheColumns() {
		if writable[column] {
			t.Errorf("%q is writable, but NetBox maintains it and drops the write", column)
		}
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly, so a value NetBox returns would be diffed and PATCHed forever",
				column)
		}
	}
}

// TestClusterScopeIsTheSharedUnion asserts the kind took the one line rather than restating
// the union. Restating it is how a `dcim.SiteGroup` spelling NetBox rejects, or a forgotten
// cache column, gets in -- and virtualization.Cluster is the second kind to take it, which is
// the only evidence that the helper generalises.
func TestClusterScopeIsTheSharedUnion(t *testing.T) {
	d := virtualizationDescriptor(t, "NetBoxCluster")

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly the scope pair", d.GenericFKs)
	}

	if want := ScopeFK("scope", ScopeCascadesFromEvery()); !reflect.DeepEqual(d.GenericFKs[0], want) {
		t.Errorf("the scope pair is not registry.ScopeFK(\"scope\"):\n got %+v\nwant %+v",
			d.GenericFKs[0], want)
	}

	// Rule 4 names exactly one containment ref, and for a scoped kind it is the scope: NetBox
	// itself deletes a site's clusters with it, through `_site on_delete=CASCADE`.
	//
	// This assertion was inverted when the kind landed (#210), on the reading that the missing
	// `clusters GenericRelation` on dcim.Site meant a site-scoped cluster does not cascade.
	// It meant the opposite: the GenericRelations on dcim.Region and dcim.SiteGroup exist
	// because `_region` and `_site_group` are SET_NULL, and dcim.Site and dcim.Location need
	// none because their cached column is CASCADE (#214).
	if d.ContainmentRef != "scope" {
		t.Errorf("ContainmentRef = %q, want \"scope\": every member of the scope union deletes "+
			"this kind with it -- two by GenericRelation, two by a CASCADE cached column",
			d.ContainmentRef)
	}

	for _, member := range d.GenericFKs[0].Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("the scope member %s does not cascade; deleting that scope deletes the "+
				"cluster in netbox, so without the owner reference the CR survives and the "+
				"engine recreates the row", member.Spec)
		}
	}
}

// TestClusterNaturalKeysPinGrouplessnessRatherThanOmittingIt is the identity claim, and it
// pins the one place this kind's key is *narrower* than its NetBox constraints.
//
// `meta.constraints` is `(('group','name'), ('_site','name'))`. The first is candidate 1. The
// second is not expressible as a lookup here -- the site id lives inside the scope union and
// the engine never writes a resolved generic FK back into the spec a filter reads -- so the
// fallback is `name` with `group_id` pinned null. Pinned rather than omitted: an omitted
// `group_id` means "this name in any group", and every groupless cluster would adopt an
// unrelated grouped one.
func TestClusterNaturalKeysPinGrouplessnessRatherThanOmittingIt(t *testing.T) {
	d := virtualizationDescriptor(t, "NetBoxCluster")

	want := []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "group_id", Spec: "groupRef"},
				{Filter: "name", Spec: "name"},
			},
		},
		{
			Fields:     []KeyField{{Filter: "name", Spec: "name"}},
			NullFields: []NullField{{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// NBO-206: an FK pin is the sentinel value, not a suffix -- NetBox registers only negation
	// on an FK filter, so neither __isnull nor __empty exists for group_id.
	if got := want[1].NullFields[0]; got.Filter != "group_id" || got.Column != NullColumnRef {
		t.Errorf("null pin renders as %q, want group_id__isnull", got)
	}

	// No candidate reads the scope, in either half. One that did would be Applicable as soon
	// as the union resolved and then fail params() as unfilterable -- a stopped reconcile
	// rather than a lookup (internal/reconciler/refs.go, applyGenericFK).
	for i, key := range d.NaturalKeys {
		for _, spec := range key.specFields() {
			if _, isPair := d.GenericFKFor(spec); isPair {
				t.Errorf("natural key %d reads the generic FK %q, which has no single value to filter on",
					i, spec)
			}
		}
	}
}

// TestClusterCandidatesFollowTheGroup walks the three states `groupRef` can be in, because
// which candidate applies is the whole of this kind's identity logic.
//
// The middle case is the one worth having: a cluster whose group exists in Git but not yet in
// NetBox must not fall through to the groupless candidate. That lookup would find an unrelated
// groupless cluster of the same name, adopt it, and PATCH a group onto it -- moving every VM
// and device that hangs off it.
func TestClusterCandidatesFollowTheGroup(t *testing.T) {
	d := virtualizationDescriptor(t, "NetBoxCluster")

	for _, tc := range []struct {
		name      string
		state     SpecState
		want      int
		wantFirst string
	}{
		{
			name: "grouped and resolved uses (group, name)",
			state: SpecState{
				Declared: []string{"name", "typeRef", "groupRef"},
				Resolved: []string{"name", "typeRef", "groupRef"},
			},
			want:      1,
			wantFirst: "group_id",
		},
		{
			name: "groupless uses name with the group pinned null",
			state: SpecState{
				Declared: []string{"name", "typeRef"},
				Resolved: []string{"name", "typeRef"},
			},
			want:      1,
			wantFirst: "name",
		},
		{
			name: "group declared but unresolved matches nothing and waits",
			state: SpecState{
				Declared: []string{"name", "typeRef", "groupRef"},
				Resolved: []string{"name", "typeRef"},
			},
			want: 0,
		},
		{
			// A scoped, groupless cluster is identified exactly as an unscoped one is: the
			// scope contributes nothing to the lookup. That is the limitation the descriptor
			// documents rather than a state it gets wrong -- an ambiguous name is a Conflict.
			name: "scoped and groupless still uses the name candidate",
			state: SpecState{
				Declared: []string{"name", "typeRef", "scope"},
				Resolved: []string{"name", "typeRef", "scope"},
			},
			want:      1,
			wantFirst: "name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != tc.want {
				t.Fatalf("Candidates() returned %d candidates (%+v), want %d", len(got), got, tc.want)
			}

			if tc.want > 0 && got[0].Fields[0].Filter != tc.wantFirst {
				t.Errorf("leading candidate filters on %q, want %q", got[0].Fields[0].Filter, tc.wantFirst)
			}
		})
	}
}

// TestClusterFieldMapCoversEverySpecField guards the defect the explicit table exists to
// prevent: NetBox ignores a field name it does not know rather than rejecting it, so a spec
// field with no entry -- or with a camelCase entry -- is written nowhere and reported as
// success.
//
// Reflected over the CRD's JSON tags rather than listed, so adding a field to the Go type
// without a descriptor entry fails here.
func TestClusterFieldMapCoversEverySpecField(t *testing.T) {
	for kind, spec := range map[string]reflect.Type{
		"NetBoxClusterType":  reflect.TypeFor[netboxv1alpha1.NetBoxClusterTypeSpec](),
		"NetBoxClusterGroup": reflect.TypeFor[netboxv1alpha1.NetBoxClusterGroupSpec](),
		"NetBoxCluster":      reflect.TypeFor[netboxv1alpha1.NetBoxClusterSpec](),
	} {
		t.Run(kind, func(t *testing.T) {
			d := virtualizationDescriptor(t, kind)
			envelope := jsonFieldNames(reflect.TypeFor[netboxv1alpha1.NetBoxObjectSpec]())

			for _, name := range jsonFieldNames(spec) {
				if slices.Contains(envelope, name) {
					continue
				}

				_, mapped := d.FieldFor(name)
				_, pair := d.GenericFKFor(name)

				if !mapped && !pair {
					t.Errorf("spec field %q is in neither Fields nor GenericFKs, so it would be "+
						"sent verbatim and silently dropped", name)
				}
			}
		})
	}
}

// TestClusterNeedsNoFieldClassesBeyondItsFKs is the "no per-kind engine code" claim. A choice
// column and three foreign keys need no class beyond ClassRefOne, and nothing here is to-many,
// an array or an object-type list -- so a class that stopped carrying its weight would show up
// as a failure rather than as dead data.
func TestClusterNeedsNoFieldClassesBeyondItsFKs(t *testing.T) {
	d := virtualizationDescriptor(t, "NetBoxCluster")

	if got := d.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none", got)
	}
	if got := d.ArrayFields(); len(got) != 0 {
		t.Errorf("ArrayFields() = %v, want none", got)
	}
	if got := d.ObjectTypeListFields(); len(got) != 0 {
		t.Errorf("ObjectTypeListFields() = %v, want none", got)
	}

	refs := make([]string, 0, 3)
	for _, f := range d.Fields {
		if f.Class == ClassRefOne {
			refs = append(refs, f.API)
		}
	}

	if want := []string{"type", "group", "tenant"}; !slices.Equal(refs, want) {
		t.Errorf("to-one references = %v, want %v", refs, want)
	}
}

// TestClusterCatalogueKindsAreContainedByNothing is rule 4 from the other side. A cluster type
// is not a container: deleting it must not delete the clusters that use it, and NetBox's
// PROTECT would refuse the delete anyway.
func TestClusterCatalogueKindsAreContainedByNothing(t *testing.T) {
	for _, kind := range []string{"NetBoxClusterType", "NetBoxClusterGroup"} {
		t.Run(kind, func(t *testing.T) {
			d := virtualizationDescriptor(t, kind)

			if d.ContainmentRef != "" {
				t.Errorf("ContainmentRef = %q, want empty: a catalogue kind has no container",
					d.ContainmentRef)
			}
			if len(d.GenericFKs) != 0 {
				t.Errorf("GenericFKs = %v, want none", d.GenericFKs)
			}
			if len(d.Deferred) != 0 {
				t.Errorf("Deferred = %v, want none: there is no self-reference to defer", d.Deferred)
			}
		})
	}
}

// clusterDriftRules are the comparison rules the engine derives for this kind: the scope pair
// compared as a unit rather than as two independent columns.
func clusterDriftRules(t *testing.T) netbox.FieldRules {
	t.Helper()

	pair := virtualizationDescriptor(t, "NetBoxCluster").GenericFKs[0]

	return netbox.FieldRules{GenericFKs: []netbox.GenericFK{
		{TypeField: pair.TypeField, IDField: pair.IDField},
	}}
}

// TestClusterDriftsCleanlyAgainstNetBoxsReadShape is the anti-hot-loop assertion, and the
// acceptance criterion "re-reconciling an unchanged scoped cluster issues zero writes" at the
// layer that decides it.
//
// NetBox returns `status` as {"value","label"}, the three FKs as nested objects, `scope` as a
// resolved object the operator never wrote, and five read-only annotations -- two related-object
// counts and three allocation sums (v4.6.8 ClusterSerializer). A second reconcile must still
// find nothing to do.
func TestClusterDriftsCleanlyAgainstNetBoxsReadShape(t *testing.T) {
	sent := netbox.Object{
		"name": "proxmox-ams", "status": "active",
		"description": "", "comments": "",
		"type": float64(3), "group": float64(5), "tenant": nil,
		"scope_type": "dcim.site", "scope_id": float64(41),
	}
	live := netbox.Object{
		"name":        "proxmox-ams",
		"status":      map[string]any{"value": "active", "label": "Active"},
		"type":        map[string]any{"id": float64(3), "name": "Proxmox VE", "slug": "proxmox"},
		"group":       map[string]any{"id": float64(5), "name": "Production", "slug": "production"},
		"tenant":      nil,
		"description": "", "comments": "",
		"scope_type": "dcim.site",
		"scope_id":   float64(41),
		"scope":      map[string]any{"id": float64(41), "name": "Home"},
		// The read-only annotations NetBox returns on every read. Each is a PATCH loop if the
		// comparison treats it as a column the spec forgot to set.
		"device_count":         float64(4),
		"virtualmachine_count": float64(17),
		"allocated_vcpus":      "12.00",
		"allocated_memory":     float64(49152),
		"allocated_disk":       float64(2048),
		"created":              "2026-08-21T10:00:00Z",
		"last_updated":         "2026-08-21T10:00:00Z",
	}

	if drift := netbox.Drift(live, sent, clusterDriftRules(t)); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}

// TestClusterUnscopedDriftsCleanlyAgainstNull is the empty-union-versus-null case, which is
// exactly where an "always send the scope" implementation starts a PATCH loop. An unscoped
// cluster declares no scope, NetBox returns both columns as null, and there is nothing to do.
//
// The four caches are in the object too. NetBox's cluster serializer does not return them, but
// the descriptor declares them read-only regardless -- and a comparison that only tolerates
// what today's serializer happens to emit is one NetBox release from a hot loop.
func TestClusterUnscopedDriftsCleanlyAgainstNull(t *testing.T) {
	sent := netbox.Object{"name": "proxmox-lab", "status": "active", "type": float64(3)}
	live := netbox.Object{
		"name":       "proxmox-lab",
		"status":     map[string]any{"value": "active", "label": "Active"},
		"type":       map[string]any{"id": float64(3), "name": "Proxmox VE"},
		"scope_type": nil, "scope_id": nil, "scope": nil,
		"_site": nil, "_region": nil, "_site_group": nil, "_location": nil,
	}

	if drift := netbox.Drift(live, sent, clusterDriftRules(t)); len(drift) != 0 {
		t.Errorf("an unscoped cluster would PATCH %v against a null scope", drift)
	}
}
