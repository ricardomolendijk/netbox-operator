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

	// Refs turns the references in a spec into NetBox ids. Nil resolves nothing: every
	// declared reference is then reported unresolved and left out of the payload, which is
	// what the engine did before internal/resolver existed.
	Refs RefResolver

	// Status persists status updates.
	Status StatusWriter

	// Finalizers persists the finalizer that keeps a CR alive until its NetBox object has
	// been dealt with.
	Finalizers FinalizerWriter

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

	// refs is what the references that did not resolve mean for this object. Zero when
	// every reference resolved, which is what ready() checks before reporting Ready=True.
	refs refWait

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

	desired, state, refs, err := spec.desired(p.desc)
	if err != nil {
		return err
	}
	p.spec, p.desired, p.state = spec, desired, state

	return p.resolveRefs(ctx, refs)
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

		if err != nil {
			return match{}, lookupFailure(p.desc.Endpoint, params, err)
		}

		if live != nil {
			return match{live: live, byNaturalKey: true}, nil
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
		p.driftCondition(false, netboxv1alpha1.ReasonNoDrift, "netbox matches the spec")
		p.result = metrics.ResultUnchanged

		return p.ready(ctx)
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
	if _, err := p.endpoint.Client.Delete(ctx, p.desc.Endpoint, id); err != nil {
		return p.stop(ctx, fmt.Errorf("deleting netbox %s/%d to recreate it: %w", p.desc.Endpoint, id, err))
	}

	created, err := p.endpoint.Client.Create(ctx, p.desc.Endpoint, p.desired)
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

		// Debug, not info: a non-writing endpoint finds the same drift on every resync
		// and writes nothing, so at info this is one identical line per object per resync
		// forever. What changed is nothing; drift_detected_total and the DriftDetected
		// condition are the signals that scale.
		log.V(1).Info(out.what + ": netbox was not written")
		p.engine.event(p.obj, out.event, "%s: would have written %s (%s)", out.what, p.desc.Endpoint, detail)
		p.condition(netboxv1alpha1.ConditionSynced, false, out.synced, out.why)
		p.driftCondition(true, netboxv1alpha1.ReasonDriftDetected, p.uncorrected(detail))
		p.result = out.result

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

	return p.ready(ctx)
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
