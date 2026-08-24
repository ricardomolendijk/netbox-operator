package reconciler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// RefResolver turns the references in a spec into NetBox ids.
//
// Consumer-defined, like every other collaborator the engine has: one method, and the NetBox
// client is a parameter rather than resolver state because which NetBox a reference resolves
// against is a property of the *referrer's* endpoint, which is known per pass.
//
// It hands back a Resolution rather than an error per reference, because most of what it
// reports is not a failure: a target that has not been created yet is a state to wait in, and
// an error return would put controller-runtime's backoff on top of a wait an event will clear.
type RefResolver interface {
	ResolveAll(
		ctx context.Context, nb resolver.LookupClient, obj client.Object, d registry.Descriptor,
	) (resolver.Resolution, error)
}

// refWait is what unresolved references do to the object: what to say, when to decide
// again, and which spec fields are waiting.
//
// Carried on the pass rather than acted on where it is discovered, because the consequence
// lands later in the reconcile: the write is withheld unless every unresolved reference is
// one the descriptor defers, and it is Ready that has to say so either way.
type refWait struct {
	// message names every reference that did not resolve, and why. It goes into both the
	// RefsResolved and the Ready condition, so a human reads the same sentence either way.
	message string

	// requeue is the resolver's own interval, or zero when nothing improves on a timer.
	requeue time.Duration

	// unresolved names the declared references that did not become ids, in descriptor
	// order. It is the input to the precondition rule of issue #195, and it is the set
	// difference "declared minus resolved" rather than the resolver's blocker list: a
	// reference the resolver never reported on at all is just as unresolved as one it
	// refused, and writing without it would be the same silent omission.
	unresolved []string
}

// wait is when to come back: the resolver's interval, or the endpoint's resync when that is
// sooner.
//
// The sooner of the two rather than the resolver's, because a resync would have happened
// anyway and holding an object for ten minutes when it was going to be re-examined in one
// buys nothing. A zero resync leaves the resolver's own interval standing; the engine does
// not pass one -- see ready() on why driftMode: Off does not reach here -- and handling it
// keeps the arithmetic free of a special case.
func (w refWait) wait(resync time.Duration) time.Duration {
	if w.requeue == 0 || (resync > 0 && resync < w.requeue) {
		return resync
	}

	return w.requeue
}

// resolveRefs turns the references the spec declares into ids, and reports the ones it could
// not.
//
// Reported here, acted on by blockedRefs: a reference the spec declares is a precondition
// for the write (issue #195), so an unresolved one withholds the create *and* the update
// unless the descriptor defers that field. What makes it a precondition rather than an
// omission is that the spec set the key -- if a user wrote a scope, a row without one is not
// what they asked for, and ADR-0005 is about not writing what nobody asked for.
//
// It used to depend on an accident. An unresolved reference was left out of the payload and
// the object created anyway; a reference that happened to be part of the kind's natural key
// blocked one step later, because no candidate was applicable. So dcim.Location wrote nothing
// and ipam.Prefix wrote an unscoped row, for the same class of failure, and nobody designed
// that (issue #195).
//
// What keeps a *deferred* field from being the same silent omission is ready(): its
// RefsResolved=False forces Ready=False with ReasonWaitingForRef, so a reference applied by a
// later PATCH still cannot pass a readiness check while it is missing.
func (p *pass) resolveRefs(ctx context.Context, declared []string) error {
	if len(declared) == 0 {
		p.condition(netboxv1alpha1.ConditionRefsResolved, true,
			netboxv1alpha1.ReasonAllResolved, "no unresolved references")

		return nil
	}

	resolution, err := p.resolution(ctx)
	if err != nil {
		return err
	}

	// Kept for ownParent, keyed by the descriptor's own spelling of the containment field.
	// One lookup rather than a branch inside applyResolved, because ByField is keyed the
	// same way for an ordinary reference and for a generic-FK union -- so a containment
	// parent reached through the scope union of #179 needs no second case here.
	p.containment = resolution.ByField[p.desc.ContainmentRef]

	resolved, notes := p.applyResolved(resolution)

	// The set difference, not a length comparison: the resolver reads the object's own JSON
	// and the declared list comes from the spec map restoreEmpty has already touched, so the
	// two are not guaranteed to be the same size and "same size" would be the wrong question
	// even when they are.
	unresolved := make([]string, 0, len(declared))

	for _, spec := range declared {
		if !slices.Contains(resolved, spec) {
			unresolved = append(unresolved, spec)
		}
	}

	if len(unresolved) == 0 {
		logf.FromContext(ctx).V(1).Info("resolved every reference",
			"action", "build", "refs", resolved, "unreadyTargets", notes)
		p.condition(netboxv1alpha1.ConditionRefsResolved, true, netboxv1alpha1.ReasonAllResolved,
			join(fmt.Sprintf("resolved %s", strings.Join(resolved, ", ")), notes))

		return nil
	}

	p.reportUnresolved(ctx, resolution, unresolved, notes)

	return nil
}

// blockedRefs are the declared references that must resolve before anything is written.
//
// The rule of issue #195, in one place and with no branch on Kind: a reference the spec
// declares is a precondition for the write. Declared and resolved is written as it always
// was; declared and unresolved withholds the create *and* the update; not declared is not
// here at all, because resolveRefs only ever reports on the keys the spec set.
//
// A to-many field set to `[]` is declared and needs no resolution, so it is never in this
// list: the resolver files an empty FieldRefs for it and applyResolved counts it resolved,
// which is what keeps #161's partial-list rule and #169's "empty is an instruction" both
// true. An explicitly-empty field is still an instruction the engine carries out; it is
// only an *unresolvable target* that is a precondition.
//
// Deferred fields are the deliberate exception, and the only one. They exist so that an
// object can be created before a reference resolves and PATCHed afterwards -- a Device's
// `primary_ip4` needs an address that needs an interface that needs the Device, so no apply
// order and therefore no precondition can ever be satisfied first (registry.DeferAlways).
// Blocking on one would deadlock the two-pass write this engine has, which is a worse
// outcome than the partial row the rule exists to prevent, and the descriptor saying
// `Deferred` is the author stating exactly that trade for exactly that field.
func (p *pass) blockedRefs() []string {
	blocked := make([]string, 0, len(p.refs.unresolved))

	for _, spec := range p.refs.unresolved {
		if p.deferred.defers(spec) {
			continue
		}

		blocked = append(blocked, spec)
	}

	return blocked
}

// waitForRefs is the exit for an object whose write is withheld: nothing is sent to NetBox,
// and the object says which references it is waiting for.
//
// Not stop(): nothing failed. classify() would have to grow an arm for a state that is
// already fully described -- RefsResolved carries the reason the resolver gave, one of
// RefKindUnavailable, RefNotReady, RefNotFound or the five others, and reusing it here is
// what keeps "you declared a ref whose Kind does not exist" from being flattened into "the
// target is missing". Ready reports ReasonWaitingForRef, which is the question a
// `kubectl wait` is asking, exactly as it does for a deferred reference in ready().
//
// resync() rather than driftResync(): an object that has not been written has not settled,
// so driftMode: Off must not switch off the one retry that will ever write it. Same
// argument ready() makes for an unresolved reference and stop() for an endpoint.
//
// No Event, and debug rather than info. This is the normal state of a graph applied in any
// order -- a whole manifest applied at once puts every object with a forward reference
// through it -- so an Event or an info line here is one per object per pass for as long as
// the reference takes.
func (p *pass) waitForRefs(ctx context.Context, blocked []string) (ctrl.Result, error) {
	p.result = metrics.ResultWaiting

	logf.FromContext(ctx).V(1).Info("withholding the write; a declared reference is unresolved",
		"action", "wait", "refs", blocked)

	p.condition(netboxv1alpha1.ConditionReady, false, netboxv1alpha1.ReasonWaitingForRef,
		fmt.Sprintf("nothing was written: %s must resolve before netbox %s is created or updated; %s",
			strings.Join(blocked, ", "), p.desc.Endpoint, p.refs.message))

	return p.finish(ctx, p.refs.wait(p.resync()))
}

// unreadyTargets names the targets a resolved reference points at that are not Ready
// themselves.
//
// Reported, not blocked, and that is NBO-089's decision: a reference needs its target to hold
// an id, not to be Ready. `driftMode: Report` makes Ready=False the steady state of every
// object at an endpoint by design, so blocking on it stalled every object pointing into an
// adoption namespace for the length of the adoption. The resolver refuses only the target
// states where the id is actually the wrong one (see resolver.targetFailures).
//
// It still has to be *said*, though. A referrer reporting Ready=True over an id whose object
// is unfinished is exactly what somebody debugging needs told, and RefsResolved is the
// condition they are already reading.
func unreadyTargets(spec string, refs resolver.FieldRefs) []string {
	notes := make([]string, 0, len(refs))

	for _, ref := range refs {
		if ref.TargetNotReady == "" {
			continue
		}

		notes = append(notes, fmt.Sprintf("%s -> %s: resolved, target not ready (%s)",
			spec, ref.Target, ref.TargetNotReady))
	}

	return notes
}

// join appends the notes to a message, and is the one place the separator is written.
func join(message string, notes []string) string {
	if len(notes) == 0 {
		return message
	}

	return strings.Join(append([]string{message}, notes...), "; ")
}

// resolution runs the resolver, and reports nothing resolved when the engine has none.
//
// A nil resolver is the M1 contract rather than a wiring bug: every declared reference is
// then reported unresolved and left out of the payload, which is what the engine did before
// internal/resolver existed and what a caller assembling an Engine for a test that is not
// about references still gets.
func (p *pass) resolution(ctx context.Context) (resolver.Resolution, error) {
	if p.engine.Refs == nil {
		return resolver.Resolution{}, nil
	}

	resolution, err := p.engine.Refs.ResolveAll(ctx, p.endpoint.Client, p.obj, p.desc)
	if err != nil {
		return resolver.Resolution{}, fmt.Errorf("resolving the references of %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	return resolution, nil
}

// applyResolved writes every resolved reference into the payload and reports which spec
// fields those were, plus the note each unready target owes the condition.
//
// Descriptor order rather than map order: the resolved list ends up in a condition message,
// and a message that reorders itself between passes is unreviewable. The ordinary fields
// first and the polymorphic pairs after, because that is the order the Descriptor declares
// them in and neither list is a subset of the other.
func (p *pass) applyResolved(resolution resolver.Resolution) (resolved, notes []string) {
	resolved = make([]string, 0, len(resolution.ByField))
	notes = make([]string, 0, len(resolution.ByField))

	for _, field := range p.desc.Fields {
		refs, ok := resolution.ByField[field.Spec]
		if !ok {
			continue
		}

		p.applyRef(field, refs)
		resolved = append(resolved, field.Spec)
		notes = append(notes, unreadyTargets(field.Spec, refs)...)
	}

	for _, pair := range p.desc.GenericFKs {
		refs, ok := resolution.ByField[pair.Spec]
		if !ok {
			continue
		}

		p.applyGenericFK(pair, refs)
		resolved = append(resolved, pair.Spec)
		notes = append(notes, unreadyTargets(pair.Spec, refs)...)
	}

	return resolved, notes
}

// applyGenericFK writes both halves of a polymorphic reference, and is the only thing that
// ever writes either.
//
// Both columns every time, from one resolved Result. That is what makes the pair atomic on
// the way out: there is no code path that can set an id against a type it was not resolved
// with, which is the failure NetBox answers by attaching the object to a completely
// different row that happens to share a primary key.
//
// The zero Result is the union written empty, and clears both columns rather than leaving
// them out. Leaving them out would mean "do not manage this reference", which is what an
// *absent* field means -- an empty one is an instruction.
//
// Written into p.spec under the *two column names* rather than under the union's own spec
// field, which is what lets a natural key filter on a polymorphic pair (#180). A pair has no
// single value, so `{Filter: "scope_id", Spec: "scope"}` could never render -- but its two
// halves are two ordinary scalars once resolved, and `{Filter: "scope_id", Spec: "scope_id"}`
// renders exactly as `{Filter: "vrf_id", Spec: "vrfRef"}` does. ipam.VLANGroup is the kind
// that needs it, unique on (scope_type, scope_id, slug); registry.declaresSpecField accepts
// the two column names for the same reason. See docs/concepts/generic-refs.md, "Natural keys".
//
// The cleared pair writes nothing there and resolves neither column. A candidate matching on
// `scope_type` is then inapplicable, which is correct: a globally-scoped group's identity is
// the *null-pinned* candidate, and falling through to a value match on a column that holds
// null would send `?scope_type=` and adopt every group sharing its slug.
func (p *pass) applyGenericFK(pair registry.GenericFKSpec, refs resolver.FieldRefs) {
	objectType, id := genericFKValues(refs)
	p.desired[pair.TypeField], p.desired[pair.IDField] = objectType, id
	p.state.Resolved = append(p.state.Resolved, pair.Spec)

	if objectType == nil {
		return
	}

	// float64 for the id, because that is what every JSON number in a decoded spec is and
	// what filterValue renders; an int64 there would be dropped as unfilterable.
	p.spec[pair.TypeField], p.spec[pair.IDField] = objectType, float64(refs[0].ID)
	p.state.Resolved = append(p.state.Resolved, pair.TypeField, pair.IDField)
}

// genericFKValues renders one resolved polymorphic reference as its two column values.
//
// One FieldRefs holding exactly one Result, which is what ResolveAll files for a union: the
// pair is one reference, so there is no list here and no partial-list rule to apply. A zero
// Result -- the union written empty -- is the pair cleared.
func genericFKValues(refs resolver.FieldRefs) (objectType, id any) {
	if len(refs) == 0 || refs[0].ObjectType == "" {
		return nil, nil
	}

	return refs[0].ObjectType, refs[0].ID
}

// applyRef puts a resolved id everywhere the rest of the pass looks for it.
//
// Two places, because the two vocabularies are both real: the payload holds NetBox column
// names, and the natural key matches on CR spec field names -- dcim.Region is unique on
// `(parent, name)` and filters on `parent_id`, so the lookup that decides whether to create
// or adopt needs the id under `parentRef`. Writing it into the decoded spec is what "a
// reference has become an id" means to every later step.
func (p *pass) applyRef(field registry.Field, refs resolver.FieldRefs) {
	// A reference written empty is the column cleared: null in the payload, and nothing for a
	// natural key to filter on. Declared but not resolved, exactly as an emptied EmptyIsNull
	// scalar is (payload.go, writeValue and filterValue) -- so a candidate that matches on
	// this field is inapplicable rather than filtering on id 0, which would adopt whatever
	// NetBox returns for a primary key that cannot exist.
	if cleared(field, refs) {
		p.desired[field.API] = nil

		return
	}

	payload, filterable := refValues(field, refs)

	p.desired[field.API] = payload
	p.spec[field.Spec] = filterable
	p.state.Resolved = append(p.state.Resolved, field.Spec)
}

// cleared reports whether this to-one reference resolved to no object at all.
//
// The zero Result is the carrier, as it is for an empty union (genericFKValues reads the same
// answer off ObjectType). Id zero is the sentinel and is safe as one: NetBox primary keys
// start at 1, which is why v1alpha1.ObjectRef.ID rejects zero rather than treating it as
// unset. A to-many field has no such state -- `[]` is its empty statement and resolves to an
// empty list of ids, not to one absent id.
func cleared(field registry.Field, refs resolver.FieldRefs) bool {
	return !field.Class.ToMany() && len(refs) == 1 && refs[0].ID == 0
}

// refValues renders resolved references twice: as the value NetBox is sent, and as the value
// the decoded spec carries for a natural-key filter to read.
//
// A bare id for a to-one field and a list of ids for a to-many. The list is []any of float64
// rather than []int64, and that is not cosmetic: netbox.IDsOf reads a desired M2M list
// through asInt, which knows float64, int and string and not int64, so an []int64 would
// compare as the empty set and the operator would PATCH the same list forever -- the hot
// loop docs/concepts/drift.md opens by warning about.
func refValues(field registry.Field, refs resolver.FieldRefs) (payload, filterable any) {
	if !field.Class.ToMany() {
		// float64 in the spec because that is what every JSON number in a decoded spec is, and
		// filterValue renders exactly those shapes. An int64 there would be dropped as
		// unfilterable.
		return refs[0].ID, float64(refs[0].ID)
	}

	ids := refs.IDs()
	list := make([]any, 0, len(ids))

	for _, id := range ids {
		list = append(list, float64(id))
	}

	// The same list on both sides. A to-many reference has no single value a query parameter
	// could carry, and registry.ErrToManyNaturalKey rejects a descriptor that keys on one, so
	// there is no filter here to render differently for.
	return list, list
}

// reportUnresolved records the references that did not become ids.
//
// Two kinds of them, and they are reported differently because they are fixed differently: a
// reference the resolver refused carries its own reason and requeue, while one the resolver
// never saw is a reference this build cannot resolve at all -- a to-many reference, whose
// cardinality no Descriptor states yet (NBO-041).
func (p *pass) reportUnresolved(
	ctx context.Context, resolution resolver.Resolution, unresolved, notes []string,
) {
	blocked := make([]string, 0, len(resolution.Blocked))
	for _, blocker := range resolution.Blocked {
		blocked = append(blocked, blocker.Field)
	}

	dropped := make([]string, 0, len(unresolved))

	for _, spec := range unresolved {
		if !slices.Contains(blocked, spec) {
			dropped = append(dropped, spec)
		}
	}

	reason, message := resolution.Reason(), join(messageFor(resolution, dropped), notes)
	if len(resolution.Blocked) == 0 {
		reason = netboxv1alpha1.ReasonNotImplemented
	}

	p.refs = refWait{message: message, requeue: resolution.Requeue(), unresolved: unresolved}

	// Debug, not info: a reference waiting for its target is the normal state during a first
	// apply, and one line per object per pass would drown the log at cluster scale. The
	// condition is the durable signal.
	logf.FromContext(ctx).V(1).Info("references are unresolved",
		"action", "build", "reason", reason, "blocked", blocked, "dropped", dropped)

	p.condition(netboxv1alpha1.ConditionRefsResolved, false, reason, message)
}

// messageFor renders the refused references and the ones this build cannot resolve at all
// into one sentence.
func messageFor(resolution resolver.Resolution, dropped []string) string {
	parts := make([]string, 0, 2)

	if refused := resolution.Message(); refused != "" {
		parts = append(parts, refused)
	}

	if len(dropped) > 0 {
		parts = append(parts,
			fmt.Sprintf("references this build cannot resolve were left out of the payload: %v", dropped))
	}

	return strings.Join(parts, "; ")
}
