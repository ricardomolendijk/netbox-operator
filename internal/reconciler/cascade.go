package reconciler

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Cascading deletion: taking the CRs that stand in the way of a refused delete with it
// (#304).
//
// NetBox declares almost every foreign key `on_delete=PROTECT`, which is what lets this
// operator do without a deletion-ordering table: a refusal plus a backed-off retry *is* the
// topological sort (finalizer.go). It sorts an order; it does not supply the deletes. So a
// user who deletes a site and expects its VLANs to go with it gets a CR parked at
// `Deleting=False, Reason=Protected` until they work out, from NetBox's prose, which other
// CRs to delete and in which order -- which is the manual topological sort the design was
// supposed to remove.
//
// The annotation supplies the deletes, and the whole of the mechanism is that it deletes
// *Kubernetes objects* and lets their own finalizers do the NetBox work. Nothing here issues
// a DELETE to NetBox, and that is the safety property rather than an implementation detail:
// an object a human made in the NetBox UI has no CR, is therefore invisible to the reverse
// index below, and cannot be deleted by this path however loudly NetBox names it as a
// blocker.
//
// Whose CRs, exactly: every CR that *references* this one, found through resolver.RefIndex --
// the same reverse edge the reference watches are built on, so a referrer that this operator
// would re-enqueue on a change is one it can also find here. Not the blockers NetBox named:
// mapping "MAX_a_DMZ (1301) (19)" back to a CR means parsing a sentence that is a Django
// translation string, and a parser between a user and their data is not a thing to build.
// Every referrer goes instead, which is a superset and a coherent one -- a CR pointing at an
// object that is being deleted has a reference about to dangle whether or not NetBox
// mentioned it.

// Referrers finds the CRs that point at one object.
//
// Consumer-defined and one method, per CONTRIBUTING.md, and it lives behind an interface for
// a harder reason than testability: answering it needs the typed informer cache and the field
// index registered on it (resolver.RefIndex, internal/controller/refwatch.go), and both live
// in internal/controller -- which imports this package. The interface is the seam that keeps
// the dependency pointing one way.
//
// Nil is a supported wiring. The engine then reports the refusal exactly as it did before the
// annotation existed, rather than failing a deletion over a collaborator a test did not need.
type Referrers interface {
	// Referring lists every CR of every registered kind that names obj in a reference.
	//
	// Across all namespaces, because a reference may cross one: a team's VLAN pointing at a
	// shared site is the ordinary shape of this API (ADR-0002), and it is exactly the CR
	// whose NetBox object is in the way.
	Referring(ctx context.Context, obj client.Object) ([]client.Object, error)
}

// cascades reports whether this CR asked for a refused delete to take its referrers with it.
func (p *pass) cascades() bool {
	return p.obj.GetAnnotations()[netboxv1alpha1.CascadeDeleteAnnotation] == "true"
}

// cascade deletes the CRs referencing this one and reports what it deleted.
//
// Returns an empty list when there is nothing to cascade *to*, which is the case that matters
// most: the delete was refused by something this cluster does not manage. The caller then
// reports Protected exactly as it always did, because deleting every CR the operator can see
// and finding NetBox still refusing is not a reason to keep deleting things.
//
// A referrer that is already going -- deletionTimestamp set -- is counted and not deleted
// again. The parent retries on its own backoff and this runs once per retry, so re-issuing a
// DELETE for an object mid-deletion would be one API call per referrer per attempt, for a
// state that resolves itself.
//
// One level, and the annotation is *not* copied onto what it deletes. Propagating it would
// mean writing metadata onto a CR that Git owns, which specGuard refuses by design
// (ADR-0005 section 1) -- and rightly: a cascade that silently annotates objects on its way
// through is one whose blast radius is not visible in any manifest. In NetBox's model this
// costs less than it sounds, because the deep chains fan out from one object rather than
// hanging off each other: a site's prefixes and VLANs both reference the *site*, so they are
// all in this one set. A referrer whose own delete is then refused by something further out
// reports Protected and names it, and annotating that object is the user's next move.
func (p *pass) cascade(ctx context.Context) (cascaded, error) {
	referrers, err := p.engine.Referrers.Referring(ctx, p.obj)
	if err != nil {
		return cascaded{}, fmt.Errorf("finding the CRs referencing %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	var out cascaded

	for _, referrer := range referrers {
		name := describeCR(referrer)

		if !referrer.GetDeletionTimestamp().IsZero() {
			out.waiting = append(out.waiting, name)

			continue
		}

		if err := p.engine.Children.Delete(ctx, referrer); err != nil {
			// Gone between the list and the delete is the outcome that was wanted, reached
			// by somebody else. Anything else stops the pass: a cascade that silently
			// deleted some of what is in the way and gave up on the rest would report a
			// blocked deletion whose cause is now one API error in one log line.
			if !apierrors.IsNotFound(err) {
				return cascaded{}, fmt.Errorf("cascading the delete of %s/%s to %s: %w",
					p.obj.GetNamespace(), p.obj.GetName(), name, err)
			}

			continue
		}

		out.deleted = append(out.deleted, name)
	}

	slices.Sort(out.deleted)
	slices.Sort(out.waiting)

	return out, nil
}

// cascaded is what one cascading pass did, split by whether this pass is the one that did it.
//
// The split exists for the Event. This runs again on every backed-off retry for as long as
// the referrers take to go, and an Event per attempt saying "deleted nothing, still waiting"
// is how the Events somebody needed get evicted from the namespace. So the Event fires on
// deleted and the condition -- which is rewritten in place and costs nothing to repeat --
// carries both.
type cascaded struct {
	// deleted are the CRs this pass deleted.
	deleted []string

	// waiting are the CRs already on their way out when this pass looked.
	waiting []string
}

// any reports whether the cascade has anything in flight, which is what makes the deletion
// "waiting for work that is happening" rather than "blocked on a decision nobody has made".
func (c cascaded) any() bool { return len(c.deleted) > 0 || len(c.waiting) > 0 }

// describeCR names one CR the way an Event has to: Kind first, then namespace and name, so a
// message listing several of different kinds reads as a list of objects rather than of names.
func describeCR(obj client.Object) string {
	kind := obj.GetObjectKind().GroupVersionKind().Kind
	if kind == "" {
		kind = fmt.Sprintf("%T", obj)
	}

	return fmt.Sprintf("%s %s/%s", kind, obj.GetNamespace(), obj.GetName())
}

// message is what the Deleting condition says while the referrers go.
func (c cascaded) message(endpoint string, id int64, cause error) string {
	parts := make([]string, 0, 2)
	if len(c.deleted) > 0 {
		parts = append(parts, fmt.Sprintf("deleted %s", strings.Join(c.deleted, ", ")))
	}
	if len(c.waiting) > 0 {
		parts = append(parts, fmt.Sprintf("still going: %s", strings.Join(c.waiting, ", ")))
	}

	return fmt.Sprintf(
		"netbox refused to delete %s/%d and %s=true, so the CRs referencing this one go first "+
			"(%s); this delete retries once their own finalizers have removed their netbox "+
			"objects. netbox said: %v",
		endpoint, id, netboxv1alpha1.CascadeDeleteAnnotation, strings.Join(parts, "; "), cause)
}
