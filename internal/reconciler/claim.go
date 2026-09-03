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
		return p.releasing(ctx)
	}

	// Before the guard below and before any NetBox call, so there is no ordering in which
	// the engine POSTs to an allocation endpoint without a durable finalizer already behind
	// it. A no-op on every pass after the first.
	if err := takeFinalizer(ctx, e.Finalizers, claim); err != nil {
		return ctrl.Result{}, err
	}

	// The immutability of ADR-0004 as a guard clause. This is what makes "reconcile fifty
	// times, POST once" structural rather than a property of the code below it: past this
	// line no code path in this file can allocate, whatever it finds.
	//
	// It short-circuits the *allocation*, not the whole pass. Since #167 the settled path
	// occasionally re-reads the pin -- see settled -- because "never allocate again" and
	// "never look again" are not the same promise, and the second one is what let two claims
	// hold one address after a restore with neither of them able to notice.
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

	// identityExplicit records that the identity came from spec.allocationIdentity rather
	// than from the derivation, which is the one case a reclaim has to check provenance for.
	// See refuseForeignReclaim.
	identityExplicit bool

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
	identity, explicit, err := p.allocationIdentity()
	if err != nil {
		return p.stop(ctx, err)
	}
	p.identity, p.identityExplicit = identity, explicit

	live, err := p.resolvePool(ctx)
	if err != nil {
		return p.blockedOnPool(ctx, err)
	}

	if err := p.admitPool(live); err != nil {
		return p.stop(ctx, err)
	}

	p.warnUnexpectedPool(live)

	found, err := p.findByIdentity(ctx)
	if err != nil {
		return p.stop(ctx, err)
	}

	if found != nil {
		return p.reclaim(ctx, found)
	}

	return p.allocate(ctx)
}

// allocationIdentity is this claim's identity, whether it was given rather than derived, and
// the check that there is somewhere to keep it.
//
// The check is not optional and has no override. The provenance stamp is optional for an
// ordinary object -- an unstamped object is merely unattributed -- but for a claim the
// identity store is what makes a lost HTTP response recoverable, and without it every retry
// of a POST that actually committed allocates another address. So an endpoint that cannot
// store one allocates nothing at all: zero POSTs, a condition, an Event.
func (p *claimPass) allocationIdentity() (string, bool, error) {
	stamp := p.endpoint.Provenance

	if !stamp.Applicable() || stamp.AllocationIdentityField == "" ||
		!slices.Contains(stamp.Fields, stamp.AllocationIdentityField) {
		return "", false, refuse(netboxv1alpha1.ReasonIdempotencyKeyUnavailable,
			netboxv1alpha1.EventAllocationConflict,
			"this endpoint has nowhere to store an allocation identity, so nothing was allocated:"+
				" set spec.managedBy.clusterID on the netboxendpoint and let the bootstrap create the"+
				" %q custom field, or create it by hand with type=text and filter_logic=exact on"+
				" object type %q",
			provenance.DefaultAllocationIdentityField, p.desc.ObjectType)
	}

	// The explicit override wins, for the case the derived value cannot survive by
	// construction: a claim that has been renamed and should keep its address. It is
	// returned as explicit so the reclaim path knows it is holding a value somebody typed
	// rather than one this operator computed -- see refuseForeignReclaim.
	if explicit := p.claim.ClaimSpec().AllocationIdentity; explicit != "" {
		return explicit, true, nil
	}

	return AllocationIdentity(p.endpoint.Allocator.URL(),
		p.claim.GetNamespace(), p.desc.GVK.Kind, p.claim.GetName()), false, nil
}

// owner is the CR behind this pass, as the stamp names it.
//
// The same shape as the declarative engine's pass.owner, and read by both of the two things
// that need it: the allocating POST's stamp, and the reclaim path's check that the object it
// matched is not somebody else's.
func (p *claimPass) owner() provenance.Owner {
	return provenance.Owner{
		Kind:      p.desc.GVK.Kind,
		Namespace: p.claim.GetNamespace(),
		Name:      p.claim.GetName(),
		UID:       string(p.claim.GetUID()),
	}
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
	spec, err := p.specFields()
	if err != nil {
		return netboxv1alpha1.ObjectRef{}, err
	}

	raw, ok := spec[p.desc.Pool.Spec]
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

// specFields is the claim's spec as raw JSON, field by field.
//
// One round trip through the claim's own JSON representation, which is how every part of this
// operator reads a spec it knows nothing about. The field *names* are then declared exactly
// once, on the ClaimDescriptor, and a claim kind with a field shared code has never heard of --
// NBO-064's prefixLength and size -- is a descriptor entry rather than an interface change.
func (p *claimPass) specFields() (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(p.claim)
	if err != nil {
		return nil, fmt.Errorf("encoding %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	var envelope struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, fmt.Errorf("decoding the spec of %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	return envelope.Spec, nil
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

// warnUnexpectedPool says so when the pool is one this claim kind allocates out of but did not
// expect.
//
// The other half of the container asymmetry admitPool's comment describes. A prefix claim
// expects a `container` and will carve a child out of an `active` prefix anyway, because
// subdividing a network that is already in service is unusual rather than wrong -- and the
// operator refusing it would be the operator overruling a decision somebody has already
// recorded in NetBox. So it allocates and says what it noticed, once, on the pass that
// allocated.
//
// A Warning rather than a condition: the allocation succeeded, and a claim whose Ready is True
// must not also carry a complaint that nothing can clear.
func (p *claimPass) warnUnexpectedPool(live netbox.Object) {
	if len(p.desc.PoolExpectedStatus) == 0 {
		return
	}

	status := netbox.ChoiceOf(live["status"])
	if slices.Contains(p.desc.PoolExpectedStatus, status) {
		return
	}

	p.engine.warnClaim(p.claim, netboxv1alpha1.EventPoolUnexpectedStatus,
		"pool %s (netbox %s/%d) has status %q, and %s expects one of %v;"+
			" allocating out of it anyway",
		p.pool.display, p.pool.endpoint, p.pool.id, status, p.desc.GVK.Kind,
		p.desc.PoolExpectedStatus)
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

	if err := p.refuseForeignReclaim(live, id, value); err != nil {
		return p.stop(ctx, err)
	}

	metrics.AllocationsTotal.WithLabelValues(p.desc.GVK.Kind, metrics.AllocationReclaimed).Inc()

	return p.commit(ctx, live, id, value, allocation{
		reason: netboxv1alpha1.ReasonReclaimedByIdentity,
		event:  netboxv1alpha1.EventAllocationReclaimed,
		detail: fmt.Sprintf("reclaimed netbox %s/%d (%s) by allocation identity %s%s",
			p.desc.Endpoint, id, value, p.identity, p.handover(live)),
	})
}

// refuseForeignReclaim stops a *given* identity from reclaiming any object this endpoint
// cannot attribute to this CR: one another CR is stamped as owning, and one carrying no stamp
// this endpoint can read.
//
// The gap this closes. An allocation identity is the whole of the claim engine's ownership
// proof: findByIdentity matches one custom field, and reclaim then adopts whatever came back.
// A derived identity is safe on its own, because sha256(url, namespace, kind, name) already
// contains the namespace -- no namespace can compute another's. spec.allocationIdentity is
// not: it is a free string on a CR any namespace may create, and the value it would need is
// printed in the other claim's own status.allocationIdentity and Events. So a claim in one
// namespace could name another namespace's identity, adopt its address, report it as its own,
// and -- under the default deletionPolicy: Delete -- delete the live NetBox object on the way
// out. The pool check above does not catch it: pointing at the same pool is exactly what the
// attack does.
//
// So the given case is checked and the derived case is not, and that split is the point:
// every legitimate use of the derived identity keeps working untouched, including the two this
// engine exists to support -- a cluster rebuilt from Git, and a claim deleted and re-applied
// from the same manifest. Both re-derive the same identity and both carry the same owner
// stamp, so neither reaches a verdict here.
//
// provenance.Stamp.Conflict is the judge of a stamp that *is* readable, rather than a
// comparison written here, because it already encodes which differences mean somebody else owns
// this: a foreign cluster stamp, or a foreign owner. It deliberately does *not* count a foreign
// uid whose owner still matches -- that is the re-applied manifest, and treating it as a
// conflict is how this check would have been switched off in a week.
//
// # Why an unreadable stamp is refused rather than allowed (issue #299)
//
// Conflict answers "no conflict" for an object it cannot read a stamp off, and that answer is
// only safe when "unstamped" is a fact about the object. It is not: Claim.Stamped() reads the
// live object by the field names of the endpoint *doing the reading*, and for a claim in
// another namespace that endpoint is one that namespace wrote. An endpoint whose spec.managedBy
// renames uidField, clusterField and ownerField -- while keeping allocationIdentityField, which
// has to match or findByIdentity would never locate the object -- sees every object its
// neighbour stamped as unstamped, and #271's guard evaporates. There is no ownership signal
// here that the referring endpoint cannot reconfigure, so this fails closed instead: an object
// carrying somebody's allocation identity that this endpoint cannot attribute to this CR is
// suspicious, not free.
//
// The cost is the migration case, and it is charged deliberately. A given identity pointed at a
// genuinely unstamped pre-existing NetBox object is now refused too, because from in here it is
// the same read: three empty custom fields. The refusal writes nothing, deletes nothing and
// names the one step that clears it -- stamp the object for this claim in NetBox -- so the
// migration is one manual field away rather than impossible. Do not "simplify" this back to
// returning nil on an unstamped object: that is the whole of the bypass.
//
// Only the given case pays any of this. A derived identity returns above and is untouched,
// which is every reclaim after a cluster rebuild, a re-applied manifest, or a lost status write.
func (p *claimPass) refuseForeignReclaim(live netbox.Object, id int, value string) error {
	if !p.identityExplicit {
		return nil
	}

	if conflict, found := p.endpoint.Provenance.Conflict(live, p.owner()); found {
		return refuse(netboxv1alpha1.ReasonForeignAllocation,
			netboxv1alpha1.EventForeignAllocation,
			"netbox %s/%d (%s) carries the allocation identity %s this claim's"+
				" spec.allocationIdentity names, but it is stamped as belonging to %s, so nothing was"+
				" allocated and nothing was changed. An identity that names somebody else's object is"+
				" a claim pointed at somebody else's allocation: unset spec.allocationIdentity to let"+
				" this claim derive its own, or have the owner release that object first. If that"+
				" stamp names a cr of your own that no longer exists -- this claim under an earlier"+
				" name -- hand the object over deliberately instead: %s",
			p.desc.Endpoint, id, value, p.identity, conflict.Writer(), p.stampRemedy())
	}

	if p.endpoint.Provenance.Read(live).Stamped() {
		return nil
	}

	return refuse(netboxv1alpha1.ReasonForeignAllocation,
		netboxv1alpha1.EventForeignAllocation,
		"netbox %s/%d (%s) carries the allocation identity %s this claim's"+
			" spec.allocationIdentity names, but it carries no provenance this endpoint can read,"+
			" so nothing was allocated and nothing was changed. An object this endpoint cannot"+
			" attribute is not proof of anything -- it is equally an object another namespace's"+
			" endpoint stamped under custom field names of its own choosing -- and the identity is"+
			" the only thing a reclaim matches on. To hand this object to this claim, %s; to let"+
			" this claim allocate a new object instead, unset spec.allocationIdentity",
		p.desc.Endpoint, id, value, p.identity, p.stampRemedy())
}

// stampRemedy is the manual step that makes an unattributable object attributable to this
// claim: the custom field to set in NetBox, and what to set it to.
//
// Rendered from the endpoint's own configuration rather than from the default names, because
// the endpoint that refuses is the endpoint that will read it back, and naming k8s_owner to
// somebody who renamed it sends them to a field the guard does not look at. The fields are
// switchable one by one, so the first one that is switched on is the one named -- and an
// endpoint with all three off can attribute nothing at all, which is a spec.managedBy to fix
// rather than a NetBox object.
func (p *claimPass) stampRemedy() string {
	stamp := p.endpoint.Provenance

	switch {
	case stamp.OwnerField != "":
		return fmt.Sprintf("set its custom field %s to %q in netbox", stamp.OwnerField, p.owner().Ref())
	case stamp.ClusterField != "":
		return fmt.Sprintf("set its custom field %s to %q in netbox", stamp.ClusterField, stamp.ClusterID)
	case stamp.UIDField != "":
		return fmt.Sprintf("set its custom field %s to %q in netbox", stamp.UIDField, p.claim.GetUID())
	default:
		return "give this endpoint's spec.managedBy an ownerField -- with uidField, clusterField" +
			" and ownerField all switched off it cannot attribute any object to any cr"
	}
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

	payload, err := p.payload()
	if err != nil {
		return p.stop(ctx, err)
	}

	if err := p.admitRequest(payload); err != nil {
		return p.stop(ctx, err)
	}

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

// payload is the allocating POST's body: the provenance stamp, the identity, and this claim
// kind's allocation parameters.
//
// NetBox injects the allocated value (and the pool's vrf) and otherwise honours the full
// write serializer, so `custom_fields` and `tags` ride along on the atomic call.
func (p *claimPass) payload() (netbox.Object, error) {
	payload := netbox.Object{}

	target := provenance.Target{Taggable: p.desc.Taggable, CustomFields: p.desc.CustomFieldable}

	if applied, ok := p.endpoint.Provenance.Apply(payload, nil, p.owner(), target); ok {
		p.stamped = &applied
	}

	// After the stamp, because Apply merges into whatever `custom_fields` already holds and
	// the identity is not one of the stamp's own values -- provenance.Stamp deliberately
	// never writes it, since which identity an object carries is the allocation engine's
	// answer and nobody else's.
	netbox.SetCustomField(payload, p.identityField(), p.identity)

	if err := p.requestFields(payload); err != nil {
		return nil, err
	}

	return payload, nil
}

// requestFields copies this claim kind's allocation parameters out of the spec and into the
// body.
//
// The parameters, not the desired state: `prefix_length` on a prefix claim is what makes the
// request a request, and there is no version of the call that omits it. An absent optional one
// is left out rather than sent empty -- the wire default and "not set" are the same thing for
// every parameter here, and a claim kind whose parameter had a third state would need the
// three-state treatment docs/concepts/field-ownership.md gives an ordinary field.
//
// Values pass through as the JSON they already are, so an integer stays an integer: NetBox's
// PrefixLengthSerializer rejects `"26"` by type, not by value, and a quoted number would be a
// 400 nobody could read.
func (p *claimPass) requestFields(payload netbox.Object) error {
	if len(p.desc.RequestFields) == 0 {
		return nil
	}

	spec, err := p.specFields()
	if err != nil {
		return err
	}

	for _, field := range p.desc.RequestFields {
		raw, ok := spec[field.Spec]
		if !ok {
			continue
		}

		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("decoding %s of %s/%s: %w",
				field.Spec, p.claim.GetNamespace(), p.claim.GetName(), err)
		}

		payload[field.API] = value
	}

	return nil
}

// admitRequest refuses a request the resolved pool cannot satisfy, before the POST rather than
// after it.
//
// One check, and it is the mask length: a claim asking for a prefix at least as short as its
// parent's. Data-driven through ClaimDescriptor.RequestLengthField, because a claim kind
// without a mask length in its request has nothing to check and the pool it would be checked
// against is resolved at runtime -- CEL sees neither.
//
// The point of doing it here is the count of POSTs. `prefixLength: 16` on a /16 parent is
// accepted by NetBox: `available-prefixes` subtracts the child prefixes from the parent and
// hands out what is left, which for an empty parent is the parent, so the claim would get a
// second /16 identical to the first and report success. Refusing costs zero requests and says
// which two numbers disagree.
func (p *claimPass) admitRequest(payload netbox.Object) error {
	if p.desc.RequestLengthField == "" {
		return nil
	}

	length, ok := netbox.IntOf(payload[p.desc.RequestLengthField])
	if !ok {
		// Nothing to check rather than an error: the field is required by the CRD and by
		// NetBox's own serializer, and a missing one is reported by whichever of them the
		// request reaches first.
		return nil
	}

	family := p.pool.cidr.Addr().BitLen()

	if length > family {
		return refuse(netboxv1alpha1.ReasonInvalid, netboxv1alpha1.EventInvalid,
			"%s is %d, which is not a mask length for the family of pool %s (%d bits);"+
				" nothing was allocated", p.desc.RequestLengthField, length, p.pool.display, family)
	}

	if length > p.pool.cidr.Bits() {
		return nil
	}

	return refuse(netboxv1alpha1.ReasonInvalid, netboxv1alpha1.EventInvalid,
		"%s is %d and the pool %s is a /%d, so the request is not smaller than the pool it would"+
			" come out of -- netbox would hand out the pool itself and this claim would hold a"+
			" duplicate of it; nothing was allocated",
		p.desc.RequestLengthField, length, p.pool.display, p.pool.cidr.Bits())
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

	// The read-after-write this pass already did proves the same three facts the periodic
	// verification checks -- the object exists, holds this value, and carries this identity --
	// so dating it here is what stops a freshly allocated claim from re-reading NetBox a
	// moment later to be told what it has just been told. The reclaim path is the same read
	// by a different route and gets the same credit.
	p.datePinVerification()

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

	// The interval, not zero: a claim that has just allocated is a settled claim, and the
	// timer is what brings it back to re-read its pin (settled).
	return p.ready(ctx, out.detail, p.resync())
}

// settled is a claim that already holds an allocation: nothing to do, and -- once per
// verification interval -- one question to ask NetBox.
//
// # Why this path reads at all (issue #167)
//
// It used to read nothing, ever, and that was load-bearing: it is what makes "reconcile fifty
// times, POST once" structural. The cost was that status.address was a pin nothing ever
// re-checked. Restore NetBox from a snapshot predating an allocation and the restored
// database has no record of it, so it offers that address to the next claim that asks --
// while the claim already holding it goes on reporting Ready=True, indefinitely, because it
// never looks. Two claims, one address, and neither of them can tell. A hazard nobody can
// detect is not a hazard anyone can avoid, which is why this is a guard in code rather than a
// row in a runbook.
//
// # Why once per interval, and not per pass
//
// A read per pass would put a NetBox request behind every wake-up, and a settled claim is
// woken by more than the clock: its own status writes, its pool changing, every informer
// resync. So the schedule is on the clock instead -- status.lastVerifiedAt -- and the
// interval is the endpoint's own resyncPeriod, which is already the rate at which this same
// endpoint re-reads every NetBoxObject in the cluster. A claim costs one indexed GET per
// interval on top of that, and fifty passes inside one interval still cost nothing.
//
// # Why this does not break quiescence
//
// The e2e gate asserts **zero mutating requests** across two resync periods
// (docs/operations/e2e.md, harness.mutatingMethods = POST, PATCH, DELETE). Nothing below can
// reach any of the three: the verification reads, compares, and writes its verdict to the
// CR's status. Quiescence is a promise about what this operator writes to NetBox, not a
// promise that it stops looking -- the declarative engine has re-read every object it owns
// every resync since it existed, and passes the same gate.
func (p *claimPass) settled(ctx context.Context, value string) (ctrl.Result, error) {
	// Resolved here rather than in Reconcile, and from the informer cache rather than over
	// the wire: the interval below is this endpoint's, and reading it must not be able to
	// fail. A miss leaves p.endpoint zero -- no client, so no verification -- which is
	// exactly what this path did before the verification existed, and is what keeps the
	// older promise that an already-allocated claim settles while NetBox is unreachable.
	if endpoint, ok := p.engine.Endpoints.Endpoint(
		ctx, p.claim.GetNamespace(), p.claim.ClaimSpec().EndpointRef); ok {
		p.endpoint = endpoint
	}

	if wait, due := p.pinVerification(time.Now()); !due {
		logf.FromContext(ctx).V(1).Info("already allocated; nothing to do",
			"action", "none", "netboxID", p.claim.ClaimStatus().NetBoxID,
			"allocationIdentity", p.claim.ClaimStatus().AllocationIdentity)

		return p.pinned(ctx, value, wait)
	}

	return p.verifyPin(ctx, value)
}

// pinVerification is how long until this claim re-reads its pin, and whether that is now.
func (p *claimPass) pinVerification(now time.Time) (time.Duration, bool) {
	every := p.resync()

	if !p.canVerifyPin() {
		return every, false
	}

	// Never verified: a claim allocated before this operator carried the check, or one whose
	// allocating pass could not date it. Due immediately, which on an upgrade is one extra
	// indexed GET per settled claim, once -- the same shape as the declarative engine's first
	// sweep over every object it owns.
	last := p.claim.ClaimStatus().LastVerifiedAt
	if last == nil {
		return 0, true
	}

	if elapsed := now.Sub(last.Time); elapsed < every {
		return every - elapsed, false
	}

	return 0, true
}

// canVerifyPin reports whether there is anything to verify against.
//
// Three inputs, none of them this pass's to produce: a client, which means the endpoint is
// Ready; the identity the allocated object was stamped with, which the allocating pass wrote
// to status; and the custom field it lives in, which is spec.managedBy.allocationIdentityField
// and is renameable -- searching a hard-coded k8s_allocation_identity on an endpoint that
// renamed it would find nothing and report every settled claim in that namespace as lost.
//
// Missing any of the three, the answer is silence rather than a verdict. An object this
// endpoint cannot search for is not an object that is gone.
func (p *claimPass) canVerifyPin() bool {
	return p.endpoint.Client != nil &&
		p.claim.ClaimStatus().AllocationIdentity != "" &&
		p.identityField() != ""
}

// verifyPin asks the question the settled path never used to ask: is the value in status
// still held, in NetBox, by an object carrying this claim's allocation identity?
//
// By identity and not by id. status.netboxID is a primary key, and a restored database
// reissues those to other objects -- a read by id can be answered by a stranger. The identity
// is this engine's ownership proof everywhere else in this file, it is indexed, and it is
// what a restore cannot fabricate. findByIdentity is that query already, so this reuses a
// mechanism rather than adding one, down to inheriting its verdict on two objects sharing one
// identity.
//
// # Where this sits next to refuseForeignReclaim (#306)
//
// That guard decides whether a claim may *adopt* an object it found, and since #306 it fails
// closed on a stamp it cannot read, because the endpoint doing the reading can rename the
// stamp fields out from under its neighbour. This one adopts nothing and grants nothing: its
// only possible effect is to take Ready away. So it does not consult a stamp, and it cannot
// contradict that guard -- there is no state in which this reports an allocation sound that
// refuseForeignReclaim would have refused, because refusing is not an outcome available here.
// The identity it compares is the one this operator wrote into NetBox itself.
//
// Deliberately *not* checked: whether the object carrying the identity still has the id in
// status.netboxID. That id is the deletion path's input and #304 is in flight there; the
// identity is the ownership proof, so an object that carries it holds this claim's allocation
// whatever its id has become.
func (p *claimPass) verifyPin(ctx context.Context, value string) (ctrl.Result, error) {
	p.identity = p.claim.ClaimStatus().AllocationIdentity

	held, err := p.findByIdentity(ctx)

	var refused *refusal
	switch {
	case errors.As(err, &refused):
		return p.contested(ctx, netboxv1alpha1.ReasonAllocationConflict,
			netboxv1alpha1.EventAllocationConflict, "%s", refused.Error())
	case err != nil:
		return p.unverifiable(ctx, value, err)
	}

	if held != nil {
		if got, _ := held[p.desc.ResultField].(string); got == value {
			return p.verified(ctx, value)
		}
	}

	return p.pinNotHeld(ctx, value, held)
}

// pinNotHeld reports a pin this claim's own object no longer backs, after finding out who
// holds the value now.
//
// The second read is the difference between a hazard a human is told about and one they can
// act on: the remedy for one address under two claims is choosing which of them keeps it, and
// that choice needs the other object's id and identity. It costs nothing in the steady state,
// because it only runs on a pass that has already established something is wrong.
//
// carrier is the object that does carry this claim's identity while holding some *other*
// value -- rare, and worth naming when it happens, because it says the allocation moved
// rather than vanished.
func (p *claimPass) pinNotHeld(
	ctx context.Context, value string, carrier netbox.Object,
) (ctrl.Result, error) {
	holder, err := p.endpoint.Client.GetOne(ctx, p.desc.Endpoint,
		netbox.Params{}.Match(p.desc.ResultField, netbox.LookupExact, value))

	var ambiguous *netbox.AmbiguousError

	switch {
	case errors.As(err, &ambiguous):
		// Matches() rather than Error(), because the sentence already names the endpoint and
		// the value the query was for and saying either twice reads worse for saying more.
		return p.contested(ctx, netboxv1alpha1.ReasonAllocationConflict,
			netboxv1alpha1.EventAllocationConflict,
			"several netbox %s hold %s=%q -- %s -- and %s carries this claim's allocation identity"+
				" %s, so this claim cannot prove any of them is its own. %s",
			p.desc.Endpoint, p.desc.ResultField, value, ambiguous.Matches(),
			p.pinPreamble(carrier), p.identity, pinRemedy)
	case err != nil:
		// The read that found the problem succeeded; this one did not. A verdict is published
		// only when both halves of it are known, so the pin stands and the next pass -- a
		// transient tier away, not a whole interval -- asks again.
		return p.unverifiable(ctx, value, err)
	}

	if holder == nil {
		return p.contested(ctx, netboxv1alpha1.ReasonAllocationLost,
			netboxv1alpha1.EventAllocationLost,
			"no netbox %s holds %s=%q and none carries this claim's allocation identity %s%s, so"+
				" the allocation this claim reports is not in netbox. A netbox restored from a"+
				" snapshot predating the allocation is the usual cause, and the value is free for"+
				" netbox to hand to the next claim that asks. %s",
			p.desc.Endpoint, p.desc.ResultField, value, p.identity, p.pinCarrier(carrier), pinRemedy)
	}

	id, _ := holder.ID()

	return p.contested(ctx, netboxv1alpha1.ReasonAllocationConflict,
		netboxv1alpha1.EventAllocationConflict,
		"netbox %s/%d holds %s=%q, which this claim reports as its own, and %s rather than this"+
			" claim's %s%s. Nothing was allocated, changed or deleted: both claims to that value"+
			" may be in service, and only a human knows which. %s",
		p.desc.Endpoint, id, p.desc.ResultField, value, p.holderIdentity(holder), p.identity,
		p.pinCarrier(carrier), pinRemedy)
}

// pinRemedy is the sentence every verification verdict ends on.
//
// It leads with the trap rather than with the fix, because the instinct -- delete the losing
// claim and re-apply it -- is the one move that can destroy the allocation the operator
// decided to keep: a claim frees its address by DELETEing the object at status.netboxID
// unconditionally, and a restored NetBox has reissued that id to something else
// (docs/operations/gitops.md, "restoring NetBox from backup").
const pinRemedy = "Before deleting this claim, set spec.deletionPolicy: Retain on it or remove" +
	" the netbox object by hand: deleting it as it stands DELETEs the object at its" +
	" status.netboxID, and a restored netbox may have reissued that id to somebody else's" +
	" allocation. This claim re-checks on its own and clears the condition if the object comes" +
	" back."

// pinPreamble names the object carrying this claim's identity, for the ambiguous-holder
// message where there is no single holder to name instead.
func (p *claimPass) pinPreamble(carrier netbox.Object) string {
	if carrier == nil {
		return "no netbox object"
	}

	id, _ := carrier.ID()

	return fmt.Sprintf("netbox %s/%d", p.desc.Endpoint, id)
}

// pinCarrier says where the claim's own allocation went, when it went somewhere rather than
// nowhere. Empty for the ordinary case, so the common message does not carry a clause about
// a state it is not in.
func (p *claimPass) pinCarrier(carrier netbox.Object) string {
	if carrier == nil {
		return ""
	}

	id, _ := carrier.ID()
	elsewhere, _ := carrier[p.desc.ResultField].(string)

	return fmt.Sprintf(" (the object that does carry it, netbox %s/%d, holds %s=%q instead)",
		p.desc.Endpoint, id, p.desc.ResultField, elsewhere)
}

// holderIdentity is how the message names what the current holder is, which is the fact the
// verdict turns on. An object with no identity this endpoint can read is not this claim's
// either -- it is equally a row a restore brought back and a hand-made object.
func (p *claimPass) holderIdentity(holder netbox.Object) string {
	identity := netbox.CustomFieldOf(holder, p.identityField())
	if identity == "" {
		return fmt.Sprintf("carries no %s this endpoint can read", p.identityField())
	}

	return fmt.Sprintf("carries allocation identity %s", identity)
}

// errPinNotVerified is what the log carries for a verification verdict. A sentinel rather
// than the message, so the log's error is a type and the wording is a value, exactly as every
// other classified failure in this package is.
var errPinNotVerified = errors.New("the allocation this claim reports is not its own")

// contested reports a pin NetBox no longer backs, and changes nothing else.
//
// Deliberately not stop(). stop() also sets Allocated=False, which is right for a claim that
// has never allocated and wrong for every claim that reaches here: Allocated is a historical
// fact, once True never False (ADR-0004), and this condition is about who holds the
// allocation now rather than about whether it happened. Ready is what goes False, because
// Ready is what `kubectl wait` and every dependent workload read.
//
// Nothing is written to NetBox on any path below -- no re-allocation, no adoption, no
// delete. Both claims over one address are entitled to it and only a human knows which NIC,
// DNS record or firewall rule is already configured with it; an operator that picked one
// would be converting a bookkeeping accident into a live network change.
func (p *claimPass) contested(
	ctx context.Context, reason, event, format string, args ...any,
) (ctrl.Result, error) {
	p.datePinVerification()
	p.result = metrics.ResultError

	message := fmt.Sprintf(format, args...)

	// Read before the condition below overwrites it: an Event and an error-level line on
	// every pass of a state that lasts until somebody changes NetBox would be one page of the
	// same sentence per claim per interval.
	changed := p.transitioned(netboxv1alpha1.ConditionReady, metav1.ConditionFalse, reason)

	log := logf.FromContext(ctx).WithValues("action", "stop", "reason", reason,
		"netboxID", p.claim.ClaimStatus().NetBoxID, "allocationIdentity", p.identity)

	if changed {
		log.Error(errPinNotVerified, "the allocation could not be verified", "detail", message)
		p.engine.warnClaim(p.claim, event, "%s", message)
	} else {
		log.V(1).Info("the allocation still could not be verified", "detail", message)
	}

	p.condition(netboxv1alpha1.ConditionReady, false, reason, message)

	return p.finish(ctx, refusedRetry)
}

// verified is NetBox agreeing with the pin. The one outcome that costs a read and says
// nothing.
func (p *claimPass) verified(ctx context.Context, value string) (ctrl.Result, error) {
	p.datePinVerification()

	logf.FromContext(ctx).V(1).Info("the allocation is still this claim's",
		"action", "none", "netboxID", p.claim.ClaimStatus().NetBoxID,
		"allocationIdentity", p.identity)

	return p.pinned(ctx, value, p.resync())
}

// unverifiable is a verification that could not be carried out. The pin stands.
//
// "Could not ask" is not "the answer is no". Flipping every claim in a cluster to Ready=False
// because NetBox is restarting would make this guard the outage it exists to prevent, and the
// settled path's older promise -- an already-allocated claim settles while NetBox is
// unreachable -- is not one #167 has any reason to spend.
//
// status.lastVerifiedAt is deliberately left where it was, so the next pass tries again
// rather than waiting out a full interval on the strength of a request that failed. The
// requeue comes from the shared outcome table, so an unreachable NetBox, a 429 and an expired
// token are waited out here exactly as long as they are everywhere else.
func (p *claimPass) unverifiable(ctx context.Context, value string, cause error) (ctrl.Result, error) {
	logf.FromContext(ctx).V(1).Info("could not verify the allocation; keeping it",
		"action", "none", "netboxID", p.claim.ClaimStatus().NetBoxID, "err", cause.Error())

	return p.pinned(ctx, value, classify(cause, p.resync()).requeue)
}

// datePinVerification records that a verification ran, which is what makes the next one an
// interval away rather than a wake-up away.
//
// On the verdict paths as well as the clean one. A contested claim that re-read NetBox on
// every wake-up would become a poll -- and its own status write is one of the things that
// wakes it.
func (p *claimPass) datePinVerification() {
	now := metav1.Now()
	p.claim.ClaimStatus().LastVerifiedAt = &now
}

// pinned is a settled claim reporting what it holds: the whole of what this path did on every
// pass before the verification existed, and what it still does on every pass the interval has
// not run out on.
func (p *claimPass) pinned(
	ctx context.Context, value string, requeue time.Duration,
) (ctrl.Result, error) {
	status := p.claim.ClaimStatus()

	p.result = metrics.ResultUnchanged
	p.condition(netboxv1alpha1.ConditionAllocated, true, netboxv1alpha1.ReasonAddressAllocated,
		fmt.Sprintf("netbox %s/%d holds %s", p.desc.Endpoint, status.NetBoxID, value))

	return p.ready(ctx, fmt.Sprintf("%s is allocated", value), requeue)
}

// claimDeleteAttempts bounds the retry on a delete that will not go through, after which the
// claim releases its finalizer anyway.
//
// Eleven, with protectedBackoff's intervals, is a little over eighteen minutes of trying. The
// number is a judgement rather than a derivation: long enough that a NetBox restart, a
// certificate rotation or a dependent object being deleted in the same sweep all resolve
// inside it, short enough that a `kubectl delete namespace` is not indistinguishable from a
// hang. Raising it trades namespaces that take longer to delete for fewer leaked addresses,
// and lowering it the other way.
//
// It was eight, which was the same eighteen minutes at the ten-second base protectedRetryBase
// used to have; both moved together in #289 so that the bound stays a bound on time. What
// #289 also fixed is that the count was not being spent on *attempts* at all -- every wake-up
// of a blocked claim spent one, so all of them went inside a millisecond and the address was
// reported retained before anything had a chance to unblock.
const claimDeleteAttempts = 11

// claimRelease is the finalizer coming off a claim, and what to say about it.
type claimRelease struct {
	// event is the Event reason recorded for it. Empty says nothing, which is the right
	// amount to say about a claim that never allocated.
	event string

	// message is what the Event says.
	message string

	// warn marks a release that leaves an allocation behind in NetBox. A human has to see
	// that; the rest is routine.
	warn bool

	// retained marks one that AllocationsRetained should also count, so that "how many has
	// this cluster left behind" stays answerable long after the Event has aged out.
	retained bool
}

// releasing is the deletion sequence: it frees the allocated object, or says why it did not.
//
// #213 shipped this pass making zero NetBox calls, and said why -- "this finalizer cannot
// wedge a namespace", however unreachable NetBox is, because there was nothing to call and
// nothing that could refuse. #225 spends that property: a Delete policy necessarily puts a
// DELETE on the deletion path. So it is bought back deliberately rather than assumed, and
// deleteBlocked is where.
//
// The order of the steps is the design, exactly as it is in finalizer.go: everything that
// needs no NetBox call is answered first, so a Retain claim, a claim someone has written the
// break-glass annotation on, and a claim that never got as far as allocating all complete
// while NetBox is unreachable. An escape hatch that only works when it is not needed is not
// an escape hatch.
func (p *claimPass) releasing(ctx context.Context) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(p.claim, netboxv1alpha1.Finalizer) {
		logf.FromContext(ctx).V(1).Info("no finalizer of ours to release", "action", "none")

		return ctrl.Result{}, nil
	}

	p.result = metrics.ResultDeleted

	if out, ok := p.releaseWithoutDeleting(); ok {
		return p.release(ctx, out)
	}

	// Before both blocked paths below, because both of them count against the bound: this is
	// what keeps eleven attempts eleven attempts *over eighteen minutes* rather than eleven
	// passes in as many milliseconds, each woken by the status write the last one made
	// (#289, deletionHold). Without it a claim deleted while its endpoint is briefly not
	// Ready reports its address retained before the endpoint has had a chance to come back.
	status := p.claim.ClaimStatus()
	if remaining, holding := deletionHold(
		status.DeletionAttempts, status.LastDeletionAttempt, time.Now(),
	); holding {
		logf.FromContext(ctx).V(1).Info("holding off the next delete attempt",
			"action", "delete", "netboxID", status.NetBoxID, "in", remaining.String())

		return p.finish(ctx, remaining)
	}

	endpoint, ok := p.engine.Endpoints.Endpoint(ctx,
		p.claim.GetNamespace(), p.claim.ClaimSpec().EndpointRef)
	if !ok {
		// Blocking rather than leaking, for as long as the bound allows. The allocated
		// object is real, its id is known, and it will still be deletable when the endpoint
		// comes back.
		p.endpoint = Endpoint{}

		return p.deleteBlocked(ctx, netboxv1alpha1.ReasonWaitingForEndpoint,
			fmt.Errorf("%w: netboxendpoint %q in namespace %q", errEndpointNotReady,
				p.claim.ClaimSpec().EndpointRef, p.claim.GetNamespace()))
	}
	p.endpoint = endpoint

	return p.free(ctx, int(p.claim.ClaimStatus().NetBoxID))
}

// releaseWithoutDeleting reports the cases where a claim's finalizer comes off with no NetBox
// call at all.
func (p *claimPass) releaseWithoutDeleting() (claimRelease, bool) {
	status := p.claim.ClaimStatus()

	// First, and unconditionally: the break-glass overrides every other consideration,
	// including a delete that would otherwise be attempted. The same annotation and the same
	// Event as every other kind (finalizer.go) -- a human who has decided to accept an
	// orphan should not have to know which of the two engines owns the CR.
	if p.claim.GetAnnotations()[netboxv1alpha1.SkipFinalizerAnnotation] == "true" {
		return claimRelease{
			event: netboxv1alpha1.EventFinalizerSkipped,
			message: fmt.Sprintf(
				"%s=true: dropping the finalizer without calling netbox, so %s/%d is left behind",
				netboxv1alpha1.SkipFinalizerAnnotation, p.desc.Endpoint, status.NetBoxID),
			warn:     true,
			retained: status.NetBoxID != 0,
		}, true
	}

	if deletionPolicyOf(p.claim.ClaimSpec().DeletionPolicy) ==
		netboxv1alpha1.DeletionRetain {
		return claimRelease{
			event:    netboxv1alpha1.EventAddressRetained,
			message:  p.retainedMessage("spec.deletionPolicy is Retain"),
			retained: true,
		}, true
	}

	// Nothing was ever allocated, so there is nothing to free, nothing to leave behind and no
	// endpoint to wait for. This is the ordinary outcome for a claim deleted before it could
	// allocate, or while its pool was still unresolved, and it must not need NetBox.
	if status.NetBoxID == 0 && p.claim.Allocated() == "" {
		return claimRelease{}, true
	}

	// An id the operator does not have is an id it will not go looking for. It could search
	// by allocation identity -- that is exactly what the reclaim path does -- but doing it
	// here would mean issuing a DELETE against whatever a search happened to return at
	// deletion time, on the strength of a status write that is known to have failed. The
	// allocated value in status is the lead a human needs; the identity is how they find it.
	if status.NetBoxID == 0 {
		return claimRelease{
			event: netboxv1alpha1.EventAddressRetained,
			message: fmt.Sprintf("no netbox object is recorded in status.netboxID, so %s was not freed;"+
				" either the allocation never completed, or it did and the status write recording its"+
				" id did not. An object carrying allocation identity %q may be left behind",
				p.claim.Allocated(), status.AllocationIdentity),
			warn:     true,
			retained: true,
		}, true
	}

	return claimRelease{}, false
}

// retainedMessage is what an Event says about an allocation left in NetBox.
//
// One wording for every reason an allocation is left behind -- a deliberate Retain, a
// non-writing endpoint, a delete the operator gave up on -- because a human's next question is
// the same in all three: which address, which id, which identity. The reason is the prefix.
func (p *claimPass) retainedMessage(why string) string {
	status := p.claim.ClaimStatus()

	return fmt.Sprintf("%s: netbox %s/%d (%s) was left in place and not deleted; it still carries"+
		" allocation identity %s, so re-applying this claim reclaims it -- to free it, delete the"+
		" netbox object",
		why, p.desc.Endpoint, status.NetBoxID, p.claim.Allocated(), status.AllocationIdentity)
}

// free issues the DELETE that gives the allocated object back to its pool.
//
// The same four answers as the declarative engine's deleteObject, in the same shape and for
// the same reasons (finalizer.go): a clean delete and a 404 both release, a refusal and
// everything else keep the finalizer and come back later. The one difference is that "later"
// is bounded here -- see deleteBlocked.
func (p *claimPass) free(ctx context.Context, id int) (ctrl.Result, error) {
	deleted, err := p.endpoint.Client.Delete(ctx, p.desc.Endpoint, id)

	var notFound *netbox.NotFoundError
	var protected *netbox.ProtectedError

	switch {
	case err == nil:
		return p.release(ctx, p.freed(id, deleted))
	// Already gone is the end state the claim asked for, reached by somebody else. Calling it
	// a failure would keep the finalizer on forever waiting for a delete that can never
	// succeed, because there is nothing left to delete.
	case errors.As(err, &notFound):
		return p.release(ctx, claimRelease{
			event: netboxv1alpha1.EventDeleted,
			message: fmt.Sprintf("netbox %s/%d (%s) was already gone",
				p.desc.Endpoint, id, p.claim.Allocated()),
		})
	case errors.As(err, &protected):
		return p.deleteBlocked(ctx, netboxv1alpha1.ReasonProtected, err)
	}

	// Everything else is about NetBox's availability rather than about this object, so the
	// existing table picks the reason and the finalizer stays on.
	return p.deleteBlocked(ctx, classify(err, p.resync()).reason, err)
}

// freed describes a delete that came back clean.
//
// A suppressed answer is a client that sent nothing, and it is read off the answer rather than
// from the endpoint's mode -- exactly as the declarative engine reads it, and this is what
// makes driftMode Report and mode DryRun honour themselves here for free. Both reach the
// engine as a client that physically cannot mutate NetBox (netboxendpoint_controller.go's
// clientMode), so there is no `if` on this path to forget: the DELETE is the same call, it just
// never leaves the process. Carrying the mode alongside would be a second source of truth for
// one fact, and whichever of the two drifted would have the Event claim a deletion that never
// happened.
func (p *claimPass) freed(id int, out netbox.Object) claimRelease {
	if netbox.Suppressed(out) {
		return claimRelease{
			event: netboxv1alpha1.EventAddressRetained,
			message: p.retainedMessage(fmt.Sprintf(
				"the endpoint sends no writes (driftMode Report or mode DryRun), so nothing was sent:"+
					" would have deleted netbox %s/%d", p.desc.Endpoint, id)),
			warn:     true,
			retained: true,
		}
	}

	return claimRelease{
		event: netboxv1alpha1.EventDeleted,
		message: fmt.Sprintf("freed netbox %s/%d (%s), which is available for reallocation again",
			p.desc.Endpoint, id, p.claim.Allocated()),
	}
}

// deleteBlocked is a delete that did not go through: when to try again, or the decision to
// stop trying.
//
// Not an error return, for the reason finalizer.go's protected() gives: an error puts
// controller-runtime's own backoff on top of the interval chosen here, and it reports a state
// that needs a human -- or needs somebody else to delete something -- as a controller failure.
//
// The give-up is the whole reason a Delete default is safe to ship. The declarative engine
// keeps the finalizer forever on a delete NetBox will not accept, and its escape hatch is a
// human writing the skip annotation onto the CR; a claim cannot afford that, because #174's
// inline claimFrom means the claims in a namespace are created by machinery rather than by
// hand and a namespace full of them would have to be unwedged one CR at a time. So past
// claimDeleteAttempts the claim releases anyway and reports the allocation as retained.
//
// That degradation is deliberately *the outcome #213 already shipped*: the address is left
// allocated, the AddressRetained Event names it and the retained counter counts it. Trading a
// better default for a leak the operator already knows how to report is a trade; trading it
// for a namespace that will not delete is a new failure mode, and that is not a price this
// change is allowed to charge.
func (p *claimPass) deleteBlocked(ctx context.Context, reason string, cause error) (ctrl.Result, error) {
	status := p.claim.ClaimStatus()
	status.DeletionAttempts++
	p.result = metrics.ResultWaiting

	// The time goes with the count, for the reason deletionHold gives: a count nothing can
	// date is a count every wake-up increments.
	now := metav1.Now()
	status.LastDeletionAttempt = &now

	if status.DeletionAttempts >= claimDeleteAttempts {
		return p.release(ctx, claimRelease{
			event: netboxv1alpha1.EventAddressRetained,
			message: p.retainedMessage(fmt.Sprintf(
				"gave up trying to delete it after %d attempts, the last of which said: %v",
				status.DeletionAttempts, cause)),
			warn:     true,
			retained: true,
		})
	}

	wait := protectedBackoff(status.DeletionAttempts)

	// Once, at the threshold. An Event per attempt is noise at cluster scale; none at all
	// makes a stuck deletion silent, which is worse. NetBox's own words are carried through
	// verbatim, because "cannot delete" without a reason is the worst possible operator
	// experience.
	if status.DeletionAttempts == protectedEventAfter {
		p.engine.warnClaim(p.claim, netboxv1alpha1.EventDeleteBlocked,
			"netbox %s/%d has not been deleted after %d attempts: %v. After %d it will be left"+
				" allocated and the finalizer released",
			p.desc.Endpoint, status.NetBoxID, status.DeletionAttempts, cause, claimDeleteAttempts)
	}

	logf.FromContext(ctx).Info("freeing the allocation is blocked",
		"action", "delete", "reason", reason, "netboxID", status.NetBoxID,
		"attempt", status.DeletionAttempts, "cause", cause.Error())

	p.condition(netboxv1alpha1.ConditionDeleting, false, reason,
		fmt.Sprintf("%v; attempt %d of %d, retrying in %s",
			cause, status.DeletionAttempts, claimDeleteAttempts, wait))

	return p.finish(ctx, wait)
}

// release removes the finalizer, which is what lets Kubernetes finish deleting the claim.
//
// The Event is emitted after the removal has been accepted, not before, exactly as the
// declarative engine does it: an Event announcing a release that then failed to persist is a
// record of something that did not happen. No status is written either -- the claim is about
// to stop existing, so a status update races the delete and nothing would ever read it. The
// Event is the record that outlives the CR, which is why the retained ones carry the address,
// the id and the identity rather than a pointer to a status nobody can read any more.
//
// No requeue on any path: a claim whose finalizer is off is about to stop existing, and there
// is nothing left to come back to.
func (p *claimPass) release(ctx context.Context, out claimRelease) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(p.claim, netboxv1alpha1.Finalizer)

	if err := p.engine.Finalizers.UpdateFinalizers(ctx, p.claim); err != nil {
		controllerutil.AddFinalizer(p.claim, netboxv1alpha1.Finalizer)

		return ctrl.Result{}, fmt.Errorf("releasing the finalizer on %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	if out.retained {
		metrics.AllocationsRetained.WithLabelValues(p.desc.GVK.Kind).Inc()
	}

	if out.event == "" {
		logf.FromContext(ctx).V(1).Info("nothing was allocated; nothing to free",
			"action", "release")

		return ctrl.Result{}, nil
	}

	logf.FromContext(ctx).Info("released the finalizer",
		"action", "release", "reason", out.event, "detail", out.message,
		"netboxID", p.claim.ClaimStatus().NetBoxID)

	if out.warn {
		p.engine.warnClaim(p.claim, out.event, "%s", out.message)

		return ctrl.Result{}, nil
	}

	p.engine.event(p.claim, out.event, "%s", out.message)

	return ctrl.Result{}, nil
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
// The requeue is the caller's, and on a settled claim it is when that claim next re-reads its
// pin. It used to be unconditionally zero, on the grounds that "there is nothing left to
// re-check: the allocation is immutable and this engine never re-reads it" -- which was true
// of the engine and not of NetBox. A restore predating an allocation erases it, and a claim
// with no timer never finds out (#167). Drift correction of the allocated object is still the
// declarative engine's job, on the NetBoxIPAddress the claim will materialise (NBO-025,
// NBO-032); this timer buys one indexed GET per claim per interval and nothing else.
func (p *claimPass) ready(
	ctx context.Context, detail string, requeue time.Duration,
) (ctrl.Result, error) {
	p.condition(netboxv1alpha1.ConditionRefsResolved, true, netboxv1alpha1.ReasonAllResolved,
		"no unresolved references")
	p.condition(netboxv1alpha1.ConditionReady, true, netboxv1alpha1.ReasonAddressAllocated, detail)

	return p.finish(ctx, requeue)
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
		// A claim is reconciled from the same informer cache an object is, from a controller
		// that requeues on its own timers, so it loses the same race for the same reason and
		// the answer is the same one (staleStatusWrite).
		if staleStatusWrite(ctx, err) {
			p.result = metrics.ResultWaiting

			return ctrl.Result{RequeueAfter: Jitter(staleRetry)}, nil
		}

		p.result = metrics.ResultError

		return ctrl.Result{}, fmt.Errorf("updating the status of %s/%s: %w",
			p.claim.GetNamespace(), p.claim.GetName(), err)
	}

	return ctrl.Result{RequeueAfter: Jitter(requeue)}, nil
}

// event records a Kubernetes Event for a claim, when there is a recorder.
func (e *ClaimEngine) event(claim Claim, reason, format string, args ...any) {
	emit(e.Events, claim, nil, "Normal", reason, format, args...)
}

// warnClaim records a Warning Event for a state that needs a human.
func (e *ClaimEngine) warnClaim(claim Claim, reason, format string, args ...any) {
	emit(e.Events, claim, nil, "Warning", reason, format, args...)
}
