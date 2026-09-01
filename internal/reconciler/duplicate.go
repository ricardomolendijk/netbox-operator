package reconciler

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Duplicate handling: what a natural-key lookup means on a kind where NetBox may hold
// several objects that match it (decision #177, NBO-025).
//
// The problem is real and specific to IPAM. `ipam.VRF.enforce_unique` defaults to true, so
// most duplicate addresses are refused by NetBox itself -- but with it false, or with the
// instance-wide ENFORCE_GLOBAL_UNIQUE off, NetBox accepts them, and for anycast and the
// VRRP/HSRP/GLBP/CARP virtual addresses that is the point rather than a mistake. NetBox
// therefore decides, through configuration this operator neither owns nor can fully read,
// and the natural key has to cope with the answer being "several".
//
// Which is what makes the provenance stamp part of the identity: nothing else distinguishes
// two rows holding one address. `k8s_uid` carries the CR's own metadata.uid
// (internal/provenance, docs/operations/provenance.md), so exactly one of the matches can
// be this CR's, and it says so on itself.
//
// The rule, applied to whatever one candidate matched:
//
//	one match carries this CR's uid          -> that is ours; claim it, no adoption question
//	several carry it                         -> refuse; two objects claim one identity
//	none carries it, every match is stamped   -> those belong to other CRs; create another
//	none carries it, some match is unstamped  -> refuse
//
// The last line is the one that had to be decided rather than derived. An unstamped match is
// an address created before the operator, or by another tool, and it may well be the one
// this CR meant; creating a third copy beside it is the worst available outcome, so "I
// cannot tell which of these is mine" refuses.

// unclaimableDuplicate is a natural key that matched NetBox objects this CR may not claim,
// on a kind where duplicates are permitted.
//
// Reported as a Conflict, like every other "NetBox holds something this object cannot safely
// take over": it names the matches, and nothing about *this* object changes it.
type unclaimableDuplicate struct {
	endpoint string
	matches  string
	why      string
	fix      string
}

func (e *unclaimableDuplicate) Error() string {
	return fmt.Sprintf("netbox %s holds %s: %s; %s", e.endpoint, e.matches, e.why, e.fix)
}

// errDuplicateNeedsProvenance is spec.allowDuplicate on an endpoint that stamps nothing.
//
// Refused up front rather than at the first collision, and refused even when nothing matched
// yet: the field makes the stamp this object's identity, so without one the operator would
// create an object it could never recognise again -- and would then create another on the
// first reconcile that lost status.id. That is the double-allocation shape of issue #167,
// which is reason enough not to write the first copy.
var errDuplicateNeedsProvenance = errors.New(
	"spec.allowDuplicate needs the endpoint's spec.managedBy: without a provenance stamp " +
		"there is nothing that could tell this object's address apart from an identical one")

// errDuplicateOnGeneratedChild is spec.allowDuplicate on an object the operator materialised.
//
// The same double-allocation shape as the sentinel above, reached from the other direction and
// worse. A materialised child is re-created from an unchanged manifest by design -- that is what
// deriving its name deterministically is *for* -- so it is the object most likely to be created
// again after losing status.id, which with the flag set means a second NetBox object rather than
// its own. Refused before the lookup, so nothing is created either way.
//
// The inline sugar has no such field, so a parent cannot ask for one (NBO-033), and the
// admission webhook refuses a hand edit that adds it at apply time. This is the reconcile-time
// backstop that keeps the webhook's failurePolicy: Ignore honest
// (docs/operations/admission-webhooks.md): with the webhook down, the edit is admitted and the
// object stops rather than duplicating.
var errDuplicateOnGeneratedChild = errors.New(
	"spec.allowDuplicate may not be set on an object the operator materialised: a child that " +
		"lost status.id would create a second netbox object rather than find its own")

// duplicate turns one candidate's lookup result into the object this pass may act on.
//
// It is a pass-through for every kind whose Descriptor declares no DuplicateSpec, and for
// every object of such a kind that has not set it -- which is all of them but this one. The
// live object and the error arrive exactly as Client.GetOne returned them, and leave that
// way unless duplicates are in play.
func (p *pass) duplicate(live netbox.Object, err error) (match, error) {
	if !p.allowsDuplicate() {
		return match{live: live, byNaturalKey: live != nil}, err
	}

	if generatedChild(p.obj) {
		return match{}, errDuplicateOnGeneratedChild
	}

	if !p.stampIdentifies() {
		return match{}, errDuplicateNeedsProvenance
	}

	matches := matchedObjects(live, err)
	if len(matches) == 0 {
		// Nothing matched, or a failure that is not ambiguity at all: either way this is not
		// a duplicate question, and the error goes back untouched.
		return match{}, err
	}

	return p.claimStamped(matches)
}

// claimStamped picks this CR's own object out of the matches, or explains why it will not.
func (p *pass) claimStamped(matches []netbox.Object) (match, error) {
	uid := string(p.obj.GetUID())
	field := p.endpoint.Provenance.UIDField

	mine := make([]netbox.Object, 0, 1)
	unstamped := make([]netbox.Object, 0, len(matches))

	for _, obj := range matches {
		stamped := p.endpoint.Provenance.Read(obj).UID

		if stamped == uid {
			mine = append(mine, obj)

			continue
		}

		if stamped == "" {
			unstamped = append(unstamped, obj)
		}
	}

	if len(mine) > 1 {
		return match{}, &unclaimableDuplicate{
			endpoint: p.desc.Endpoint, matches: renderMatches(mine),
			why: fmt.Sprintf("more than one carries this object's %s=%s stamp", field, uid),
			fix: "delete all but one in netbox; two objects cannot share one identity",
		}
	}

	if len(mine) == 1 {
		return p.claimOwnStamp(mine[0])
	}

	if len(unstamped) > 0 {
		return match{}, &unclaimableDuplicate{
			endpoint: p.desc.Endpoint, matches: renderMatches(unstamped),
			why: "spec.allowDuplicate makes the provenance stamp this object's identity and " +
				"these carry none, so another copy could not be told apart from them",
			fix: "unset spec.allowDuplicate and set spec.onConflict: Adopt to take one over, " +
				"or stamp it by adopting it first",
		}
	}

	// Every match belongs to another CR, so this one's object does not exist yet. Nothing is
	// returned, which sends the pass down the create path -- the whole point of the field.
	return match{}, nil
}

// claimOwnStamp is the match that carries this CR's stamp.
//
// Named for what it does rather than the shorter `own`, which NBO's owner-reference step
// (owners.go, landed separately) already uses for setting a Kubernetes owner reference. Two
// methods called `own` on one type meant one of them lost -- the textual merge was clean
// because they live in different files, and only the compiler saw it.
//
// byNaturalKey is deliberately false: an object stamped with this CR's own metadata.uid was
// created by this CR, so it is not an adoption and must not need spec.onConflict. status.id
// is written here for the same reason claim() writes it on the adoption path -- update()
// PATCHes by the recorded id, and this is the pass that learned it.
func (p *pass) claimOwnStamp(live netbox.Object) (match, error) {
	id, ok := live.ID()
	if !ok {
		return match{}, fmt.Errorf("%w: matched by its provenance stamp", errNoObjectID)
	}

	p.obj.NetBoxStatus().ID = int64(id)

	return match{live: live}, nil
}

// generatedChild reports whether the operator materialised obj, which it can tell from the
// *controller* owner reference naming one of its own Kinds.
//
// The controller reference specifically, and the distinction is the same one specGuard makes:
// ADR-0003 has the operator set two kinds of owner reference in this group, and only the
// controller one means "the operator created this". A *non-controller* containment reference is
// on an ordinary hand-written CR whose parent happens to be in the same namespace, and treating
// that as generated would refuse a legitimate anycast address the moment its interface moved
// into the same namespace.
func generatedChild(obj client.Object) bool {
	owner := metav1.GetControllerOf(obj)
	if owner == nil {
		return false
	}

	group, err := schema.ParseGroupVersion(owner.APIVersion)

	return err == nil && group.Group == netboxv1alpha1.GroupName
}

// allowsDuplicate reports whether this object has asked for duplicate handling.
//
// Read off the decoded spec by the name the Descriptor gives, which is how the engine reads
// every other per-object value: no branch on Kind, and a kind that declares no such field
// cannot reach any of the behaviour above.
func (p *pass) allowsDuplicate() bool {
	if p.desc.DuplicateSpec == "" {
		return false
	}

	allowed, _ := p.spec[p.desc.DuplicateSpec].(bool)

	return allowed
}

// stampIdentifies reports whether the stamp this endpoint writes can identify one object.
//
// Every clause is a way the uid is silently not written: no spec.managedBy at all, the uid
// field switched off by name, a definition the bootstrap could not create in NetBox
// (provenance.Stamp.customFields skips a name that is not in Fields), or a CR with no
// metadata.uid, which only a hand-built object in a test has.
func (p *pass) stampIdentifies() bool {
	stamp := p.endpoint.Provenance

	return stamp.Applicable() && p.desc.CustomFieldable && stamp.UIDField != "" &&
		slices.Contains(stamp.Fields, stamp.UIDField) && p.obj.GetUID() != ""
}

// matchedObjects are the objects one lookup matched: the several an ambiguity carries, or
// the one that was returned.
//
// The ambiguity error is where a multi-match already lives (NBO-074 put the matched set on
// it), so this needs no second request for a body NetBox has already sent.
func matchedObjects(live netbox.Object, err error) []netbox.Object {
	var ambiguous *netbox.AmbiguousError
	if errors.As(err, &ambiguous) {
		return ambiguous.Objects
	}

	if live != nil {
		return []netbox.Object{live}
	}

	return nil
}

// renderMatches names the objects, in the spelling netbox.AmbiguousError uses: `id 11
// (10.0.20.1/24)`. The display string is what a human recognises and the id is what they
// act on, so both.
func renderMatches(objs []netbox.Object) string {
	parts := make([]string, 0, len(objs))

	for _, obj := range objs {
		id, ok := obj.ID()
		if !ok {
			parts = append(parts, "an object with no netbox id")

			continue
		}

		part := fmt.Sprintf("id %d", id)
		if display, _ := obj["display"].(string); display != "" {
			part += " (" + display + ")"
		}

		parts = append(parts, part)
	}

	return strings.Join(parts, ", ")
}
