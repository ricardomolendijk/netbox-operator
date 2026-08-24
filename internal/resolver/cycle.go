package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// MaxRefDepth is how many blocking references the walk follows away from one object before
// it refuses to follow any more. A chain of exactly MaxRefDepth references is walked to its
// end and converges; the reference after that is not followed and the object reports
// ErrRefDepthExceeded instead.
//
// 32 is an order of magnitude above anything the schema produces. The self-referential MPTT
// trees are the only structures that nest at all -- dcim.Region, dcim.SiteGroup,
// dcim.Location, tenancy.TenantGroup (docs/netbox-schema.md, `ForeignKey -> self`) -- and a
// geographic or organisational hierarchy 32 levels deep is a mistake rather than a model.
// Every other chain is a handful of hops: a Device's site's region's parent is four.
//
// The alternative to a cap is an unbounded walk on a graph a manifest author controls, which
// is a reconcile that never ends. The alternative to *reporting* the cap is treating a long
// chain as a cycle, which sends a user hunting for one that does not exist -- so it is its
// own error and its own condition reason.
const MaxRefDepth = 32

// maxRefVisits bounds the objects one walk reads, which bounds the read amplification the
// check adds to a reconcile.
//
// The depth cap alone does not bound it: depth limits the length of a path and not the
// breadth of a graph, and a kind declaring several blocking references branches. A walk
// visits each object at most once, so this is a cap on distinct objects and therefore on
// cache reads: at most 256 per reconcile, and one or two in the shape a manifest usually
// has.
//
// Exceeding it is reported as ErrRefDepthExceeded rather than as "no cycle". The honest
// answer at that point is that the graph was too large to prove anything about, and
// answering "no cycle" would be a guess dressed as a verdict.
const maxRefVisits = 256

// RefNode is one object in the reference graph: a Kind and a namespaced name.
//
// Both halves are load-bearing. The Kind, because two Kinds may hold objects of the same
// name and a graph keyed on the name alone reports cycles that are not there. The
// namespace, because every Kind is namespaced (docs/decisions/0002-crd-scoping.md) and a
// team namespace pointing at a shared catalogue is the ordinary shape, so a cycle that
// crosses a namespace is on the normal path rather than exotic.
type RefNode struct {
	// GVK is the Kind of the object.
	GVK schema.GroupVersionKind

	// Key is its namespace and name.
	Key types.NamespacedName
}

// String renders the node the way every other reference message renders a target:
// `netboxregion/team-a/emea`.
func (n RefNode) String() string {
	return strings.ToLower(n.GVK.Kind) + "/" + n.Key.Namespace + "/" + n.Key.Name
}

// RefPath is an ordered walk through the reference graph, and for a cycle it starts and ends
// at the object reporting it.
//
// Each participant reports the cycle from its own perspective, so the path a user reads
// begins at the object they were looking at. A cycle rendered from an arbitrary "winner"
// would make a reader check whether their object is even in it.
type RefPath []RefNode

// String renders the path as `netboxregion/team-a/a -> netboxregion/team-a/b ->
// netboxregion/team-a/a`, and a one-hop cycle as `netboxregion/team-a/a -> itself`.
//
// The self-reference gets its own spelling because it is the mistake people actually make --
// `parentRef` naming the object it is written on -- and `a -> a` reads like a rendering bug
// rather than like an answer.
func (p RefPath) String() string {
	if len(p) == 2 && p[0] == p[1] {
		return p[0].String() + " -> itself"
	}

	rendered := make([]string, 0, len(p))
	for _, node := range p {
		rendered = append(rendered, node.String())
	}

	return strings.Join(rendered, " -> ")
}

// Check walks the blocking-reference graph away from obj and reports a cycle that comes back
// to it.
//
// Reads only, over the informer cache, and never NetBox: a cycle is a fact about Kubernetes
// objects, so the check cannot be rate-limited, cannot fail because NetBox is down, and
// costs nothing that a reconcile was not going to pay anyway. It is safe to call before any
// decision has been made, which is where ResolveAll calls it and where NBO-044's admission
// webhook will.
//
// An edge into a namespace this object has no NetBoxRefGrant into is not followed and not
// reported past -- the walk uses the same grant check resolution does, so the path it prints
// can only name objects the reader was permitted to reference (see follow).
//
// Only `name`-mode references are followed, and this is what bounds the whole problem: a
// `slug`, a `lookup` or an `id` terminates in NetBox, where there is no CR to wait for and
// therefore no Kubernetes deadlock to be in. A cycle needs every edge to be a CR waiting on
// another CR.
//
// The walk reads the cache, so it can miss a cycle that was created a moment ago. That is
// deliberate: the next reconcile of any participant catches it, and the cost of being late
// is a delayed condition rather than a wrong write. Nothing is cached between reconciles for
// the same reason -- a stale verdict of "no cycle" is far worse than recomputing a walk that
// is bounded at 256 cache reads.
func (r *Resolver) Check(ctx context.Context, obj client.Object, d registry.Descriptor) error {
	refs, err := refsOf(obj, d)
	if err != nil {
		return err
	}

	start := RefNode{
		GVK: d.GVK,
		Key: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()},
	}

	return r.checkFrom(ctx, start, d, refs)
}

// checkFrom is Check over references the caller has already decoded, so a resolution pass
// reads the spec once rather than once per thing it does with it.
func (r *Resolver) checkFrom(
	ctx context.Context, start RefNode, d registry.Descriptor, refs []fieldRefs,
) error {
	walk := &cycleWalk{
		resolver: r, start: start,
		seen: map[RefNode]bool{start: true}, from: map[RefNode]RefNode{},
	}

	frontier, err := walk.follow(ctx, blockingHops(start, d, refs, nil))
	if err != nil {
		return err
	}

	return walk.run(ctx, frontier)
}

// hop is one edge of the walk: the object it arrives at, where it came from, and the field on
// the object being reconciled that its whole branch left through.
type hop struct {
	// node is the object this edge points at.
	node RefNode

	// from is the object holding the reference.
	from RefNode

	// head is the reference on the *start* object this branch descends from. It is what the
	// report names, because the field a user can edit is the one on the object whose
	// condition they are reading -- naming a field on some object three hops away would be
	// true and useless.
	//
	// One element rather than one field: a to-many reference contributes one edge per element
	// (NBO-088), and the message has to name the element that closed the ring rather than
	// the field it happened to be written under.
	head refElement
}

// cycleWalk is one breadth-first search away from one object.
//
// Breadth-first rather than depth-first, for two reasons that are both about the answer
// rather than the traversal. It finds the *shortest* cycle through the start object, which is
// the one worth putting in a message. And it visits every object at most once while still
// knowing each one's true distance from the start, so pruning a repeat visit can never hide a
// cycle that a deeper path would have found -- which a depth-first walk with a shared visited
// set cannot promise.
type cycleWalk struct {
	resolver *Resolver

	// start is the object being reconciled, and the only object whose revisit is a cycle.
	start RefNode

	// seen is every object already queued, keyed by Kind and namespaced name. Revisiting an
	// object that is not the start is a diamond rather than a cycle -- two references
	// arriving at one object is ordinary -- so the walk prunes there instead of reporting.
	seen map[RefNode]bool

	// from records how each object was reached, so a detected cycle can be rendered as the
	// path it actually is.
	from map[RefNode]RefNode

	// visits counts the objects the walk has taken on, which is what bounds the reads it
	// makes, against maxRefVisits.
	visits int
}

// run walks the frontier level by level until it comes back to the start, runs out of graph,
// or hits a limit.
func (w *cycleWalk) run(ctx context.Context, frontier []hop) error {
	for depth := 1; len(frontier) > 0; depth++ {
		next := make([]hop, 0, len(frontier))

		for _, edge := range frontier {
			if edge.node == w.start {
				return w.cycle(edge)
			}

			if w.seen[edge.node] {
				continue
			}

			w.seen[edge.node] = true
			w.from[edge.node] = edge.from
			w.visits++

			if w.visits > maxRefVisits {
				return w.tooLarge(edge)
			}

			children, err := w.expand(ctx, edge)
			if err != nil {
				return err
			}

			if len(children) > 0 && depth == MaxRefDepth {
				return w.tooDeep(edge, children[0])
			}

			next = append(next, children...)
		}

		frontier = next
	}

	return nil
}

// expand reads one object and returns the blocking references it holds.
//
// An object that is not there terminates its branch rather than failing the walk: a chain
// through a missing object is not a cycle, it is a reference to something that does not
// exist, and resolution reports it as exactly that. Reporting a cycle here would send a user
// looking for one that is not in their manifests.
func (w *cycleWalk) expand(ctx context.Context, edge hop) ([]hop, error) {
	// A Kind with no Descriptor, like a `slug` reference, is outside the CR graph: there is
	// nothing for it to wait on in Kubernetes, so it cannot be part of a deadlock here.
	target, known := w.resolver.kinds().Get(edge.node.GVK)
	if !known {
		return nil, nil
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(edge.node.GVK)

	if err := w.resolver.Objects.Get(ctx, edge.node.Key, live); err != nil {
		var noMatch *apimeta.NoKindMatchError

		switch {
		case apierrors.IsNotFound(err):
			return nil, nil
		case errors.As(err, &noMatch), runtime.IsNotRegisteredError(err):
			// The CRD is not installed, so no object of that Kind can exist to wait on.
			return nil, nil
		default:
			return nil, fmt.Errorf("reading %s while checking it for a reference cycle: %w",
				edge.node, err)
		}
	}

	refs, err := refsOf(live, target)
	if err != nil {
		return nil, err
	}

	return w.follow(ctx, blockingHops(edge.node, target, refs, &edge.head))
}

// follow keeps the edges this walk is allowed to follow and drops the rest, so that every hop
// the walk ever holds has been authorised.
//
// The one choke point, and it is why it filters edges rather than checking them where they are
// used: an unauthorised object must not be followed *or* named, and the walk names an object in
// three places -- the cycle path, the chain beyond the depth cap, and the object the visit cap
// stopped at. Filtering here is what makes all three safe at once.
//
// Without it the walk was an existence oracle for a namespace the referrer has no grant into.
// It reads CRs directly rather than through authorise(), so a namespace could close a ring
// through an object it may not reference and read that object's name back out of its own
// condition message -- precisely what authorise()'s ordering exists to prevent, reached by
// another path (NBO-092). An unauthorised edge is now a terminus: the cycle through it is not
// reported as a cycle, and the reference across it is still denied as RefDenied, which is the
// half the reader can act on.
//
// Authorised from the perspective of the object being reconciled, because that object's
// condition is what carries the path. Its own edges are therefore checked exactly as
// resolution checks them, and no node the walk reports is one the reader could not have
// referenced itself.
func (w *cycleWalk) follow(ctx context.Context, edges []hop) ([]hop, error) {
	kept := make([]hop, 0, len(edges))

	for _, edge := range edges {
		permitted, _, err := w.resolver.permits(
			ctx, w.start.Key.Namespace, edge.node.GVK.Kind, edge.node.Key)
		if err != nil {
			return nil, fmt.Errorf("authorising %s -> %s while checking for a reference cycle: %w",
				edge.from, edge.node, err)
		}

		if !permitted {
			continue
		}

		kept = append(kept, edge)
	}

	return kept, nil
}

// blockingHops turns one object's references into the edges the walk follows: the blocking
// ones, in descriptor order so the shortest cycle found is also the first one written down.
//
// head is the branch's field on the start object, and nil for the start object itself, whose
// references are each the head of their own branch.
func blockingHops(
	from RefNode, d registry.Descriptor, refs []fieldRefs, head *refElement,
) []hop {
	edges := make([]hop, 0, len(refs))

	for _, declared := range refs {
		// One edge per element, so a ring closed by the third tag in a list is found and
		// reported as that tag rather than as the field. A to-many reference is as capable of
		// deadlocking a graph as a to-one, and the walk prunes on the node it arrives at, so
		// the extra breadth costs nothing beyond the objects it would have read anyway.
		for _, element := range declared.elements() {
			if !blocking(d, element) {
				continue
			}

			branch := element
			if head != nil {
				branch = *head
			}

			edges = append(edges, hop{node: targetNode(from, element), from: from, head: branch})
		}
	}

	return edges
}

// blocking reports whether the walk follows this reference, which is the specification that
// makes the check correct rather than merely plausible: follow an edge if and only if the
// referring object cannot be created until that edge resolves.
//
// Everything else about the rule follows from that sentence:
//
//   - Only `name` mode. The other three modes resolve against NetBox, which holds no CR that
//     could be waiting for this one.
//   - A DeferAlways field is never followed. It is deferred precisely because it cannot be
//     resolvable at create time -- a Device's `primary_ip4` needs an address that needs an
//     interface that needs the Device -- and NBO-015's second PATCH is what breaks that
//     cycle by design. Reporting it would make the deferred design unusable.
//   - A DeferIfUnresolved field is followed only when a natural-key candidate matches on it.
//     Then the engine refuses to create the object without it (NaturalKey.Applicable: a
//     Region whose parent is declared and unresolved matches no candidate and waits), so it
//     genuinely blocks. A `lag` that no candidate names does not: the object is created and
//     the field is PATCHed in later.
func blocking(d registry.Descriptor, ref refElement) bool {
	if modeOf(ref.ref) != ModeName {
		return false
	}

	mode, deferred := deferModeOf(d, ref.field.API)
	if !deferred {
		return true
	}

	return mode == registry.DeferIfUnresolved && inNaturalKey(d, ref.field.Spec)
}

// deferModeOf reports how a NetBox field is deferred, if it is. Keyed on the API name
// because that is the spelling Descriptor.Deferred uses.
func deferModeOf(d registry.Descriptor, apiField string) (registry.DeferMode, bool) {
	for _, deferred := range d.Deferred {
		if deferred.APIField == apiField {
			return deferred.Mode, true
		}
	}

	return "", false
}

// inNaturalKey reports whether any lookup candidate matches on this spec field.
//
// Only the value-matched fields count. A candidate that pins the same filter to null asserts
// the field was never declared, so it says nothing about waiting for one that was.
func inNaturalKey(d registry.Descriptor, spec string) bool {
	for _, candidate := range d.NaturalKeys {
		for _, field := range candidate.Fields {
			if field.Spec == spec {
				return true
			}
		}
	}

	return false
}

// targetNode is the object a reference points at, in the namespace it resolves in: the one it
// names, or the referring object's own.
func targetNode(from RefNode, ref refElement) RefNode {
	namespace := ref.ref.Namespace
	if namespace == "" {
		namespace = from.Key.Namespace
	}

	return RefNode{
		GVK: ref.field.Target,
		Key: types.NamespacedName{Namespace: namespace, Name: ref.ref.Name},
	}
}

// cycle is the report: the path from the start object back to itself.
func (w *cycleWalk) cycle(edge hop) error {
	path := append(w.pathTo(edge.from), w.start)

	return w.blocked(ErrRefCycle, edge.head, path, path.String())
}

// tooDeep is the report for a chain that keeps going past MaxRefDepth.
//
// It fires on the edge leaving the last object the walk was willing to read, so a chain that
// merely *ends* at the limit converges: the limit is on references followed, not on
// references declared. Only the two ends of the chain are named in the message -- the middle
// of a 32-hop chain is not what anybody edits -- while Path carries all of it for a caller
// that wants the evidence.
func (w *cycleWalk) tooDeep(edge, beyond hop) error {
	path := append(w.pathTo(edge.node), beyond.node)

	return w.blocked(ErrRefDepthExceeded, edge.head, path,
		fmt.Sprintf("the chain of blocking references from %s is more than %d deep (%s -> ... -> %s)",
			w.start, MaxRefDepth, w.start, beyond.node))
}

// tooLarge is the report for a graph with more objects in it than the walk will read.
//
// Reported rather than passed over in silence: the walk stopped without proving anything, and
// "no cycle" would be a guess. It shares ErrRefDepthExceeded because it is the same answer to
// the same question -- the reference graph around this object is too big to reason about, and
// the fix is to make it smaller.
func (w *cycleWalk) tooLarge(edge hop) error {
	path := w.pathTo(edge.node)

	return w.blocked(ErrRefDepthExceeded, edge.head, path,
		fmt.Sprintf("the blocking references reachable from %s span more than %d objects, "+
			"so the search for a cycle was stopped at %s", w.start, maxRefVisits, edge.node))
}

// blocked builds the typed error, reported against the reference on the object being
// reconciled rather than against whichever edge the walk happened to be on.
func (w *cycleWalk) blocked(cause error, head refElement, path RefPath, detail string) *Error {
	return &Error{
		Cause: cause, Field: head.field.Spec, Ref: head.ref, Mode: ModeName,
		TargetGVK: head.field.Target, Target: targetNode(w.start, head).Key,
		Path: path, Detail: detail,
	}
}

// pathTo reconstructs the walk from the start object to node, in order.
func (w *cycleWalk) pathTo(node RefNode) RefPath {
	reversed := RefPath{node}

	for node != w.start {
		parent, reached := w.from[node]
		if !reached {
			// Unreachable: every node the walk queued was recorded on the way in. Broken out
			// of rather than followed anyway, because the cost of being wrong about that is a
			// reconcile that never returns, and a truncated path is a worse message rather
			// than a hung controller.
			break
		}

		node = parent
		reversed = append(reversed, node)
	}

	path := make(RefPath, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}

	return path
}
