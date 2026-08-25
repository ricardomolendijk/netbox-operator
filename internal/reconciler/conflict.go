package reconciler

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// Multi-writer conflict reporting (NBO-047).
//
// The operator does not serialise writes between clusters, and will not: issue #18 settled
// that, and docs/operations/provenance.md carries the reasoning -- a check in front of every
// write, then a lease with a TTL for the cluster that dies holding one, then a break-glass for
// when the lease is wrong, all for a configuration that is a mistake in every case anybody has
// named. So this file adds no gate. It names the other writer, on the object, in an Event and
// in a counter, and then the reconcile writes exactly what it was going to write.
//
// That makes reporting the whole product, which in turn makes over-reporting the only way to
// break it: a condition that is True on objects nobody is fighting over is a condition people
// learn to skip. provenance.Stamp.Conflict is where the line is drawn and why.

// conflictSustainedAfter is how many consecutive reconciles with the same claimant turn a
// conflict from a flap into a fight worth an Event of its own.
//
// Five, against the endpoint's resyncPeriod: at the 10-minute default that is most of an hour
// of two clusters taking turns, which nothing transient survives -- a migration, a rebuild or
// somebody restamping an object by hand is over in one or two passes. A count rather than a
// duration because a reconcile is the only moment the operator can observe anything, and a
// wall-clock threshold would fire on the first pass after a long resync.
const conflictSustainedAfter = 5

// reportConflict records that the object this pass is about to write to carries somebody
// else's stamp -- and then lets the write happen.
//
// Called from update() with the live object in hand, and only from there: a create has no live
// object, so there is nobody to conflict with, and an adopt reaches update() like every other
// path that found something. One call site for every write the engine makes.
//
// It returns nothing, because there is nothing for the caller to decide.
func (p *pass) reportConflict(ctx context.Context, live netbox.Object) {
	status := p.obj.NetBoxStatus()

	found, ok := p.endpoint.Provenance.Conflict(live, p.owner())
	if !ok {
		// Cleared rather than set to False. The absence of the condition is the normal state
		// -- see ConditionConflict -- and this is also the "returns to zero" half of the
		// report: an overlap somebody has just fixed has to stop being reported on the very
		// next pass, or the fix is unverifiable.
		status.Conflict = nil
		meta.RemoveStatusCondition(&status.Conditions, netboxv1alpha1.ConditionConflict)

		return
	}

	conflict := p.observedConflict(found)
	status.Conflict = conflict

	message := conflictMessage(p.desc.Endpoint, status.ID, p.endpoint.Provenance.ClusterID, found)
	p.condition(netboxv1alpha1.ConditionConflict, true, found.Reason, message)
	metrics.Conflicts.WithLabelValues(p.desc.GVK.Kind, found.Reason).Inc()

	// Info rather than V(1): a reconcile that changes nothing logs at debug (CONTRIBUTING.md),
	// and this one is about to change something in NetBox that another writer will change back.
	logf.FromContext(ctx).Info("another writer claims this netbox object; writing anyway",
		"action", "conflict", "netboxID", status.ID, "reason", found.Reason,
		"claimant", found.Writer(), "observations", conflict.Observations)

	// On the first sighting of this claimant, and once more if it is still there several passes
	// later. Not on every pass: an Event is an API object, and one per object per resync evicts
	// the Events somebody actually needed (see stop()'s transition guard, same argument).
	if conflict.Observations == 1 {
		p.engine.warn(p.obj, netboxv1alpha1.EventConflict, "%s", message)

		return
	}

	if conflict.Observations == conflictSustainedAfter {
		p.engine.warn(p.obj, netboxv1alpha1.EventConflictSustained,
			"still claimed by %s after %d consecutive reconciles: %s",
			found.Writer(), conflict.Observations, message)
	}
}

// observedConflict is this pass's conflict, with the observation count carried forward from
// the status as it was stored.
//
// The count lives in the status because a controller has no memory between passes:
// status.deletionAttempts exists for the same reason, and a counter that resets on every
// leader election or restart could not tell a flap from a fight at all.
//
// A changed claimant restarts the count. Somebody else taking the object over is a new fight,
// and inheriting the old count would announce it as sustained on its first pass.
func (p *pass) observedConflict(found provenance.Conflict) *netboxv1alpha1.ConflictStatus {
	now := metav1.Now()
	out := &netboxv1alpha1.ConflictStatus{
		Reason:        found.Reason,
		ClusterID:     found.ClusterID,
		Owner:         found.Owner,
		Observations:  1,
		FirstObserved: &now,
	}

	prev := p.before.Conflict
	if prev == nil || prev.Reason != out.Reason ||
		prev.ClusterID != out.ClusterID || prev.Owner != out.Owner {
		return out
	}

	out.Observations = prev.Observations + 1
	if prev.FirstObserved != nil {
		out.FirstObserved = prev.FirstObserved
	}

	return out
}

// conflictMessage is what the condition and the Event say.
//
// It has to carry four things or it is a notification rather than a report: which NetBox
// object, who else claims it, which cluster is complaining, and what to actually do. The last
// one is the point -- the answer is never "wait", because nothing about waiting resolves this,
// and it is never "turn a setting on", because there is no setting.
func conflictMessage(endpoint string, id int64, mine string, found provenance.Conflict) string {
	// The owner stamp is switchable off per endpoint, so the instruction cannot assume it: an
	// object whose stamp names a cluster and no CR is still resolvable, from the other side.
	other := found.Owner
	if other == "" {
		other = "whatever in " + found.Writer() + " writes it, which its stamp does not name"
	}

	return fmt.Sprintf(
		"netbox %s/%d is also claimed by %s; this cluster (%s) has written to it anyway and the other writer"+
			" will write it back, so the object flaps between the two specs. Writes are deliberately not"+
			" serialised between writers, so nothing here resolves on its own: stop one of the two claims --"+
			" delete or suspend %s, or narrow one of the two specs so they describe different netbox objects"+
			" (docs/operations/multi-writer.md)",
		endpoint, id, found.Writer(), mine, other)
}
