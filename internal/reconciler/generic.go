// Package reconciler is the only place a create, adopt or update decision is made.
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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
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

	// List returns every object matching params.
	//
	// The engine lists rather than asking for exactly one, because a natural key that
	// matches several objects has to name every one of them in the Conflict it reports,
	// and a count is not something an operator can act on. See docs/concepts/engine.md.
	List(ctx context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error)
}

// Writer mutates NetBox. A DryRun client implements it by returning the payload marked
// suppressed instead of sending it, which the engine detects with netbox.Suppressed.
type Writer interface {
	Create(ctx context.Context, endpoint string, payload netbox.Object) (netbox.Object, error)
	Patch(ctx context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error)

	// Delete is used only by the recreate strategy, for a kind whose identity lives
	// somewhere a PATCH cannot reach. Deletion because a CR went away is the finalizer's
	// job (NBO-007).
	Delete(ctx context.Context, endpoint string, id int) error
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
}

// Endpoints hands out the client for one NetBoxEndpoint by namespace and name. A miss
// means the endpoint is not Ready, which is a wait rather than a failure.
type Endpoints interface {
	Endpoint(namespace, name string) (Endpoint, bool)
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

	// Status persists status updates.
	Status StatusWriter

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

	p := &pass{engine: e, obj: obj, before: obj.NetBoxStatus().DeepCopy(), desc: descriptor}

	endpoint, ok := e.Endpoints.Endpoint(obj.GetNamespace(), obj.NetBoxSpec().EndpointRef)
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: netboxendpoint %q in namespace %q",
			errEndpointNotReady, obj.NetBoxSpec().EndpointRef, obj.GetNamespace()))
	}
	p.endpoint = endpoint

	if err := p.build(ctx); err != nil {
		return p.stop(ctx, err)
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
}

// build renders the spec into a payload and reports the references it had to leave out.
func (p *pass) build(ctx context.Context) error {
	spec, err := specOf(p.obj)
	if err != nil {
		return err
	}

	desired, state, refs, err := spec.desired(p.desc)
	if err != nil {
		return err
	}
	p.spec, p.desired, p.state = spec, desired, state

	if len(refs) == 0 {
		p.condition(netboxv1alpha1.ConditionRefsResolved, true,
			netboxv1alpha1.ReasonAllResolved, "no unresolved references")

		return nil
	}

	// References are accepted and ignored until internal/resolver lands (NBO-012).
	// Reporting it is the difference between an honest M1 boundary and a silent omission:
	// everything else about the object is still reconciled.
	logf.FromContext(ctx).V(1).Info("references are not resolved in this build",
		"action", "build", "refs", refs)
	p.condition(netboxv1alpha1.ConditionRefsResolved, false,
		netboxv1alpha1.ReasonNotImplemented,
		fmt.Sprintf("references are not resolved yet and were left out of the payload: %v", refs))

	return nil
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

		matches, err := p.endpoint.Client.List(ctx, p.desc.Endpoint, params)
		if err != nil {
			return match{}, fmt.Errorf("looking up netbox %s by %v: %w", p.desc.Endpoint, params, err)
		}

		// Recorded even when nothing matched: the first question about an object that was
		// not adopted is what the engine actually looked for.
		p.obj.NetBoxStatus().NaturalKey = params

		if len(matches) > 1 {
			return match{}, &ambiguousMatch{params: params, ids: idsOf(matches)}
		}

		if len(matches) == 1 {
			return match{live: matches[0], byNaturalKey: true}, nil
		}
	}

	return match{}, nil
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

	created, err := p.endpoint.Client.Create(ctx, p.desc.Endpoint, p.desired)
	if err != nil {
		return p.stop(ctx, fmt.Errorf("creating netbox %s: %w", p.desc.Endpoint, err))
	}

	return p.applyWrite(ctx, created, netboxv1alpha1.EventCreated, "create", "created")
}

// update PATCHes the difference, and nothing at all when there is none.
func (p *pass) update(ctx context.Context, live netbox.Object) (ctrl.Result, error) {
	changes := netbox.Changes(live, p.desired, fieldRules(p.desc))
	if len(changes) == 0 {
		logf.FromContext(ctx).V(1).Info("no drift",
			"netboxID", p.obj.NetBoxStatus().ID, "action", "none")
		p.condition(netboxv1alpha1.ConditionSynced, true,
			netboxv1alpha1.ReasonNoDrift, "netbox matches the spec")

		return p.ready(ctx)
	}

	id := int(p.obj.NetBoxStatus().ID)
	if p.desc.UpdateStrategy == registry.UpdateRecreate && touchesIdentity(p.desc, changes) {
		return p.recreate(ctx, id, changes)
	}

	patched, err := p.endpoint.Client.Patch(ctx, p.desc.Endpoint, id, payloadOf(changes))
	if err != nil {
		return p.stop(ctx, fmt.Errorf("patching netbox %s/%d: %w", p.desc.Endpoint, id, err))
	}

	return p.applyWrite(ctx, patched, netboxv1alpha1.EventUpdated, "update", renderChanges(changes))
}

// recreate replaces an object whose identity cannot be PATCHed.
//
// dcim.Cable is the case: its identity is its terminations, and the unique constraint on
// (termination_type, termination_id) keeps the wanted endpoint occupied until the old cable
// is gone, so the replacement cannot be created first
// (docs/netbox-schema.md -> dcim.Cable.meta.constraints).
func (p *pass) recreate(ctx context.Context, id int, changes []netbox.Change) (ctrl.Result, error) {
	if err := p.endpoint.Client.Delete(ctx, p.desc.Endpoint, id); err != nil {
		return p.stop(ctx, fmt.Errorf("deleting netbox %s/%d to recreate it: %w", p.desc.Endpoint, id, err))
	}

	created, err := p.endpoint.Client.Create(ctx, p.desc.Endpoint, p.desired)
	if err != nil {
		return p.stop(ctx, fmt.Errorf("recreating netbox %s: %w", p.desc.Endpoint, err))
	}

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
		log.Info("dry run: netbox was not written")
		p.engine.event(p.obj, event, "dry run: would have written %s (%s)", p.desc.Endpoint, detail)
		p.condition(netboxv1alpha1.ConditionSynced, false,
			netboxv1alpha1.ReasonDriftDetectedDryRun, "the endpoint is in DryRun, so nothing was sent")

		return p.pending(ctx, netboxv1alpha1.ReasonDryRunPending,
			fmt.Sprintf("dry run: netbox was not written (%s)", detail))
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

	return p.ready(ctx)
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

// idsOf reads the NetBox ids out of a list of matches, for a Conflict that names them.
func idsOf(matches []netbox.Object) []int {
	ids := make([]int, 0, len(matches))

	for _, obj := range matches {
		if id, ok := obj.ID(); ok {
			ids = append(ids, id)
		}
	}

	return ids
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
