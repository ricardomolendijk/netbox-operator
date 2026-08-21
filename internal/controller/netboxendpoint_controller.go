package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// secretRefIndex indexes endpoints by the Secret they read, so a Secret event can find
// the endpoints to re-reconcile without listing every endpoint in the cluster.
const secretRefIndex = "spec.secretRefs"

// NetBoxEndpointReconciler turns a NetBoxEndpoint into a live, authenticated,
// version-checked client that object controllers fetch from the cache.
type NetBoxEndpointReconciler struct {
	client.Client
	Cache *ClientCache
}

// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxendpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// All three Secret verbs are load-bearing under the label-selected cache: a selected
// informer still issues a LIST before it WATCHes, and `watch` is what makes a rotated
// token take effect without a restart. RBAC cannot filter by label, so this grant stays
// cluster-wide until per-namespace Roles land; see docs/operations/rbac.md and NBO-072.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile builds a client for one endpoint and records what it found.
//
// It never returns an error for anything about NetBox's availability: an unreachable or
// misconfigured NetBox is a condition on this object, not a controller failure, or the
// manager's error rate becomes a function of someone else's uptime.
func (r *NetBoxEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	endpoint := &netboxv1alpha1.NetBoxEndpoint{}
	if err := r.Get(ctx, req.NamespacedName, endpoint); err != nil {
		if apierrors.IsNotFound(err) {
			r.Cache.Forget(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching endpoint: %w", err)
	}

	token, secretVersion, err := r.readToken(ctx, endpoint)
	if err != nil {
		return r.fail(ctx, endpoint, reasonFor(err), err)
	}

	cfg, err := r.buildConfig(ctx, endpoint, token)
	if err != nil {
		return r.fail(ctx, endpoint, reasonForConfig(err), err)
	}
	nbClient, err := netbox.New(cfg)
	if err != nil {
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonInvalidConfig, err)
	}

	status, err := nbClient.Status(ctx)
	if err != nil {
		return r.fail(ctx, endpoint, reasonFor(err), err)
	}

	// Recorded before the gate: whatever NetBox said is the most useful thing in status
	// when the answer is "that is not a version".
	endpoint.Status.NetBoxVersion = status.Version
	endpoint.Status.Plugins = status.Plugins

	version, supported, err := netbox.SupportedVersion(status.Version)
	if err != nil {
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonVersionUnparseable,
			fmt.Errorf("netbox reported version %q: %w", status.Version, err))
	}

	if !supported {
		// The guard that matters. NetBox 4.2 replaced `site` with a polymorphic scope on
		// four models, and writing `site` to 4.2+ silently no-ops -- so an operator run
		// against an out-of-range server does not fail, it quietly does nothing. Refuse
		// instead.
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonVersionUnsupported,
			fmt.Errorf("netbox %s is outside the supported range >=%s, <%s",
				version, netbox.MinVersion, netbox.MaxVersion))
	}

	r.Cache.put(clientKey{
		namespace:     endpoint.Namespace,
		name:          endpoint.Name,
		generation:    endpoint.Generation,
		secretVersion: secretVersion,
	}, nbClient)

	logf.FromContext(ctx).Info("endpoint ready",
		"url", endpoint.Spec.URL, "netboxVersion", status.Version,
		"mode", cfg.Mode, "plugins", status.Plugins,
		"insecureSkipVerify", cfg.InsecureSkipVerify)

	setCondition(endpoint, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "token accepted")
	setCondition(endpoint, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, fmt.Sprintf("netbox %s", status.Version))
	setCondition(endpoint, netboxv1alpha1.ConditionReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "client available")

	if err := r.writeStatus(ctx, endpoint); err != nil {
		// controller-runtime discards RequeueAfter when the error is non-nil, so the two
		// must never be returned together or a status conflict silently loses the resync.
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncPeriod(endpoint)}, nil
}

// readToken fetches the API token and the Secret's resourceVersion, which is what makes
// the client cache invalidate on rotation.
func (r *NetBoxEndpointReconciler) readToken(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint) (string, string, error) {
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
func unreadableSecret(what string, name types.NamespacedName, err error) error {
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
		Mode:  netbox.Mode(e.Spec.Mode),
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

// fail records why the endpoint is not usable and drops any cached client, so object
// controllers stop writing through a connection that has since been rejected.
func (r *NetBoxEndpointReconciler) fail(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint, reason string, cause error) (ctrl.Result, error) {
	r.Cache.Forget(e.Namespace, e.Name)
	logf.FromContext(ctx).Error(cause, "endpoint not ready", "reason", reason)

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

	if err := r.writeStatus(ctx, e); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: failureBackoff(reason)}, nil
}

// failureBackoff spaces out retries by how likely a retry is to help. A version mismatch
// will not fix itself, so re-probing every 30 seconds is pure noise.
func failureBackoff(reason string) time.Duration {
	switch reason {
	case netboxv1alpha1.ReasonVersionUnsupported, netboxv1alpha1.ReasonVersionUnparseable:
		return 10 * time.Minute
	case netboxv1alpha1.ReasonAuthError, netboxv1alpha1.ReasonInvalidConfig:
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
	case apierrors.IsNotFound(err):
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

func resyncPeriod(e *netboxv1alpha1.NetBoxEndpoint) time.Duration {
	if e.Spec.ResyncPeriod.Duration > 0 {
		return e.Spec.ResyncPeriod.Duration
	}
	return 10 * time.Minute
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

func (r *NetBoxEndpointReconciler) writeStatus(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint) error {
	e.Status.ObservedGeneration = e.Generation
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
			"secret", obj.GetName())
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
