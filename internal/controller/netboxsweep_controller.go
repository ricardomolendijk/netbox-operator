package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// Fallbacks for a NetBoxSweep that reached the controller without the CRD defaults applied
// -- a hand-built object in a test, or a spec stored before a field existed. They are the
// same values the +kubebuilder:default markers carry, and they exist in Go as well so the
// two halves cannot drift to different notions of "the default".
const (
	defaultSweepInterval    = 24 * time.Hour
	defaultSweepTimeout     = 10 * time.Minute
	defaultSweepMaxFindings = 100
)

// sweepRetry is how long after a refused run to try again.
//
// Fixed and short rather than spec.interval, because every refusal is a state somebody else
// is fixing -- an endpoint coming Ready, a driftMode being changed back, a NetBox coming
// back up -- and waiting a day to notice would make the sweep useless for the whole day
// after a five-minute outage. It is short enough to notice and long enough that a NetBox
// which is down does not get scanned in a loop.
const sweepRetry = 5 * time.Minute

// NetBoxSweepReconciler reports NetBox objects this namespace has left behind, and never
// deletes one.
//
// It holds no Writer of any kind against NetBox: the only NetBox call it makes is List,
// which is why "a sweep cannot delete" is a property of the code rather than of a flag
// (docs/operations/sweeps.md).
type NetBoxSweepReconciler struct {
	client.Client

	// Clients is the cache the NetBoxEndpoint controller fills. A miss means the endpoint
	// is not Ready, which refuses the run rather than failing it.
	//
	// Read directly rather than through endpointProvider, because a sweep needs List and
	// reconciler.Endpoint deliberately narrows the client to the four calls the engine
	// makes. Going through the provider would mean widening that interface for the one
	// caller that must never write.
	Clients *ClientCache

	// Scheme resolves a Kind to its typed list, which is how the sweep reads every CR of
	// every listed kind without a branch on Kind and without a second informer per kind.
	Scheme *runtime.Scheme

	// Recorder emits the Events a `kubectl describe` shows. Optional: a nil recorder
	// records nothing.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsweeps,verbs=get;list;watch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsweeps/status,verbs=get;update;patch

// No `netboxsweeps/finalizers` and no per-kind rule here. A sweep owns nothing in NetBox,
// so deleting one needs no finalizer; and the CRs it lists are already granted
// `get;list;watch` by their own controllers' markers, so a kind that ships is sweepable
// without a second grant. A wildcard rule over the group would only add reach the operator
// does not need.

// Reconcile runs one sweep, or refuses to.
//
// Every exit writes a `Ready` condition and requeues, and a refusal leaves status.findings
// and status.summary exactly as the last completed run left them. That asymmetry is the
// whole safety design: an empty findings list must only ever mean "the last complete scan
// found nothing", never "the last scan could not see anything".
func (r *NetBoxSweepReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = logf.IntoContext(ctx, logf.FromContext(ctx).WithValues("kind", "NetBoxSweep"))

	sweep := &netboxv1alpha1.NetBoxSweep{}
	if err := r.Get(ctx, req.NamespacedName, sweep); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("fetching sweep: %w", err)
	}

	before := sweep.Status.DeepCopy()

	if sweep.Spec.Suspend {
		// No requeue: an edit to spec.suspend is a watch event on this object, so there is
		// nothing a timer would notice that the watch does not.
		return ctrl.Result{}, r.suspended(ctx, sweep, before)
	}

	setSweepCondition(sweep, netboxv1alpha1.ConditionSweepSuspended, metav1.ConditionFalse,
		netboxv1alpha1.ReasonSweepScheduled, "scheduled")

	endpoint, reason, err := r.resolveEndpoint(ctx, sweep)
	if err != nil {
		return r.refuse(ctx, sweep, before, reason, err)
	}

	descriptors, reason, err := sweepDescriptors(sweep.Spec.Kinds)
	if err != nil {
		return r.refuse(ctx, sweep, before, reason, err)
	}

	started := time.Now()
	scanCtx, cancel := context.WithTimeout(ctx, sweepDuration(sweep.Spec.Timeout, defaultSweepTimeout))
	defer cancel()

	result, err := r.scan(scanCtx, sweep, endpoint, descriptors)
	if err != nil {
		return r.refuse(ctx, sweep, before, scanReason(err), err)
	}

	return r.complete(ctx, sweep, before, result, time.Since(started))
}

// suspended records a sweep that is deliberately not running.
//
// `Ready` is left exactly as it was, because a suspended sweep has not failed: its findings
// are still the last true answer, and overwriting the condition would lose the reason the
// last real run settled on.
func (r *NetBoxSweepReconciler) suspended(ctx context.Context, sweep *netboxv1alpha1.NetBoxSweep,
	before *netboxv1alpha1.NetBoxSweepStatus,
) error {
	metrics.SweepRuns.WithLabelValues(netboxv1alpha1.ReasonSweepSuspended).Inc()

	sweep.Status.NextRunTime = nil
	setSweepCondition(sweep, netboxv1alpha1.ConditionSweepSuspended, metav1.ConditionTrue,
		netboxv1alpha1.ReasonSweepSuspended, "spec.suspend is true; findings are preserved")

	return r.writeStatus(ctx, sweep, before)
}

// refuse records a run that did not happen, with the reason and zero changes to the
// findings.
func (r *NetBoxSweepReconciler) refuse(ctx context.Context, sweep *netboxv1alpha1.NetBoxSweep,
	before *netboxv1alpha1.NetBoxSweepStatus, reason string, cause error,
) (ctrl.Result, error) {
	metrics.SweepRuns.WithLabelValues(reason).Inc()

	changed := conditionTransitioned(sweep.Status.Conditions, netboxv1alpha1.ConditionSweepReady,
		metav1.ConditionFalse, reason)

	// Error rather than info: every refusal needs somebody to change something, and none of
	// them resolves on its own (CONTRIBUTING.md, "Logging"). At debug on a repeat, because
	// a NetBox that is down would otherwise emit one error per sweep per retry forever.
	log := logf.FromContext(ctx)
	if changed {
		log.Error(cause, "sweep refused", "action", "sweep", "reason", reason)
		r.event(sweep, corev1.EventTypeWarning, netboxv1alpha1.EventSweepRefused,
			fmt.Sprintf("%s: %v; findings are from %s", reason, cause, lastRun(sweep)))
	} else {
		log.V(1).Info("sweep still refused", "action", "sweep", "reason", reason, "err", cause.Error())
	}

	sweep.Status.NextRunTime = ptrTime(time.Now().Add(sweepRetry))
	setSweepCondition(sweep, netboxv1alpha1.ConditionSweepReady, metav1.ConditionFalse,
		reason, cause.Error())

	if err := r.writeStatus(ctx, sweep, before); err != nil {
		// controller-runtime discards RequeueAfter when the error is non-nil, so the two
		// must never be returned together.
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reconciler.Jitter(sweepRetry)}, nil
}

// complete records a run that scanned every listed kind.
func (r *NetBoxSweepReconciler) complete(ctx context.Context, sweep *netboxv1alpha1.NetBoxSweep,
	before *netboxv1alpha1.NetBoxSweepStatus, result sweepResult, took time.Duration,
) (ctrl.Result, error) {
	metrics.SweepRuns.WithLabelValues(netboxv1alpha1.ReasonSweepComplete).Inc()
	result.publish()

	interval := sweepDuration(sweep.Spec.Interval, defaultSweepInterval)
	now := time.Now()

	// report() before the summary is read: it is where the grace period splits an unclaimed
	// object into orphaned and suspected, so the counts are not final until it has run.
	sweep.Status.Findings, sweep.Status.FindingsTruncated = result.report(maxFindings(sweep))
	sweep.Status.Summary = result.summary
	sweep.Status.LastRunTime = ptrTime(now)
	sweep.Status.NextRunTime = ptrTime(now.Add(interval))
	sweep.Status.LastRunDuration = took.Round(time.Millisecond).String()
	setSweepCondition(sweep, netboxv1alpha1.ConditionSweepReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonSweepComplete, result.summarise())

	logf.FromContext(ctx).Info("sweep complete", "action", "sweep",
		"kinds", len(sweep.Spec.Kinds), "lists", result.lists,
		"scanned", result.summary.Scanned, "claimed", result.summary.Claimed,
		"orphans", result.summary.Orphans, "suspected", result.summary.Suspected,
		"unattributed", result.summary.Unattributed, "foreign", result.summary.Foreign,
		"took", took.String())

	// The whole list at debug, because status.findings is capped and the cap is exactly
	// where a large report stops being readable.
	if log := logf.FromContext(ctx).V(1); log.Enabled() {
		for _, finding := range result.findings {
			log.Info("sweep finding", "action", "sweep", "findingKind", finding.Kind,
				"netboxID", finding.NetBoxID, "display", finding.Display,
				"owner", finding.Owner, "reason", string(finding.Reason), "url", finding.URL)
		}
	}

	if result.summary.Orphans > 0 {
		r.event(sweep, corev1.EventTypeNormal, netboxv1alpha1.EventOrphansFound,
			fmt.Sprintf("%d orphaned netbox object(s) with no live CR in this namespace; "+
				"see status.findings. Nothing was deleted", result.summary.Orphans))
	}

	if err := r.writeStatus(ctx, sweep, before); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reconciler.Jitter(interval)}, nil
}

// writeStatus persists what this pass observed, and writes nothing when it observed what
// was already stored.
//
// Every watcher wakes on a resourceVersion bump, so a status that says nothing new is an
// Argo CD refresh and an audit entry per sweep per run. The comparison is of the whole
// status rather than a chosen few fields, for the same reason the endpoint controller's is
// (NBO-078).
func (r *NetBoxSweepReconciler) writeStatus(ctx context.Context, sweep *netboxv1alpha1.NetBoxSweep,
	before *netboxv1alpha1.NetBoxSweepStatus,
) error {
	sweep.Status.ObservedGeneration = sweep.Generation
	if equality.Semantic.DeepEqual(before, &sweep.Status) {
		logf.FromContext(ctx).V(1).Info("status unchanged; not writing", "action", "none")

		return nil
	}

	if err := r.Status().Update(ctx, sweep); err != nil {
		return fmt.Errorf("updating sweep status: %w", err)
	}

	return nil
}

// sweepEndpoint is the endpoint the sweep scans through, once every guard has passed.
type sweepEndpoint struct {
	// lister is the NetBox client, narrowed to the one call a sweep is allowed to make.
	lister sweepLister

	// tag is the managedBy tag's name, used as the `?tag=` filter.
	tag string

	// clusterFilter and clusterID are the `?cf_<field>=<id>` half of the scope: the filter
	// name NetBox expects, and this cluster's identifier.
	clusterFilter string
	clusterID     string

	// uidField and ownerField are the custom-field names to read a stamp out of a listed
	// object.
	uidField   string
	ownerField string
}

// sweepLister is the only NetBox capability a sweep has.
//
// Narrowed from *netbox.Client to one read, which is the structural half of "a sweep never
// deletes": there is no Delete on this interface to call by accident, and a future change
// that wanted one would have to widen a type whose doc comment says why it must not.
type sweepLister interface {
	List(ctx context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error)
}

// resolveEndpoint applies every guard that can refuse a run before a single request is
// sent, and returns the refusal reason alongside the error so the caller does not have to
// classify it.
func (r *NetBoxSweepReconciler) resolveEndpoint(ctx context.Context,
	sweep *netboxv1alpha1.NetBoxSweep,
) (sweepEndpoint, string, error) {
	name := sweep.Spec.EndpointRef

	nbClient, stamp, ok := r.Clients.Lookup(sweep.Namespace, name)
	if !ok {
		return sweepEndpoint{}, netboxv1alpha1.ReasonSweepEndpointNotReady,
			fmt.Errorf("netboxendpoint %s/%s has no usable client", sweep.Namespace, name)
	}

	// Covers spec.mode: DryRun and driftMode: Report together, because both hand out a
	// client that cannot write and therefore a cluster whose CRs never got a status.id --
	// every object would look unclaimed. Read off the client rather than the spec so a
	// mode that suppresses writes cannot be missed by a second copy of the rule.
	if nbClient.DryRun() {
		return sweepEndpoint{}, netboxv1alpha1.ReasonSweepEndpointDryRun,
			fmt.Errorf("netboxendpoint %s/%s hands out a client that cannot write "+
				"(spec.mode: DryRun, or driftMode: Report), so no CR has a status.id and "+
				"every object would look unclaimed", sweep.Namespace, name)
	}

	endpoint := &netboxv1alpha1.NetBoxEndpoint{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: sweep.Namespace, Name: name}, endpoint); err != nil {
		return sweepEndpoint{}, netboxv1alpha1.ReasonSweepEndpointNotReady,
			fmt.Errorf("reading netboxendpoint %s/%s: %w", sweep.Namespace, name, err)
	}

	if endpoint.Spec.DriftMode == netboxv1alpha1.DriftOff {
		return sweepEndpoint{}, netboxv1alpha1.ReasonSweepDriftOff,
			fmt.Errorf("netboxendpoint %s/%s has driftMode: Off, so the operator is not "+
				"tracking netbox state and the absence of a claim proves nothing",
				sweep.Namespace, name)
	}

	if err := stampUsable(stamp); err != nil {
		return sweepEndpoint{}, netboxv1alpha1.ReasonSweepProvenanceDisabled,
			fmt.Errorf("netboxendpoint %s/%s: %w", sweep.Namespace, name, err)
	}

	return sweepEndpoint{
		lister:        nbClient,
		tag:           stamp.Tag,
		clusterFilter: "cf_" + stamp.ClusterField,
		clusterID:     stamp.ClusterID,
		uidField:      stamp.UIDField,
		ownerField:    stamp.OwnerField,
	}, "", nil
}

// params is the query one kind's scan sends.
//
// The cluster filter is what makes a sweep safe against a shared NetBox: two clusters
// writing one NetBox each stamp their own spec.managedBy.clusterID, they are never
// coordinated (NBO-047, docs/operations/provenance.md), and this filter is the only thing
// standing between "my orphans" and "the other cluster's healthy objects". It is a
// server-side exact match, so an object of another cluster is never even fetched.
//
// The tag is added only for a kind whose model carries `tags`: NetBox rejects a filter a
// model has no field for, so sending it unconditionally would 400 on the kinds that most
// need scanning.
func (e sweepEndpoint) params(d registry.Descriptor) netbox.Params {
	query := netbox.Params{e.clusterFilter: e.clusterID}
	if d.Taggable && e.tag != "" {
		query["tag"] = e.tag
	}

	return query
}

// ptrTime is metav1.Time's missing address-of.
func ptrTime(t time.Time) *metav1.Time {
	out := metav1.NewTime(t)

	return &out
}

// lastRun renders when the findings in status were last true, for a refusal message.
func lastRun(sweep *netboxv1alpha1.NetBoxSweep) string {
	if sweep.Status.LastRunTime == nil {
		return "no completed run"
	}

	return sweep.Status.LastRunTime.UTC().Format(time.RFC3339)
}

// sweepDuration applies a fallback to a duration that may have reached the controller
// without its CRD default.
func sweepDuration(value metav1.Duration, fallback time.Duration) time.Duration {
	if value.Duration > 0 {
		return value.Duration
	}

	return fallback
}

// maxFindings is the report cap, with the CRD default as the fallback.
func maxFindings(sweep *netboxv1alpha1.NetBoxSweep) int {
	if sweep.Spec.MaxFindings > 0 {
		return int(sweep.Spec.MaxFindings)
	}

	return defaultSweepMaxFindings
}

// scanReason classifies why a scan stopped.
//
// Truncation and the timeout are called out by name because they are the two failures that
// would otherwise be reported as "netbox is unhappy" while actually meaning "the answer you
// are looking at is incomplete" -- and an incomplete list is the input that turns a report
// into a false accusation.
func scanReason(err error) string {
	var truncated *netbox.TruncatedError
	if errors.As(err, &truncated) {
		return netboxv1alpha1.ReasonSweepTruncated
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return netboxv1alpha1.ReasonSweepTimeout
	}

	return netboxv1alpha1.ReasonSweepAPIError
}

// setSweepCondition writes one condition onto a sweep.
func setSweepCondition(sweep *netboxv1alpha1.NetBoxSweep, condType string,
	status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&sweep.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: sweep.Generation,
	})
}

// event records an Event, when there is a recorder to record it to.
func (r *NetBoxSweepReconciler) event(sweep *netboxv1alpha1.NetBoxSweep, eventtype, reason, message string) {
	if r.Recorder == nil {
		return
	}

	r.Recorder.Eventf(sweep, nil, eventtype, reason, netboxv1alpha1.EventAction(reason),
		"%s", netboxv1alpha1.EventNote(message))
}

// SetupWithManager registers the sweep controller.
//
// No watch on the NetBoxEndpoint and no watch on the swept kinds. A sweep is a periodic
// report, not a convergence loop: waking it on every CR event of every listed kind would
// turn a daily list of every stamped object into one per CR write, which is the load
// spec.interval exists to bound.
func (r *NetBoxSweepReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&netboxv1alpha1.NetBoxSweep{}).
		Named("netboxsweep").
		Complete(r); err != nil {
		return fmt.Errorf("building sweep controller: %w", err)
	}

	return nil
}
