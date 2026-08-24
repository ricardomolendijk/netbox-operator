package reconciler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// deferredRetry is the wait between a create that stripped a deferred field and the pass
// that applies it.
//
// Short, and unlike every interval in errors.go it is not a retry after a failure: nothing
// went wrong, the value is in hand, and the only reason it was not sent is that the object
// it belongs to did not exist yet. Ten minutes of resync between the POST and the PATCH
// would make `primary_ip4` land ten minutes after the machine it names.
const deferredRetry = 5 * time.Second

// deferredField is one of the descriptor's deferrals joined to the field-map entry that
// writes it, for one object.
//
// The join is why this exists at all: Descriptor.Deferred names NetBox columns
// (`primary_ip4`) while everything the engine reports names CR spec fields
// (`primaryIP4Ref`), and the pending list is read by humans.
type deferredField struct {
	// spec is the CR spec field, which is what status.deferredPending lists.
	spec string

	// api is the NetBox column, which is what the payload is keyed by.
	api string

	// mode is when the deferral applies.
	mode registry.DeferMode

	// resolved reports that this pass has an id to write. False means the reference did
	// not resolve, so there is nothing to defer *to* yet.
	resolved bool
}

// deferral is what one pass does about the descriptor's deferred fields.
//
// Computed once per pass, after references are resolved and before anything is written, so
// that "which fields did we leave out" and "which fields are still pending" are two reads
// of one decision rather than two independent recomputations that can disagree.
type deferral struct {
	fields []deferredField
}

// newDeferral joins the descriptor's deferrals to this object's spec and resolution state.
//
// Only declared fields are carried. A deferral for a field the user never set is not
// pending and never was: spec omission means "do not manage", and listing it would make
// every object of the kind permanently unready over a field nobody asked for.
func newDeferral(d registry.Descriptor, state registry.SpecState, desired netbox.Object) deferral {
	out := deferral{fields: make([]deferredField, 0, len(d.Deferred))}

	for _, declared := range d.Deferred {
		field, ok := writerOf(d, declared.APIField)
		if !ok || !slices.Contains(state.Declared, field.Spec) {
			continue
		}

		_, resolved := desired[field.API]

		out.fields = append(out.fields, deferredField{
			spec: field.Spec, api: field.API, mode: declared.Mode, resolved: resolved,
		})
	}

	return out
}

// defers reports that this spec field is one the descriptor lets the engine write after the
// create, so an unresolved one must not withhold the write.
//
// The exception to issue #195's precondition rule, and the reason deferred fields exist at
// all: a `primary_ip4` that cannot be created until the Device is would deadlock against a
// rule that refuses to create the Device until `primary_ip4` resolves. `!resolved` is
// redundant against the caller's set -- blockedRefs only asks about references that did not
// resolve -- and is kept because the two questions are different ones, and a future caller
// asking this about a resolved field would get the wrong answer without it.
func (d deferral) defers(spec string) bool {
	for _, field := range d.fields {
		if field.spec == spec && !field.resolved {
			return true
		}
	}

	return false
}

// writerOf returns the field-map entry that writes one NetBox column as a reference.
//
// The miss is unreachable in a booted manager -- registry.ErrDeferredNotRef rejects a
// deferral no reference writes, at manager start -- and is skipped rather than asserted
// because the alternative is the engine failing a reconcile over a descriptor bug the boot
// check exists to catch first.
func writerOf(d registry.Descriptor, api string) (registry.Field, bool) {
	for _, field := range d.Fields {
		if field.Class.Ref() && field.API == api {
			return field, true
		}
	}

	return registry.Field{}, false
}

// strip are the API columns to keep out of a create payload.
//
// Only DeferAlways, and only when the reference resolved. Both halves are the difference
// between the two modes rather than an optimisation:
//
//   - Unresolved needs no stripping. The engine already leaves an unresolved reference out
//     of the payload (see resolveRefs), so DeferIfUnresolved's "defer only when it does not
//     resolve" is satisfied by doing nothing, and its "include it when it does" is satisfied
//     by not stripping it.
//   - Which is exactly why DeferIfUnresolved is the only mode a natural-key field may use
//     (registry.ErrDeferredNaturalKey). Stripping a resolved `parent` would change the
//     object's natural key from `(parent, name)` to `(name)`, and the lookup that decided
//     to create would have been asking a different question from the create it decided on.
func (d deferral) strip() []string {
	out := make([]string, 0, len(d.fields))

	for _, field := range d.fields {
		if field.mode == registry.DeferAlways && field.resolved {
			out = append(out, field.api)
		}
	}

	return out
}

// createPayload is the POST body: desired minus what cannot be set at create time, and the
// list of columns removed.
//
// The strip is applied to a copy, and p.desired -- the payload every later pass diffs the
// live object against -- keeps the field. That asymmetry is the whole of NBO-015's
// hot-loop defence, and it only works in this direction:
//
//   - Strip the request and diff with the field present, and the diff is satisfied by a
//     PATCH the request never carries: drift forever, a write per resync for the lifetime of
//     the object. That is the failure docs/concepts/drift.md opens by warning about.
//   - Strip both, and the field is neither written at create time nor ever compared, so it
//     is silently dropped -- the omission issue #132 exists to make impossible.
//
// Stripping only the create leaves the very next pass with a true statement about the
// object: NetBox lacks a field the spec asks for, so it is ordinary drift, and one PATCH
// corrects it. That PATCH is the second pass, and it carries only the deferred column
// because the create carried everything else.
func (d deferral) createPayload(desired netbox.Object) (netbox.Object, []string) {
	stripped := d.strip()
	if len(stripped) == 0 {
		return desired, nil
	}

	payload := make(netbox.Object, len(desired))
	maps.Copy(payload, desired)

	for _, api := range stripped {
		delete(payload, api)
	}

	return payload, stripped
}

// pending are the CR spec fields whose deferred value NetBox does not hold yet, given the
// object as it now stands.
//
// A nil live object means NetBox holds nothing this pass can inspect -- the create was
// suppressed, or has not happened -- so every declared deferral is still pending. That is
// what makes the list computed from references rather than from writes: a DryRun endpoint
// reports the same pending set as a live one, because the question is what the spec asks
// for that NetBox lacks.
func (d deferral) pending(live, desired netbox.Object, rules netbox.FieldRules) []string {
	out := make([]string, 0, len(d.fields))

	for _, field := range d.fields {
		if field.applied(live, desired, rules) {
			continue
		}

		out = append(out, field.spec)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// applied reports that NetBox already holds the value this field asks for.
//
// Compared with netbox.Drift rather than by equality, because the read and write shapes of
// a reference differ: `primary_ip4` comes back as a nested object and is written as an id.
// Using the differ is also what keeps "still pending" and "still drift" from disagreeing --
// if they could, an applied field would stay pending forever, or a pending one would report
// itself done.
func (f deferredField) applied(live, desired netbox.Object, rules netbox.FieldRules) bool {
	if !f.resolved || live == nil {
		return false
	}

	return len(netbox.Drift(live, netbox.Object{f.api: desired[f.api]}, rules)) == 0
}

// recordDeferred stores the pending list on the status and returns it.
//
// A status field rather than only a condition message because the state is legitimate and
// can be permanent: a `primary_ip4` whose address is never created stays here forever, on
// purpose, and "what is this object still waiting to write" has to be answerable from
// `kubectl get -o yaml` (docs/concepts/object-lifecycle.md).
func (p *pass) recordDeferred(live netbox.Object) []string {
	pending := p.deferred.pending(live, p.desired, fieldRules(p.desc))
	p.obj.NetBoxStatus().DeferredPending = pending

	return pending
}

// settle is the exit for a pass that got as far as a live NetBox object: it records what is
// still deferred, and reports Ready only when nothing is.
//
// Ready=False here is the same commitment ready() makes for an unresolved reference (issue
// #132), for the same reason: the object exists, `kubectl apply` succeeded, and a field the
// user asked for is not in NetBox. Reporting Ready=True would make `kubectl wait
// --for=condition=Ready` pass over exactly that omission.
//
// An unresolved reference wins the reason, because it is the more specific answer to "why":
// the engine has nothing to write, rather than something it has not sent yet. Both states
// populate status.deferredPending, so the field list is there either way.
func (p *pass) settle(ctx context.Context, live netbox.Object) (ctrl.Result, error) {
	pending := p.recordDeferred(live)
	if len(pending) == 0 || p.refs.message != "" {
		return p.ready(ctx)
	}

	// Debug, not info: this is the normal state between the create and the PATCH that
	// completes it, and at info a whole manifest applied at once would log a line per
	// object per pass. The condition is the durable signal.
	logf.FromContext(ctx).V(1).Info("a deferred field is not applied yet",
		"action", "defer", "netboxID", p.obj.NetBoxStatus().ID, "deferred", pending)

	p.condition(netboxv1alpha1.ConditionReady, false,
		netboxv1alpha1.ReasonDeferredFieldPending, p.deferredMessage(pending))

	return p.finish(ctx, p.deferredRequeue())
}

// deferredRequeue is when to come back for a pending deferral.
//
// Fast exactly once, and only after the pass that brought the object into existence: that is
// the one interval where a short retry buys anything, because the value is already in hand
// and the only thing missing was the object to hang it on.
//
// Every later pending pass falls back to the resync, which is the guard rather than the
// fallback. A PATCH NetBox accepts and silently ignores -- an undeclared read-only column is
// how that happens (docs/netbox-schema.md, preamble) -- leaves the field pending forever, and
// deferredRetry on that path would turn the ordinary once-per-resync PATCH loop into a
// five-second one.
//
// Never driftResync(): the object has not settled, so driftMode: Off must not switch off the
// one pass that will ever apply the field. Same argument ready() makes for a reference and
// stop() for an endpoint.
func (p *pass) deferredRequeue() time.Duration {
	if p.result == metrics.ResultCreated || p.result == metrics.ResultRecreated {
		return deferredRetry
	}

	return p.resync()
}

// deferredMessage names what is pending and what will write it.
//
// The reason alone cannot distinguish "a PATCH is coming" from "this will never clear", and
// those are the two states a reader has to tell apart; the field names are what makes the
// difference checkable against the referenced objects.
func (p *pass) deferredMessage(pending []string) string {
	return fmt.Sprintf(
		"netbox %s/%d exists; %s cannot be set at create time and is applied by a follow-up patch",
		p.desc.Endpoint, p.obj.NetBoxStatus().ID, strings.Join(pending, ", "))
}
