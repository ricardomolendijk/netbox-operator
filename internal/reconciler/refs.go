package reconciler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
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

// refWait is what unresolved references do to the object: what to say, and when to decide
// again.
//
// Carried on the pass rather than acted on where it is discovered, because the consequence
// lands at the end of the reconcile: the object is still created, and it is Ready that has to
// report the omission.
type refWait struct {
	// message names every reference that did not resolve, and why. It goes into both the
	// RefsResolved and the Ready condition, so a human reads the same sentence either way.
	message string

	// requeue is the resolver's own interval, or zero when nothing improves on a timer.
	requeue time.Duration
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
// An unresolved reference is left out of the payload and reported; it does not block the
// write. That is a deliberate product decision (issue #132): the engine's convergence story
// is that a graph applied in any order makes progress, and refusing to create an object over
// an optional reference that may never resolve turns an optional field into a required one. A
// reference that *is* part of the kind's identity blocks anyway, one step later and for a
// better reason -- no natural-key candidate is applicable, so the engine waits rather than
// adopting the wrong object.
//
// What keeps that from being a silent omission is ready(): RefsResolved=False forces
// Ready=False with ReasonWaitingForRef, so a dropped reference cannot pass a readiness check.
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

	if len(resolved) == len(declared) {
		logf.FromContext(ctx).V(1).Info("resolved every reference",
			"action", "build", "refs", resolved, "unreadyTargets", notes)
		p.condition(netboxv1alpha1.ConditionRefsResolved, true, netboxv1alpha1.ReasonAllResolved,
			join(fmt.Sprintf("resolved %s", strings.Join(resolved, ", ")), notes))

		return nil
	}

	p.reportUnresolved(ctx, resolution, declared, resolved, notes)

	return nil
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
	ctx context.Context, resolution resolver.Resolution, declared, resolved, notes []string,
) {
	blocked := make([]string, 0, len(resolution.Blocked))
	for _, blocker := range resolution.Blocked {
		blocked = append(blocked, blocker.Field)
	}

	dropped := make([]string, 0, len(declared))

	for _, spec := range declared {
		if !slices.Contains(resolved, spec) && !slices.Contains(blocked, spec) {
			dropped = append(dropped, spec)
		}
	}

	reason, message := resolution.Reason(), join(messageFor(resolution, dropped), notes)
	if len(resolution.Blocked) == 0 {
		reason = netboxv1alpha1.ReasonNotImplemented
	}

	p.refs = refWait{message: message, requeue: resolution.Requeue()}

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
