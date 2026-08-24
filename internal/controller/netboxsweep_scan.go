package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// sweepVerdict is what one listed NetBox object turned out to be.
type sweepVerdict int

const (
	// verdictClaimed is an object a live CR in this namespace still owns. Healthy, and
	// never a finding -- whatever that CR's own conditions say. A CR sitting on
	// WaitingForRef is not an orphan.
	verdictClaimed sweepVerdict = iota

	// verdictForeign is an object stamped for another namespace of this same cluster. Not
	// this sweep's business: it belongs to that namespace's own sweep, and acting on a
	// namespace this sweep cannot see is exactly the reach ADR-0002 takes away.
	verdictForeign

	// verdictUnattributed is an object with this cluster's stamp and no usable owner stamp,
	// so it cannot be attributed to any namespace.
	verdictUnattributed

	// verdictOrphan is an object stamped for this namespace whose owning CR is gone.
	verdictOrphan
)

// sweepReasons is every finding reason, so a completed run can zero the series for a reason
// it did not produce this time.
var sweepReasons = []netboxv1alpha1.SweepFindingReason{
	netboxv1alpha1.SweepOrphaned,
	netboxv1alpha1.SweepSuspected,
	netboxv1alpha1.SweepUnattributed,
}

// sweepKey identifies one finding across runs, which is what carries FirstSeen -- and
// therefore the grace-period clock -- from one run to the next.
type sweepKey struct {
	kind string
	id   int64
}

// sweepResult is one whole run.
type sweepResult struct {
	// kinds are the Kinds actually scanned, in spec order. Kept so the metric can be zeroed
	// for a kind that produced nothing: without it a resolved orphan would leave its series
	// pinned at the last non-zero value forever.
	kinds []string

	// findings are every finding, uncapped and unsorted. The cap is applied by report()
	// on the way into status, so summary counts and the debug log see the whole truth.
	findings []netboxv1alpha1.SweepFinding

	summary netboxv1alpha1.SweepSummary

	// lists is how many NetBox list calls the run issued: exactly one per scanned kind.
	// Each may have followed several pages internally, so it is a floor on the request
	// count rather than the count itself -- see docs/operations/sweeps.md for the arithmetic.
	lists int
}

// sweepScope is everything classify needs that does not change between objects of one kind.
type sweepScope struct {
	// kind is the lowercased Kind, which is the spelling the owner stamp uses
	// (provenance.Owner.Ref).
	kind string

	// namespace is the sweep's own namespace, and the only one it will attribute anything
	// to.
	namespace string

	uidField   string
	ownerField string

	// claimedIDs are the NetBox ids live CRs of this kind record in status.id.
	claimedIDs map[int64]bool

	// claimedUIDs are the metadata.uid values of live CRs of this kind. Checked as well as
	// the ids, so a CR whose status was lost -- restored from a backup, or wiped by hand --
	// still protects its object.
	claimedUIDs map[string]bool
}

// sweepPass is one run's shared inputs, so the per-kind and per-object functions below stay
// short enough to read.
type sweepPass struct {
	reconciler *NetBoxSweepReconciler
	namespace  string
	endpoint   sweepEndpoint

	// prior is FirstSeen from the previous run's status, keyed by (kind, id). A finding
	// absent from it is being seen for the first time and its clock starts now -- which is
	// also what happens to a finding the previous run's cap dropped, so the grace period
	// restarts rather than expiring unnoticed. That fails in the safe direction: towards
	// Suspected, never towards Orphaned.
	prior map[sweepKey]metav1.Time

	// grace is spec.gracePeriod. Zero reports every finding as Orphaned on first sight.
	grace time.Duration

	// now is one timestamp for the whole run, so every finding's age is measured against
	// the same clock reading rather than against wherever the loop had got to.
	now time.Time
}

// scan lists every stamped object of every listed kind and classifies it. It writes
// nothing, anywhere.
func (r *NetBoxSweepReconciler) scan(ctx context.Context, sweep *netboxv1alpha1.NetBoxSweep,
	endpoint sweepEndpoint, descriptors []registry.Descriptor,
) (sweepResult, error) {
	pass := &sweepPass{
		reconciler: r,
		namespace:  sweep.Namespace,
		endpoint:   endpoint,
		prior:      priorFirstSeen(sweep.Status.Findings),
		grace:      sweep.Spec.GracePeriod.Duration,
		now:        time.Now(),
	}

	out := sweepResult{kinds: make([]string, 0, len(descriptors))}
	for _, descriptor := range descriptors {
		out.kinds = append(out.kinds, descriptor.GVK.Kind)

		if err := pass.scanKind(ctx, descriptor, &out); err != nil {
			// One failed kind refuses the whole run. A report covering the kinds that
			// happened to answer is indistinguishable from a complete one, and the missing
			// kind is silently exonerated.
			return sweepResult{}, err
		}
	}

	return out, nil
}

// scanKind scans one kind: the claims from Kubernetes, the live objects from NetBox, and one
// verdict per object.
func (p *sweepPass) scanKind(ctx context.Context, descriptor registry.Descriptor, out *sweepResult) error {
	scope, err := p.reconciler.claims(ctx, descriptor.GVK, p.namespace)
	if err != nil {
		return err
	}

	scope.kind = strings.ToLower(descriptor.GVK.Kind)
	scope.namespace = p.namespace
	scope.uidField, scope.ownerField = p.endpoint.uidField, p.endpoint.ownerField

	live, err := p.endpoint.lister.List(ctx, descriptor.Endpoint, p.endpoint.params(descriptor))
	if err != nil {
		return fmt.Errorf("listing %s: %w", descriptor.Endpoint, err)
	}

	out.lists++
	out.summary.Scanned += int32(len(live)) //nolint:gosec // a page-capped list cannot overflow int32

	for _, object := range live {
		verdict := classify(object, scope)
		out.count(verdict)

		if verdict == verdictClaimed || verdict == verdictForeign {
			continue
		}

		out.findings = append(out.findings, p.finding(object, descriptor.GVK.Kind, verdict))
	}

	return nil
}

// count records one verdict in the summary. The two that are never listed are still counted,
// so `scanned` adds up and nobody reads the gap as a lost object.
func (s *sweepResult) count(verdict sweepVerdict) {
	switch verdict {
	case verdictClaimed:
		s.summary.Claimed++
	case verdictForeign:
		s.summary.Foreign++
	case verdictUnattributed:
		s.summary.Unattributed++
	case verdictOrphan:
		// Split into Orphans and Suspected by finding(), which is where the grace period
		// lives; counted there rather than here so there is one place that decides it.
	}
}

// classify decides what one listed NetBox object is.
//
// The order of the checks is the safety property. Claims are tested **first**, before the
// object is attributed to a namespace at all: a kind whose NetBox model carries no
// `custom_fields` cannot hold an owner stamp, and a claimed object with no stamp must read
// as claimed rather than as unattributed. Attribution only ever narrows what is already
// unclaimed.
func classify(object netbox.Object, scope sweepScope) sweepVerdict {
	fields := customFieldsOf(object)

	if id, ok := object.ID(); ok && scope.claimedIDs[int64(id)] {
		return verdictClaimed
	}

	if uid := stampValue(fields, scope.uidField); uid != "" && scope.claimedUIDs[uid] {
		return verdictClaimed
	}

	kind, namespace, ok := parseOwner(stampValue(fields, scope.ownerField))
	if !ok {
		return verdictUnattributed
	}

	if kind != scope.kind || namespace != scope.namespace {
		return verdictForeign
	}

	return verdictOrphan
}

// finding builds the status entry for one unclaimed object, and is where the grace period
// decides whether it is an accusation or a suspicion.
func (p *sweepPass) finding(object netbox.Object, kind string, verdict sweepVerdict) netboxv1alpha1.SweepFinding {
	fields := customFieldsOf(object)
	id, _ := object.ID()

	firstSeen, seen := p.prior[sweepKey{kind: kind, id: int64(id)}]
	if !seen {
		firstSeen = metav1.NewTime(p.now)
	}

	reason := netboxv1alpha1.SweepUnattributed
	if verdict == verdictOrphan {
		reason = netboxv1alpha1.SweepSuspected
		if p.now.Sub(firstSeen.Time) >= p.grace {
			reason = netboxv1alpha1.SweepOrphaned
		}
	}

	return netboxv1alpha1.SweepFinding{
		Kind:      kind,
		NetBoxID:  int64(id),
		Display:   asStampString(object["display"]),
		URL:       asStampString(object["url"]),
		Owner:     stampValue(fields, p.endpoint.ownerField),
		UID:       stampValue(fields, p.endpoint.uidField),
		FirstSeen: firstSeen,
		Reason:    reason,
	}
}

// claims are the NetBox objects live CRs of one kind in one namespace still own.
//
// The list is typed, resolved through the scheme from the kind's own GVK, so it reads the
// informer the kind's own controller already runs. An unstructured list would work and is
// the obvious thing to reach for, but controller-runtime caches unstructured informers
// separately from typed ones: it would stand up a second watch and a second copy of every
// CR of every swept kind, for data the manager already holds.
//
// Terminating CRs count as claims. Their finalizer has not come off, so the operator is
// still going to deal with the NetBox object itself, and reporting it in the meantime would
// be a finding that resolves on its own.
func (r *NetBoxSweepReconciler) claims(ctx context.Context, gvk schema.GroupVersionKind,
	namespace string,
) (sweepScope, error) {
	listGVK := gvk
	listGVK.Kind += "List"

	object, err := r.Scheme.New(listGVK)
	if err != nil {
		return sweepScope{}, fmt.Errorf("resolving %s: %w", listGVK, err)
	}

	list, ok := object.(client.ObjectList)
	if !ok {
		return sweepScope{}, fmt.Errorf("%s is not a list type", listGVK)
	}

	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return sweepScope{}, fmt.Errorf("listing %s in %s: %w", gvk.Kind, namespace, err)
	}

	items, err := meta.ExtractList(list)
	if err != nil {
		return sweepScope{}, fmt.Errorf("reading the %s list: %w", listGVK, err)
	}

	scope := sweepScope{
		claimedIDs:  make(map[int64]bool, len(items)),
		claimedUIDs: make(map[string]bool, len(items)),
	}

	for _, item := range items {
		claim, ok := item.(reconciler.Object)
		if !ok {
			return sweepScope{}, fmt.Errorf("%T is not a netbox object kind", item)
		}

		scope.claimedUIDs[string(claim.GetUID())] = true

		if id := claim.NetBoxStatus().ID; id != 0 {
			scope.claimedIDs[id] = true
		}
	}

	return scope, nil
}

// sweepDescriptors resolves spec.kinds to descriptors, and returns the refusal reason for
// the first kind it cannot use.
//
// A table lookup keyed on the Kind name the user wrote, never a branch on Kind: adding a
// kind to the catalogue makes it sweepable with no edit here (CONTRIBUTING.md,
// "Extensibility").
func sweepDescriptors(kinds []string) ([]registry.Descriptor, string, error) {
	byKind := make(map[string]registry.Descriptor, len(registry.List()))
	for _, descriptor := range registry.List() {
		byKind[descriptor.GVK.Kind] = descriptor
	}

	out := make([]registry.Descriptor, 0, len(kinds))
	for _, kind := range kinds {
		descriptor, known := byKind[kind]
		if !known {
			return nil, netboxv1alpha1.ReasonSweepUnknownKind,
				fmt.Errorf("kind %s has no registered descriptor in this build", kind)
		}

		if !descriptor.CustomFieldable {
			return nil, netboxv1alpha1.ReasonSweepKindNotStampable,
				fmt.Errorf("kind %s maps to %s, whose netbox model has no custom_fields "+
					"column, so its objects cannot carry this cluster's stamp and can never "+
					"be attributed", kind, descriptor.ObjectType)
		}

		out = append(out, descriptor)
	}

	return out, "", nil
}

// stampUsable reports whether a resolved stamp is enough to scope a sweep.
//
// All three fields are required and all three have to provably exist in NetBox. The cluster
// field is the scope, the uid field is the claim check that survives a lost status, and the
// owner field is the only thing that says which namespace an object belongs to. Missing any
// one of them makes the answer a guess, and "I cannot tell whose objects these are" has
// exactly one safe reading.
func stampUsable(stamp provenance.Stamp) error {
	if !stamp.Applicable() {
		return errors.New("spec.managedBy writes no stamp (no clusterID, or its tag was " +
			"never resolved), so nothing distinguishes this cluster's objects from anybody else's")
	}

	for what, field := range map[string]string{
		"cluster": stamp.ClusterField,
		"uid":     stamp.UIDField,
		"owner":   stamp.OwnerField,
	} {
		if field == "" || !slices.Contains(stamp.Fields, field) {
			return fmt.Errorf("the %s custom field (%q) does not exist in netbox; "+
				"the endpoint's provenance bootstrap has to create it before a sweep can "+
				"tell whose objects these are", what, field)
		}
	}

	return nil
}

// priorFirstSeen indexes the previous run's findings, so a finding that is still there keeps
// the clock it started with.
func priorFirstSeen(findings []netboxv1alpha1.SweepFinding) map[sweepKey]metav1.Time {
	out := make(map[sweepKey]metav1.Time, len(findings))
	for _, finding := range findings {
		out[sweepKey{kind: finding.Kind, id: finding.NetBoxID}] = finding.FirstSeen
	}

	return out
}

// customFieldsOf reads the `custom_fields` container off a listed object. NetBox returns it
// on every CustomFieldsMixin model, containing every custom field defined for the object
// type -- including the ones this operator knows nothing about, which are left alone.
func customFieldsOf(object netbox.Object) map[string]any {
	fields, _ := object[provenance.CustomFieldsField].(map[string]any)

	return fields
}

// stampValue reads one custom field as a string. An absent field, a null and a non-string
// all read as empty, which is the same answer for the same reason: there is no stamp there.
func stampValue(fields map[string]any, name string) string {
	if name == "" {
		return ""
	}

	return asStampString(fields[name])
}

// asStampString renders a JSON value as a string, and anything that is not one as empty.
func asStampString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(text)
}

// parseOwner splits a `k8s_owner` stamp into the Kind and namespace it names.
//
// The spelling is provenance.Owner.Ref's: `<lowercased kind>/<namespace>/<name>`. Anything
// that is not exactly three non-empty segments is not a stamp this operator wrote, and is
// reported as unattributed rather than parsed optimistically -- a half-read owner is how a
// sweep would attribute somebody else's object to itself.
func parseOwner(owner string) (kind, namespace string, ok bool) {
	parts := strings.Split(owner, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

// report is what goes into status: the findings, sorted and capped, and whether the cap bit.
//
// Sorted so the cap drops the least actionable rather than whatever the map iterated to:
// Orphaned first, then Suspected, then Unattributed, and within each by kind and NetBox id.
// Stable ordering is also what stops a status write on every run over an unchanged set.
func (s *sweepResult) report(limit int) ([]netboxv1alpha1.SweepFinding, bool) {
	findings := slices.Clone(s.findings)
	slices.SortFunc(findings, func(a, b netboxv1alpha1.SweepFinding) int {
		return cmp.Or(
			cmp.Compare(reasonRank(a.Reason), reasonRank(b.Reason)),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.NetBoxID, b.NetBoxID),
		)
	})

	// Counted here rather than in scanKind, because this is where the grace period has
	// already decided which of the two an unclaimed object is.
	s.summary.Orphans, s.summary.Suspected = 0, 0
	for _, finding := range findings {
		if finding.Reason == netboxv1alpha1.SweepOrphaned {
			s.summary.Orphans++
		}
		if finding.Reason == netboxv1alpha1.SweepSuspected {
			s.summary.Suspected++
		}
	}

	if len(findings) <= limit {
		return findings, false
	}

	return findings[:limit], true
}

// reasonRank orders the finding reasons by how much they need somebody to act.
func reasonRank(reason netboxv1alpha1.SweepFindingReason) int {
	return slices.Index(sweepReasons, reason)
}

// publish sets the findings gauge for every scanned kind, including the zeros.
//
// The zeros are the point: without them an orphan that somebody adopts or deletes by hand
// would leave its series pinned at the last non-zero value until the process restarted, and
// an alert on it would never clear.
func (s *sweepResult) publish() {
	counts := make(map[sweepMetricKey]int, len(s.kinds)*len(sweepReasons))
	for _, kind := range s.kinds {
		for _, reason := range sweepReasons {
			counts[sweepMetricKey{kind: kind, reason: string(reason)}] = 0
		}
	}

	for _, finding := range s.findings {
		counts[sweepMetricKey{kind: finding.Kind, reason: string(finding.Reason)}]++
	}

	for key, count := range counts {
		metrics.SweepFindings.WithLabelValues(key.kind, key.reason).Set(float64(count))
	}
}

// sweepMetricKey is one gauge series.
type sweepMetricKey struct {
	kind   string
	reason string
}

// summarise is the one-line condition message. It reports the counts rather than a verdict,
// because "3 orphans" is actionable and "found orphans" is not.
func (s *sweepResult) summarise() string {
	return fmt.Sprintf("scanned %d stamped object(s) over %d kind(s) in %d list call(s): "+
		"%d claimed, %d orphaned, %d suspected, %d unattributed, %d in other namespaces; "+
		"nothing was deleted",
		s.summary.Scanned, len(s.kinds), s.lists, s.summary.Claimed, s.summary.Orphans,
		s.summary.Suspected, s.summary.Unattributed, s.summary.Foreign)
}
