package resolver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestCheck is the graph table: one row per shape a reference graph can have, and what the
// check is allowed to conclude from it.
//
// The rows that report nothing matter as much as the rows that report a cycle. A check that
// cannot tell a 30-hop hierarchy, a diamond or a dead-end from a ring is worse than no check
// at all: it would report `RefCycle` on manifests that are correct, and the reason would stop
// meaning anything.
func TestCheck(t *testing.T) {
	tests := []struct {
		name string

		graph []target
		start types.NamespacedName
		kinds fakeDescriptors

		// wantCause is the sentinel expected back, and nil for the graphs that must report
		// nothing at all.
		wantCause error

		// wantMessage is a substring the message must contain. For a cycle that is the path,
		// which is the entire product: "a cycle was detected" tells a user nothing they can
		// act on.
		wantMessage string

		// wantPath is the ordered walk the error carries, rendered.
		wantPath []string

		// readErr replaces every read, for the failures that are not about one object being
		// absent.
		readErr error

		// wantFailure marks a read that failed for a reason of its own, which is an error
		// rather than a verdict about the manifest.
		wantFailure bool
	}{
		{
			name:  "an object with no references has no cycle",
			graph: []target{regionCR("team-a", "a", nil)},
			start: namespacedName("team-a", "a"),
		},
		{
			// The typo people actually make, and the one dcim.Region's schema cannot prevent:
			// `parent` is a ForeignKey -> self (docs/netbox-schema.md).
			name:        "a self-reference is a cycle, and says so in its own words",
			graph:       []target{regionCR("team-a", "a", parentRef("a"))},
			start:       namespacedName("team-a", "a"),
			wantCause:   ErrRefCycle,
			wantMessage: "netboxregion/team-a/a -> itself",
			wantPath:    []string{"netboxregion/team-a/a", "netboxregion/team-a/a"},
		},
		{
			name: "two objects waiting on each other are a cycle",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("a")),
			},
			start:       namespacedName("team-a", "a"),
			wantCause:   ErrRefCycle,
			wantMessage: "netboxregion/team-a/a -> netboxregion/team-a/b -> netboxregion/team-a/a",
			wantPath: []string{
				"netboxregion/team-a/a", "netboxregion/team-a/b", "netboxregion/team-a/a",
			},
		},
		{
			name: "three objects in a ring are a cycle",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("c")),
				regionCR("team-a", "c", parentRef("a")),
			},
			start:     namespacedName("team-a", "a"),
			wantCause: ErrRefCycle,
			wantMessage: "netboxregion/team-a/a -> netboxregion/team-a/b -> " +
				"netboxregion/team-a/c -> netboxregion/team-a/a",
			wantPath: []string{
				"netboxregion/team-a/a", "netboxregion/team-a/b",
				"netboxregion/team-a/c", "netboxregion/team-a/a",
			},
		},
		{
			// On the normal path rather than exotic: every Kind is namespaced, so a team
			// namespace pointing at a shared catalogue is what a reference usually looks like
			// (docs/decisions/0002-crd-scoping.md).
			name: "a cycle that crosses a namespace is a cycle",
			graph: []target{
				regionCR("team-a", "a", parentRefIn("catalogue", "b")),
				regionCR("catalogue", "b", parentRefIn("team-a", "a")),
			},
			start:     namespacedName("team-a", "a"),
			wantCause: ErrRefCycle,
			wantMessage: "netboxregion/team-a/a -> netboxregion/catalogue/b -> " +
				"netboxregion/team-a/a",
			wantPath: []string{
				"netboxregion/team-a/a", "netboxregion/catalogue/b", "netboxregion/team-a/a",
			},
		},
		{
			// Two objects of the same name under two Kinds are two objects. A graph keyed on
			// the name alone would call this a cycle and be wrong about a manifest that
			// converges.
			name: "the same name under two kinds is not a cycle",
			graph: []target{
				siteCR("team-a", "a", map[string]any{
					"name": "a", "regionRef": map[string]any{"name": "a"},
				}),
				regionCR("team-a", "a", nil),
			},
			start: namespacedName("team-a", "a"),
			kinds: fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
				siteGVK:   siteRefsRegion(),
				regionGVK: regionDescriptor(),
			}},
		},
		{
			name: "a chain that ends converges",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("c")),
				regionCR("team-a", "c", nil),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			// A chain through an object that does not exist is a missing reference, not a
			// ring. Reporting RefCycle here would send a user hunting for a cycle that is not
			// in their manifests, while what they actually have to create is `c`.
			name: "a chain that ends in a missing object is not a cycle",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("c")),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			// Even when the missing object is the one that would have closed the ring: the
			// cache says it is not there, so there is nothing waiting on anything.
			name: "a ring through a missing object is not a cycle",
			graph: []target{
				regionCR("team-a", "a", parentRef("gone")),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			// Two paths arriving at one object is ordinary structure. Only a return to the
			// object being reconciled is a cycle; anything else is a diamond, and the walk
			// prunes rather than reporting.
			name: "a diamond is not a cycle",
			graph: []target{
				forkCR("a", parentRef("b"), altRef("c")),
				forkCR("b", parentRef("d"), nil),
				forkCR("c", parentRef("d"), nil),
				forkCR("d", nil, nil),
			},
			start: namespacedName("team-a", "a"),
			kinds: forkKinds(),
		},
		{
			// An object that points *into* a ring it is not part of reports no cycle of its
			// own, and that is the honest answer: it is waiting for `a`, which is a state, and
			// its message quotes `a`'s own condition -- which is where the ring is named. A
			// cycle reported here would be a cycle this object cannot fix by editing itself.
			name: "an object pointing into a cycle is not in it",
			graph: []target{
				regionCR("team-a", "x", parentRef("a")),
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("a")),
			},
			start: namespacedName("team-a", "x"),
		},
		{
			name:  "a chain of exactly the depth limit converges",
			graph: chain("team-a", MaxRefDepth),
			start: namespacedName("team-a", "r0"),
		},
		{
			// A long chain is not a ring, and must not be reported as one: the fix is to
			// flatten the hierarchy, not to hunt for a reference that comes back.
			name:      "a chain one reference past the limit is too deep, not a cycle",
			graph:     chain("team-a", MaxRefDepth+1),
			start:     namespacedName("team-a", "r0"),
			wantCause: ErrRefDepthExceeded,
			wantMessage: fmt.Sprintf(
				"the chain of blocking references from netboxregion/team-a/r0 is more than %d deep "+
					"(netboxregion/team-a/r0 -> ... -> netboxregion/team-a/r%d)",
				MaxRefDepth, MaxRefDepth+1),
		},
		{
			// The three NetBox-side modes cannot participate at all: they resolve against
			// NetBox, where there is no CR to be waiting on. This is what bounds the problem
			// to the CR graph.
			name: "a slug reference terminates the walk",
			graph: []target{
				regionCR("team-a", "a", map[string]any{"slug": "b"}),
				regionCR("team-a", "b", parentRef("a")),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			name: "a lookup reference terminates the walk",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", map[string]any{"lookup": map[string]any{"name": "a"}}),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			name: "an id reference terminates the walk",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", map[string]any{"id": int64(12)}),
			},
			start: namespacedName("team-a", "a"),
		},
		{
			// A Kind with no Descriptor is outside the CR graph: nothing can be waiting on it
			// here, and it costs no read at all.
			name:  "a reference to an unavailable kind terminates the walk",
			graph: []target{siteCR("team-a", "a", map[string]any{"tenantRef": map[string]any{"name": "acme"}})},
			start: namespacedName("team-a", "a"),
			kinds: fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
				siteGVK: siteDescriptor(),
			}},
		},
		{
			// The CRD is not installed, so no object of that Kind exists to be waiting on --
			// the same answer as a Kind with no descriptor, arriving from the RESTMapper
			// instead of from the registry.
			name: "a kind whose CRD is missing terminates the walk",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("a")),
			},
			start:   namespacedName("team-a", "a"),
			readErr: noKindMatch(regionGVK),
		},
		{
			// A read that failed for a reason of its own is not a verdict about the manifest.
			// Reporting "no cycle" would be a guess, and reporting one would be a lie.
			name: "a failed read is a failure rather than a verdict",
			graph: []target{
				regionCR("team-a", "a", parentRef("b")),
			},
			start:       namespacedName("team-a", "a"),
			readErr:     errAPIServer,
			wantFailure: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{objects: tc.graph, err: tc.readErr}

			err := checkGraph(t, reader, tc.kinds, tc.graph, tc.start)

			switch {
			case tc.wantFailure:
				assertFailure(t, err)
			case tc.wantCause != nil:
				assertCycle(t, err, tc.wantCause, tc.wantMessage, tc.wantPath)
			default:
				if err != nil {
					t.Fatalf("Check() = %v, want no cycle", err)
				}
			}
		})
	}
}

// TestCheckReportsOnEveryMemberOfACycle is the acceptance criterion the whole design turns on.
//
// A user looks at one object. If the cycle is reported on whichever object happened to
// reconcile first, everybody else in the ring reports a plain wait, and the reader concludes
// their object is fine and somebody else's is broken. Every member reports it, from its own
// perspective -- and nothing coordinates that, because the walk is relative to the object it
// starts from and every member of a ring is on it.
func TestCheckReportsOnEveryMemberOfACycle(t *testing.T) {
	graph := []target{
		regionCR("team-a", "a", parentRef("b")),
		regionCR("team-a", "b", parentRefIn("catalogue", "c")),
		regionCR("catalogue", "c", parentRefIn("team-a", "a")),
	}

	wantPaths := map[string][]string{
		"team-a/a": {
			"netboxregion/team-a/a", "netboxregion/team-a/b",
			"netboxregion/catalogue/c", "netboxregion/team-a/a",
		},
		"team-a/b": {
			"netboxregion/team-a/b", "netboxregion/catalogue/c",
			"netboxregion/team-a/a", "netboxregion/team-a/b",
		},
		"catalogue/c": {
			"netboxregion/catalogue/c", "netboxregion/team-a/a",
			"netboxregion/team-a/b", "netboxregion/catalogue/c",
		},
	}

	for _, member := range graph {
		key := namespacedName(member.namespace, member.name)

		t.Run(key.String(), func(t *testing.T) {
			err := checkGraph(t, &fakeReader{objects: graph}, fakeDescriptors{}, graph, key)

			want := wantPaths[key.String()]
			assertCycle(t, err, ErrRefCycle, strings.Join(want, " -> "), want)
		})
	}
}

// TestCheckFollowsOnlyBlockingEdges is the table that separates a correct implementation from
// a plausible one.
//
// NetBox's own foreign-key graph has cycles in it that are supposed to be there --
// `Device -> primary_ip4 -> IPAddress -> assigned_object -> Interface -> device` -- and
// NBO-015's two-pass PATCH is what resolves them. A detector that cannot tell those from a
// Kubernetes deadlock makes the deferred design unusable, so the rule is not "follow every
// reference" but "follow a reference the referring object cannot be created without".
func TestCheckFollowsOnlyBlockingEdges(t *testing.T) {
	tests := []struct {
		name      string
		deferred  []registry.DeferredField
		keys      []registry.NaturalKey
		wantCause error
	}{
		{
			// The ordinary case: nothing deferred, so the object cannot be written until the
			// reference resolves, and a ring of them never resolves.
			name:      "an ordinary reference blocks",
			keys:      regionDescriptor().NaturalKeys,
			wantCause: ErrRefCycle,
		},
		{
			// Deferred unconditionally because it cannot be resolvable at create time by
			// construction. The second PATCH breaks the ring by design, so reporting it would
			// refuse a manifest that converges.
			name:     "a DeferAlways reference does not block",
			deferred: []registry.DeferredField{{APIField: "parent", Mode: registry.DeferAlways}},
			keys:     regionDescriptor().NaturalKeys,
		},
		{
			// `lag` on an interface: deferred when it does not resolve, and no candidate needs
			// it, so the object is created and the field is PATCHed in afterwards. Nothing is
			// waiting.
			name:     "a DeferIfUnresolved reference no natural key needs does not block",
			deferred: []registry.DeferredField{{APIField: "parent", Mode: registry.DeferIfUnresolved}},
			keys: []registry.NaturalKey{{
				Fields: []registry.KeyField{{Filter: "name", Spec: "name"}},
			}},
		},
		{
			// The case that distinguishes the two: `parent` is deferred *and* a natural-key
			// candidate matches on it, so the engine refuses to create the object without it
			// (NaturalKey.Applicable) and the ring genuinely deadlocks.
			name:      "a DeferIfUnresolved reference a natural key needs blocks",
			deferred:  []registry.DeferredField{{APIField: "parent", Mode: registry.DeferIfUnresolved}},
			keys:      regionDescriptor().NaturalKeys,
			wantCause: ErrRefCycle,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := regionDescriptor()
			d.Deferred = tc.deferred
			d.NaturalKeys = tc.keys

			graph := []target{
				regionCR("team-a", "a", parentRef("b")),
				regionCR("team-a", "b", parentRef("a")),
			}

			kinds := fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{regionGVK: d}}
			resolver := &Resolver{Objects: &fakeReader{objects: graph}, Kinds: kinds}

			err := resolver.Check(context.Background(), graph[0].object(), d)

			if tc.wantCause == nil {
				if err != nil {
					t.Fatalf("Check() = %v, want no cycle", err)
				}

				return
			}

			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("Check() = %v, want %v", err, tc.wantCause)
			}
		})
	}
}

// TestCheckBoundsWhatItReads is the other half of the depth cap. Depth bounds the length of a
// path and says nothing about the breadth of a graph, so a kind with two blocking references
// doubles the work per level -- and the walk reads CRs, once per reconcile, for every object
// in the cluster that has any.
//
// The cap is honest about what it is: the search stopped without proving anything, so it
// reports that rather than "no cycle".
func TestCheckBoundsWhatItReads(t *testing.T) {
	// A binary tree, which is the cheapest way to make a graph broad without making it deep:
	// 511 objects inside 9 levels, well within MaxRefDepth.
	const nodes = 511

	graph := make([]target, 0, nodes)
	for i := 1; i <= nodes; i++ {
		var left, right map[string]any
		if 2*i <= nodes {
			left, right = parentRef(fmt.Sprintf("n%d", 2*i)), altRef(fmt.Sprintf("n%d", 2*i+1))
		}

		graph = append(graph, forkCR(fmt.Sprintf("n%d", i), left, right))
	}

	reader := &fakeReader{objects: graph}

	err := checkGraph(t, reader, forkKinds(), graph, namespacedName("team-a", "n1"))

	if !errors.Is(err, ErrRefDepthExceeded) {
		t.Fatalf("Check() = %v, want %v", err, ErrRefDepthExceeded)
	}

	if want := fmt.Sprintf("span more than %d objects", maxRefVisits); !strings.Contains(err.Error(), want) {
		t.Errorf("Check() = %q, want it to contain %q", err.Error(), want)
	}

	// The point of the cap: the reads a reconcile pays for are bounded by a constant and not
	// by the size of the namespace.
	if reader.reads > maxRefVisits {
		t.Errorf("cluster reads = %d, want at most %d", reader.reads, maxRefVisits)
	}
}

// TestResolveAllReportsACycle is the engine-facing contract: a cycle arrives as the object's
// blocker, with the reason and the message a condition is written from, and no NetBox request
// on the way.
func TestResolveAllReportsACycle(t *testing.T) {
	graph := []target{
		regionCR("team-a", "a", parentRef("b")),
		regionCR("team-a", "b", parentRef("a")),
	}

	nb := &fakeNetBox{}
	resolver := &Resolver{
		Objects: &fakeReader{objects: graph},
		Kinds:   fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{regionGVK: regionDescriptor()}},
	}

	resolution, err := resolver.ResolveAll(
		context.Background(), nb, graph[0].object(), regionDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v, want a blocker rather than an error", err)
	}

	if len(resolution.Blocked) != 1 || resolution.Blocked[0].Field != "parentRef" {
		t.Fatalf("Blocked = %+v, want one blocker on parentRef", resolution.Blocked)
	}

	if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefCycle {
		t.Errorf("Reason() = %q, want %q", got, netboxv1alpha1.ReasonRefCycle)
	}

	want := "netboxregion/team-a/a -> netboxregion/team-a/b -> netboxregion/team-a/a"
	if !strings.Contains(resolution.Message(), want) {
		t.Errorf("Message() = %q, want it to name the cycle %q", resolution.Message(), want)
	}

	// A cycle is cleared by an edit, and an edit arrives as a watch event. A timer here would
	// re-derive the same verdict for as long as the manifest stands.
	if got := resolution.Requeue(); got != 0 {
		t.Errorf("Requeue() = %v, want no requeue at all", got)
	}

	// Not one request, of any kind: the object cannot be created and the reference cannot
	// resolve, so there is nothing NetBox could be asked that would help.
	assertCalls(t, nb, nil)
}

// TestResolveAllFailsWhenTheWalkCannotRead is the other half of the guard: a cluster read that
// failed is the engine's backoff, not a condition about the manifest. Reporting "no cycle" and
// carrying on would resolve references against a graph nobody managed to look at.
func TestResolveAllFailsWhenTheWalkCannotRead(t *testing.T) {
	graph := []target{regionCR("team-a", "a", parentRef("b"))}

	resolver := &Resolver{
		Objects: &fakeReader{objects: graph, err: errAPIServer},
		Kinds:   regionKinds(),
	}

	_, err := resolver.ResolveAll(
		context.Background(), &fakeNetBox{}, graph[0].object(), regionDescriptor())

	if !errors.Is(err, errAPIServer) {
		t.Fatalf("ResolveAll() = %v, want %v", err, errAPIServer)
	}

	var refErr *Error
	if errors.As(err, &refErr) {
		t.Errorf("ResolveAll() = %v, want a plain failure rather than a blocked reference", err)
	}
}

// TestResolveAllReadsEachObjectOnce pins the read cost of a pass. The cycle check and the
// resolution want the same objects, and a check that doubled the reads of every reconcile
// would be paid for on every object in the cluster rather than on the broken ones.
func TestResolveAllReadsEachObjectOnce(t *testing.T) {
	graph := []target{
		{
			gvk: regionGVK, namespace: "team-a", name: "b", id: 12,
			ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			spec: map[string]any{"name": "b", "parentRef": parentRef("c")},
		},
		{
			gvk: regionGVK, namespace: "team-a", name: "c", id: 13,
			ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			spec: map[string]any{"name": "c"},
		},
	}

	reader := &fakeReader{objects: graph}
	resolver := &Resolver{
		Objects: reader,
		Kinds:   fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{regionGVK: regionDescriptor()}},
	}

	obj := regionCR("team-a", "a", parentRef("b")).object()

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, regionDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	if got := resolution.ByField["parentRef"].IDs(); len(got) != 1 || got[0] != 12 {
		t.Fatalf("ByField[parentRef].IDs() = %v, want [12]", got)
	}

	// Two objects in the chain, two reads: the walk read `b` and `c`, and resolving `parentRef`
	// reused the walk's `b` rather than fetching it again.
	if reader.reads != 2 {
		t.Errorf("cluster reads = %d, want 2", reader.reads)
	}
}

// TestCheckIgnoresTheReferrersOwnStatus is why the walk does not stop at an object that looks
// resolved.
//
// Both members of this ring are Ready with an id, which is what a cycle looks like the instant
// after somebody edits two working objects into one. Pruning at a Ready target would find no
// cycle, resolve both references and PATCH both parents into NetBox, leaving NetBox's own
// MPTT check to refuse it -- a write we should never have made, and a condition that blames
// NetBox for a manifest problem.
func TestCheckIgnoresTheReferrersOwnStatus(t *testing.T) {
	ready := func(name, parent string) target {
		return target{
			gvk: regionGVK, namespace: "team-a", name: name, id: 12,
			ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			spec: map[string]any{"name": name, "parentRef": parentRef(parent)},
		}
	}

	graph := []target{ready("a", "b"), ready("b", "a")}

	if err := checkGraph(t, &fakeReader{objects: graph}, fakeDescriptors{}, graph,
		namespacedName("team-a", "a")); !errors.Is(err, ErrRefCycle) {
		t.Fatalf("Check() = %v, want %v", err, ErrRefCycle)
	}
}

// TestCheckStopsAtAnEdgeItMayNotFollow is NBO-092.
//
// The grant check is deliberately made *before* the target is read, so that a denied reference
// cannot tell a missing object from a present one in a namespace it has no access to. The cycle
// walk did not go through it: it read CRs directly, followed `name` edges across namespaces and
// reported the ring, so a namespace with no grant could close a ring through a foreign object
// and read that object's name back out of its own condition.
//
// The graph below is that construction. `team-a` may not reference `catalogue`, and the ring
// only closes through it.
func TestCheckStopsAtAnEdgeItMayNotFollow(t *testing.T) {
	graph := []target{
		regionCR("team-a", "a", parentRef("b")),
		regionCR("team-a", "b", parentRefIn("catalogue", "x")),
		regionCR("catalogue", "x", parentRefIn("team-a", "a")),
	}

	start := graph[0].object()

	t.Run("no grant, so the walk stops and the ring is not reported", func(t *testing.T) {
		reader := &fakeReader{objects: graph}
		resolver := &Resolver{Objects: reader, Kinds: regionKinds(), Grants: &fakeGrants{}}

		if err := resolver.Check(context.Background(), start, regionDescriptor()); err != nil {
			t.Fatalf("Check() = %v, want no verdict: the ring exists only through an edge team-a may not follow", err)
		}

		// The oracle closed: the object in `catalogue` was never read, so nothing about it --
		// that it is there, that it points back -- can reach team-a's condition.
		if reader.reads != 1 {
			t.Errorf("cluster reads = %d, want 1: only team-a/b is readable from team-a", reader.reads)
		}
	})

	t.Run("nothing the referrer reports names the object it may not reference", func(t *testing.T) {
		resolver := &Resolver{
			Objects: &fakeReader{objects: graph}, Kinds: regionKinds(), Grants: &fakeGrants{},
		}

		resolution, err := resolver.ResolveAll(
			context.Background(), &fakeNetBox{}, start, regionDescriptor())
		if err != nil {
			t.Fatalf("ResolveAll() = %v", err)
		}

		// `a` waits on `b`, which is the truth from where `a` stands, and `b` reports the denial
		// on its own reference -- the grant is what is missing, and that is what is actionable.
		if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefNotReady {
			t.Errorf("Reason() = %q, want %q", got, netboxv1alpha1.ReasonRefNotReady)
		}

		if strings.Contains(resolution.Message(), "catalogue") {
			t.Errorf("Message() = %q, want it to name nothing in a namespace team-a has no grant into",
				resolution.Message())
		}
	})

	t.Run("the denied edge is reported as RefDenied on the object holding it", func(t *testing.T) {
		resolver := &Resolver{
			Objects: &fakeReader{objects: graph}, Kinds: regionKinds(), Grants: &fakeGrants{},
		}

		resolution, err := resolver.ResolveAll(
			context.Background(), &fakeNetBox{}, graph[1].object(), regionDescriptor())
		if err != nil {
			t.Fatalf("ResolveAll() = %v", err)
		}

		if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefDenied {
			t.Fatalf("Reason() = %q, want %q: the missing grant is the actionable half", got, netboxv1alpha1.ReasonRefDenied)
		}

		if !strings.Contains(resolution.Message(), "not permitted to reference") {
			t.Errorf("Message() = %q, want the denial and its remedy", resolution.Message())
		}
	})

	t.Run("with the grant the whole ring is reported", func(t *testing.T) {
		resolver := &Resolver{
			Objects: &fakeReader{objects: graph}, Kinds: regionKinds(),
			// Only `catalogue` needs one: the edge back into team-a lands in the walking
			// object's own namespace, which is free.
			Grants: &fakeGrants{grants: []netboxv1alpha1.NetBoxRefGrant{catalogueGrant("catalogue")}},
		}

		err := resolver.Check(context.Background(), start, regionDescriptor())

		want := []string{
			"netboxregion/team-a/a", "netboxregion/team-a/b",
			"netboxregion/catalogue/x", "netboxregion/team-a/a",
		}
		assertCycle(t, err, ErrRefCycle, strings.Join(want, " -> "), want)
	})

	t.Run("no grant reader at all fails closed and loudly", func(t *testing.T) {
		resolver := &Resolver{Objects: &fakeReader{objects: graph}, Kinds: regionKinds()}

		err := resolver.Check(context.Background(), start, regionDescriptor())

		// A wiring bug in the operator rather than a verdict about the manifest, exactly as
		// resolution treats it: an error the engine backs off and logs, not a silent allow and
		// not a denial somebody would go and write grants for.
		if !errors.Is(err, ErrNoGrantReader) {
			t.Fatalf("Check() = %v, want %v", err, ErrNoGrantReader)
		}

		var refErr *Error
		if errors.As(err, &refErr) {
			t.Errorf("Check() = %v, want a plain failure rather than a blocked reference", err)
		}
	})
}

// TestCheckAuthorisesNothingInsideOneNamespace keeps the common case free. Every object in the
// cluster with a reference pays for this walk on every reconcile, and a grant LIST per edge
// would put one on the hot path of almost all of them.
func TestCheckAuthorisesNothingInsideOneNamespace(t *testing.T) {
	graph := []target{
		regionCR("team-a", "a", parentRef("b")),
		regionCR("team-a", "b", parentRef("a")),
	}

	grants := &fakeGrants{}
	resolver := &Resolver{Objects: &fakeReader{objects: graph}, Kinds: regionKinds(), Grants: grants}

	err := resolver.Check(context.Background(), graph[0].object(), regionDescriptor())

	// A cycle wholly inside the referrer's own namespace still reports its whole path.
	want := []string{"netboxregion/team-a/a", "netboxregion/team-a/b", "netboxregion/team-a/a"}
	assertCycle(t, err, ErrRefCycle, strings.Join(want, " -> "), want)

	if grants.lists != 0 || grants.nsReads != 0 {
		t.Errorf("grant lists = %d, namespace reads = %d, want none: a same-namespace walk authorises nothing",
			grants.lists, grants.nsReads)
	}
}

// BenchmarkCheck is the cost of the guard on the path where it finds nothing, which is every
// reconcile of every healthy object: a chain as long as the walk will follow.
func BenchmarkCheck(b *testing.B) {
	graph := chain("team-a", MaxRefDepth)
	resolver := &Resolver{
		Objects: &fakeReader{objects: graph},
		Kinds:   fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{regionGVK: regionDescriptor()}},
	}

	obj := graph[0].object()
	d := regionDescriptor()

	for b.Loop() {
		if err := resolver.Check(context.Background(), obj, d); err != nil {
			b.Fatalf("Check() = %v", err)
		}
	}
}

// checkGraph runs the check on one object of a graph, from that object's own perspective.
func checkGraph(
	t *testing.T, reader *fakeReader, kinds fakeDescriptors, graph []target, start types.NamespacedName,
) error {
	t.Helper()

	if len(kinds.byGVK) == 0 {
		kinds = regionKinds()
	}

	for _, candidate := range graph {
		if candidate.namespace != start.Namespace || candidate.name != start.Name {
			continue
		}

		d, ok := kinds.Get(candidate.gvk)
		if !ok {
			t.Fatalf("the test graph starts at a %s, which has no descriptor", candidate.gvk.Kind)
		}

		resolver := &Resolver{Objects: reader, Kinds: kinds, Grants: openGrants(graph)}

		return resolver.Check(context.Background(), candidate.object(), d)
	}

	t.Fatalf("the test graph holds no object %s", start)

	return nil
}

// openGrants is a cluster where every namespace in the graph is referenceable by every other,
// which is what the fixtures assumed before the walk authorised the edges it follows: a graph
// that crosses a namespace now needs the grant a real cluster would need (NBO-092). The rows
// that stay inside one namespace never read it.
func openGrants(graph []target) *fakeGrants {
	open := &fakeGrants{}

	seen := map[string]bool{}
	for _, candidate := range graph {
		if seen[candidate.namespace] {
			continue
		}

		seen[candidate.namespace] = true
		open.grants = append(open.grants, catalogueGrant(candidate.namespace))
	}

	return open
}

// assertCycle checks the verdict, the message and the path: the classification is what tooling
// keys on, and the path is what a human acts on.
func assertCycle(t *testing.T, err, cause error, message string, path []string) {
	t.Helper()

	if !errors.Is(err, cause) {
		t.Fatalf("Check() = %v, want %v", err, cause)
	}

	var refErr *Error
	if !errors.As(err, &refErr) {
		t.Fatalf("Check() = %v, want an *Error errors.As can recover", err)
	}

	if message != "" && !strings.Contains(err.Error(), message) {
		t.Errorf("Check() = %q, want it to contain %q", err.Error(), message)
	}

	if path == nil {
		return
	}

	rendered := make([]string, 0, len(refErr.Path))
	for _, node := range refErr.Path {
		rendered = append(rendered, node.String())
	}

	if !reflect.DeepEqual(rendered, path) {
		t.Errorf("Error.Path = %v, want %v", rendered, path)
	}
}

// regionDescriptor is dcim.Region's shape, which is the shape a cycle needs: a self-referential
// `parentRef` that a natural-key candidate matches on, so the object cannot be created until
// it resolves (internal/registry/dcim_region.go).
func regionDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK: regionGVK, Endpoint: "dcim/regions", ObjectType: "dcim.region",
		Fields: []registry.Field{
			{Spec: "name", API: "name"},
			{Spec: "parentRef", API: "parent", Class: registry.ClassRefOne, Target: regionGVK},
		},
		NaturalKeys: []registry.NaturalKey{
			{Fields: []registry.KeyField{
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			}},
			{
				Fields:     []registry.KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []registry.NullField{{Filter: "parent_id", Spec: "parentRef", Column: registry.NullColumnRef}},
			},
		},
	}
}

// forkDescriptor is a kind with two blocking references, for the shapes one reference cannot
// make: a diamond, and a graph that is broad rather than deep.
func forkDescriptor() registry.Descriptor {
	d := regionDescriptor()
	d.Fields = append(d.Fields,
		registry.Field{Spec: "altRef", API: "alt", Class: registry.ClassRefOne, Target: regionGVK})

	return d
}

// siteRefsRegion is a referrer of a different Kind pointing at the region graph, for the rows
// about two Kinds sharing a name.
func siteRefsRegion() registry.Descriptor {
	return registry.Descriptor{
		GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site",
		Fields: []registry.Field{{Spec: "name", API: "name"}, regionField()},
	}
}

// regionKinds is the descriptor source for the region graph.
func regionKinds() fakeDescriptors {
	return fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
		regionGVK: regionDescriptor(),
	}}
}

// forkKinds is the descriptor source for the two-reference graph.
func forkKinds() fakeDescriptors {
	return fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
		regionGVK: forkDescriptor(),
	}}
}

// regionCR is one NetBoxRegion as the cache holds it: a spec with a name and, when it has one,
// a parent reference written in whichever mode the row is about.
func regionCR(namespace, name string, parent map[string]any) target {
	spec := map[string]any{"name": name}
	if parent != nil {
		spec["parentRef"] = parent
	}

	return target{gvk: regionGVK, namespace: namespace, name: name, spec: spec}
}

// forkCR is one object with two references, in the tests' own namespace: the shapes it
// builds -- a diamond, and a graph that is broad rather than deep -- are about the number of
// edges rather than about where they cross to.
func forkCR(name string, parent, alt map[string]any) target {
	cr := regionCR("team-a", name, parent)
	if alt != nil {
		cr.spec["altRef"] = alt
	}

	return cr
}

// siteCR is one NetBoxSite, for the rows that need a second Kind.
func siteCR(namespace, name string, spec map[string]any) target {
	return target{gvk: siteGVK, namespace: namespace, name: name, spec: spec}
}

// parentRef is a `name`-mode reference in the referring object's own namespace.
func parentRef(name string) map[string]any {
	return map[string]any{"name": name}
}

// parentRefIn is a `name`-mode reference that names its namespace.
func parentRefIn(namespace, name string) map[string]any {
	return map[string]any{"name": name, "namespace": namespace}
}

// altRef is the second reference on a forkCR, in the same shape.
func altRef(name string) map[string]any { return parentRef(name) }

// chain returns `r0 -> r1 -> ... -> rN`: hops references long, and acyclic.
func chain(namespace string, hops int) []target {
	graph := make([]target, 0, hops+1)

	for i := range hops + 1 {
		if i == hops {
			graph = append(graph, regionCR(namespace, fmt.Sprintf("r%d", i), nil))

			continue
		}

		graph = append(graph,
			regionCR(namespace, fmt.Sprintf("r%d", i), parentRef(fmt.Sprintf("r%d", i+1))))
	}

	return graph
}
