// Package reconciler is the only place a create, adopt, update or delete decision is made.
//
// One Engine drives every kind. Everything that differs between kinds arrives as data on
// a registry.Descriptor, so there is no branch on Kind anywhere below and adding a kind is
// three new files and no edit here (CONTRIBUTING.md, "Extensibility"). Where the engine
// cannot act it says so in a condition and requeues; it returns an error only for a
// programming mistake, never for anything about NetBox's availability, because a returned
// error means backoff and backoff on a normal waiting state is minutes of latency for
// nothing.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// DefaultResync is the requeue interval used when an endpoint declares none. It matches
// NetBoxEndpoint's own default, so drift is re-checked on the same cadence either way.
const DefaultResync = 10 * time.Minute

// Reader locates NetBox objects. Split from Writer so a test can supply a client that
// physically cannot write, which is a stronger assertion than counting calls.
type Reader interface {
	// GetByID fetches one object by its NetBox id. A missing object is a
	// *netbox.NotFoundError, which is how the engine learns that status.id is stale.
	GetByID(ctx context.Context, endpoint string, id int) (netbox.Object, error)

	// GetOne returns the one object matching params, nil when nothing matches, and a
	// *netbox.AmbiguousError when several do.
	//
	// The engine asks for one rather than listing and counting for itself. The Conflict it
	// reports has to name every matching NetBox id, and since NBO-074 the client's error
	// carries them -- so a second decision here about when a lookup is ambiguous would only
	// be one that can disagree with the client's. See docs/concepts/engine.md.
	GetOne(ctx context.Context, endpoint string, params netbox.Params) (netbox.Object, error)
}

// Writer mutates NetBox. A DryRun client implements every method by returning a suppressed
// Object instead of sending anything, which the engine detects with netbox.Suppressed.
type Writer interface {
	Create(ctx context.Context, endpoint string, payload netbox.Object) (netbox.Object, error)
	Patch(ctx context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error)

	// Delete removes one object by id. Used by the recreate strategy, for a kind whose
	// identity lives somewhere a PATCH cannot reach, and by the finalizer when a CR goes
	// away.
	//
	// A refused delete is a *netbox.ProtectedError and a missing one a
	// *netbox.NotFoundError; both are ordinary answers rather than failures. A real delete
	// returns a nil Object; a DryRun client returns a suppressed one instead of sending
	// anything, so the engine detects a suppressed delete the same way it detects a
	// suppressed create.
	Delete(ctx context.Context, endpoint string, id int) (netbox.Object, error)
}

// NetBoxClient is what one endpoint hands the engine: both halves of the API it needs.
type NetBoxClient interface {
	Reader
	Writer
}

// Endpoint is the usable part of one NetBoxEndpoint. A struct rather than the CR, so the
// engine neither reads nor caches Kubernetes objects it does not own.
type Endpoint struct {
	// Client talks to this NetBox.
	Client NetBoxClient

	// Resync is how often to re-check for drift with no CR change. Zero means
	// DefaultResync.
	Resync time.Duration

	// DriftMode is what this endpoint does about drift. The empty value means
	// DriftCorrect, so an endpoint stored before the field existed behaves as the CRD
	// default says it should.
	//
	// The engine reads it to decide what to *say* and when to come back, never to decide
	// whether it may write: Report mode is enforced by handing the engine a client that
	// physically cannot mutate NetBox, because a mode that is one forgotten `if` away
	// from writing is not a mode anyone can trust
	// (docs/decisions/0005-gitops-coexistence.md).
	DriftMode netboxv1alpha1.DriftMode

	// Allocator is the advisory-locked allocation half of this endpoint's client.
	//
	// A field of its own rather than a method on Client, because adding a method to
	// NetBoxClient would make every fake in every test implement an allocation call it has
	// no business having -- and because an endpoint that cannot allocate is a state worth
	// being able to represent. Nil for one; the allocation engine reports it as the wiring
	// bug it is rather than dereferencing it (claim.go).
	//
	// The declarative engine never reads it. Allocation is not a mode of a create.
	Allocator Allocator

	// Provenance is the stamp this endpoint's bootstrap resolved: the tag id and the
	// custom-field names that provably exist in NetBox (NBO-075). The zero value stamps
	// nothing, which is what an endpoint with no spec.managedBy hands over.
	//
	// Resolved by the endpoint controller rather than by the engine because the tag id can
	// only be learned from NetBox, and learning it once per endpoint is the difference
	// between two extra requests per reconcile and two per resync period.
	Provenance provenance.Stamp
}

// Endpoints hands out the client for one NetBoxEndpoint by namespace and name. A miss
// means the endpoint is not Ready, which is a wait rather than a failure.
//
// The context is the reconcile's own: an implementation reads Kubernetes objects to answer,
// so it needs the cancellation the reconcile worker is subject to and the request-scoped
// logger everything else on this path logs through (CONTRIBUTING.md, "Logging"). Today's
// implementation answers from an informer cache and cannot block, but a signature that
// cannot be cancelled is one that makes a blocking read unnoticeable: the controller runs a
// single worker by default, so one uncancellable read stalls every object of the kind
// (NBO-080).
//
// Still (Endpoint, bool) rather than (Endpoint, error), because the engine has exactly two
// things it can do -- use the endpoint, or wait for it -- and a third return it cannot act
// on differently is a wider seam for no behaviour. An implementation that could not find
// out says so in the log, with the context this now carries.
type Endpoints interface {
	Endpoint(ctx context.Context, namespace, name string) (Endpoint, bool)
}

// Descriptors is where per-kind facts come from.
type Descriptors interface {
	Get(gvk schema.GroupVersionKind) (registry.Descriptor, bool)
}

// StatusWriter persists an object's status subresource. Narrowed to the one call the
// engine makes, which is also the structural guarantee that it cannot write a spec
// (docs/decisions/0005-gitops-coexistence.md).
type StatusWriter interface {
	UpdateStatus(ctx context.Context, obj client.Object) error
}

// Recorder emits Kubernetes Events, narrowed from record.EventRecorder so the engine is
// testable without an event broadcaster.
type Recorder interface {
	Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any)
}

// Object is the CR the engine reconciles: any kind that embeds the shared envelope. The
// two accessors are all the per-kind code a generated kind needs, because everything else
// the engine reads comes from the object's JSON form and from its Descriptor.
type Object interface {
	client.Object

	// NetBoxSpec returns the engine-owned part of the spec.
	NetBoxSpec() *netboxv1alpha1.NetBoxObjectSpec

	// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
	NetBoxStatus() *netboxv1alpha1.NetBoxObjectStatus
}

// Engine reconciles one object of any registered kind.
type Engine struct {
	// Descriptors resolves a GVK to its per-kind facts. Nil means the package-level
	// registry.
	Descriptors Descriptors

	// Endpoints resolves a spec.endpointRef to a client.
	Endpoints Endpoints

	// Refs turns the references in a spec into NetBox ids. Nil resolves nothing: every
	// declared reference is then reported unresolved and left out of the payload, which is
	// what the engine did before internal/resolver existed.
	Refs RefResolver

	// Status persists status updates.
	Status StatusWriter

	// Finalizers persists the finalizer that keeps a CR alive until its NetBox object has
	// been dealt with.
	Finalizers FinalizerWriter

	// Owners persists the containment owner reference of ADR-0003 rule 4. Nil is a wiring
	// bug rather than a mode: a kind whose descriptor names a ContainmentRef then fails its
	// reconcile loudly, instead of silently never cascading (owners.go).
	Owners OwnerWriter

	// Children creates, updates and prunes the child CRs a parent's inline lists declare
	// (ADR-0003 rule 5, children.go). Nil is a wiring bug rather than a mode, for the same
	// reason a nil Owners is: a kind that implements InlineParent then reports it on the
	// object instead of quietly materialising nothing.
	Children ChildWriter

	// GitOps is the annotation set every materialised child carries so a GitOps tool does
	// not treat it as drift. Nil means DefaultGitOps -- Argo CD on, Flux off -- so an engine
	// wired without an opinion gets the documented default rather than a silent nothing.
	GitOps *GitOps

	// Events records what changed in NetBox. Optional.
	Events Recorder

	// Scheme derives an object's GVK, and is the reason the engine cannot be handed the
	// wrong descriptor for an object: the GVK comes from the object rather than from
	// whoever wired the controller.
	Scheme *runtime.Scheme
}

// Reconcile brings one object's NetBox counterpart in line with its spec.
func (e *Engine) Reconcile(ctx context.Context, obj Object) (ctrl.Result, error) {
	descriptor, err := e.descriptorFor(obj)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = logf.IntoContext(ctx, logf.FromContext(ctx).WithValues(
		"kind", descriptor.GVK.Kind, "namespace", obj.GetNamespace(), "name", obj.GetName(),
		"endpoint", descriptor.Endpoint))

	// The initial result is ResultError because a path that returns without deciding an
	// outcome is a bug, and it should look like one on the dashboard rather than like a
	// success.
	p := &pass{
		engine: e, obj: obj, before: obj.NetBoxStatus().DeepCopy(),
		desc: descriptor, result: metrics.ResultError,
	}

	// Deferred, so every exit lands in exactly one bucket and reconcile_total is a count
	// of reconciles rather than a count of the paths somebody remembered to instrument.
	started := time.Now()
	defer func() { metrics.ObserveReconcile(descriptor.GVK.Kind, p.result, time.Since(started)) }()

	if !obj.GetDeletionTimestamp().IsZero() {
		return p.deleting(ctx)
	}

	// Before the endpoint is even resolved, so that there is no ordering in which the
	// engine writes to NetBox without a durable finalizer already behind it. See claim.
	if err := e.claim(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}

	endpoint, ok := e.Endpoints.Endpoint(ctx, obj.GetNamespace(), obj.NetBoxSpec().EndpointRef)
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: netboxendpoint %q in namespace %q",
			errEndpointNotReady, obj.NetBoxSpec().EndpointRef, obj.GetNamespace()))
	}
	p.endpoint = endpoint

	if err := p.build(ctx); err != nil {
		return p.stop(ctx, err)
	}

	// After build, because the key is read from the decoded spec; before anything reaches
	// NetBox, because the whole point is that nothing does. A refused object never locates,
	// never creates and never deletes: its status.id stays zero, so deleting the CR releases
	// the finalizer without a DELETE (finalizer.go, releaseWithoutDeleting).
	if err := p.reserved(); err != nil {
		return p.stop(ctx, err)
	}

	// After build, because the parent's namespace, Kind and uid come out of reference
	// resolution; before the NetBox write, so that an object whose lifecycle depends on
	// another is marked as such before anything exists for it to leak. A failure here is a
	// returned error and stops the pass, exactly as a failed finalizer write does in claim():
	// both are metadata the engine has to get onto the object before it goes further, and
	// both are fixed by retrying rather than by a condition.
	if err := p.ownParent(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// A reference the spec declares is a precondition for the write (issue #195). Before
	// locate rather than before create, because the rule is about the update too: an object
	// that already exists must not be PATCHed towards a payload a declared reference is
	// missing from. After ownParent, because the owner reference is metadata on the CR rather
	// than a NetBox write, and a child whose parent resolved should be collectable with it
	// even while it waits on some other reference.
	if blocked := p.blockedRefs(); len(blocked) > 0 {
		return p.waitForRefs(ctx, blocked)
	}

	found, err := p.locate(ctx)
	if err != nil {
		return p.stop(ctx, err)
	}

	if found.live == nil {
		return p.create(ctx)
	}

	return p.claim(ctx, found)
}

// descriptorFor resolves the descriptor for obj's kind. A missing one is a returned error
// rather than a condition: a controller running for an unregistered kind is a wiring bug
// that no requeue will fix, and registry validation at boot exists to catch it earlier.
func (e *Engine) descriptorFor(obj Object) (registry.Descriptor, error) {
	gvk, err := apiutil.GVKForObject(obj, e.Scheme)
	if err != nil {
		return registry.Descriptor{}, fmt.Errorf("resolving the group-version-kind of %T: %w", obj, err)
	}

	descriptors := Descriptors(registryLookup{})
	if e.Descriptors != nil {
		descriptors = e.Descriptors
	}

	descriptor, ok := descriptors.Get(gvk)
	if !ok {
		return registry.Descriptor{}, fmt.Errorf("no descriptor is registered for %s", gvk)
	}

	return descriptor, nil
}

// registryLookup is the package-level registry as a Descriptors.
type registryLookup struct{}

// Get returns the descriptor registered for gvk.
func (registryLookup) Get(gvk schema.GroupVersionKind) (registry.Descriptor, bool) {
	return registry.Get(gvk)
}

// pass is one reconcile of one object: the inputs every step shares, so each step is a
// named method with a receiver rather than a paragraph with seven parameters.
type pass struct {
	engine   *Engine
	obj      Object
	before   *netboxv1alpha1.NetBoxObjectStatus
	desc     registry.Descriptor
	endpoint Endpoint

	// spec is the object's spec as JSON names to values, which is how the engine reads a
	// spec it knows nothing about.
	spec specFields

	// desired is the payload built from the spec, minus what must not be written.
	desired netbox.Object

	// state is which spec fields are set, and which hold a value a filter can use.
	state registry.SpecState

	// refs is what the references that did not resolve mean for this object. Zero when
	// every reference resolved, which is what ready() checks before reporting Ready=True.
	refs refWait

	// containment is the resolved containment reference -- the one spec field whose target
	// gets an owner reference. Empty when the descriptor names none, or when the spec left
	// it unset, or when it did not resolve; ownParent treats all three the same way.
	containment resolver.FieldRefs

	// deferred is what this pass does about the descriptor's deferred fields: which are
	// kept out of the create payload, and which NetBox does not hold yet.
	deferred deferral

	// live is the NetBox object this pass located, or nil when nothing was located --
	// including a create, whose object did not exist when the pass began. Read by the
	// deferred report, which has to distinguish "NetBox already holds this" from "NetBox
	// holds nothing to hold it in".
	live netbox.Object

	// result is this pass's outcome, one of the metrics.Result* values. Written by
	// whichever step decided, read once by the deferred observation in Reconcile.
	result string
}

// build renders the spec into a payload and turns its references into ids.
func (p *pass) build(ctx context.Context) error {
	spec, err := specOf(p.obj)
	if err != nil {
		return err
	}

	// Before the payload is built: a field the user explicitly emptied is missing from the
	// marshalled spec, and putting it back is what lets an empty value clear a NetBox one
	// (NBO-079). Everything downstream then sees an ordinary value.
	owned := ownershipOf(p.obj)
	p.reportUntrackedOwnership(ctx, owned)
	spec.restoreEmpty(p.obj, owned)

	// After restoreEmpty, which would otherwise put an emptied inline list back: an inline
	// list is not a NetBox column and must not reach a payload (payload.go).
	spec.dropInlineChildren(p.obj)

	// The opposite direction, and the only window that works for it: a reference the inline
	// sugar *derives* has to be in the map desired() reads, so that it lands in state.Declared
	// and is deferred and resolved exactly as a written one is -- and it must not be in the map
	// restoreEmpty compares against, which is about what the user wrote (NBO-033).
	if err := spec.derive(p.obj); err != nil {
		return err
	}

	desired, state, refs, err := spec.desired(p.desc)
	if err != nil {
		return err
	}
	p.spec, p.desired, p.state = spec, desired, state

	if err := p.resolveRefs(ctx, refs); err != nil {
		return err
	}

	// After resolution, because whether a deferral applies depends on whether its
	// reference became an id -- which is the whole of what DeferIfUnresolved means.
	p.deferred = newDeferral(p.desc, p.state, p.desired)

	return nil
}

// reserved refuses an object whose NetBox counterpart the provenance bootstrap writes.
//
// The one place two writers for one NetBox object could otherwise happen, and the operator
// is both of them: the bootstrap creates the `k8s-managed` tag and the custom fields
// spec.managedBy names before this endpoint reported Ready, derives their `object_types` from
// the descriptor registry and widens it as kinds are added (internal/provenance). A CR for
// one of those is a second writer of the object every stamped object in the cluster depends
// on -- and the CR would win, because it PATCHes on every resync. Narrowing `object_types` on
// `k8s_uid` deletes the stored value from every object of the types removed; deleting the
// definition deletes all of them.
//
// So: refuse, and write nothing. Not "merge", because there is no merge -- the CR's spec is
// the whole desired state for the columns it declares. Not "adopt", because adopting is how
// the CR becomes the writer. Not "exclude the field from the payload", because a CR whose
// `objectTypes` is silently ignored reports itself synced while NetBox holds something else.
//
// Data-driven both ways round, so nothing here is about a Kind: the descriptor says which
// spec field holds a key for its NetBox model (ReservedKeySpec), and the endpoint says which
// keys are reserved for that model (provenance.Config.Reserved). A kind with no
// ReservedKeySpec, an endpoint with no spec.managedBy, and an endpoint whose field names were
// changed all fall out of the same two lookups.
func (p *pass) reserved() error {
	if p.desc.ReservedKeySpec == "" {
		return nil
	}

	// A non-string, or the empty string, is not a key that can collide. The spec field's own
	// validation is what rejects it as a value; this guard is not the place to duplicate it.
	key, ok := p.spec[p.desc.ReservedKeySpec].(string)
	if !ok || key == "" {
		return nil
	}

	if !slices.Contains(p.endpoint.Provenance.Reserved()[p.desc.ObjectType], key) {
		return nil
	}

	return fmt.Errorf("%w: %s %q is configured on netboxendpoint %q as part of spec.managedBy, "+
		"so the operator writes it and this object writes nothing; "+
		"rename it, or switch that provenance field off with the empty string "+
		"(docs/operations/provenance.md)",
		errReservedByProvenance, p.desc.ReservedKeySpec, key, p.obj.NetBoxSpec().EndpointRef)
}

// reportUntrackedOwnership records that this object carries no spec ownership metadata, so
// the pass fell back to reading intent off the Go zero value.
//
// Not silent, because the consequence is silent: on such an object an explicitly-empty
// string, bool or plain number is indistinguishable from an absent one, so clearing it in
// Git changes nothing in NetBox and no condition disagrees. That is the bug NBO-079 fixes,
// and this counter is how an operator finds out it is still happening.
//
// A metric rather than an Info line, because it is true on every reconcile of such an
// object rather than once: a log line here would be one per object per resync forever, and
// CONTRIBUTING.md is explicit that a reconcile which changes nothing logs at debug. The
// debug line carries the diagnosis for whoever turns verbosity up
// (docs/operations/observability.md).
func (p *pass) reportUntrackedOwnership(ctx context.Context, owned specOwnership) {
	if owned.tracked {
		return
	}

	metrics.SpecOwnershipUntracked.WithLabelValues(p.desc.GVK.Kind).Inc()
	logf.FromContext(ctx).V(1).Info("no spec field ownership to read; managing non-zero fields only",
		"action", "fallback", "fieldManager", FieldManager)
}

// match is the live object the engine will act on, and how it was found.
type match struct {
	// live is the NetBox object, or nil when nothing matched.
	live netbox.Object

	// byNaturalKey reports that the object was found by lookup rather than by status.id.
	// Only such an object raises the adoption question: one found by an id this CR
	// recorded is already ours.
	byNaturalKey bool
}

// locate finds the live NetBox object, in the order that makes adoption safe: the id this
// CR already recorded, and only then the natural key.
func (p *pass) locate(ctx context.Context) (match, error) {
	id := int(p.obj.NetBoxStatus().ID)
	if id == 0 {
		return p.lookup(ctx)
	}

	live, err := p.endpoint.Client.GetByID(ctx, p.desc.Endpoint, id)

	var notFound *netbox.NotFoundError
	if err != nil && !errors.As(err, &notFound) {
		return match{}, fmt.Errorf("fetching netbox %s/%d: %w", p.desc.Endpoint, id, err)
	}

	if live != nil {
		return match{live: live}, nil
	}

	// A 404, or a success with no object in the body: gone behind our back. Not an error --
	// clearing the id and falling through to the natural key is how the object gets
	// re-created, or re-adopted if it came back. Treating an empty body as "found" instead
	// would send the engine down the create path and duplicate the object.
	logf.FromContext(ctx).Info("netbox object is gone; clearing status.id",
		"netboxID", id, "action", "recover")
	p.obj.NetBoxStatus().ID, p.obj.NetBoxStatus().Adopted = 0, false

	return p.lookup(ctx)
}

// lookup tries each natural-key candidate in the descriptor's order and stops at the first
// that answers.
//
// No applicable candidate is not the same as no match: it means identity cannot be
// established yet -- a parent that does not resolve, say -- and the engine has to wait.
// Creating there would make a second object, and falling through to a candidate that omits
// the parent would adopt an unrelated top-level object and then reparent it.
func (p *pass) lookup(ctx context.Context) (match, error) {
	candidates := p.desc.Candidates(p.state)
	if len(candidates) == 0 {
		return match{}, errNoCandidate
	}

	for _, candidate := range candidates {
		params, err := p.spec.params(candidate)
		if err != nil {
			return match{}, err
		}

		live, err := p.endpoint.Client.GetOne(ctx, p.desc.Endpoint, params)

		// Recorded before the error is looked at, and even when nothing matched: the first
		// question about an object that was not adopted is what the engine actually looked
		// for, and that is most of the answer when the lookup turned out to be ambiguous.
		p.obj.NetBoxStatus().NaturalKey = params

		// On a kind where NetBox may legitimately hold several objects matching one key,
		// which of them is this CR's is decided by the provenance stamp rather than by the
		// filter (decision #177, duplicate.go). A pass-through for every other kind.
		found, err := p.duplicate(live, err)
		if err != nil {
			return match{}, lookupFailure(p.desc.Endpoint, params, err)
		}

		if found.live != nil {
			return found, nil
		}
	}

	return match{}, nil
}

// lookupFailure wraps a failed natural-key lookup with what it was looking for, except for
// the one failure that is already a complete sentence.
//
// A *netbox.AmbiguousError is returned untouched: it becomes the Conflict condition's
// message verbatim, and it already names the endpoint, the query and every matching id, so
// wrapping it would only say the query twice.
func lookupFailure(endpoint string, params netbox.Params, err error) error {
	var ambiguous *netbox.AmbiguousError
	if errors.As(err, &ambiguous) {
		return err
	}

	return fmt.Errorf("looking up netbox %s by %v: %w", endpoint, params, err)
}

// claim acts on a live object: refuse it, adopt it, or update it.
func (p *pass) claim(ctx context.Context, found match) (ctrl.Result, error) {
	if !found.byNaturalKey {
		return p.update(ctx, found.live)
	}

	id, ok := found.live.ID()
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: matched by %v", errNoObjectID, p.obj.NetBoxStatus().NaturalKey))
	}

	// Adoption is opt-in. Finding an object somebody else made is not permission to take
	// it over, because the very next step reconciles it towards this spec and there is no
	// undo for that.
	if !adopts(p.obj) {
		return p.stop(ctx, &refusedAdoption{id: id})
	}

	status := p.obj.NetBoxStatus()
	status.ID, status.Adopted = int64(id), true
	logf.FromContext(ctx).Info("adopted an existing netbox object", "netboxID", id, "action", "adopt")
	p.engine.event(p.obj, netboxv1alpha1.EventAdopted,
		"adopted netbox %s/%d, matched by %v", p.desc.Endpoint, id, status.NaturalKey)

	return p.update(ctx, found.live)
}

// create writes a new object. AdoptOnly stops here: it exists for objects a human owns,
// where the operator should correct drift and never bring one into existence.
func (p *pass) create(ctx context.Context) (ctrl.Result, error) {
	if policyOf(p.obj) == netboxv1alpha1.ConflictAdoptOnly {
		return p.stop(ctx, errAdoptOnly)
	}

	// Stamp before the payload is built, not after: createPayload copies p.desired, so a
	// stamp applied afterwards would reach the status and never the POST body.
	// No live object to merge with, so the tag list is exactly the operator's own tag.
	p.stamp(ctx, nil)

	payload, stripped := p.deferred.createPayload(p.desired)
	if len(stripped) > 0 {
		logf.FromContext(ctx).V(1).Info("deferring fields the create cannot carry",
			"action", "create", "deferred", stripped)
	}

	created, err := p.endpoint.Client.Create(ctx, p.desc.Endpoint, payload)
	if err != nil {
		return p.stop(ctx, fmt.Errorf("creating netbox %s: %w", p.desc.Endpoint, err))
	}

	return p.applyWrite(ctx, created, netboxv1alpha1.EventCreated, "create", "created")
}

// update PATCHes the difference, and nothing at all when there is none.
func (p *pass) update(ctx context.Context, live netbox.Object) (ctrl.Result, error) {
	p.live = live

	// Before the stamp, which is about to overwrite the very fields the report reads: the
	// claim on the live object is the other writer's, and the payload's is ours (NBO-047).
	// Reporting and not refusing -- see conflict.go.
	p.reportConflict(ctx, live)

	// Before the comparison, and with the live object in hand: an adopted object gains the
	// stamp here, and one that already carries it produces no change at all.
	p.stamp(ctx, live)

	changes := netbox.Changes(live, p.desired, fieldRules(p.desc))
	if len(changes) == 0 {
		logf.FromContext(ctx).V(1).Info("no drift",
			"netboxID", p.obj.NetBoxStatus().ID, "action", "none")
		p.condition(netboxv1alpha1.ConditionSynced, true,
			netboxv1alpha1.ReasonNoDrift, "netbox matches the spec")
		p.driftCondition(false, netboxv1alpha1.ReasonNoDrift, "netbox matches the spec")
		p.result = metrics.ResultUnchanged

		return p.settle(ctx, live)
	}

	p.driftDetected(changes)

	id := int(p.obj.NetBoxStatus().ID)
	if p.desc.UpdateStrategy == registry.UpdateRecreate && touchesIdentity(p.desc, changes) {
		return p.recreate(ctx, id, changes)
	}

	patched, err := p.endpoint.Client.Patch(ctx, p.desc.Endpoint, id, payloadOf(changes))
	if err != nil {
		return p.stop(ctx, fmt.Errorf("patching netbox %s/%d: %w", p.desc.Endpoint, id, err))
	}
	p.driftCorrected(patched, changes)

	return p.applyWrite(ctx, patched, netboxv1alpha1.EventUpdated, "update", renderChanges(changes))
}

// recreate replaces an object whose identity cannot be PATCHed.
//
// dcim.Cable is the case: its identity is its terminations, and the unique constraint on
// (termination_type, termination_id) keeps the wanted endpoint occupied until the old cable
// is gone, so the replacement cannot be created first
// (docs/netbox-schema.md -> dcim.Cable.meta.constraints).
func (p *pass) recreate(ctx context.Context, id int, changes []netbox.Change) (ctrl.Result, error) {
	// Retain means "never destroy this NetBox object", and a recreate destroys it. The two
	// instructions contradict each other, so the operator refuses rather than silently
	// picking one -- and refuses in this direction because a recreate is unrecoverable while a
	// refusal is one edit away from either outcome. The message names the fields that changed,
	// since reverting one of them is half the fix (errRecreateRetained).
	if deletionPolicyOf(p.obj.NetBoxSpec().DeletionPolicy, p.desc.RetainOnDelete) == netboxv1alpha1.DeletionRetain {
		return p.stop(ctx, fmt.Errorf("%w: %s", errRecreateRetained, renderChanges(changes)))
	}

	if _, err := p.endpoint.Client.Delete(ctx, p.desc.Endpoint, id); err != nil {
		return p.stop(ctx, fmt.Errorf("deleting netbox %s/%d to recreate it: %w", p.desc.Endpoint, id, err))
	}

	// Stripped for the same reason a create is: the replacement is a create, and a
	// DeferAlways reference still cannot point at an object that does not exist yet.
	payload, _ := p.deferred.createPayload(p.desired)

	created, err := p.endpoint.Client.Create(ctx, p.desc.Endpoint, payload)
	if err != nil {
		return p.stop(ctx, fmt.Errorf("recreating netbox %s: %w", p.desc.Endpoint, err))
	}
	p.driftCorrected(created, changes)

	// status.id is only ever taken from a create response, so a DryRun -- where the delete
	// is suppressed too -- keeps the id of the object that is still there rather than
	// reporting one that never existed.
	return p.applyWrite(ctx, created, netboxv1alpha1.EventRecreated, "recreate", renderChanges(changes))
}

// applyWrite records the result of a write. A suppressed response is a DryRun: it carries
// the payload that would have been sent and no id, so none of it may reach status.
func (p *pass) applyWrite(ctx context.Context, written netbox.Object, event, action, detail string) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("action", action, "changes", detail)

	if netbox.Suppressed(written) {
		out := p.suppression(event)
		drift := p.uncorrected(detail)

		// Debug, not info: a non-writing endpoint finds the same drift on every resync
		// and writes nothing, so at info this is one identical line per object per resync
		// forever. What changed is nothing; drift_detected_total and the DriftDetected
		// condition are the signals that scale.
		log.V(1).Info(out.what + ": netbox was not written")

		// On the transition only, for the reason the log line above was demoted -- an
		// Event is the more expensive of the two, since it is an API object that costs
		// etcd and evicts the Events somebody was watching for. driftMode: Report is meant
		// to be left running for a week over a whole NetBox, which is standing drift on
		// every object at once, so this is the path where a per-resync Event does the most
		// damage (NBO-087).
		if p.newDrift(out.synced, drift) {
			p.engine.event(p.obj, out.event, "%s: would have written %s (%s)",
				out.what, p.desc.Endpoint, detail)
		}

		// Both unguarded, and deliberately: the conditions are the standing state, which
		// is precisely why the Event need not repeat, and p.result feeds reconcile_total,
		// a count of reconciles rather than of changes. Do not "fix" either to match the
		// Event.
		p.condition(netboxv1alpha1.ConditionSynced, false, out.synced, out.why)
		p.driftCondition(true, netboxv1alpha1.ReasonDriftDetected, drift)
		p.result = out.result

		// Against the located object rather than the suppressed response: a suppressed
		// write carries the payload that was not sent, so reading it would report a
		// deferred field as applied on an endpoint that wrote nothing. Nil on a create,
		// where NetBox holds no such object at all.
		p.recordDeferred(p.live)

		return p.pending(ctx, out.ready,
			fmt.Sprintf("%s: netbox was not written (%s)", out.what, detail))
	}

	status := p.obj.NetBoxStatus()

	id, hasID := written.ID()
	if !hasID && status.ID == 0 {
		// Nothing else in the object is trustworthy if the write that was supposed to
		// create it did not come back with an id, and status.id must never hold one that
		// was not proven server-side.
		return p.stop(ctx, fmt.Errorf("%w: after %s", errNoObjectID, action))
	}

	if hasID {
		status.ID = int64(id)
	}

	// Only when the response carried one: a 204 with an empty body would otherwise blank a
	// url that is still correct.
	if url := urlOf(written); url != "" {
		status.URL = url
	}

	status.LastSyncTime = &metav1.Time{Time: metav1.Now().Time}
	p.recordHash(ctx)

	log.Info("wrote netbox", "netboxID", status.ID)

	p.engine.event(p.obj, event, "netbox %s/%d: %s", p.desc.Endpoint, status.ID, detail)
	p.condition(netboxv1alpha1.ConditionSynced, true,
		netboxv1alpha1.ReasonDriftCorrected, detail)
	p.driftCondition(false, netboxv1alpha1.ReasonDriftCorrected, detail)
	if result, ok := writeResults[action]; ok {
		p.result = result
	}

	return p.settle(ctx, written)
}

// writeResults maps a write action onto the metric result it counts as. Data next to
// applyWrite rather than a parameter, so the action in the log line and the bucket on the
// dashboard cannot disagree about what just happened.
var writeResults = map[string]string{
	"create":   metrics.ResultCreated,
	"update":   metrics.ResultUpdated,
	"recreate": metrics.ResultRecreated,
}

// suppression is how a write that was never sent gets reported.
//
// Two shapes rather than one, because `mode: DryRun` and `driftMode: Report` are set in
// different fields and fixed in different ways: a reason naming DryRun on an endpoint
// whose mode is Apply sends whoever reads it looking at the wrong field, and a dashboard
// that cannot tell "this endpoint is a rehearsal" from "this endpoint is live and drift is
// somebody else's to fix" cannot answer which one to switch off.
type suppression struct {
	// what names the reason nothing was sent, in the log line, the Event and the Ready
	// message -- so all three agree without three copies of the phrasing.
	what string

	// event is the Event reason. A DryRun keeps the write's own reason, because the
	// endpoint is rehearsing that write; Report replaces it, since "updated" and "would
	// have updated" must not read alike in `kubectl describe`.
	event string

	synced string
	ready  string
	result string

	// why is the Synced message: which field made this endpoint refuse the write.
	why string
}

// suppression returns how to report a suppressed write, given the Event reason the write
// would have carried.
func (p *pass) suppression(writeEvent string) suppression {
	if p.endpoint.DriftMode == netboxv1alpha1.DriftReport {
		return suppression{
			what:   "report only",
			event:  netboxv1alpha1.EventDriftDetected,
			synced: netboxv1alpha1.ReasonDriftReported,
			ready:  netboxv1alpha1.ReasonReportPending,
			result: metrics.ResultReported,
			why:    "the endpoint's driftMode is Report, so nothing was sent",
		}
	}

	return suppression{
		what:   "dry run",
		event:  writeEvent,
		synced: netboxv1alpha1.ReasonDriftDetectedDryRun,
		ready:  netboxv1alpha1.ReasonDryRunPending,
		result: metrics.ResultDryRun,
		why:    "the endpoint is in DryRun, so nothing was sent",
	}
}

// uncorrected renders the drift left standing by a suppressed write.
//
// A suppressed create leaves status.id at zero, and that is the one case where the drift is
// not a field list: NetBox does not hold the object at all, so "created" -- the detail an
// applied create would report -- describes something that did not happen.
func (p *pass) uncorrected(detail string) string {
	if p.obj.NetBoxStatus().ID == 0 {
		return "netbox holds no such object; the whole payload is uncorrected drift"
	}

	return detail
}

// driftCondition records whether NetBox currently differs from the spec with nothing done
// about it. False after a correction as well as after a pass that found nothing, so the
// condition is a stable statement about NetBox rather than one that flaps on every write.
func (p *pass) driftCondition(detected bool, reason, message string) {
	p.condition(netboxv1alpha1.ConditionDriftDetected, detected, reason, message)
}

// driftDetected counts every field that differs, whether or not it gets corrected.
func (p *pass) driftDetected(changes []netbox.Change) {
	for _, change := range changes {
		metrics.DriftDetected.WithLabelValues(p.desc.GVK.Kind, change.Field).Inc()
	}
}

// driftCorrected counts the fields NetBox actually accepted.
//
// A suppressed response means the endpoint is in DryRun and nothing was sent, so the
// fields stay detected-but-uncorrected. The gap between the two counters is what makes
// Report mode legible (docs/decisions/0005-gitops-coexistence.md) instead of
// indistinguishable from a healthy cluster.
func (p *pass) driftCorrected(written netbox.Object, changes []netbox.Change) {
	if netbox.Suppressed(written) {
		return
	}

	for _, change := range changes {
		metrics.DriftCorrected.WithLabelValues(p.desc.GVK.Kind, change.Field).Inc()
	}
}

// recordHash stores a digest of the payload NetBox just accepted, as the record of what
// was sent -- NetBox canonicalises some values, so the request and the response
// legitimately differ. A failure to hash is logged and dropped: it is a debugging aid, and
// losing it must not fail a write that has already happened.
func (p *pass) recordHash(ctx context.Context) {
	hash, err := netbox.Hash(p.desired)
	if err != nil {
		logf.FromContext(ctx).V(1).Info("could not hash the applied payload", "err", err.Error())

		return
	}

	p.obj.NetBoxStatus().LastAppliedHash = hash
}

func urlOf(obj netbox.Object) string {
	url, ok := obj["url"].(string)
	if !ok {
		return ""
	}

	return url
}

// adopts reports whether this object's policy permits taking over an existing NetBox
// object.
func adopts(obj Object) bool {
	policy := policyOf(obj)

	return policy == netboxv1alpha1.ConflictAdopt || policy == netboxv1alpha1.ConflictAdoptOnly
}

// policyOf returns the object's conflict policy, defaulting to the safe one. The CRD
// defaults it as well; this is the guard for an object stored before that default existed.
func policyOf(obj Object) netboxv1alpha1.ConflictPolicy {
	if policy := obj.NetBoxSpec().OnConflict; policy != "" {
		return policy
	}

	return netboxv1alpha1.ConflictFail
}

// touchesIdentity reports whether a change is to a field a PATCH cannot reach.
func touchesIdentity(d registry.Descriptor, changes []netbox.Change) bool {
	for _, change := range changes {
		for _, field := range d.RecreateOn {
			if change.Field == field {
				return true
			}
		}
	}

	return false
}

// payloadOf turns a change set into the PATCH body, so the Event and the request are built
// from the same list and cannot disagree about which fields were sent.
func payloadOf(changes []netbox.Change) netbox.Object {
	payload := make(netbox.Object, len(changes))

	for _, change := range changes {
		payload[change.Field] = change.New
	}

	return payload
}

// event records a Kubernetes Event, when there is a recorder to record it to.
func (e *Engine) event(obj Object, reason, format string, args ...any) {
	if e.Events == nil {
		return
	}

	e.Events.Eventf(obj, "Normal", reason, format, args...)
}
