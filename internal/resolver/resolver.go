// Package resolver is the only place a reference becomes a NetBox id.
//
// Everything the operator does about ordering -- what it requeues, what it reports, what
// the ref watches re-enqueue (index.go) -- is a consequence of how precisely this package
// classifies "I cannot resolve this yet". So every failure is a typed error carrying the
// field it came from, and `ErrRefNotReady` is a *state* rather than a failure: a graph
// applied in any order converges only if "the target has not been created yet" is normal.
//
// A reference needs its target to hold an **id**, not to be `Ready`. Those are different
// promises, and readiness is the wrong one: `driftMode: Report` makes `Ready=False` a steady
// state by design, so requiring it blocked every object in an adoption namespace indefinitely
// (NBO-089). See targetFailures for the small set of target states that do refuse.
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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

	// TargetGVK and TargetUID identify that CR as an owner reference has to: by Kind and
	// by uid, not by name. Set for ModeName only, for the same reason Target is.
	//
	// They are here because a `metav1.OwnerReference` needs a uid, and this is the one place
	// in the operator that reads the target object at all. The alternative -- the engine
	// fetching the target itself to learn its uid -- would be a second read of a CR in
	// another namespace on a path with no grant check, which is precisely the leak NBO-092
	// closed (grants.go, and the comment on authorise about ordering).
	//
	// Their absence is load-bearing rather than incidental: a reference resolved by `slug`,
	// `lookup` or a raw `id` names a NetBox row and no Kubernetes object, so there is
	// nothing an owner reference could point at. The containment owner reference of
	// ADR-0003 rule 4 reads the zero uid as exactly that (reconciler/owners.go).
	TargetGVK schema.GroupVersionKind
	TargetUID types.UID

	// TargetNotReady quotes the target's own Ready condition when the reference resolved
	// against a target that is not Ready, and is empty otherwise.
	//
	// A resolved reference that still has something to say. Requiring readiness rather than
	// an id made `driftMode: Report` unusable -- Report is Ready=False as a *steady state* by
	// design, so every object in an adoption namespace blocked everything pointing at it, for
	// the whole adoption (NBO-089). The referrer proceeds, and this is how it says so, on the
	// condition somebody debugging is already reading.
	TargetNotReady string
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

// FieldRefs are the resolved references of one spec field, in the order the spec listed
// them: exactly one for a to-one field, and one per element for a to-many.
//
// It is in Resolution.ByField only when *every* element resolved, and that is the whole of
// NBO-088's partial-list rule made structural: there is no representation for three of five
// tags, so no caller can write one. Writing three would look like success while being a
// deletion -- NetBox's M2M write replaces the list rather than adding to it.
type FieldRefs []Result

// IDs are the NetBox ids to write, sorted and deduplicated.
//
// Sorted because NetBox does not preserve M2M order and netbox.Drift compares these as a
// set (docs/concepts/drift.md rule 3), so the order the spec listed them in is not data.
// Rendering it into the payload anyway would make two specs that mean the same thing
// produce two different create bodies and two different log lines, and invite a reader to
// believe an order the comparison then ignores. Deduplicated because two references to the
// same object are one member of a set, which is what NetBox stores either way.
func (f FieldRefs) IDs() []int64 {
	ids := make([]int64, 0, len(f))
	for _, result := range f {
		ids = append(ids, result.ID)
	}

	slices.Sort(ids)

	return slices.Compact(ids)
}

// Resolution is the outcome of resolving every reference on one object.
type Resolution struct {
	// ByField holds what resolved, keyed by CR spec field name -- the spelling the user
	// wrote, not the NetBox column.
	ByField map[string]FieldRefs

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

	resolution := Resolution{ByField: make(map[string]FieldRefs, len(refs)+len(generics))}
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
		resolved, blocked, err := pass.resolveField(ctx, nb, referrer, d.GVK, declared)
		if err != nil {
			return Resolution{}, err
		}

		if len(blocked) > 0 {
			resolution.Blocked = append(resolution.Blocked, blocked...)

			continue
		}

		resolution.ByField[declared.field.Spec] = resolved
	}

	for _, generic := range generics {
		resolved, blocked, err := pass.resolveGeneric(ctx, nb, referrer, d.GVK, generic)
		if err != nil {
			return Resolution{}, err
		}

		if len(blocked) > 0 {
			resolution.Blocked = append(resolution.Blocked, blocked...)

			continue
		}

		// Keyed on the union's own spec field and not on the member that resolved. A to-one
		// pair files one Result, which is what the engine writes both columns from; a to-many
		// pair files one per element, in the order the manifest listed them, and the engine
		// renders the whole list from them.
		resolution.ByField[generic.pair.Spec] = resolved
	}

	return resolution, nil
}

// resolveGeneric resolves every polymorphic reference one spec field carries, and reports the
// field as resolved only when all of them are.
//
// All or nothing, for the reason resolveField is: `a_terminations` is written as a whole list
// and NetBox replaces the CableTermination rows behind it wholesale, so a cable with one of
// two ends resolved would be created connected at one end -- a half-cable nobody asked for,
// on a kind where correcting it later means delete and recreate.
//
// One Blocker per unresolved element, each naming its own indexed path, because each has its
// own mode, target and therefore retry policy.
func (r *Resolver) resolveGeneric(
	ctx context.Context, nb LookupClient, referrer types.NamespacedName,
	referrerGVK schema.GroupVersionKind, generic declaredGeneric,
) (FieldRefs, []Blocker, error) {
	resolved := make(FieldRefs, 0, len(generic.unions))

	var blocked []Blocker

	for i, union := range generic.unions {
		result, err := r.ResolveGenericFK(ctx, GenericRequest{
			NetBox: nb, Referrer: referrer, ReferrerGVK: referrerGVK,
			Pair: generic.pair, Union: union, Index: i,
		})

		var refErr *Error
		switch {
		case err == nil:
			resolved = append(resolved, result)
		case errors.As(err, &refErr):
			blocked = append(blocked, blockerFor(refErr))
		default:
			return nil, nil, err
		}
	}

	return resolved, blocked, nil
}

// resolveField resolves every reference one spec field carries, and reports the field as
// resolved only when all of them are.
//
// All or nothing, which is NBO-088's rule for a partially resolvable list: a `tags` field
// where three of five resolve contributes nothing to the payload and names the two that did
// not. Writing the three is worse than writing none, because NetBox's M2M write is a full
// replacement -- a short list deletes whatever it leaves out -- and the object would then
// report a successful write of a value nobody asked for.
//
// One Blocker per unresolved element rather than one per field. Each element has its own
// mode, its own target and therefore its own retry policy -- a missing CR is woken by an
// event and a missing NetBox slug by nothing but a timer -- and Resolution already folds a
// set of blockers into one reason, one message naming all of them, and the soonest retry.
//
// Each of those blockers is stamped with the element's position, which is the only place in
// the operator that knows it: below here a Request holds one reference and has nothing to
// count against, and above here the field has collapsed to a name (Error.Index).
func (r *Resolver) resolveField(
	ctx context.Context, nb LookupClient, referrer types.NamespacedName,
	referrerGVK schema.GroupVersionKind, declared fieldRefs,
) (FieldRefs, []Blocker, error) {
	resolved := make(FieldRefs, 0, len(declared.refs))

	var blocked []Blocker

	for index, element := range declared.elements() {
		result, err := r.Resolve(ctx, Request{
			NetBox: nb, Referrer: referrer, ReferrerGVK: referrerGVK,
			Field: element.field, Ref: element.ref,
		})

		var refErr *Error
		switch {
		case err == nil:
			resolved = append(resolved, result)
		case errors.As(err, &refErr):
			blocked = append(blocked, blockerFor(atElement(refErr, declared.field, index)))
		default:
			return nil, nil, err
		}
	}

	return resolved, blocked, nil
}

// atElement records which element of a to-many reference a refusal came from, so the message
// names `importTargets[1]` rather than the field its twenty route targets share.
//
// A no-op on a to-one field: an index there would be a position invented for a field that has
// exactly one value, and `parentRef[0]` reads like the first of several.
func atElement(err *Error, field registry.Field, index int) *Error {
	if !field.Class.ToMany() {
		return err
	}

	err.Index = &index

	return err
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
//
// An empty reference on a column the descriptor declares nullable is the zero Result and no
// error: the field was written and selects nothing, so the column is cleared. That is the same
// answer an empty union gets (genericfk.go) and the same instruction an emptied EmptyIsNull
// scalar carries -- a value being written, not an omission. It is distinct from a field the
// spec never set, which never reaches here at all, because spec omission means "do not manage"
// and conflating the two would clear a foreign key somebody else owns.
//
// Nothing is looked up for it, not even the target Kind: clearing a column asks no question
// about the Kind it used to point at, and a NetBox call for a reference that selects nothing
// would be a call whose answer is discarded.
func (r *Resolver) Resolve(ctx context.Context, req Request) (Result, error) {
	if modeOf(req.Ref) == "" && req.Field.EmptyIsNull && !req.Field.Class.ToMany() {
		return Result{}, nil
	}

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

	// Unreachable through the API server: ObjectRef's CEL rules require exactly one mode,
	// and the one type that permits none of them -- v1alpha1.OptionalRef -- is only declared
	// on a field whose descriptor entry carries EmptyIsNull, which returned above. Kept
	// because a stored object predating a CEL rule, or a caller building a Request by hand,
	// must not resolve to "no reference at all" and have the field silently dropped.
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

	state, notReady := readinessOf(live)
	if notReady && slices.Contains(targetFailures, state.reason) {
		return Result{}, req.targetFailed(key, state.detail)
	}

	result := Result{
		ID: id, ObjectType: target.ObjectType, Mode: ModeName, Target: key,
		// The uid off the object that was just read, and the Kind off the descriptor entry
		// that decided which object to read. Neither is guessed, and a uid read here cannot
		// belong to a different object than the id read above it: both come from the one
		// snapshot this pass took (passReader).
		TargetGVK: req.Field.Target, TargetUID: live.GetUID(),
	}
	if notReady {
		// Reported on the referrer rather than blocking it. Quoting the target's own words is
		// the whole point either way: without them a human debugs the referrer for an hour
		// before noticing the target is the unfinished one.
		result.TargetNotReady = state.detail
	}

	return result, nil
}

// targetFailures are the target Ready reasons that stop a referrer from using the id the
// target holds.
//
// The discriminator is the **reason**, not the status, and this list is the whole of the
// decision NBO-089 asked for.
//
// `Ready=False` is not one state. NBO-065's `driftMode: Report` makes it the *steady* state of
// every object at an endpoint by design -- drift is detected, reported and never corrected --
// so requiring readiness meant every object in a Report namespace blocked everything pointing
// at it, indefinitely. Report is the mode meant to run for a week over an existing NetBox
// during an adoption, which is exactly when a catalogue namespace holds the objects everything
// points at, so "correct but unusable" was the honest description. `mode: DryRun` has the
// identical shape.
//
// An id, meanwhile, is only written once the object provably exists in NetBox, and that is the
// entire claim a referrer needs in order to write its own payload.
//
// What survives of the counter-argument is that an id can be *stale* rather than merely
// uncorrected, and these three are where that happens -- the object the CR manages is not the
// object the CR now describes:
//
//   - Conflict: NetBox holds an object matching this CR's natural key that it may not claim,
//     so an id it still carries came from a key it no longer has.
//   - AdoptOnly: onConflict is AdoptOnly and nothing matched, which is the same shape.
//   - Invalid: NetBox rejected the payload, so the object's fields are not what the CR says,
//     and a referrer pointing at it propagates a state nobody asked for.
//
// Everything else -- ReportPending, DryRunPending, WaitingForRef, DeferredFieldPending,
// APIError, WaitingForEndpoint, WaitingForKey -- is a target whose id is right and whose work
// is unfinished, and the referrer proceeds while saying so.
//
// The default is therefore *proceed*, and that direction is deliberate. A block-list that
// missed a genuinely broken state lets one stale id through, reported on the referrer's own
// condition. An allow-list that missed a benign one reintroduces the cluster-wide stall this
// list exists to end, and would stay invisible until somebody ran Report in anger.
var targetFailures = []string{
	netboxv1alpha1.ReasonConflict,
	netboxv1alpha1.ReasonAdoptOnly,
	netboxv1alpha1.ReasonInvalid,
}

// targetState is what a target CR's own Ready condition says about the id it holds.
type targetState struct {
	// reason is the target's Ready reason, and empty when it reports no Ready condition.
	reason string

	// detail is the condition quoted, for the referrer to carry verbatim.
	detail string
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

// fieldRefs are the references one spec field carries: exactly one for a to-one field, and
// as many as the spec listed for a to-many.
//
// Grouped by field rather than flattened, because the field is the unit of the answer. A
// to-many either resolves entirely or contributes nothing, so the thing that decides has to
// see the whole field at once -- see resolveField.
type fieldRefs struct {
	field registry.Field
	refs  []netboxv1alpha1.ObjectRef
}

// elements flattens one field's references into the pairs everything that works a single
// reference at a time takes: resolution, and the cycle walk's edges.
func (f fieldRefs) elements() []refElement {
	out := make([]refElement, 0, len(f.refs))
	for _, ref := range f.refs {
		out = append(out, refElement{field: f.field, ref: ref})
	}

	return out
}

// refElement is one reference written under one field.
type refElement struct {
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
func refsOf(obj client.Object, d registry.Descriptor) ([]fieldRefs, error) {
	spec, err := SpecMap(obj)
	if err != nil {
		return nil, err
	}

	refs := make([]fieldRefs, 0, len(d.Fields))

	for _, field := range d.Fields {
		raw, set := spec[field.Spec]
		if !field.Class.Ref() || !set || string(raw) == "null" {
			continue
		}

		written, err := refsIn(field, raw)
		if err != nil {
			return nil, fmt.Errorf("decoding %s of %s/%s: %w", field.Spec, obj.GetNamespace(), obj.GetName(), err)
		}

		refs = append(refs, fieldRefs{field: field, refs: written})
	}

	return refs, nil
}

// SpecMap returns obj's spec as JSON names to undecoded values.
//
// One encode shared by the ordinary references and the polymorphic ones, so a pass that has
// both does not serialise the object twice -- and so both read the object through exactly the
// same representation the API server stores.
//
// Exported for the admission webhook (NBO-044), which compares two objects' natural-key
// values byte for byte. That comparison is only sound while both sides are read through one
// encoder: the same value reached as an int64 and as a float64 marshals identically here and
// would not survive two hand-rolled decodings.
func SpecMap(obj client.Object) (map[string]json.RawMessage, error) {
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

// refsIn decodes one field's value into the references it carries, in the shape its class
// says the field takes.
//
// The class decides and the value is not sniffed for its shape. A to-many field holding an
// object, or a to-one field holding a list, means the descriptor and the CRD disagree about
// that field -- unreachable through the API server, which types the field from the same
// declaration -- and it is refused rather than coerced. Coercion is what the previous
// behaviour amounted to: a list was skipped, so a declared to-many reference was dropped
// and reported NotImplemented, which is why no to-many reference in the catalogue could be
// implemented at all (NBO-088).
//
// An empty list is not a missing value. `[]` resolves to no ids and is written as an empty
// list, because it is a user saying this object has no route targets -- which is a different
// statement from saying nothing about them, and that one is an absent field that never
// reaches here.
func refsIn(field registry.Field, raw json.RawMessage) ([]netboxv1alpha1.ObjectRef, error) {
	if field.Class.ToMany() {
		var list []netboxv1alpha1.ObjectRef
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("as a list of references: %w", err)
		}

		return list, nil
	}

	var one netboxv1alpha1.ObjectRef
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("as one reference: %w", err)
	}

	return []netboxv1alpha1.ObjectRef{one}, nil
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

// readinessOf reads the target's own Ready condition, and reports whether it is anything
// other than True.
//
// It reports rather than decides. What a not-Ready target means for the referrer is
// targetFailures' judgement, keyed on the reason this returns -- so the reading of the
// condition and the policy over it are separable, and the policy is a list somebody can argue
// with rather than a branch buried in a read.
func readinessOf(live *unstructured.Unstructured) (targetState, bool) {
	raw, found, err := unstructured.NestedSlice(live.Object, "status", "conditions")
	if err != nil || !found {
		// An id but no conditions at all: the object was written to NetBox by a build that
		// reported no conditions, or the status is mid-write. The id is proven either way.
		return targetState{}, false
	}

	for _, entry := range raw {
		condition, ok := entry.(map[string]any)
		if !ok || condition["type"] != netboxv1alpha1.ConditionReady {
			continue
		}

		if condition["status"] == string(metav1.ConditionTrue) {
			return targetState{}, false
		}

		reason, _ := condition["reason"].(string)

		return targetState{reason: reason, detail: fmt.Sprintf("target Ready=%v, Reason=%v: %q",
			condition["status"], condition["reason"], condition["message"])}, true
	}

	return targetState{}, false
}
