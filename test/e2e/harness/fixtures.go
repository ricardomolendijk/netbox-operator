package harness

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// FixtureNamespaces are the namespaces the fixtures live in, and therefore the namespaces
// the chart must grant the manager Secret access to. Declared here rather than in the
// operator's install so that adding a namespace to a fixture set is one edit.
var FixtureNamespaces = []string{"netbox-catalog", "team-a"}

// EndpointNames maps each fixture namespace to the NetBoxEndpoint the fixtures in it name.
// Both point at the same NetBox: the crossing being tested is between namespaces, not
// between NetBoxes.
var EndpointNames = map[string]string{
	"netbox-catalog": "catalogue",
	"team-a":         "team",
}

// Fixture is one manifest file holding exactly one object.
type Fixture struct {
	// File is the base name, which is also the object's position in dependency order.
	File string

	// Object is the parsed manifest. Copied before every apply, because a client's Patch
	// writes the server's response back into it.
	Object *unstructured.Unstructured
}

// Key identifies the fixture's object for a log line or an error.
func (f Fixture) Key() string {
	return fmt.Sprintf("%s %s/%s",
		f.Object.GetKind(), f.Object.GetNamespace(), f.Object.GetName())
}

// LoadFixtures reads a fixture directory in file-name order, which the directory's README
// defines as dependency order.
//
// One object per file is required rather than conventional: the suite permutes the set and a
// multi-document file would be an unsplittable unit inside every permutation.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading fixture directory %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	fixtures := make([]Fixture, 0, len(names))
	for _, name := range names {
		fixture, err := loadFixture(dir, name)
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, fixture)
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("fixture directory %s holds no manifests", dir)
	}
	return fixtures, nil
}

func loadFixture(dir, name string) (Fixture, error) {
	path := filepath.Join(dir, name)
	body, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if strings.Contains(string(body), "\n---\n") {
		return Fixture{}, fmt.Errorf("%s holds more than one document; "+
			"the suite permutes one object per file", path)
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(body, obj); err != nil {
		return Fixture{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if obj.GetKind() == "" || obj.GetName() == "" {
		return Fixture{}, fmt.Errorf("%s has no kind or no name", path)
	}
	return Fixture{File: name, Object: obj}, nil
}

// Reverse returns the fixtures in exactly the opposite order.
//
// A separate, deterministic case rather than one of the random permutations, because it is
// the worst case for a naive implementation and the one a human can reason about when it
// fails.
func Reverse(fixtures []Fixture) []Fixture {
	out := make([]Fixture, len(fixtures))
	for i := range fixtures {
		out[i] = fixtures[len(fixtures)-1-i]
	}
	return out
}

// Permute returns a shuffled copy, driven by rng so a seed reproduces the order exactly.
func Permute(fixtures []Fixture, rng *rand.Rand) []Fixture {
	out := make([]Fixture, len(fixtures))
	copy(out, fixtures)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// SplitGrants separates the NetBoxRefGrants from everything else.
//
// The run it exists for: apply every referrer first and they all sit at RefDenied, then apply
// the grant and it has to move them to Ready by itself, with no resync to help.
func SplitGrants(fixtures []Fixture) (referrers, grants []Fixture) {
	for _, fixture := range fixtures {
		if fixture.Object.GetKind() == "NetBoxRefGrant" {
			grants = append(grants, fixture)
			continue
		}
		referrers = append(referrers, fixture)
	}
	return referrers, grants
}

// Order returns the file names in the order given, for printing in a failure.
func Order(fixtures []Fixture) []string {
	names := make([]string, len(fixtures))
	for i := range fixtures {
		names[i] = fixtures[i].File
	}
	return names
}

// ApplyOptions tunes one apply pass.
type ApplyOptions struct {
	// MaxJitter is the upper bound on the pause between two applies. Zero applies the set
	// as fast as the API server accepts it.
	//
	// It matters: without a pause the manager may coalesce the whole set into one work-queue
	// drain and never observe the intermediate states this gate is about.
	MaxJitter time.Duration

	// Rng drives the jitter, so a seed reproduces the timing as well as the order.
	Rng *rand.Rand

	// Between runs after each apply, before the jitter. The manager-restart run uses it to
	// kill the manager partway through.
	Between func(ctx context.Context, index int) error
}

// Apply creates the fixtures in the given order, one request each.
//
// One request each and never a batch: `kubectl apply -f dir/` hands the API server the whole
// set in one go, and the manager may then never see a referrer before its target.
func Apply(ctx context.Context, c client.Client, fixtures []Fixture, opts ApplyOptions) error {
	for i, fixture := range fixtures {
		obj := fixture.Object.DeepCopy()
		if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("applying %s (%s): %w", fixture.File, fixture.Key(), err)
		}
		if opts.Between != nil {
			if err := opts.Between(ctx, i); err != nil {
				return fmt.Errorf("between applies at index %d (%s): %w", i, fixture.File, err)
			}
		}
		if err := sleepJitter(ctx, opts); err != nil {
			return err
		}
	}
	return nil
}

func sleepJitter(ctx context.Context, opts ApplyOptions) error {
	if opts.MaxJitter <= 0 || opts.Rng == nil {
		return nil
	}
	pause := time.Duration(opts.Rng.Int64N(int64(opts.MaxJitter)))
	select {
	case <-ctx.Done():
		return fmt.Errorf("jittering between applies: %w", ctx.Err())
	case <-time.After(pause):
		return nil
	}
}

// DeleteAll removes the fixtures in the given order and waits for every one of them to be
// gone -- finalizer released, NetBox side settled.
//
// The order is a parameter because "delete in random order" is one of NBO-017's assertions:
// there is no manual ordering to get right, and a PROTECT 409 has to resolve itself.
func DeleteAll(ctx context.Context, c client.Client, fixtures []Fixture, timeout time.Duration) error {
	for _, fixture := range fixtures {
		obj := fixture.Object.DeepCopy()
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting %s (%s): %w", fixture.File, fixture.Key(), err)
		}
	}
	return WaitGone(ctx, c, fixtures, timeout)
}

// WaitGone waits until none of the fixtures' objects is in the API server.
//
// A CR still present after its delete means a finalizer is holding it, which is the state
// NBO-017 asserts against by name: no stuck finalizers after a random-order teardown.
func WaitGone(ctx context.Context, c client.Client, fixtures []Fixture, timeout time.Duration) error {
	return WaitFor(ctx, "every fixture CR to be gone", timeout,
		func(ctx context.Context) (bool, string, error) {
			var remaining []string
			for _, fixture := range fixtures {
				present, detail, err := stillPresent(ctx, c, fixture)
				if err != nil {
					return false, "", err
				}
				if present {
					remaining = append(remaining, detail)
				}
			}
			if len(remaining) == 0 {
				return true, "", nil
			}
			return false, strings.Join(remaining, "; "), nil
		})
}

func stillPresent(ctx context.Context, c client.Client, fixture Fixture) (bool, string, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(fixture.Object.GroupVersionKind())
	key := types.NamespacedName{
		Namespace: fixture.Object.GetNamespace(),
		Name:      fixture.Object.GetName(),
	}

	err := c.Get(ctx, key, obj)
	if apierrors.IsNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("getting %s: %w", fixture.Key(), err)
	}
	return true, fmt.Sprintf("%s still present (finalizers %v)",
		fixture.Key(), obj.GetFinalizers()), nil
}

// DeferredWrites is an upper bound on the follow-up PATCHes the fixtures can legitimately
// cost: every field the kinds involved declare as deferred.
//
// An upper bound and not an exact count, on purpose. Whether a deferred field needs its
// PATCH depends on the order the objects happened to be applied in, which is the variable
// this suite is sweeping -- so the write budget has to hold for the worst case or it would
// fail on some permutations and pass on others.
func DeferredWrites(fixtures []Fixture) int {
	var total int
	for _, fixture := range fixtures {
		descriptor, ok := registry.Get(fixture.Object.GroupVersionKind())
		if !ok {
			continue
		}
		total += len(descriptor.Deferred)
	}
	return total
}

// CrossesNamespace reports whether any reference in the fixture's spec points into another
// namespace, and therefore whether a NetBoxRefGrant governs it.
//
// Found by walking the spec for a `namespace` key rather than by knowing which field of which
// kind is a reference: `namespace` is only ever a member of ObjectRef, and a helper that knew
// the field names would need editing every time a kind is added.
func CrossesNamespace(fixture Fixture) bool {
	spec, found, err := unstructured.NestedMap(fixture.Object.Object, "spec")
	if err != nil || !found {
		return false
	}
	return referencesOtherNamespace(spec, fixture.Object.GetNamespace())
}

func referencesOtherNamespace(value any, own string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if namespace, ok := typed["namespace"].(string); ok && namespace != "" && namespace != own {
			return true
		}
		for _, nested := range typed {
			if referencesOtherNamespace(nested, own) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if referencesOtherNamespace(nested, own) {
				return true
			}
		}
	}
	return false
}

// SeedFromEnv returns the PRNG seed and where it came from, so a run prints a value a
// failure can be reproduced with.
func SeedFromEnv() (seed uint64, explicit bool, err error) {
	raw := os.Getenv(EnvSeed)
	if raw == "" {
		return rand.Uint64(), false, nil
	}
	parsed, parseErr := parseUint(raw)
	if parseErr != nil {
		return 0, false, fmt.Errorf("%s=%q is not an unsigned integer: %w", EnvSeed, raw, parseErr)
	}
	return parsed, true, nil
}

func parseUint(raw string) (uint64, error) {
	var value uint64
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return 0, errors.New("not a number")
	}
	return value, nil
}
