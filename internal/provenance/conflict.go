package provenance

import (
	"strings"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Reading the stamp back, and what a foreign one means (NBO-047).
//
// Writing the stamp answers "who manages this"; reading it answers "does anybody else think
// they do". The second question is the whole of multi-writer reporting, and it is a pure
// function of the live object's custom fields and this endpoint's own identity -- so it lives
// here, next to the code that wrote those fields, rather than in the engine where it would be
// one more thing a reconcile does.
//
// What this deliberately does *not* do is stop the write. The operator does not serialise
// writes between clusters and will not: see docs/operations/provenance.md, "Two clusters, one
// NetBox", and issue #18. A conflict is reported, named, counted, and then the reconcile
// proceeds exactly as it would have.

// Claim is what a live NetBox object says about who last wrote it: this endpoint's three
// stamp fields, read back by the names this endpoint is configured with.
//
// Every field is independently optional, because every one of them can be switched off by
// name on the endpoint (docs/operations/provenance.md, "Stamp less"). So the questions below
// are asked of whichever values are actually there rather than of a stamp assumed complete.
type Claim struct {
	// UID is the owning CR's metadata.uid.
	UID string

	// ClusterID is the writing cluster's identifier.
	ClusterID string

	// Owner is the owning CR as `<lowercased kind>/<namespace>/<name>`.
	Owner string
}

// Stamped reports whether this object carries any attribution at all.
//
// The tag deliberately does not count. A tag says "an operator manages this" and names
// neither which one nor which CR, so an object carrying only a tag is unattributable -- see
// Conflict for why that is not reported as a conflict.
func (c Claim) Stamped() bool { return c.UID != "" || c.Owner != "" || c.ClusterID != "" }

// Writer names the other writer as concretely as its stamp allows, for a condition message
// and an Event.
//
// A conflict you cannot attribute is a conflict you cannot resolve, so this always renders
// something actionable, and never renders a value it does not have: the fields are switchable
// per endpoint, and "in cluster " with nothing after it has sent somebody looking for a
// cluster called the empty string.
func (c Claim) Writer() string {
	switch {
	case c.Owner != "" && c.ClusterID != "":
		return c.Owner + " in cluster " + c.ClusterID
	case c.Owner != "":
		return c.Owner
	case c.ClusterID != "":
		return "cluster " + c.ClusterID
	default:
		return "the cr with uid " + c.UID
	}
}

// Read pulls this endpoint's stamp off a live NetBox object.
//
// On Config rather than on Stamp because reading needs only the names: a Stamp is a Config the
// bootstrap resolved, and whether a definition exists in NetBox is a question about writing.
// An object whose custom field is absent, null, or holds something that is not a string reads
// as empty, which is the same answer as "not stamped" and the only one that is safe -- a
// number where a uid should be is not somebody else's claim.
func (c Config) Read(live netbox.Object) Claim {
	fields, _ := live[CustomFieldsField].(map[string]any)

	return Claim{
		UID:       stampValue(fields, c.UIDField),
		ClusterID: stampValue(fields, c.ClusterField),
		Owner:     stampValue(fields, c.OwnerField),
	}
}

func stampValue(fields map[string]any, name string) string {
	if name == "" {
		return ""
	}

	value, _ := fields[name].(string)

	return strings.TrimSpace(value)
}

// Conflict is another writer's claim on a live object, and which kind of conflict it is.
type Conflict struct {
	Claim

	// Reason is netboxv1alpha1.ReasonForeignCluster or ReasonForeignOwner.
	Reason string
}

// Conflict reports whether the live object's stamp names a writer that is not this one.
//
// The rules, as guard clauses in the order they are asked. Each one is a fact about the live
// object rather than about its kind, so adding a kind changes nothing here:
//
//	this endpoint stamps nothing         -> no verdict. Nothing was ever written to compare
//	the object carries no stamp           -> not a conflict: unmanaged, or managed by a kind
//	                                        that cannot carry a stamp. The adoption question,
//	                                        which spec.onConflict already answers
//	cluster stamp set and not ours        -> ForeignCluster
//	owner stamp set and not ours          -> ForeignOwner
//	otherwise                             -> not a conflict
//
// Three things are deliberately *not* conflicts, because reporting them would make the report
// worthless:
//
//   - **An unstamped object.** It is unmanaged -- or managed by something that left no name --
//     and taking it over is what spec.onConflict is for. Every object that predates the
//     operator is in this set, so reporting it would mean reporting everything.
//   - **A human's edit in the NetBox UI, corrected on the next pass.** That is drift, not a
//     competing claim: Git is authoritative (ADR-0005), the operator is working as designed,
//     and NBO-003 already reports it as drift. Calling it a conflict would be wrong, not just
//     noisy.
//   - **A foreign uid whose cluster and owner stamps are still ours.** That is this very
//     manifest, deleted and re-applied: the CR's metadata.uid changes and nothing else does.
//     Every `kubectl delete && kubectl apply` would otherwise raise a conflict against itself,
//     which is precisely how a condition gets ignored. The uid is therefore never on its own
//     grounds for a verdict -- it is the tie-breaker duplicate.go uses for identity, and that
//     is a different question.
//
// The cluster is checked before the owner because it is the fact nothing else can see: two
// clusters cannot read each other's CRs, so an overlap between them is invisible except in the
// stamp, while two namespaces in one cluster are at least both in front of the same operator.
func (s Stamp) Conflict(live netbox.Object, mine Owner) (Conflict, bool) {
	if !s.Applicable() {
		return Conflict{}, false
	}

	claim := s.Read(live)
	if !claim.Stamped() {
		return Conflict{}, false
	}

	if claim.ClusterID != "" && claim.ClusterID != s.ClusterID {
		return Conflict{Claim: claim, Reason: netboxv1alpha1.ReasonForeignCluster}, true
	}

	if claim.Owner != "" && claim.Owner != mine.Ref() {
		return Conflict{Claim: claim, Reason: netboxv1alpha1.ReasonForeignOwner}, true
	}

	return Conflict{}, false
}
