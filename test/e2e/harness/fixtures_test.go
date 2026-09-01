package harness

import (
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// graphDir is the shipped convergence graph, read from the harness package's own directory.
// The tests below assert properties of the fixtures themselves, because a graph that stopped
// exercising a crossing would still pass every run of the gate.
func graphDir() string { return filepath.Join("..", "fixtures", "graph") }

func TestTheShippedGraphLoadsInDependencyOrder(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}

	names := Order(fixtures)
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	if !slices.Equal(names, sorted) {
		t.Errorf("fixtures are not in file-name order, which is the dependency order:\n%v", names)
	}

	for _, fixture := range fixtures {
		if fixture.Object.GetNamespace() == "" {
			t.Errorf("%s has no namespace; every kind in v1alpha1 is namespaced", fixture.File)
		}
		if !slices.Contains(FixtureNamespaces, fixture.Object.GetNamespace()) {
			t.Errorf("%s is in namespace %q, which is not in FixtureNamespaces %v -- the chart "+
				"grants the manager Secret access namespace by namespace, so the harness would "+
				"never seed an endpoint there",
				fixture.File, fixture.Object.GetNamespace(), FixtureNamespaces)
		}
	}
}

// The graph is only a gate if it still contains the things it was built to exercise. Each
// case below is a row of test/e2e/fixtures/graph/README.md.
func TestTheShippedGraphStillExercisesEveryReferenceShape(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}

	var crossings int
	kinds := map[string]bool{}
	for _, fixture := range fixtures {
		kinds[fixture.Object.GetKind()] = true
		if CrossesNamespace(fixture) {
			crossings++
		}
	}

	// Three crossings, one grant. Fewer crossings and the grant stops being load-bearing;
	// no grant and the RefDenied run has nothing to apply last.
	if crossings < 3 {
		t.Errorf("the graph makes %d cross-namespace references, want at least 3", crossings)
	}
	if !kinds["NetBoxRefGrant"] {
		t.Error("the graph has no NetBoxRefGrant, so the grant-last run cannot exist")
	}

	// The scope union, the generic FK and the required reference each have exactly one
	// carrier in the graph, so naming the kinds is naming the coverage.
	for _, kind := range []string{
		"NetBoxVLANGroup",         // spec.scope: the scope union, and half of its identity
		"NetBoxPrefix",            // spec.scope again, through a different member
		"NetBoxContactAssignment", // spec.objectRef: the generic-FK pair
		"NetBoxLocation",          // spec.siteRef: required, and no natural key without it
		"NetBoxContact",           // spec.groups: a to-many
		"NetBoxRegion",            // spec.parentRef: self-referential
	} {
		if !kinds[kind] {
			t.Errorf("the graph no longer contains a %s, so it stops covering that shape", kind)
		}
	}
}

func TestCrossesNamespaceFindsARefAtAnyDepth(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want bool
	}{
		{"a top-level ref into another namespace", map[string]any{
			"parentRef": map[string]any{"namespace": "netbox-catalog", "name": "nl"},
		}, true},
		{"a ref nested inside a union", map[string]any{
			"scope": map[string]any{
				"regionRef": map[string]any{"namespace": "netbox-catalog", "name": "nl"},
			},
		}, true},
		{"a ref inside a list", map[string]any{
			"groups": []any{map[string]any{"namespace": "netbox-catalog", "name": "ops"}},
		}, true},
		{"a ref that stays at home", map[string]any{
			"parentRef": map[string]any{"name": "emea"},
		}, false},
		// A namespace equal to the object's own is not a crossing: a namespace does not
		// grant itself access to itself, so no grant governs it.
		{"a ref naming its own namespace", map[string]any{
			"parentRef": map[string]any{"namespace": "team-a", "name": "emea"},
		}, false},
		{"no refs at all", map[string]any{"name": "leaf"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fixtureWithSpec(t, "team-a", tc.spec)
			if got := CrossesNamespace(fixture); got != tc.want {
				t.Errorf("CrossesNamespace() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReverseIsExactlyTheOppositeOrder(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}

	reversed := Order(Reverse(fixtures))
	forward := Order(fixtures)
	for i := range forward {
		if reversed[i] != forward[len(forward)-1-i] {
			t.Fatalf("Reverse() is not the exact opposite at index %d: %q vs %q",
				i, reversed[i], forward[len(forward)-1-i])
		}
	}
	if len(reversed) != len(forward) {
		t.Errorf("Reverse() returned %d fixtures, want %d", len(reversed), len(forward))
	}
}

// A seed is only worth printing if it reproduces the run. Two PRNGs from one seed must give
// the same permutation, and Permute must not disturb the caller's slice -- the suite permutes
// the same graph twenty times.
func TestPermuteIsReproducibleAndNonDestructive(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}
	original := Order(fixtures)

	const seed = uint64(0xC0FFEE)
	first := Order(Permute(fixtures, rand.New(rand.NewPCG(seed, 1))))
	second := Order(Permute(fixtures, rand.New(rand.NewPCG(seed, 1))))

	if !slices.Equal(first, second) {
		t.Errorf("one seed produced two orders, so a printed seed reproduces nothing:\n%v\n%v",
			first, second)
	}
	if !slices.Equal(Order(fixtures), original) {
		t.Errorf("Permute() reordered its argument:\n%v\n%v", Order(fixtures), original)
	}
	if slices.Equal(first, original) {
		t.Log("this seed's permutation happens to be the identity; not a failure, but the " +
			"run it drives proves nothing beyond the forward case")
	}
}

func TestSplitGrantsSeparatesAuthorisationFromData(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}

	referrers, grants := SplitGrants(fixtures)
	if len(grants) == 0 {
		t.Fatal("SplitGrants() found no grant in the shipped graph")
	}
	if len(referrers)+len(grants) != len(fixtures) {
		t.Errorf("SplitGrants() returned %d + %d fixtures, want %d",
			len(referrers), len(grants), len(fixtures))
	}
	for _, grant := range grants {
		if grant.Object.GetKind() != "NetBoxRefGrant" {
			t.Errorf("%s is not a grant", grant.File)
		}
	}
	for _, referrer := range referrers {
		if referrer.Object.GetKind() == "NetBoxRefGrant" {
			t.Errorf("%s is a grant and was left with the referrers", referrer.File)
		}
	}
}

func TestLoadFixturesRefusesAMultiDocumentFile(t *testing.T) {
	// The suite permutes one object per file, so a file holding two would be an
	// unsplittable unit inside every permutation -- and the reverse run would not be the
	// reverse.
	dir := t.TempDir()
	body := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: one\n---\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: two\n"
	writeFixture(t, dir, "00-two.yaml", body)

	_, err := LoadFixtures(dir)
	if err == nil {
		t.Fatal("LoadFixtures() accepted a two-document file")
	}
	if !strings.Contains(err.Error(), "more than one document") {
		t.Errorf("LoadFixtures() error does not say why: %v", err)
	}
}

func TestLoadFixturesRefusesAnEmptyDirectory(t *testing.T) {
	if _, err := LoadFixtures(t.TempDir()); err == nil {
		t.Fatal("LoadFixtures() accepted a directory with no manifests")
	}
}

func TestDeferredWritesBoundsTheFollowUpPatches(t *testing.T) {
	fixtures, err := LoadFixtures(graphDir())
	if err != nil {
		t.Fatalf("LoadFixtures(%s) = %v", graphDir(), err)
	}

	// The write budget is objects + this. A negative or absurd figure would make the
	// economy assertion either unfailable or unpassable, which is the only way it can be
	// wrong without anyone noticing.
	deferred := DeferredWrites(fixtures)
	if deferred < 0 || deferred > len(fixtures) {
		t.Errorf("DeferredWrites() = %d for %d fixtures, which cannot be right",
			deferred, len(fixtures))
	}
}
