// This file is the allocation engine: the only place in the operator that asks NetBox for a
// free object rather than for a named one.
//
// It is a second engine beside the declarative one in generic.go rather than a mode of it,
// because the two do genuinely different things -- one drives an object towards a fixed
// desired state forever, the other does one irreversible thing and then stops
// (docs/decisions/0004-claims-first-allocation.md). What they share they share by sharing
// code: the endpoint provider, the finalizer sequence, the condition and requeue tables, the
// provenance stamp and the outcome classification are all the same ones.
//
// Everything that differs between claim kinds is data on a registry.ClaimDescriptor, so
// there is no branch on Kind below and NBO-064's prefix and ip-range claims are new files in
// internal/registry and nothing here.
//
// The failure modes *are* the feature. Every one of them is a named method with guard
// clauses, and the thing that must not happen to this file is those collapsing into one
// function with the failure modes as nested ifs: a missed idempotency check does not raise
// an error, it silently burns one address per retry and nobody notices until the /24 is
// full.

package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// identityLength is how many hex characters of the digest the allocation identity keeps.
//
// Sixteen, per docs/decisions/0005-gitops-coexistence.md section 3. It is a collision
// domain of 2^64 over (NetBox, namespace, Kind, name) tuples, which is not a birthday
// problem at any cluster size, and it fits a NetBox custom-field value a human can read out
// of the UI and paste into a filter. Changing it re-rolls every allocation in every cluster,
// which is why a unit test pins the derivation to a golden value.
const identityLength = 16

// customFieldFilter is the prefix NetBox's REST API puts in front of a custom field's name
// to filter on it: `?cf_k8s_allocation_identity=9f2c41b7ae05d813`.
//
// Equality filtering needs the definition's `filter_logic` to be `exact`
// (extras.CustomField.filter_logic, whose default is FILTER_LOOSE); the provenance
// bootstrap creates it that way. Under loose filtering the query is a substring match, which
// for a fixed-width identity is the same answer -- but a shorter spec.allocationIdentity
// would over-match, and over-matching lands on AllocationConflict, which refuses. The
// failure direction is safe either way, and exact makes it correct rather than lucky.
const customFieldFilter = "cf_"

// refusedRetry is the wait for every state a claim cannot allocate out of.
//
// One tier for all of them, and it is deliberately the same ten minutes as truncatedRetry
// for the same reason: nothing the operator can do clears any of these, so a fast retry only
// burns API budget re-deriving the same refusal. The states are an exhausted pool, a pool
// the operator will not allocate out of, an identity resolving outside its pool, two objects
// sharing one identity, and an endpoint with nowhere to store an identity.
//
// Not terminal, though, and that is the decision behind the number
// (https://github.com/ricardomolendijk/netbox-operator/issues/178): a claim whose pool
// somebody has just widened must converge on its own, not sit there Ready=False until a
// human touches the object. The claim also *watches* its pool, so a widened prefix
// re-enqueues it immediately -- and the two cover different fixes rather than the same one.
// Widening the prefix is a change to a Kubernetes object and the watch sees it; freeing an
// address inside NetBox is not, nothing tells the operator, and only this timer catches it.
const refusedRetry = truncatedRetry

// errUnverified is a read-after-write that did not confirm the allocation.
//
// Not classified as an API error and not as invalid: NetBox answered, and something was
// probably created -- it is what was created that could not be confirmed. Nothing is written
// to status, so the identity search on the next pass reconciles whatever actually landed,
// which is the same path that recovers a lost response.
var errUnverified = errors.New("the allocated object could not be verified")

// errNoPoolDescriptor is a claim descriptor whose pool Kind has no Descriptor, so the pool's
// NetBox endpoint cannot be looked up. registry.Validate rejects it at boot; this is the
// guard for an engine wired with a hand-built descriptor in a test.
var errNoPoolDescriptor = errors.New("no descriptor is registered for the claim's pool kind")

// Allocator is the only path to NetBox's advisory-locked allocation endpoints.
//
// Consumer-defined and two methods, satisfied by *netbox.Client. URL is on it rather than on
// Endpoint because the identity has to be derived from the same object that will do the
// POST: an identity computed from a NetBoxEndpoint field that was momentarily unreadable
// would allocate a second address exactly once, silently, and only on the unluckiest pass.
type Allocator interface {
	// URL is the NetBox this client talks to, normalised. The first component of the
	// allocation identity.
	URL() string

	// Allocate POSTs payload to one pool's advisory-locked available-* sub-path and returns
	// the object NetBox created. It does not retry: an allocating POST is not idempotent.
	Allocate(
		ctx context.Context, endpoint string, id int, sub string, payload netbox.Object,
	) (netbox.Object, error)
}

// ClaimDescriptors is where per-claim-kind facts come from.
type ClaimDescriptors interface {
	Claim(gvk schema.GroupVersionKind) (registry.ClaimDescriptor, bool)
}

// PoolResolver turns a claim's pool reference into a NetBox id.
//
// One method, narrowed from internal/resolver's Resolver: a claim has exactly one reference
// and needs no Resolution over a whole descriptor. It goes through the resolver rather than
// reading the target CR itself precisely because that is where the NetBoxRefGrant check
// lives -- a pool in another namespace is authorised there or not at all.
type PoolResolver interface {
	Resolve(ctx context.Context, req resolver.Request) (resolver.Result, error)
}

// Claim is the CR the allocation engine reconciles: any claim kind that embeds the shared
// envelope.
//
// Four methods, two of them one-liners on the kind. The two that are not pointers into
// shared state are the allocated value's accessors, because the *name* of that value is
// per-kind -- `address` on an address claim, `prefix` on NBO-064's prefix claim -- while
// nothing about how it was obtained is.
type Claim interface {
	client.Object

	// ClaimSpec returns the engine-owned part of the spec.
	ClaimSpec() *netboxv1alpha1.NetBoxClaimSpec

	// ClaimStatus returns the engine-owned part of the status, for the engine to write.
	ClaimStatus() *netboxv1alpha1.NetBoxClaimStatus

	// Allocated returns what was allocated, or the empty string. It is the first guard
	// clause of every pass: non-empty means never allocate again, ever.
	Allocated() string

	// SetAllocated records what was allocated. Called exactly once in an object's life.
	SetAllocated(value string)
}

// ClaimEngine allocates for one claim of any registered claim kind.
type ClaimEngine struct {
	// Claims resolves a GVK to its per-claim-kind facts. Nil means the package-level
	// registry.
	Claims ClaimDescriptors

	// Pools resolves the pool Kind's Descriptor, which is where the pool's NetBox endpoint
	// is written down. Nil means the package-level registry.
	Pools Descriptors

	// Endpoints resolves a spec.endpointRef to a client. The same provider the declarative
	// engine uses, so a claim and an object in one namespace cannot disagree about whether
	// their endpoint is Ready.
	Endpoints Endpoints

	// Refs resolves the pool reference. Nil is a wiring bug rather than a mode: a claim
	// with no resolver could not find its pool at all, and a claim that allocated out of a
	// pool it could not resolve is the one outcome this file exists to prevent.
	Refs PoolResolver

	// Status persists status updates.
	Status StatusWriter

	// Finalizers persists the finalizer that keeps a claim alive until its NetBox object
	// has been reported.
	Finalizers FinalizerWriter

	// Events records allocations. Optional.
	Events Recorder

	// Scheme derives a claim's GVK, so the engine cannot be handed the wrong descriptor for
	// a claim.
	Scheme *runtime.Scheme
}

// Reconcile allocates one object for one claim, or reports why it did not.
//
// A flat sequence of guards, in an order that is itself part of the design: everything that
// needs no NetBox call is answered before anything that does, so a deletion completes and an
// already-allocated claim settles while NetBox is unreachable.
func (e *ClaimEngine) Reconcile(ctx context.Context, claim Claim) (ctrl.Result, error) {
	desc, err := e.descriptorFor(claim)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = logf.IntoContext(ctx, logf.FromContext(ctx).WithValues(
		"kind", desc.GVK.Kind, "namespace", claim.GetNamespace(), "name", claim.GetName(),
		"endpoint", desc.Endpoint))

	p := &claimPass{
		engine: e, claim: claim, before: claim.ClaimStatus().DeepCopy(),
		beforeAllocated: claim.Allocated(), desc: desc, result: metrics.ResultError,
	}

	started := time.Now()
	defer func() { metrics.ObserveReconcile(desc.GVK.Kind, p.result, time.Since(started)) }()

	if !claim.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, p.releasing(ctx)
	}

	// Before the guard below and before any NetBox call, so there is no ordering in which
	// the engine POSTs to an allocation endpoint without a durable finalizer already behind
	// it. A no-op on every pass after the first.
	if err := takeFinalizer(ctx, e.Finalizers, claim); err != nil {
		return ctrl.Result{}, err
	}

	// The immutability of ADR-0004 as a guard clause. This is what makes "reconcile fifty
	// times, POST once" structural rather than a property of the code below it -- and note
	// that it needs no endpoint and issues no request, so the steady state of every claim in
	// the cluster is a reconcile that talks to nobody.
	if address := claim.Allocated(); address != "" {
		return p.settled(ctx, address)
	}

	endpoint, ok := e.Endpoints.Endpoint(ctx, claim.GetNamespace(), claim.ClaimSpec().EndpointRef)
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: netboxendpoint %q in namespace %q",
			errEndpointNotReady, claim.ClaimSpec().EndpointRef, claim.GetNamespace()))
	}
	p.endpoint = endpoint

	if endpoint.Allocator == nil {
		return ctrl.Result{}, fmt.Errorf("%w: netboxendpoint %q has no allocation client",
			errNotConfigured, claim.ClaimSpec().EndpointRef)
	}

	return p.allocateOnce(ctx)
}

// descriptorFor resolves the claim descriptor for claim's kind. A missing one is a returned
// error: a controller running for an unregistered claim kind is a wiring bug no requeue
// fixes, and registry.Validate at boot exists to catch it earlier.
func (e *ClaimEngine) descriptorFor(claim Claim) (registry.ClaimDescriptor, error) {
	gvk, err := apiutil.GVKForObject(claim, e.Scheme)
	if err != nil {
		return registry.ClaimDescriptor{}, fmt.Errorf(
			"resolving the group-version-kind of %T: %w", claim, err)
	}

	claims := ClaimDescriptors(claimLookup{})
	if e.Claims != nil {
		claims = e.Claims
	}

	desc, ok := claims.Claim(gvk)
	if !ok {
		return registry.ClaimDescriptor{}, fmt.Errorf("no claim descriptor is registered for %s", gvk)
	}

	return desc, nil
}

// pools is the Descriptors to look the pool Kind up in.
func (e *ClaimEngine) pools() Descriptors {
	if e.Pools != nil {
		return e.Pools
	}

	return registryLookup{}
}

// claimLookup is the package-level claim registry as a ClaimDescriptors.
type claimLookup struct{}

// Claim returns the claim descriptor registered for gvk.
func (claimLookup) Claim(gvk schema.GroupVersionKind) (registry.ClaimDescriptor, bool) {
	return registry.Claim(gvk)
}

// AllocationIdentity derives a claim's allocation identity.
//
// `sha256(url \n namespace \n kind \n name)`, first 16 hex characters
// (docs/decisions/0005-gitops-coexistence.md section 3). Deterministic, and that is the
// whole point: the same manifest applied to a rebuilt cluster derives the same identity,
// finds the object still carrying it in NetBox, and reclaims the same address. A UID-keyed
// idempotency key cannot do that, because a UID is regenerated exactly when the old address
// is most wanted back.
//
// A pure function, exported, and pinned by a golden test -- an accidental change to the
// derivation would silently re-roll every address in every cluster that upgrades, and the
// test is there so that breaks the build instead.
//
// The four components are the four things that make an allocation a different allocation.
// The separator is a newline because none of the four can contain one, so no pair of
// distinct tuples can render to the same string -- joining on the empty string would make
// (`.../api`, `ns`) and (`.../apin`, `s`) one identity.
func AllocationIdentity(url, namespace, kind, name string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{url, namespace, kind, name}, "\n")))

	return hex.EncodeToString(digest[:])[:identityLength]
}

// refusal is a state the allocation engine will not allocate out of.
//
// A typed error so that claimOutcome classifies by type and never by message, exactly as
// the declarative engine's outcome table does. It carries its own condition reason and Event
// because the reason *is* the classification here: unlike an HTTP failure, there is no
// underlying error whose type says which refusal this is.
type refusal struct {
	reason  string
	event   string
	message string
}

func (r *refusal) Error() string { return r.message }

// refuse builds a refusal.
func refuse(reason, event, format string, args ...any) error {
	return &refusal{reason: reason, event: event, message: fmt.Sprintf(format, args...)}
}

// claimOutcome maps a failed allocation onto what to record and when to come back.
//
// Two arms of its own and then the declarative engine's whole table, which is the reuse that
// matters: every way NetBox can be unreachable, rate limiting, unauthenticated or
// unavailable is already classified there, and a second table for claims would be a second
// table that can disagree with it.
func claimOutcome(err error, resync time.Duration) outcome {
	var refused *refusal
	if errors.As(err, &refused) {
		return outcome{
			reason: refused.reason, requeue: refusedRetry, event: refused.event,
			severe: true, result: metrics.ResultError,
		}
	}

	if errors.Is(err, errUnverified) {
		// Loud, because an unverifiable write to an allocation endpoint is the shape of the
		// bug this whole file exists to prevent -- but not an Event and not a long backoff,
		// because the identity search on the next pass resolves it without a human.
		return outcome{
			reason: netboxv1alpha1.ReasonAllocationPending, requeue: transientRetry,
			severe: true, result: metrics.ResultError,
		}
	}

	return classify(err, resync)
}

// claimPass is one reconcile of one claim.
type claimPass struct {
	engine   *ClaimEngine
	claim    Claim
	before   *netboxv1alpha1.NetBoxClaimStatus
	desc     registry.ClaimDescriptor
	endpoint Endpoint

	// beforeAllocated is what the claim held when the pass began.
	//
	// The allocated value lives on the kind's own status rather than in the shared envelope,
	// so DeepEqual over the envelope cannot see it change -- and a pass that allocated an
	// address and wrote no status would lose the one field that must never be lost.
	beforeAllocated string

	// identity is this claim's allocation identity, resolved once per pass.
	identity string

	// pool is the resolved allocation pool. Zero until resolvePool succeeds.
	pool pool

	// stamped is the provenance the allocating POST carried, for status. Unset on the
	// reclaim path, where nothing was written -- status.provenance records what the operator
	// wrote, not what it found.
	stamped *netboxv1alpha1.ProvenanceStatus

	// result is this pass's metrics.Result* bucket.
	result string
}

// pool is a resolved allocation pool: a NetBox object to POST to, and the network it covers.
type pool struct {
	// endpoint is the pool's REST path, `ipam/prefixes`.
	endpoint string

	// id is the pool object's NetBox primary key.
	id int

	// cidr is the network the pool covers, parsed. Held as a netip.Prefix rather than a
	// string because the one thing the operator does with it is ask whether an address is
	// inside it -- and doing that with string comparison is how "10.0.2.5 is in
	// 10.0.20.0/24" happens.
	cidr netip.Prefix

	// display is the pool as a human reads it, for the condition and status.
	display string
}

// allocateOnce is the allocating half of a pass, after the guards that need no NetBox.
func (p *claimPass) allocateOnce(ctx context.Context) (ctrl.Result, error) {
	identity, err := p.allocationIdentity()
	if err != nil {
		return p.stop(ctx, err)
	}
	p.identity = identity

	live, err := p.resolvePool(ctx)
	if err != nil {
		return p.blockedOnPool(ctx, err)
	}

	if err := p.admitPool(live); err != nil {
		return p.stop(ctx, err)
	}

	found, err := p.findByIdentity(ctx)
	if err != nil {
		return p.stop(ctx, err)
	}

	if found != nil {
		return p.reclaim(ctx, found)
	}

	return p.allocate(ctx)
}

// allocationIdentity is this claim's identity, and the check that there is somewhere to keep
// it.
//
// The check is not optional and has no override. The provenance stamp is optional for an
// ordinary object -- an unstamped object is merely unattributed -- but for a claim the
// identity store is what makes a lost HTTP response recoverable, and without it every retry
// of a POST that actually committed allocates another address. So an endpoint that cannot
// store one allocates nothing at all: zero POSTs, a condition, an Event.
func (p *claimPass) allocationIdentity() (string, error) {
	stamp := p.endpoint.Provenance

	if !stamp.Applicable() || stamp.AllocationIdentityField == "" ||
		!slices.Contains(stamp.Fields, stamp.AllocationIdentityField) {
		return "", refuse(netboxv1alpha1.ReasonIdempotencyKeyUnavailable,
			netboxv1alpha1.EventAllocationConflict,
			"this endpoint has nowhere to store an allocation identity, so nothing was allocated:"+
				" set spec.managedBy.clusterID on the netboxendpoint and let the bootstrap create the"+
				" %q custom field, or create it by hand with type=text and filter_logic=exact on"+
				" object type %q",
			provenance.DefaultAllocationIdentityField, p.desc.ObjectType)
	}

	// The explicit override wins, for the case the derived value cannot survive by
	// construction: a claim that has been renamed and should keep its address.
	if explicit := p.claim.ClaimSpec().AllocationIdentity; explicit != "" {
		return explicit, nil
	}

	return AllocationIdentity(p.endpoint.Allocator.URL(),
		p.claim.GetNamespace(), p.desc.GVK.Kind, p.claim.GetName()), nil
}

// identityField is the NetBox custom field the identity is stored in.
func (p *claimPass) identityField() string {
	return p.endpoint.Provenance.AllocationIdentityField
}

// resolvePool turns the claim's pool reference into a pool, and returns the pool's live
// NetBox object for the admissibility guards.
func (p *claimPass) resolvePool(ctx context.Context) (netbox.Object, error) {
	target, ok := p.engine.pools().Get(p.desc.Pool.Target)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoPoolDescriptor, p.desc.Pool.Target)
	}

	if p.engine.Refs == nil {
		return nil, fmt.Errorf("%w: no PoolResolver is wired", errNotConfigured)
	}

	ref, err := p.poolRef()
	if err != nil {
		return nil, err
	}

	resolved, err := p.engine.Refs.Resolve(ctx, resolver.Request{
		NetBox:      p.endpoint.Client,
		Referrer:    types.NamespacedName{Namespace: p.claim.GetNamespace(), Name: p.claim.GetName()},
		ReferrerGVK: p.desc.GVK,
		Field:       p.desc.Pool,
		Ref:         ref,
	})
	if err != nil {
		// Wrapped for the log, and unwrapped again by blockedOnPool for the condition: the
		// resolver's own error is already a complete sentence naming the field and the target,
		// and a condition that said the claim's name twice would be worse for having more
		// words in it.
		return nil, fmt.Errorf("resolving the pool of %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	live, err := p.endpoint.Client.GetByID(ctx, target.Endpoint, int(resolved.ID))
	if err != nil {
		return nil, fmt.Errorf("fetching the pool netbox %s/%d: %w", target.Endpoint, resolved.ID, err)
	}

	if err := p.setPool(target.Endpoint, int(resolved.ID), live); err != nil {
		return nil, err
	}

	return live, nil
}

// setPool records the resolved pool, parsing the network it covers.
func (p *claimPass) setPool(endpoint string, id int, live netbox.Object) error {
	if live == nil {
		return fmt.Errorf("the pool netbox %s/%d is gone: %w", endpoint, id,
			&netbox.NotFoundError{Endpoint: endpoint, ID: id})
	}

	value, _ := live[p.desc.PoolValueField].(string)

	cidr, err := netip.ParsePrefix(value)
	if err != nil {
		// NetBox produced a value in this column that is not a network. Not a validation
		// error against anything this operator sent, so it is reported as what it is: a pool
		// this claim cannot reason about.
		return refuse(netboxv1alpha1.ReasonPoolNotAllocatable,
			netboxv1alpha1.EventPoolNotAllocatable,
			"pool netbox %s/%d has %s=%q, which is not a network, so an allocation out of it"+
				" could not be checked against it", endpoint, id, p.desc.PoolValueField, value)
	}

	p.pool = pool{endpoint: endpoint, id: id, cidr: cidr, display: value}

	return nil
}

// poolRef reads the pool reference out of the claim's spec, by the name the descriptor
// declares.
//
// Through the spec's JSON representation rather than through a per-kind accessor, which is
// how every other part of this operator reads a spec it knows nothing about: the field name
// is then declared exactly once, on the ClaimDescriptor, and a claim kind with two possible
// pool fields (NBO-064) is a descriptor change rather than an interface change.
func (p *claimPass) poolRef() (netboxv1alpha1.ObjectRef, error) {
	encoded, err := json.Marshal(p.claim)
	if err != nil {
		return netboxv1alpha1.ObjectRef{}, fmt.Errorf("encoding %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	var envelope struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return netboxv1alpha1.ObjectRef{}, fmt.Errorf("decoding the spec of %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	raw, ok := envelope.Spec[p.desc.Pool.Spec]
	if !ok {
		return netboxv1alpha1.ObjectRef{}, fmt.Errorf("%w: %s", errUnmappedField, p.desc.Pool.Spec)
	}

	var ref netboxv1alpha1.ObjectRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return netboxv1alpha1.ObjectRef{}, fmt.Errorf("decoding %s of %s/%s: %w",
			p.desc.Pool.Spec, p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	return ref, nil
}

// admitPool refuses the pool states this claim kind will not allocate out of.
//
// Both lists are descriptor data, and that is the point: `status: container` is a refusal for
// an address claim and a *precondition* for NBO-064's prefix claim, so the same value cannot
// be a rule in shared code.
func (p *claimPass) admitPool(live netbox.Object) error {
	for _, flag := range p.desc.PoolMustNotBeTrue {
		set, _ := live[flag].(bool)
		if !set {
			continue
		}

		return refuse(netboxv1alpha1.ReasonPoolNotAllocatable,
			netboxv1alpha1.EventPoolNotAllocatable,
			"pool %s (netbox %s/%d) has %s set, which says its free space is not really free;"+
				" nothing was allocated out of it",
			p.pool.display, p.pool.endpoint, p.pool.id, flag)
	}

	status := netbox.ChoiceOf(live["status"])
	if !slices.Contains(p.desc.PoolForbiddenStatus, status) {
		return nil
	}

	return refuse(netboxv1alpha1.ReasonPoolNotAllocatable,
		netboxv1alpha1.EventPoolNotAllocatable,
		"pool %s (netbox %s/%d) has status %q, which %s does not allocate out of;"+
			" nothing was allocated",
		p.pool.display, p.pool.endpoint, p.pool.id, status, p.desc.GVK.Kind)
}

// findByIdentity searches NetBox for an object already carrying this claim's identity.
//
// Run on **every** pass that is about to allocate, unconditionally rather than only after a
// suspected failure: one indexed GET is cheaper than one leaked address. It is the single
// path that covers a lost HTTP response, a pod evicted between the POST and the status
// write, a controller-runtime retry, and a cluster rebuilt from Git -- which is why those
// are one code path and one condition reason rather than four recovery modes.
//
// Two or more matches is never an allocation. It means a previous over-allocation, and the
// operator cannot prove which of them is unused: a NIC's static configuration or a DNS
// record may be pointing at either. So it refuses, names both, and deletes nothing.
func (p *claimPass) findByIdentity(ctx context.Context) (netbox.Object, error) {
	params := netbox.Params{}.Match(customFieldFilter+p.identityField(), netbox.LookupExact, p.identity)

	live, err := p.endpoint.Client.GetOne(ctx, p.desc.Endpoint, params)

	var ambiguous *netbox.AmbiguousError
	if errors.As(err, &ambiguous) {
		return nil, refuse(netboxv1alpha1.ReasonAllocationConflict,
			netboxv1alpha1.EventAllocationConflict,
			"%s; nothing was allocated and nothing was deleted, because the operator cannot prove"+
				" which of them is unused -- delete the one that is not in service and this claim"+
				" reclaims the other", ambiguous.Error())
	}

	if err != nil {
		return nil, fmt.Errorf("searching netbox %s for allocation identity %s: %w",
			p.desc.Endpoint, p.identity, err)
	}

	return live, nil
}

// reclaim adopts an object that already carries this claim's identity.
//
// Verified rather than assumed. The object has to be inside the pool the claim names now: if
// it is not, the claim has been repointed, the prefix has been renamed, or the name has been
// reused for a different purpose, and both alternatives to refusing are worse than refusing.
// Allocating a second address would leave two objects carrying one identity -- the state
// findByIdentity can never resolve again -- and accepting the out-of-pool object would make
// prefixRef a lie.
func (p *claimPass) reclaim(ctx context.Context, live netbox.Object) (ctrl.Result, error) {
	id, ok := live.ID()
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: matched by allocation identity %s", errNoObjectID, p.identity))
	}

	value, _ := live[p.desc.ResultField].(string)

	if !p.insidePool(value) {
		return p.stop(ctx, refuse(netboxv1alpha1.ReasonReclaimedOutsidePool,
			netboxv1alpha1.EventReclaimedOutsidePool,
			"netbox %s/%d carries this claim's allocation identity %s and holds %s=%q, which is"+
				" outside the pool %s; nothing was allocated -- either delete that netbox object or"+
				" set spec.allocationIdentity to the identity of the one this claim should keep",
			p.desc.Endpoint, id, p.identity, p.desc.ResultField, value, p.pool.display))
	}

	metrics.AllocationsTotal.WithLabelValues(p.desc.GVK.Kind, metrics.AllocationReclaimed).Inc()

	return p.commit(ctx, live, id, value, allocation{
		reason: netboxv1alpha1.ReasonReclaimedByIdentity,
		event:  netboxv1alpha1.EventAllocationReclaimed,
		detail: fmt.Sprintf("reclaimed netbox %s/%d (%s) by allocation identity %s%s",
			p.desc.Endpoint, id, value, p.identity, p.handover(live)),
	})
}

// handover names the UID the reclaimed object was stamped with when it is not this claim's.
//
// It is the only signal that exists for "a different claim has held this name". On a rebuilt
// cluster the handover is entirely legitimate -- the manifest is the same and the UID is
// new -- and when two different claims are given one name over time it is a mistake, and the
// two are indistinguishable from inside the operator. So it is reported and not judged.
//
// The stale UID is deliberately **not** overwritten. It is evidence: a provenance stamp
// naming a CR that no longer exists is exactly what makes a leaked object findable, and the
// operator has nothing to gain from erasing it on the one path where it is most informative.
// The current claim's UID is recorded in status.claimUID either way.
func (p *claimPass) handover(live netbox.Object) string {
	stamped := netbox.CustomFieldOf(live, p.endpoint.Provenance.UIDField)
	if stamped == "" || stamped == string(p.claim.GetUID()) {
		return ""
	}

	return fmt.Sprintf("; it was allocated by uid %s and is now held by uid %s",
		stamped, p.claim.GetUID())
}

// allocate asks NetBox for a free object.
//
// One POST, under NetBox's own advisory lock, carrying the identity in the same body. There
// is therefore no window in which an allocated object exists without the identity that says
// whose it is -- which is what makes the search above able to recover every failure of this
// call.
func (p *claimPass) allocate(ctx context.Context) (ctrl.Result, error) {
	if p.endpoint.DriftMode == netboxv1alpha1.DriftReport {
		return p.pending(ctx, netboxv1alpha1.ReasonReportPending,
			fmt.Sprintf("the endpoint's driftMode is Report, so nothing was sent:"+
				" would have allocated one %s out of %s", p.desc.ResultField, p.pool.display))
	}

	payload := p.payload()

	allocated, err := p.endpoint.Allocator.Allocate(
		ctx, p.pool.endpoint, p.pool.id, p.desc.PoolSubPath, payload)
	if err != nil {
		return p.stop(ctx, p.allocationFailure(err))
	}

	if netbox.Suppressed(allocated) {
		return p.pending(ctx, netboxv1alpha1.ReasonDryRunPending,
			fmt.Sprintf("the endpoint is in DryRun, so nothing was sent:"+
				" would have allocated one %s out of %s", p.desc.ResultField, p.pool.display))
	}

	id, ok := allocated.ID()
	if !ok {
		return p.stop(ctx, fmt.Errorf("%w: after allocating out of %s/%d",
			errNoObjectID, p.pool.endpoint, p.pool.id))
	}

	live, value, err := p.verify(ctx, allocated, id)
	if err != nil {
		return p.stop(ctx, err)
	}

	metrics.AllocationsTotal.WithLabelValues(p.desc.GVK.Kind, metrics.AllocationAllocated).Inc()

	return p.commit(ctx, live, id, value, allocation{
		reason: netboxv1alpha1.ReasonAddressAllocated,
		event:  netboxv1alpha1.EventAllocated,
		detail: fmt.Sprintf("allocated netbox %s/%d (%s) out of pool %s",
			p.desc.Endpoint, id, value, p.pool.display),
	})
}

// payload is the allocating POST's body: the provenance stamp, plus the identity.
//
// NetBox injects the allocated value (and the pool's vrf) and otherwise honours the full
// write serializer, so `custom_fields` and `tags` ride along on the atomic call.
func (p *claimPass) payload() netbox.Object {
	payload := netbox.Object{}

	owner := provenance.Owner{
		Kind: p.desc.GVK.Kind, Namespace: p.claim.GetNamespace(),
		Name: p.claim.GetName(), UID: string(p.claim.GetUID()),
	}
	target := provenance.Target{Taggable: p.desc.Taggable, CustomFields: p.desc.CustomFieldable}

	if applied, ok := p.endpoint.Provenance.Apply(payload, nil, owner, target); ok {
		p.stamped = &applied
	}

	// After the stamp, because Apply merges into whatever `custom_fields` already holds and
	// the identity is not one of the stamp's own values -- provenance.Stamp deliberately
	// never writes it, since which identity an object carries is the allocation engine's
	// answer and nobody else's.
	netbox.SetCustomField(payload, p.identityField(), p.identity)

	return payload
}

// allocationFailure re-reports the one failure that is about the pool rather than about the
// request.
func (p *claimPass) allocationFailure(err error) error {
	var exhausted *netbox.ExhaustedError
	if !errors.As(err, &exhausted) {
		metrics.AllocationsTotal.WithLabelValues(p.desc.GVK.Kind, metrics.AllocationFailed).Inc()

		return fmt.Errorf("allocating out of netbox %s/%d: %w", p.pool.endpoint, p.pool.id, err)
	}

	metrics.AllocationsTotal.WithLabelValues(p.desc.GVK.Kind, metrics.AllocationExhausted).Inc()

	// The message names the pool and states its utilisation, because a reader told only
	// "exhausted" goes and looks the prefix up by hand. The utilisation is 100% *by NetBox's
	// own answer* rather than by a count: the operator never asks how much of a pool is free,
	// since an IPv6 /64 has 2^64 addresses and any number it could obtain would be both
	// expensive and misleading (docs/concepts/claims.md).
	return refuse(netboxv1alpha1.ReasonPoolExhausted, netboxv1alpha1.EventPoolExhausted,
		"pool %s (netbox %s/%d) is fully utilised: netbox has no free %s to allocate (%s)."+
			" Widen the prefix, or free one in netbox -- this claim retries every %s, and"+
			" immediately if the pool's own CR changes",
		p.pool.display, p.pool.endpoint, p.pool.id, p.desc.ResultField, exhausted.Body, refusedRetry)
}

// verify re-reads the allocated object and proves four things about it before a single field
// of status is written.
//
// It exists because the alternative is trusting a POST response, and the cost of being wrong
// here is an address recorded in status that NetBox does not hold -- handed to a human who
// configures a NIC with it. Any failure writes nothing at all, so the identity search on the
// next pass reconciles whatever actually landed.
func (p *claimPass) verify(ctx context.Context, allocated netbox.Object, id int) (netbox.Object, string, error) {
	live, err := p.endpoint.Client.GetByID(ctx, p.desc.Endpoint, id)
	if err != nil {
		return nil, "", fmt.Errorf("verifying netbox %s/%d: %w", p.desc.Endpoint, id, err)
	}

	if live == nil {
		return nil, "", fmt.Errorf("%w: netbox %s/%d does not exist a moment after being allocated",
			errUnverified, p.desc.Endpoint, id)
	}

	want, _ := allocated[p.desc.ResultField].(string)
	got, _ := live[p.desc.ResultField].(string)

	if got == "" || got != want {
		return nil, "", fmt.Errorf("%w: netbox %s/%d answered %s=%q to the allocation and %q to the"+
			" read that followed it", errUnverified, p.desc.Endpoint, id, p.desc.ResultField, want, got)
	}

	if carried := netbox.CustomFieldOf(live, p.identityField()); carried != p.identity {
		return nil, "", fmt.Errorf("%w: netbox %s/%d carries %s=%q, not this claim's identity %q,"+
			" so a future reclaim would not find it", errUnverified, p.desc.Endpoint, id,
			p.identityField(), carried, p.identity)
	}

	if !p.insidePool(got) {
		return nil, "", fmt.Errorf("%w: netbox %s/%d holds %s=%q, which is outside the pool %s it was"+
			" allocated from", errUnverified, p.desc.Endpoint, id, p.desc.ResultField, got, p.pool.display)
	}

	return live, got, nil
}

// insidePool reports whether an allocated value is arithmetically inside the pool.
//
// Arithmetic rather than string comparison, and net/netip rather than a mask computed here:
// `10.0.2.5` is not in `10.0.20.0/24` however alike the two look, and an IPv6 pool makes the
// point for itself. It accepts both a CIDR value (`10.0.20.37/24`, what ipam.IPAddress holds)
// and a bare address.
func (p *claimPass) insidePool(value string) bool {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return p.pool.cidr.Contains(prefix.Addr())
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}

	return p.pool.cidr.Contains(addr)
}

// allocation is what one successful allocation reports: the same three strings whether it
// was allocated or reclaimed, so the two paths cannot drift apart in what they say.
type allocation struct {
	reason string
	event  string
	detail string
}

// commit writes the allocation to status. One patch, after verification, and the only place
// status.address is ever set.
func (p *claimPass) commit(
	ctx context.Context, live netbox.Object, id int, value string, out allocation,
) (ctrl.Result, error) {
	status := p.claim.ClaimStatus()

	p.claim.SetAllocated(value)
	status.NetBoxID = int64(id)
	status.AllocationIdentity = p.identity
	status.ClaimUID = string(p.claim.GetUID())
	status.AllocatedAt = &metav1.Time{Time: time.Now()}
	status.Pool = &netboxv1alpha1.AllocationPool{
		Display: p.pool.display, Endpoint: p.pool.endpoint, ID: int64(p.pool.id),
	}
	status.Provenance = p.stamped

	if url := urlOf(live); url != "" {
		status.URL = url
	}

	p.result = metrics.ResultCreated
	p.condition(netboxv1alpha1.ConditionAllocated, true, out.reason, out.detail)

	logf.FromContext(ctx).Info("allocated", "action", "allocate", "netboxID", id,
		"pool", p.pool.display, "allocationIdentity", p.identity, "reason", out.reason)
	p.engine.event(p.claim, out.event, "%s", out.detail)

	return p.ready(ctx, out.detail)
}

// settled is a claim that already holds an allocation: nothing to do, nothing to ask NetBox.
func (p *claimPass) settled(ctx context.Context, value string) (ctrl.Result, error) {
	status := p.claim.ClaimStatus()

	logf.FromContext(ctx).V(1).Info("already allocated; nothing to do",
		"action", "none", "netboxID", status.NetBoxID, "allocationIdentity", status.AllocationIdentity)

	p.result = metrics.ResultUnchanged
	p.condition(netboxv1alpha1.ConditionAllocated, true, netboxv1alpha1.ReasonAddressAllocated,
		fmt.Sprintf("netbox %s/%d holds %s", p.desc.Endpoint, status.NetBoxID, value))

	return p.ready(ctx, fmt.Sprintf("%s is allocated", value))
}

// releasing is the deletion sequence, and it makes no NetBox call at all.
//
// A claim always retains its NetBox object
// (https://github.com/ricardomolendijk/netbox-operator/issues/182), so there is nothing to
// delete and nothing that can refuse -- which means this finalizer cannot make a namespace
// undeletable, however unreachable NetBox is. What it does instead is *report*: the operator
// is about to stop tracking an object it can still name, so it says so once, with everything
// needed to find the object again, and increments a counter that outlives the Event.
//
// That report is the whole of the garbage-collection path, and it is deliberately a report.
// The operator never deletes an object it cannot prove is unused, and by the time a claim has
// handed out an address something outside Kubernetes is using it.
// No requeue on any path, which is why it returns an error alone: a claim whose finalizer is
// off is about to stop existing, and there is nothing left to come back to.
func (p *claimPass) releasing(ctx context.Context) error {
	if !controllerutil.ContainsFinalizer(p.claim, netboxv1alpha1.Finalizer) {
		logf.FromContext(ctx).V(1).Info("no finalizer of ours to release", "action", "none")

		return nil
	}

	p.result = metrics.ResultDeleted
	p.reportRetained(ctx)

	controllerutil.RemoveFinalizer(p.claim, netboxv1alpha1.Finalizer)

	if err := p.engine.Finalizers.UpdateFinalizers(ctx, p.claim); err != nil {
		return fmt.Errorf("releasing the finalizer on %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	return nil
}

// reportRetained says what is being left behind in NetBox, once.
func (p *claimPass) reportRetained(ctx context.Context) {
	status := p.claim.ClaimStatus()
	value := p.claim.Allocated()

	if status.NetBoxID == 0 && value == "" {
		// Nothing was ever allocated, so there is nothing to leave behind. Said at debug
		// because it is the ordinary outcome for a claim deleted before it could allocate.
		logf.FromContext(ctx).V(1).Info("nothing was allocated; nothing retained", "action", "release")

		return
	}

	metrics.AllocationsRetained.WithLabelValues(p.desc.GVK.Kind).Inc()

	// Info rather than debug, and an Event as well: this is the one moment the operator has
	// the identity, the id and the value in one place, and after the CR is gone there is no
	// status left to read them from.
	logf.FromContext(ctx).Info("retained the allocated netbox object", "action", "release",
		"netboxID", status.NetBoxID, "allocationIdentity", status.AllocationIdentity)

	p.engine.event(p.claim, netboxv1alpha1.EventAddressRetained,
		"netbox %s/%d (%s) was left in place and not deleted; it still carries allocation identity"+
			" %s, so re-applying this claim reclaims it -- to free it, delete the netbox object",
		p.desc.Endpoint, status.NetBoxID, value, status.AllocationIdentity)
}

// blockedOnPool reports a pool reference that did not resolve.
//
// It returns no error and often no requeue timer: the ref watch re-enqueues the claim when
// the NetBoxPrefix gains an id, and that same watch is what makes a widened prefix converge
// immediately rather than up to ten minutes later.
func (p *claimPass) blockedOnPool(ctx context.Context, err error) (ctrl.Result, error) {
	var refErr *resolver.Error
	if !errors.As(err, &refErr) {
		return p.stop(ctx, err)
	}

	out := resolver.Classify(err)
	p.result = metrics.ResultWaiting

	// The typed error's own words rather than the wrapper's: it renders as
	// `prefixRef -> netboxprefix/homelab/home-lan: not ready (...)`, which is the field the
	// user wrote and the object it pointed at.
	message := refErr.Error()

	p.condition(netboxv1alpha1.ConditionRefsResolved, false, out.Reason, message)
	p.condition(netboxv1alpha1.ConditionAllocated, false, netboxv1alpha1.ReasonAllocationPending,
		"the pool has not resolved, so nothing has been allocated")
	p.condition(netboxv1alpha1.ConditionReady, false, netboxv1alpha1.ReasonWaitingForRef, message)

	logf.FromContext(ctx).V(1).Info("waiting for the pool reference",
		"action", "stop", "reason", out.Reason, "err", err.Error())

	return p.finish(ctx, out.Requeue)
}

// stop records why this pass could not allocate, and when to try again.
//
// Every non-success exit goes through here, so the mapping from failure to condition to
// requeue exists once -- and it returns no error, because the caller's error is the claim's
// state and returning it would put controller-runtime's millisecond backoff on top of a
// requeue that was chosen deliberately. That is the whole of the no-spin rule: an exhausted
// pool waits ten minutes, not ten milliseconds.
func (p *claimPass) stop(ctx context.Context, err error) (ctrl.Result, error) {
	out := claimOutcome(err, p.resync())
	p.result = out.result

	log := logf.FromContext(ctx).WithValues("reason", out.reason, "action", "stop")

	// Read before the conditions below overwrite it: the stored condition is the only memory
	// this engine has across passes.
	changed := p.transitioned(netboxv1alpha1.ConditionReady, metav1.ConditionFalse, out.reason)

	switch {
	case out.severe && changed:
		log.Error(err, "allocation stopped")
	case out.severe:
		log.V(1).Info("allocation is still stopped", "err", err.Error())
	default:
		log.V(1).Info("allocation waiting", "err", err.Error())
	}

	if out.event != "" && changed {
		p.engine.warnClaim(p.claim, out.event, "%s", out.message(err))
	}

	// Allocated=False as well as Ready=False, and both carry the same reason. Allocated is
	// never set False *after* it has been True -- the guard in Reconcile returns long before
	// this line -- so this can only be a claim that has never allocated, which is exactly
	// what Allocated=False means.
	p.condition(netboxv1alpha1.ConditionAllocated, false, out.reason, out.message(err))
	p.condition(netboxv1alpha1.ConditionReady, false, out.reason, out.message(err))

	return p.finish(ctx, out.requeue)
}

// pending is a pass that deliberately did not allocate: a DryRun endpoint, or one whose
// driftMode is Report. Ready=False, because saying otherwise would make `kubectl wait` lie
// about an allocation that never happened.
func (p *claimPass) pending(ctx context.Context, reason, message string) (ctrl.Result, error) {
	p.result = metrics.ResultDryRun
	if reason == netboxv1alpha1.ReasonReportPending {
		p.result = metrics.ResultReported
	}

	p.condition(netboxv1alpha1.ConditionAllocated, false, netboxv1alpha1.ReasonAllocationPending, message)
	p.condition(netboxv1alpha1.ConditionReady, false, reason, message)

	return p.finish(ctx, p.resync())
}

// ready records a claim that holds its allocation.
//
// No requeue. There is nothing left to re-check: the allocation is immutable and this engine
// never re-reads it, so a timer here would be a NetBox request per claim per interval that
// can only ever conclude what status already says. Drift correction of the allocated object
// is the declarative engine's job, on the NetBoxIPAddress the claim will materialise
// (NBO-025, NBO-032).
func (p *claimPass) ready(ctx context.Context, detail string) (ctrl.Result, error) {
	p.condition(netboxv1alpha1.ConditionRefsResolved, true, netboxv1alpha1.ReasonAllResolved,
		"no unresolved references")
	p.condition(netboxv1alpha1.ConditionReady, true, netboxv1alpha1.ReasonAddressAllocated, detail)

	return p.finish(ctx, 0)
}

// resync is this endpoint's interval, used for the states that need a backstop timer.
func (p *claimPass) resync() time.Duration {
	if p.endpoint.Resync > 0 {
		return p.endpoint.Resync
	}

	return DefaultResync
}

// transitioned reports whether writing this condition would change the claim's state. Status
// and reason only, exactly as the declarative engine's does and for the same reason: a
// message is often an error's own wording, and keying on it would re-fire the Event on every
// retry.
func (p *claimPass) transitioned(condType string, status metav1.ConditionStatus, reason string) bool {
	existing := meta.FindStatusCondition(p.before.Conditions, condType)

	return existing == nil || existing.Status != status || existing.Reason != reason
}

// condition sets one condition, always stamping the generation it was observed at.
func (p *claimPass) condition(condType string, ok bool, reason, message string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(&p.claim.ClaimStatus().Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: p.claim.GetGeneration(),
	})
}

// finish is the single exit from a pass: it always sets observedGeneration, and writes
// nothing when nothing changed.
func (p *claimPass) finish(ctx context.Context, requeue time.Duration) (ctrl.Result, error) {
	status := p.claim.ClaimStatus()
	status.ObservedGeneration = p.claim.GetGeneration()

	if equality.Semantic.DeepEqual(p.before, status) && p.claim.Allocated() == p.beforeAllocated {
		logf.FromContext(ctx).V(1).Info("status unchanged; not writing", "action", "none")

		return ctrl.Result{RequeueAfter: Jitter(requeue)}, nil
	}

	if err := p.engine.Status.UpdateStatus(ctx, p.claim); err != nil {
		p.result = metrics.ResultError

		return ctrl.Result{}, fmt.Errorf("updating the status of %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	return ctrl.Result{RequeueAfter: Jitter(requeue)}, nil
}

// event records a Kubernetes Event for a claim, when there is a recorder.
func (e *ClaimEngine) event(claim Claim, reason, format string, args ...any) {
	if e.Events == nil {
		return
	}

	e.Events.Eventf(claim, "Normal", reason, format, args...)
}

// warnClaim records a Warning Event for a state that needs a human.
func (e *ClaimEngine) warnClaim(claim Claim, reason, format string, args ...any) {
	if e.Events == nil {
		return
	}

	e.Events.Eventf(claim, "Warning", reason, format, args...)
}
