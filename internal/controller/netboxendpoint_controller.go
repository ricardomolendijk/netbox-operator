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
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// secretRefIndex indexes endpoints by the Secret they read, so a Secret event can find
// the endpoints to re-reconcile without listing every endpoint in the cluster.
const secretRefIndex = "spec.secretRefs"

// NetBoxEndpointReconciler turns a NetBoxEndpoint into a live, authenticated,
// version-checked client that object controllers fetch from the cache.
type NetBoxEndpointReconciler struct {
	client.Client
	Cache *ClientCache

	// Recorder emits the Events a user sees in `kubectl describe`, which is the only
	// answer to "why is this endpoint not working" that needs no knowledge of conditions.
	// Optional: a nil recorder simply records nothing, so a test that does not care about
	// Events does not have to wire one.
	Recorder record.EventRecorder

	// Secrets is the deploy-time namespace list the manager's Secret informer and RBAC
	// were built from, so an endpoint in a namespace nobody granted gets a condition
	// naming the namespace instead of the cache's or the API server's own words. The zero
	// value is cluster-wide, which is every namespace and therefore never rejects.
	Secrets SecretScope
}

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxendpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// There is deliberately no `secrets` marker here, and the generated ClusterRole therefore
// grants no Secret access at all. Secrets are read under one namespaced Role per namespace
// named in config/rbac/credential-namespaces/namespaces.txt, because a marker can only
// generate a cluster-wide rule -- a namespace list is deploy-time configuration and does
// not belong in Go source. Removing this comment and adding the marker back re-opens
// NBO-072; see docs/operations/rbac.md.

// Reconcile builds a client for one endpoint and records what it found.
//
// It never returns an error for anything about NetBox's availability: an unreachable or
// misconfigured NetBox is a condition on this object, not a controller failure, or the
// manager's error rate becomes a function of someone else's uptime.
func (r *NetBoxEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// `kind` completes the stable key set on every line this reconcile produces --
	// including the ones the NetBox client writes, which add `endpoint`, `method` and
	// `action` of their own. Only `kind`: controller-runtime already put `namespace` and
	// `name` on the context logger, and repeating them emits the key twice in one JSON
	// object (CONTRIBUTING.md, "Logging").
	ctx = logf.IntoContext(ctx, logf.FromContext(ctx).WithValues("kind", "NetBoxEndpoint"))

	endpoint := &netboxv1alpha1.NetBoxEndpoint{}
	if err := r.Get(ctx, req.NamespacedName, endpoint); err != nil {
		if apierrors.IsNotFound(err) {
			r.Cache.Forget(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching endpoint: %w", err)
	}

	// The status as stored, before this pass touches it. writeStatus compares against it,
	// so a resync that finds the endpoint exactly as it left it writes nothing -- which is
	// what the engine's finish() already does for every object kind.
	before := endpoint.Status.DeepCopy()

	token, secretVersion, err := r.readToken(ctx, endpoint)
	if err != nil {
		return r.fail(ctx, endpoint, before, reasonFor(err), err)
	}

	cfg, err := r.buildConfig(ctx, endpoint, token)
	if err != nil {
		return r.fail(ctx, endpoint, before, reasonForConfig(err), err)
	}
	nbClient, err := netbox.New(cfg)
	if err != nil {
		return r.fail(ctx, endpoint, before, netboxv1alpha1.ReasonInvalidConfig, err)
	}

	status, err := nbClient.Status(ctx)
	if err != nil {
		return r.fail(ctx, endpoint, before, reasonFor(err), err)
	}

	// Recorded before the gate: whatever NetBox said is the most useful thing in status
	// when the answer is "that is not a version".
	endpoint.Status.NetBoxVersion = status.Version
	endpoint.Status.Plugins = status.Plugins

	version, supported, err := netbox.SupportedVersion(status.Version)
	if err != nil {
		return r.fail(ctx, endpoint, before, netboxv1alpha1.ReasonVersionUnparseable,
			fmt.Errorf("netbox reported version %q: %w", status.Version, err))
	}

	if !supported {
		// The guard that matters. NetBox 4.2 replaced `site` with a polymorphic scope on
		// four models, and writing `site` to 4.2+ silently no-ops -- so an operator run
		// against an out-of-range server does not fail, it quietly does nothing. Refuse
		// instead.
		return r.fail(ctx, endpoint, before, netboxv1alpha1.ReasonVersionUnsupported,
			fmt.Errorf("netbox %s is outside the supported range >=%s, <%s",
				version, netbox.MinVersion, netbox.MaxVersion))
	}

	// After the version gate and before the client is cached, so that an endpoint whose
	// provenance cannot be written is never handed to an object controller (NBO-075).
	stamp, err := r.provision(ctx, endpoint, nbClient)
	if err != nil {
		reason := provenanceReason(err)
		r.provisionCondition(endpoint, metav1.ConditionFalse, reason, err.Error())

		return r.fail(ctx, endpoint, before, reason, err)
	}

	r.Cache.put(clientKey{
		namespace:     endpoint.Namespace,
		name:          endpoint.Name,
		generation:    endpoint.Generation,
		secretVersion: secretVersion,
	}, nbClient, stamp)

	return r.ready(ctx, endpoint, before, cfg, status)
}

// ready records a usable endpoint. Symmetric with fail, so the log line, the Event, the
// metric and the conditions for one outcome all live in one place.
func (r *NetBoxEndpointReconciler) ready(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint,
	before *netboxv1alpha1.NetBoxEndpointStatus, cfg netbox.Config, status netbox.ServerStatus,
) (ctrl.Result, error) {
	metrics.EndpointReconcileTotal.WithLabelValues(netboxv1alpha1.ReasonReady).Inc()

	// Info on the transition, debug on a resync that found it exactly as it was left.
	// `endpoint ready` at info every pass is one line per endpoint per resync for the
	// lifetime of the process, and a log where nothing means anything is a log nobody
	// reads (CONTRIBUTING.md, "Logging").
	became := transitioned(e, netboxv1alpha1.ConditionReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady)

	logf.FromContext(ctx).V(debugUnless(became)).Info("endpoint ready",
		"action", "probe",
		"url", e.Spec.URL, "netboxVersion", status.Version,
		// cfg.Mode rather than spec.mode, because driftMode: Report overrides it: the line
		// has to report the mode the client is actually in, or a suppressed write looks
		// like a bug.
		"mode", cfg.Mode, "driftMode", e.Spec.DriftMode, "plugins", status.Plugins,
		"insecureSkipVerify", cfg.InsecureSkipVerify)

	if became {
		r.event(e, corev1.EventTypeNormal, netboxv1alpha1.ReasonReady,
			fmt.Sprintf("netbox %s at %s accepted the token; client available",
				status.Version, e.Spec.URL))
	}

	setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "token accepted")
	setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, fmt.Sprintf("netbox %s", status.Version))
	setCondition(e, netboxv1alpha1.ConditionReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "client available")

	if err := r.writeStatus(ctx, e, before); err != nil {
		// controller-runtime discards RequeueAfter when the error is non-nil, so the two
		// must never be returned together or a status conflict silently loses the resync.
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reconciler.Jitter(resyncPeriod(e))}, nil
}

// readToken fetches the API token and the Secret's resourceVersion, which is what makes
// the client cache invalidate on rotation.
func (r *NetBoxEndpointReconciler) readToken(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint) (string, string, error) {
	// The only place this check is needed: both Secrets an endpoint can name live in the
	// endpoint's own namespace, and the token is read first, so a namespace nobody granted
	// fails here before buildConfig can reach for a CA bundle in the same namespace.
	if err := r.Secrets.Check(e.Namespace); err != nil {
		return "", "", err
	}

	secret := &corev1.Secret{}
	name := types.NamespacedName{Namespace: e.Namespace, Name: e.Spec.TokenSecretRef.Name}
	if err := r.Get(ctx, name, secret); err != nil {
		return "", "", unreadableSecret("token", name, err)
	}

	key := orDefaultKey(e.Spec.TokenSecretRef.Key, "token")
	token := string(secret.Data[key])
	if token == "" {
		return "", "", fmt.Errorf("%w: secret %s has no key %q", errTokenMissing, name, key)
	}
	return token, secret.ResourceVersion, nil
}

var (
	errTokenMissing = errors.New("token missing")
)

// unreadableSecret explains a Secret the controller could not read. It exists because the
// manager's Secret cache is label-selected: an unlabelled Secret is reported as NotFound
// even though it is sitting in the API server, and a bare "not found" sends the user
// looking for a typo in a name that is correct. The condition has to name the label.
//
// `Forbidden` gets its own wording for the same reason: it can only mean the namespace's
// Role is missing, which is a deployment step, not a manifest to re-read.
func unreadableSecret(what string, name types.NamespacedName, err error) error {
	if apierrors.IsForbidden(err) {
		// The namespace is in the manager's list -- SecretScope.Check passed -- but the
		// cluster does not agree, so the Role the list promised is missing or names other
		// verbs. Nothing the operator can fix, and worth saying plainly.
		return fmt.Errorf("reading %s secret %s: %w; the operator's namespace list "+
			"includes %s but the cluster grants it nothing there: apply the Role and "+
			"RoleBinding from config/rbac/credential-namespaces (see docs/operations/rbac.md)",
			what, name, err, name.Namespace)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading %s secret %s: %w", what, name, err)
	}
	return fmt.Errorf("reading %s secret %s: %w; the secret may exist but be invisible to "+
		"the operator, which reads only Secrets labelled %s=%s (see docs/operations/rbac.md)",
		what, name, err, CredentialLabel, CredentialLabelValue)
}

func (r *NetBoxEndpointReconciler) buildConfig(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint, token string) (netbox.Config, error) {
	cfg := netbox.Config{
		URL:   e.Spec.URL,
		Token: token,
		Mode:  clientMode(e),
		// No client-side retries on the probe. The controller already requeues, one
		// worker serves every endpoint, and four retries behind a 30s timeout let a
		// single black-holed NetBox stall every other endpoint for minutes.
		MaxRetries: netbox.Retries(0),
		Timeout:    e.Spec.Timeout.Duration,
	}
	if e.Spec.RateLimit != nil {
		cfg.QPS = float64(e.Spec.RateLimit.QPS)
		cfg.Burst = int(e.Spec.RateLimit.Burst)
	}
	if e.Spec.TLSConfig == nil {
		return cfg, nil
	}

	cfg.InsecureSkipVerify = e.Spec.TLSConfig.InsecureSkipVerify
	if e.Spec.TLSConfig.CABundleSecretRef == nil {
		return cfg, nil
	}

	ref := e.Spec.TLSConfig.CABundleSecretRef
	secret := &corev1.Secret{}
	name := types.NamespacedName{Namespace: e.Namespace, Name: ref.Name}
	if err := r.Get(ctx, name, secret); err != nil {
		return netbox.Config{}, unreadableSecret("ca bundle", name, err)
	}
	key := orDefaultKey(ref.Key, "ca.crt")
	cfg.CABundle = secret.Data[key]
	if len(cfg.CABundle) == 0 {
		return netbox.Config{}, fmt.Errorf("ca bundle secret %s has no key %q", name, key)
	}
	return cfg, nil
}

// clientMode collapses the two spec fields that can stop a write into the one thing the
// client enforces.
//
// driftMode: Report has to be genuinely non-mutating -- a mode that writes and logs
// teaches people to distrust it, which is worse than not offering it -- and the only way to
// promise that is to hand the engine a client that cannot write. The alternative, a flag
// the engine consults before each mutation, is a promise that lasts until the first write
// path somebody forgets to guard, and the finalizer's delete is already a second one
// (docs/decisions/0005-gitops-coexistence.md).
func clientMode(e *netboxv1alpha1.NetBoxEndpoint) netbox.Mode {
	if e.Spec.DriftMode == netboxv1alpha1.DriftReport {
		return netbox.ModeDryRun
	}

	return netbox.Mode(e.Spec.Mode)
}

// fail records why the endpoint is not usable and drops any cached client, so object
// controllers stop writing through a connection that has since been rejected.
func (r *NetBoxEndpointReconciler) fail(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint,
	before *netboxv1alpha1.NetBoxEndpointStatus, reason string, cause error,
) (ctrl.Result, error) {
	r.Cache.Forget(e.Namespace, e.Name)
	metrics.EndpointReconcileTotal.WithLabelValues(reason).Inc()

	// Error on the transition into this state, debug on every repeat of it. An endpoint
	// whose token was revoked re-probes every two minutes and an unreachable one every
	// thirty seconds; at error, either buries whatever is actually new under thousands of
	// identical lines a day. The Event and the condition carry the standing state.
	changed := transitioned(e, netboxv1alpha1.ConditionReady, metav1.ConditionFalse, reason)
	log := logf.FromContext(ctx).WithValues("reason", reason, "action", "probe")

	if changed {
		log.Error(cause, "endpoint not ready")
		r.event(e, corev1.EventTypeWarning, reason, cause.Error())
	} else {
		log.V(1).Info("endpoint is still not ready", "err", cause.Error())
	}

	// A failure upstream of a check must not leave that check's previous answer standing:
	// a stale Authenticated=True next to Ready=False reads as "the token is fine", which
	// is not something this reconcile established. Unknown is the honest value.
	// Every condition must describe *this* reconcile. Which ones are knowable depends on
	// how far the reconcile got, so the switch is on the stage that failed.
	switch reason {
	case netboxv1alpha1.ReasonAuthError, netboxv1alpha1.ReasonTokenMissing, netboxv1alpha1.ReasonSecretMissing:
		// Failed at or before reading the token: authentication is answered, the version
		// was never asked.
		setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionFalse, reason, cause.Error())
		setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionUnknown, reason,
			"not probed: authentication failed first")
	case netboxv1alpha1.ReasonVersionUnsupported, netboxv1alpha1.ReasonVersionUnparseable:
		// Reaching the version gate means the probe succeeded, which means the token was
		// accepted. Leaving Authenticated unwritten made it absent on a first reconcile
		// that lands straight on a version failure.
		setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionTrue,
			netboxv1alpha1.ReasonReady, "token accepted")
		setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionFalse, reason, cause.Error())
	case netboxv1alpha1.ReasonBootstrapDisabled, netboxv1alpha1.ReasonBootstrapFailed:
		// The provenance bootstrap runs after both gates, so reaching it means both were
		// passed. Writing Unknown here would retract two answers this reconcile did
		// establish, and send whoever reads it to the token or the version -- neither of
		// which is what is wrong.
		setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionTrue,
			netboxv1alpha1.ReasonReady, "token accepted")
		setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionTrue,
			netboxv1alpha1.ReasonReady, "version checked before the provenance bootstrap ran")
	default:
		// ProbeFailed, InvalidConfig, CABundleMissing: the token itself may be perfectly
		// good, so claiming Authenticated=False would point the reader at the wrong
		// Secret. Neither question was answered.
		setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionUnknown, reason,
			"not established: "+cause.Error())
		setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionUnknown, reason,
			"not probed: "+cause.Error())
	}
	setCondition(e, netboxv1alpha1.ConditionReady, metav1.ConditionFalse, reason, cause.Error())

	if err := r.writeStatus(ctx, e, before); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reconciler.Jitter(failureBackoff(reason))}, nil
}

// failureBackoff spaces out retries by how likely a retry is to help. A version mismatch
// will not fix itself, so re-probing every 30 seconds is pure noise.
//
// The tier is the interval's intent; callers requeue on reconciler.Jitter of it, which
// spreads endpoints that failed together without moving any of them out of their tier.
func failureBackoff(reason string) time.Duration {
	switch reason {
	case netboxv1alpha1.ReasonVersionUnsupported, netboxv1alpha1.ReasonVersionUnparseable:
		return 10 * time.Minute
	// A definition somebody has to create by hand, in the same tier as a version mismatch
	// for the same reason: nothing the operator does will produce one.
	case netboxv1alpha1.ReasonBootstrapDisabled:
		return 10 * time.Minute
	case netboxv1alpha1.ReasonAuthError, netboxv1alpha1.ReasonInvalidConfig,
		// A refused bootstrap is usually a token without extras.add_customfield, so it
		// belongs with AuthError: a permission grant, and a couple of minutes is soon
		// enough to notice it landing.
		netboxv1alpha1.ReasonBootstrapFailed:
		return 2 * time.Minute
	default:
		return 30 * time.Second
	}
}

// reasonFor maps a client error to a condition reason. The client already classified the
// failure by type, so this is a translation, not a re-diagnosis.
func reasonFor(err error) string {
	var authErr *netbox.AuthError
	switch {
	case errors.Is(err, errTokenMissing):
		return netboxv1alpha1.ReasonTokenMissing
	// One reason for all three ways a Secret can be out of reach -- absent, unlabelled, or
	// in a namespace the operator holds no Role for. They are one problem to the reader
	// ("the operator cannot read that Secret") and the message says which; a reason per
	// cause would be a new API constant for each, and status.conditions is API.
	case apierrors.IsNotFound(err), apierrors.IsForbidden(err), errors.Is(err, errNamespaceNotGranted):
		return netboxv1alpha1.ReasonSecretMissing
	case errors.As(err, &authErr):
		return netboxv1alpha1.ReasonAuthError
	default:
		return netboxv1alpha1.ReasonProbeFailed
	}
}

// orDefaultKey applies the per-field Secret key default. It lives here rather than as a
// CRD marker because SecretKeyRef is shared by tokenSecretRef and caBundleSecretRef,
// which need different defaults.
func orDefaultKey(key, fallback string) string {
	if key == "" {
		return fallback
	}
	return key
}

// reasonForConfig classifies a config failure. A missing Secret is a missing Secret
// whichever field referenced it.
func reasonForConfig(err error) string {
	if apierrors.IsNotFound(err) {
		return netboxv1alpha1.ReasonCABundleMissing
	}
	return netboxv1alpha1.ReasonInvalidConfig
}

// resyncPeriod is how long until the next re-probe. The fallback exists only for an object
// that reached the controller without the CRD default applied -- a hand-built object in a
// test, or a spec written before the field existed -- and it borrows the engine's constant
// rather than restating it, so the two halves of the binary cannot drift to different
// notions of "the default".
func resyncPeriod(e *netboxv1alpha1.NetBoxEndpoint) time.Duration {
	if e.Spec.ResyncPeriod.Duration > 0 {
		return e.Spec.ResyncPeriod.Duration
	}
	return reconciler.DefaultResync
}

// transitioned reports whether writing this condition would change the endpoint's state.
//
// Status and reason only; the message is deliberately excluded. A probe failure message
// carries the wording of the underlying error, and a timeout whose text differs by a
// millisecond is not a state change -- keying on it would re-fire the Event and the error
// line on every retry, which is the thing this exists to prevent.
func transitioned(e *netboxv1alpha1.NetBoxEndpoint, condType string,
	status metav1.ConditionStatus, reason string,
) bool {
	return conditionTransitioned(e.Status.Conditions, condType, status, reason)
}

// conditionTransitioned is transitioned over a bare condition list, for the kinds whose
// status is not a NetBoxEndpoint's. One implementation rather than one per controller,
// because "the message is deliberately excluded" is the part that is easy to get wrong and
// the part that decides whether an Event re-fires on every retry.
func conditionTransitioned(conditions []metav1.Condition, condType string,
	status metav1.ConditionStatus, reason string,
) bool {
	existing := meta.FindStatusCondition(conditions, condType)

	return existing == nil || existing.Status != status || existing.Reason != reason
}

// debugUnless returns the logr verbosity to log at: 0 (info) when something changed, 1
// (debug) when it did not. CONTRIBUTING.md: info means state changed.
func debugUnless(changed bool) int {
	if changed {
		return 0
	}
	return 1
}

// event records an Event, when there is a recorder to record it to. Callers emit only on
// a transition: an Event per resync would put one line per endpoint per interval into the
// namespace, and `kubectl describe` would show a page of the same thing.
func (r *NetBoxEndpointReconciler) event(e *netboxv1alpha1.NetBoxEndpoint, eventtype, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(e, eventtype, reason, message)
}

func setCondition(e *netboxv1alpha1.NetBoxEndpoint, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: e.Generation,
	})
}

// writeStatus persists what this pass observed, and writes nothing when the pass observed
// what was already stored.
//
// The comparison is of the whole status rather than a chosen few fields: a field list is
// something a future change has to remember to extend, and the field that gets missed is
// the next one added. It is stable because every condition is written through
// meta.SetStatusCondition, which leaves LastTransitionTime alone when the condition's
// status has not changed -- so an unchanged reconcile yields an identical status rather
// than a fresh timestamp. Every exit from Reconcile sets Ready, so a first pass always
// differs from the empty stored status and an endpoint still reports something.
func (r *NetBoxEndpointReconciler) writeStatus(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint,
	before *netboxv1alpha1.NetBoxEndpointStatus,
) error {
	e.Status.ObservedGeneration = e.Generation
	if equality.Semantic.DeepEqual(before, &e.Status) {
		// Every watcher of this object wakes on a resourceVersion bump, so a write that
		// says nothing new is not free: it is an Argo CD refresh and an audit entry per
		// endpoint per resync, forever.
		logf.FromContext(ctx).V(1).Info("status unchanged; not writing", "action", "none")
		return nil
	}
	if err := r.Status().Update(ctx, e); err != nil {
		return fmt.Errorf("updating endpoint status: %w", err)
	}
	return nil
}

// SetupWithManager registers the controller and the Secret watch.
func (r *NetBoxEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(),
		&netboxv1alpha1.NetBoxEndpoint{}, secretRefIndex,
		func(obj client.Object) []string {
			endpoint, ok := obj.(*netboxv1alpha1.NetBoxEndpoint)
			if !ok {
				return nil
			}
			// Both Secrets, so rotating a CA bundle is noticed as promptly as rotating a
			// token rather than waiting for the next resync.
			names := []string{endpoint.Spec.TokenSecretRef.Name}
			if tls := endpoint.Spec.TLSConfig; tls != nil && tls.CABundleSecretRef != nil {
				names = append(names, tls.CABundleSecretRef.Name)
			}
			return names
		}); err != nil {
		return fmt.Errorf("indexing endpoints by referenced secrets: %w", err)
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&netboxv1alpha1.NetBoxEndpoint{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.endpointsForSecret)).
		Named("netboxendpoint").
		Complete(r)
	if err != nil {
		return fmt.Errorf("building endpoint controller: %w", err)
	}
	return nil
}

// endpointsForSecret re-reconciles the endpoints that read a changed Secret, which is
// what makes token rotation take effect without a restart.
func (r *NetBoxEndpointReconciler) endpointsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	var endpoints netboxv1alpha1.NetBoxEndpointList
	err := r.List(ctx, &endpoints,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector(secretRefIndex, obj.GetName())})
	if err != nil {
		logf.FromContext(ctx).Error(err, "listing endpoints for a changed secret",
			"namespace", obj.GetNamespace(), "name", obj.GetName(), "action", "map")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(endpoints.Items))
	for i := range endpoints.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&endpoints.Items[i]),
		})
	}
	return requests
}
