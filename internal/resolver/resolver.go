// Package resolver is the only place a reference becomes a NetBox id.
//
// Everything the operator does about ordering -- what it requeues, what it reports, what
// the ref watches re-enqueue (index.go) -- is a consequence of how precisely this package
// classifies "I cannot resolve this yet". So every failure is a typed error carrying the
// field it came from, and `ErrRefNotReady` is a *state* rather than a failure: a graph
// applied in any order converges only if "the target has not been created yet" is normal.
//
// Resolve never writes, to NetBox or to Kubernetes. It is a read over the informer cache
// plus, for the NetBox-side modes, one GET. That is what keeps it unit-testable with a fake
// client and reusable by `nbctl plan` (NBO-038) and the admission webhook (NBO-044).
//
// There is no switch on Kind here either (CONTRIBUTING.md, "Extensibility"): the target
// Kind arrives as registry.Field.Target, and everything else about that Kind -- its NetBox
// endpoint, its object type -- comes off its own Descriptor.
package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// Mode is which of ObjectRef's four shapes a reference was written in. Recorded on the
// result because the same id means different things about what will re-enqueue the
// referrer: a `name` is a Kubernetes object an event can arrive for, a `slug` is not.
type Mode string

const (
	// ModeName resolves a CR of the target Kind through its `.status.id`.
	ModeName Mode = "name"

	// ModeSlug looks the object up in NetBox by slug.
	ModeSlug Mode = "slug"

	// ModeLookup looks the object up in NetBox by an arbitrary filter map.
	ModeLookup Mode = "lookup"

	// ModeID is a literal NetBox primary key, verified rather than trusted.
	ModeID Mode = "id"
)

// Reader reads one target CR. Satisfied by client.Client, so production reads go through
// the manager's cache and a test needs no cluster.
//
// One method, and deliberately the controller-runtime signature rather than a narrower
// one: an adapter would be code that can be wrong about which object it fetched.
type Reader interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
}

// LookupClient is the NetBox half of resolution: two reads, and no way to write.
//
// Narrowed from the client on purpose. A resolver that cannot mutate NetBox is a stronger
// guarantee than a resolver that merely does not -- "a blocked reconcile issues zero NetBox
// mutations" is then structural rather than a property of this package's control flow.
type LookupClient interface {
	// GetOne returns the one object matching params, nil when nothing matches, and a
	// *netbox.AmbiguousError when several do.
	//
	// The resolver asks for one rather than listing and counting for itself: an ambiguous
	// reference has to name every id it matched, a count is not something a human can act
	// on, and since NBO-074 the client's error carries the ids and their display strings.
	GetOne(ctx context.Context, endpoint string, params netbox.Params) (netbox.Object, error)

	// GetByID fetches one object by its NetBox id, so a raw `id` reference is verified
	// rather than trusted.
	GetByID(ctx context.Context, endpoint string, id int) (netbox.Object, error)
}

// Descriptors resolves a target Kind to its per-kind NetBox facts: the endpoint to query
// and the object type to report. Nil on a Resolver means the package-level registry.
type Descriptors interface {
	Get(gvk schema.GroupVersionKind) (registry.Descriptor, bool)
}

// Resolver turns references into NetBox ids.
//
// It holds no NetBox client. Which NetBox a reference resolves against is a property of the
// *referrer's* endpoint, which is known per reconcile pass and not per resolver, so it
// arrives as an argument.
type Resolver struct {
	// Objects reads target CRs for `name` mode.
	Objects Reader

	// Kinds resolves a target Kind's Descriptor. Nil means the package-level registry.
	Kinds Descriptors

	// Grants reads the NetBoxRefGrants that authorise a cross-namespace `name` reference.
	//
	// Nil is not "no check": a cross-namespace reference then fails with ErrNoGrantReader
	// rather than resolving. A default-deny feature that switches itself off when a field is
	// left unset is not one, and the failure has to be a wiring bug the operator reports
	// rather than a silent allow (see grants.go).
	Grants GrantReader
}

// Request is one reference to resolve.
type Request struct {
	// NetBox is the referrer's NetBox, for the three modes that query it. A `name`
	// reference needs none: the id is already recorded on the target CR.
	NetBox LookupClient

	// Referrer is the object holding the reference. It supplies the default namespace, and
	// it is what makes a self-reference recognisable.
	Referrer types.NamespacedName

	// ReferrerGVK is the referrer's Kind, needed for the same reason.
	ReferrerGVK schema.GroupVersionKind

	// Field is the descriptor entry behind the reference: which spec field it is, which
	// NetBox column it writes, and which Kind it points at.
	Field registry.Field

	// Ref is the reference as the user wrote it.
	Ref netboxv1alpha1.ObjectRef
}

// Result is one resolved reference.
type Result struct {
	// ID is the NetBox primary key to write.
	ID int64

	// ObjectType is the target's `app_label.model` spelling, taken from its Descriptor and
	// never guessed. NBO-019's generic FKs write it alongside the id.
	ObjectType string

	// Mode is how the reference was resolved.
	Mode Mode

	// Target is the CR the id came from. Set for ModeName only -- the other three modes
	// resolve against NetBox, where Kubernetes names do not exist.
	Target types.NamespacedName
}

// Blocker is one reference that did not resolve, and what that means for the object.
type Blocker struct {
	// Field is the CR spec field name, so the report lines up with what the user typed.
	Field string

	// Reason is the RefsResolved condition reason.
	Reason string

	// Requeue is when to decide again. Zero means "no timer improves on this": an event on
	// the object being waited for is what clears it, and the ref watches (index.go) are
	// what deliver one. The caller's own resync is the backstop, not the mechanism.
	Requeue time.Duration

	// Err is the typed error, for a caller that needs to branch on it.
	Err error
}

// Resolution is the outcome of resolving every reference on one object.
type Resolution struct {
	// ByField holds what resolved, keyed by CR spec field name -- the spelling the user
	// wrote, not the NetBox column.
	ByField map[string]Result

	// Blocked holds what did not, in descriptor order, so the message a human reads is
	// stable between passes.
	Blocked []Blocker
}

// Reason is the condition reason to report, which is the first blocker's.
//
// The first rather than a merged one: a reason is keyed on by tooling, so it has to be a
// single value, and the message names every blocker anyway.
func (r Resolution) Reason() string {
	if len(r.Blocked) == 0 {
		return netboxv1alpha1.ReasonAllResolved
	}

	return r.Blocked[0].Reason
}

// Message renders every blocker into the condition message, in descriptor order.
func (r Resolution) Message() string {
	lines := make([]string, 0, len(r.Blocked))
	for _, blocker := range r.Blocked {
		lines = append(lines, blocker.Err.Error())
	}

	return strings.Join(lines, "; ")
}

// Requeue is the soonest useful retry across the blockers, and zero when none of them
// improves on its own.
//
// The soonest rather than the first blocker's: an object waiting on an ambiguous slug and a
// missing NetBox object has to come back for the second one, and taking the first blocker's
// ten minutes would hold the first one up for no reason.
func (r Resolution) Requeue() time.Duration {
	var soonest time.Duration

	for _, blocker := range r.Blocked {
		if blocker.Requeue == 0 {
			continue
		}

		if soonest == 0 || blocker.Requeue < soonest {
			soonest = blocker.Requeue
		}
	}

	return soonest
}

// ResolveAll resolves every reference obj's descriptor declares.
//
// A reference that cannot be resolved is a Blocker rather than a returned error: waiting for
// a target is a normal state, and a returned error would add controller-runtime backoff on
// top of it -- minutes of latency for a state an event will clear. The error return is for
// the two things that are not about a reference at all: an object that will not encode, and
// a cluster read that failed for a reason other than absence.
func (r *Resolver) ResolveAll(ctx context.Context, nb LookupClient, obj client.Object, d registry.Descriptor) (Resolution, error) {
	refs, err := refsOf(obj, d)
	if err != nil {
		return Resolution{}, err
	}

	generics, err := genericFKsOf(obj, d)
	if err != nil {
		return Resolution{}, err
	}

	resolution := Resolution{ByField: make(map[string]Result, len(refs)+len(generics))}
	referrer := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}

	pass := r.forPass()

	// The cycle check first, and before any NetBox call. A cycle means no NetBox request this
	// pass could make would be useful -- the object cannot be created without the reference,
	// and the reference cannot resolve without the object -- so the guard is also what makes
	// "a cycle issues zero NetBox requests" structural rather than incidental.
	if cycle := pass.checkFrom(ctx, RefNode{GVK: d.GVK, Key: referrer}, d, refs); cycle != nil {
		return cycleResolution(resolution, cycle)
	}

	for _, declared := range refs {
		result, err := pass.Resolve(ctx, Request{
			NetBox: nb, Referrer: referrer, ReferrerGVK: d.GVK,
			Field: declared.field, Ref: declared.ref,
		})

		if err := record(&resolution, declared.field.Spec, result, err); err != nil {
			return Resolution{}, err
		}
	}

	for _, generic := range generics {
		result, err := pass.ResolveGenericFK(ctx, GenericRequest{
			NetBox: nb, Referrer: referrer, ReferrerGVK: d.GVK,
			Pair: generic.pair, Union: generic.union,
		})

		if err := record(&resolution, generic.pair.Spec, result, err); err != nil {
			return Resolution{}, err
		}
	}

	return resolution, nil
}

// record files one reference's outcome, and returns the error only for the failures that are
// not about a reference at all.
//
// Keyed on the CR spec field, which for a polymorphic pair is the union's own field and not
// the member that resolved: the engine writes both columns from one entry, so one entry is
// what it has to find.
func record(resolution *Resolution, spec string, result Result, err error) error {
	var refErr *Error

	switch {
	case err == nil:
		resolution.ByField[spec] = result
	case errors.As(err, &refErr):
		resolution.Blocked = append(resolution.Blocked, blockerFor(refErr))
	default:
		return err
	}

	return nil
}

// forPass is the resolver one resolution pass uses: this one, over one snapshot of the
// cluster.
//
// The snapshot is why it exists at all -- the cycle check and the resolution behind it read
// the same objects, and reading each of them once is what keeps detection from doubling the
// reads a reconcile makes (see passReader).
//
// It is a named constructor rather than a struct literal inside ResolveAll because a literal
// silently drops a field. `Grants` was omitted when this was written -- the grant check landed
// on a sibling branch -- so every cross-namespace reference failed with ErrNoGrantReader
// instead of being denied, since both authorise() and the cycle walk read the collaborator off
// the pass resolver and not off the outer one. Both branches' tests passed alone. Every field
// of Resolver has to be carried here, and a test asserts it (NBO-092).
func (r *Resolver) forPass() *Resolver {
	return &Resolver{Objects: newPassReader(r.Objects), Kinds: r.Kinds, Grants: r.Grants}
}

// cycleResolution reports a detected cycle as the object's only blocker.
//
// The only one, rather than one among the rest, for two reasons. It is the whole answer: no
// other reference on this object matters until the ring is broken, and a cycle buried behind
// two other blockers in a message is a cycle nobody reads. And stopping there is what keeps a
// cycle free of NetBox reads, since resolving the remaining references would query NetBox for
// every slug and lookup among them.
func cycleResolution(resolution Resolution, cycle error) (Resolution, error) {
	var refErr *Error
	if !errors.As(cycle, &refErr) {
		// Not a verdict at all: the cluster read behind the walk failed, which is the engine's
		// backoff rather than a condition about the manifest.
		return Resolution{}, cycle
	}

	resolution.Blocked = append(resolution.Blocked, blockerFor(refErr))

	return resolution, nil
}

// passReader reads each object at most once per resolution pass.
//
// Two things, both of which the walk in cycle.go makes worth having. It bounds the read
// amplification detection adds: the objects the walk visits are the objects resolution goes on
// to read, so without it every `name` reference costs two reads instead of one. And it gives
// one pass one snapshot, so the cycle a walk reported and the ids the resolution wrote cannot
// come from two different versions of the same object.
//
// Per pass and never longer. A reference resolves off `.status.id`, which the owning
// controller writes at its own pace, and a cache outliving the pass would hold an object at a
// version whose id has since changed.
type passReader struct {
	inner Reader
	seen  map[passKey]passEntry
}

// passKey identifies one read: the same name under two Kinds is two objects.
type passKey struct {
	gvk schema.GroupVersionKind
	key types.NamespacedName
}

// passEntry is what a read returned, including the absence a not-found is.
type passEntry struct {
	object *unstructured.Unstructured
	err    error
}

// newPassReader wraps a reader for the duration of one pass.
func newPassReader(inner Reader) *passReader {
	return &passReader{inner: inner, seen: map[passKey]passEntry{}}
}

// Get answers from this pass's snapshot, reading through on the first ask for an object.
//
// Errors come back exactly as the underlying reader produced them, cached ones included: the
// difference between "no such object", "that Kind is not installed" and "the API server said
// no" is classified downstream by readFailure, and wrapping them here would wrap them twice.
func (p *passReader) Get(
	ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
) error {
	live, ok := obj.(*unstructured.Unstructured)
	if !ok {
		// A typed object carries no Kind of its own to key the snapshot on. Nothing in this
		// package asks for one; passing it through keeps the wrapper honest rather than clever.
		return p.inner.Get(ctx, key, obj, opts...) //nolint:wrapcheck // a pass-through cache
	}

	entry, cached := p.seen[passKey{gvk: live.GroupVersionKind(), key: key}]
	if !cached {
		entry = p.read(ctx, key, live, opts...)
	}

	if entry.err != nil {
		return entry.err
	}

	live.Object = entry.object.DeepCopy().Object

	return nil
}

// read fetches one object and records what came back.
func (p *passReader) read(
	ctx context.Context, key client.ObjectKey, live *unstructured.Unstructured, opts ...client.GetOption,
) passEntry {
	gvk := live.GroupVersionKind()

	entry := passEntry{err: p.inner.Get(ctx, key, live, opts...)}
	if entry.err == nil {
		entry.object = live.DeepCopy()
	}

	p.seen[passKey{gvk: gvk, key: key}] = entry

	return entry
}

// Resolve resolves one reference to an (id, object type) pair.
func (r *Resolver) Resolve(ctx context.Context, req Request) (Result, error) {
	target, ok := r.kinds().Get(req.Field.Target)
	if !ok {
		return Result{}, req.blocked(ErrRefKindUnavailable, req.unavailableDetail())
	}

	switch modeOf(req.Ref) {
	case ModeName:
		return r.byName(ctx, req, target)
	case ModeSlug:
		return r.byQuery(ctx, req, target, ModeSlug, netbox.Params{"slug": req.Ref.Slug})
	case ModeLookup:
		return r.byQuery(ctx, req, target, ModeLookup, netbox.Params(maps.Clone(req.Ref.Lookup)))
	case ModeID:
		return r.byID(ctx, req, target)
	}

	// Unreachable through the API server: ObjectRef's CEL rules require exactly one mode.
	// Kept because a stored object predating a CEL rule, or a caller building a Request by
	// hand, must not resolve to "no reference at all" and have the field silently dropped.
	return Result{}, req.blocked(ErrRefMalformed, "")
}

// byName reads the target CR and takes its `.status.id`.
//
// The only mode that expresses a dependency the operator can wait on, and so the only one
// with states between "found" and "not found": a target that exists but has not reconciled
// yet is the common case on a first apply, and it has to wait rather than fail.
func (r *Resolver) byName(ctx context.Context, req Request, target registry.Descriptor) (Result, error) {
	key := types.NamespacedName{Namespace: req.namespace(), Name: req.Ref.Name}

	// One hop of cycle detection, which is all a single Resolve can see. A self-reference is
	// the one cycle knowable without a graph walk, and reporting it as "not ready" would
	// leave an object waiting forever for itself. Longer cycles are NBO-016, over the
	// Resolution this returns.
	if key == req.Referrer && req.Field.Target == req.ReferrerGVK {
		return Result{}, req.blocked(ErrRefCycle, "the reference points at the referring object itself")
	}

	// Before the read, not after: a namespace the referrer has no grant into must not be
	// readable even to the extent of learning whether an object is in it (grants.go).
	if err := r.authorise(ctx, req, key); err != nil {
		return Result{}, err
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(req.Field.Target)

	if err := r.Objects.Get(ctx, key, live); err != nil {
		return Result{}, req.readFailure(key, err)
	}

	if !live.GetDeletionTimestamp().IsZero() {
		return Result{}, req.notReady(key, "the target is being deleted")
	}

	id, found, err := unstructured.NestedInt64(live.Object, "status", "id")
	if err != nil || !found || id == 0 {
		// The first-apply case, and the reason ErrRefNotReady exists: the target is there
		// and has simply not been written to NetBox yet.
		return Result{}, req.notReady(key, "the target has no status.id yet")
	}

	if detail, waiting := notReadyDetail(live); waiting {
		// Quoting the target's own reason is the whole point. Without it a human debugs the
		// referrer for an hour before noticing the target is the broken one.
		return Result{}, req.notReady(key, detail)
	}

	return Result{ID: id, ObjectType: target.ObjectType, Mode: ModeName, Target: key}, nil
}

// byQuery resolves a slug or a lookup against NetBox.
//
// Zero matches and several matches are different answers: nothing to point at is a state
// that a created object clears, while several is a question only a human can settle, and
// picking the first would silently point the referrer at an unrelated object.
func (r *Resolver) byQuery(
	ctx context.Context, req Request, target registry.Descriptor, mode Mode, params netbox.Params,
) (Result, error) {
	live, err := req.NetBox.GetOne(ctx, target.Endpoint, params)
	if err != nil {
		var ambiguous *netbox.AmbiguousError
		if errors.As(err, &ambiguous) {
			// Naming the matches rather than counting them, for the reason the engine names
			// them: the reader's next step is to look at those objects and decide which one
			// the reference meant. Only the matches, because *Error's own rendering already
			// carries the field, the target kind and the query.
			return Result{}, req.blocked(ErrRefAmbiguous, fmt.Sprintf("%d netbox %s match %v: %s",
				ambiguous.Matched, target.Endpoint, params, ambiguous.Matches()))
		}

		// NetBox being unavailable is not this reference's fault, so it stays an error and
		// gets the client's classification and the engine's backoff.
		return Result{}, fmt.Errorf("looking up netbox %s by %v: %w", target.Endpoint, params, err)
	}

	if live == nil {
		return Result{}, req.blocked(ErrRefNotFound,
			fmt.Sprintf("no netbox %s matches %v", target.Endpoint, params))
	}

	id, ok := live.ID()
	if !ok {
		return Result{}, fmt.Errorf("netbox %s matched %v with an object carrying no id", target.Endpoint, params)
	}

	return Result{ID: int64(id), ObjectType: target.ObjectType, Mode: mode}, nil
}

// byID verifies a raw NetBox primary key.
//
// Verified rather than trusted: an id is the one thing in the API a user can get wrong in a
// way NetBox cannot reject -- writing a stale id succeeds and points the object at whatever
// now holds that key.
func (r *Resolver) byID(ctx context.Context, req Request, target registry.Descriptor) (Result, error) {
	id := *req.Ref.ID

	live, err := req.NetBox.GetByID(ctx, target.Endpoint, int(id))
	if err != nil {
		var notFound *netbox.NotFoundError
		if errors.As(err, &notFound) {
			return Result{}, req.blocked(ErrRefNotFound,
				fmt.Sprintf("netbox %s/%d does not exist", target.Endpoint, id))
		}

		return Result{}, fmt.Errorf("verifying netbox %s/%d: %w", target.Endpoint, id, err)
	}

	// A 200 with no object in the body. Treating it as found would write an id nothing
	// answers for, which is exactly what verifying is supposed to prevent.
	if live == nil {
		return Result{}, req.blocked(ErrRefNotFound,
			fmt.Sprintf("netbox %s/%d does not exist", target.Endpoint, id))
	}

	return Result{ID: id, ObjectType: target.ObjectType, Mode: ModeID}, nil
}

// kinds returns the descriptor source, defaulting to the package-level registry.
func (r *Resolver) kinds() Descriptors {
	if r.Kinds != nil {
		return r.Kinds
	}

	return registryLookup{}
}

// registryLookup is the package-level registry as a Descriptors.
type registryLookup struct{}

// Get returns the descriptor registered for gvk.
func (registryLookup) Get(gvk schema.GroupVersionKind) (registry.Descriptor, bool) {
	return registry.Get(gvk)
}

// ByObjectType returns the descriptor registered for an `app_label.model` string.
func (registryLookup) ByObjectType(objectType string) (registry.Descriptor, bool) {
	return registry.ByObjectType(objectType)
}

// declaredRef is one reference the descriptor declares and the object sets.
type declaredRef struct {
	field registry.Field
	ref   netboxv1alpha1.ObjectRef
}

// refsOf reads the references out of obj's spec, in descriptor order.
//
// Through the object's JSON form, like the engine's payload builder: the JSON name is the
// spelling the user writes and the spelling registry.Field.Spec carries, so it is the one
// representation where both ends of the field map agree -- and it costs a generated kind no
// per-kind code at all.
//
// Generic FKs are deliberately absent: one of those spec fields writes two columns and its
// legal targets are a union rather than one Kind, so it is read by genericFKsOf and resolved
// by ResolveGenericFK. Keeping them out of here is also what keeps them out of the cycle walk
// -- see genericfk.go on why no union that ships today can be a blocking edge.
func refsOf(obj client.Object, d registry.Descriptor) ([]declaredRef, error) {
	spec, err := specMapOf(obj)
	if err != nil {
		return nil, err
	}

	refs := make([]declaredRef, 0, len(d.Fields))

	for _, field := range d.Fields {
		raw, set := spec[field.Spec]
		if !field.Ref || !set || string(raw) == "null" {
			continue
		}

		// A to-many reference -- `tags`, ipam.VRF's `import_targets` -- is a list of
		// references written as a list of ids, and neither ObjectRef nor Field says how many
		// a field takes. Skipped rather than decoded as one reference and got wrong: the
		// caller reports what it declared and did not get back, so the field is left out of
		// the payload and said so, which is the honest answer until the M7 generator emits
		// the cardinality (NBO-041).
		if isList(raw) {
			continue
		}

		var ref netboxv1alpha1.ObjectRef
		if err := json.Unmarshal(raw, &ref); err != nil {
			return nil, fmt.Errorf("decoding %s of %s/%s: %w", field.Spec, obj.GetNamespace(), obj.GetName(), err)
		}

		refs = append(refs, declaredRef{field: field, ref: ref})
	}

	return refs, nil
}

// specMapOf returns obj's spec as JSON names to undecoded values.
//
// One encode shared by the ordinary references and the polymorphic ones, so a pass that has
// both does not serialise the object twice -- and so both read the object through exactly the
// same representation the API server stores.
func specMapOf(obj client.Object) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("encoding %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	var decoded struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decoding the spec of %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return decoded.Spec, nil
}

// isList reports whether a spec value is a JSON array, which is how a to-many reference
// arrives.
func isList(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)

	return len(trimmed) > 0 && trimmed[0] == '['
}

// modeOf reports which of the four shapes a reference was written in, and the empty Mode
// for one that set none of them.
func modeOf(ref netboxv1alpha1.ObjectRef) Mode {
	switch {
	case ref.Name != "":
		return ModeName
	case ref.Slug != "":
		return ModeSlug
	case len(ref.Lookup) > 0:
		return ModeLookup
	case ref.ID != nil:
		return ModeID
	default:
		return ""
	}
}

// namespace is the namespace to look the target up in: the reference's own, or the
// referrer's.
//
// Defaulting rather than requiring it is what makes a single-namespace manifest terse, and
// crossing a namespace explicit -- which is what the grant check hangs off: a reference that
// resolves to the referrer's own namespace is never authorised against anything.
func (req Request) namespace() string {
	if req.Ref.Namespace != "" {
		return req.Ref.Namespace
	}

	return req.Referrer.Namespace
}

// readFailure classifies a failed cluster read.
//
// "The CRD is not installed" and "the object does not exist" are the two answers that must
// not be conflated: the first is a correct manifest waiting for an operator upgrade, and
// reporting it as not-found sends whoever reads it looking for a CR they were right not to
// have written.
func (req Request) readFailure(key types.NamespacedName, err error) error {
	var noMatch *apimeta.NoKindMatchError

	switch {
	case apierrors.IsNotFound(err):
		return req.blockedTarget(ErrRefNotFound, key, "no such object in the cluster")
	case errors.As(err, &noMatch), runtime.IsNotRegisteredError(err):
		return req.blockedTarget(ErrRefKindUnavailable, key,
			fmt.Sprintf("%s is not installed in this cluster", req.Field.Target.Kind))
	default:
		return fmt.Errorf("reading %s %s: %w", req.Field.Target.Kind, key, err)
	}
}

// notReadyDetail reports whether the target's own Ready condition says to wait, and quotes
// it if so.
//
// A target that is failing resolves to ErrRefNotReady like one that is merely young. There
// is no separate reason for it: the referrer is genuinely just waiting, and a reason per
// target-failure mode multiplies the vocabulary without adding information -- the message
// carries the difference.
func notReadyDetail(live *unstructured.Unstructured) (string, bool) {
	raw, found, err := unstructured.NestedSlice(live.Object, "status", "conditions")
	if err != nil || !found {
		// An id but no conditions at all: the object was written to NetBox by a build that
		// reported no conditions, or the status is mid-write. The id is proven either way.
		return "", false
	}

	for _, entry := range raw {
		condition, ok := entry.(map[string]any)
		if !ok || condition["type"] != netboxv1alpha1.ConditionReady {
			continue
		}

		if condition["status"] == string(metav1.ConditionTrue) {
			return "", false
		}

		return fmt.Sprintf("target Ready=%v, Reason=%v: %q",
			condition["status"], condition["reason"], condition["message"]), true
	}

	return "", false
}
