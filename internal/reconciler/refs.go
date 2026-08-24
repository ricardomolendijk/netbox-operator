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

	// Descriptor order rather than map order: the resolved list ends up in a condition
	// message, and a message that reorders itself between passes is unreviewable.
	resolved := make([]string, 0, len(resolution.ByField))
	notes := make([]string, 0, len(resolution.ByField))

	for _, field := range p.desc.Fields {
		refs, ok := resolution.ByField[field.Spec]
		if !ok {
			continue
		}

		p.applyRef(field, refs)
		resolved = append(resolved, field.Spec)
		notes = append(notes, unreadyTargets(field, refs)...)
	}

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
func unreadyTargets(field registry.Field, refs resolver.FieldRefs) []string {
	notes := make([]string, 0, len(refs))

	for _, ref := range refs {
		if ref.TargetNotReady == "" {
			continue
		}

		notes = append(notes, fmt.Sprintf("%s -> %s: resolved, target not ready (%s)",
			field.Spec, ref.Target, ref.TargetNotReady))
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

// applyRef puts a resolved id everywhere the rest of the pass looks for it.
//
// Two places, because the two vocabularies are both real: the payload holds NetBox column
// names, and the natural key matches on CR spec field names -- dcim.Region is unique on
// `(parent, name)` and filters on `parent_id`, so the lookup that decides whether to create
// or adopt needs the id under `parentRef`. Writing it into the decoded spec is what "a
// reference has become an id" means to every later step.
func (p *pass) applyRef(field registry.Field, refs resolver.FieldRefs) {
	payload, filterable := refValues(field, refs)

	p.desired[field.API] = payload
	p.spec[field.Spec] = filterable
	p.state.Resolved = append(p.state.Resolved, field.Spec)
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
// never saw is a generic foreign key, whose target is a union of Kinds rather than one and
// whose dispatch is NBO-019.
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
