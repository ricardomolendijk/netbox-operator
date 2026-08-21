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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// secretRefIndex indexes endpoints by the Secret they read, so a Secret event can find
// the endpoints to re-reconcile without listing every endpoint in the cluster.
const secretRefIndex = "spec.tokenSecretRef.name"

// NetBoxEndpointReconciler turns a NetBoxEndpoint into a live, authenticated,
// version-checked client that object controllers fetch from the cache.
type NetBoxEndpointReconciler struct {
	client.Client
	Cache *ClientCache
}

// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxendpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonInvalidConfig, err)
	}
	nbClient, err := netbox.New(cfg)
	if err != nil {
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonInvalidConfig, err)
	}

	status, err := nbClient.Status(ctx)
	if err != nil {
		return r.fail(ctx, endpoint, reasonFor(err), err)
	}

	version, supported, err := netbox.SupportedVersion(status.Version)
	if err != nil {
		return r.fail(ctx, endpoint, netboxv1alpha1.ReasonVersionUnparseable,
			fmt.Errorf("netbox reported version %q: %w", status.Version, err))
	}
	endpoint.Status.NetBoxVersion = status.Version
	endpoint.Status.Plugins = status.Plugins

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
		"mode", cfg.Mode, "plugins", status.Plugins)

	setCondition(endpoint, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "token accepted")
	setCondition(endpoint, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, fmt.Sprintf("netbox %s", status.Version))
	setCondition(endpoint, netboxv1alpha1.ConditionReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonReady, "client available")

	return ctrl.Result{RequeueAfter: resyncPeriod(endpoint)}, r.writeStatus(ctx, endpoint)
}

// readToken fetches the API token and the Secret's resourceVersion, which is what makes
// the client cache invalidate on rotation.
func (r *NetBoxEndpointReconciler) readToken(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint) (string, string, error) {
	secret := &corev1.Secret{}
	name := types.NamespacedName{Namespace: e.Namespace, Name: e.Spec.TokenSecretRef.Name}
	if err := r.Get(ctx, name, secret); err != nil {
		return "", "", fmt.Errorf("reading token secret %s: %w", name, err)
	}

	key := e.Spec.TokenSecretRef.Key
	if key == "" {
		key = "token"
	}
	token := string(secret.Data[key])
	if token == "" {
		return "", "", fmt.Errorf("%w: secret %s has no key %q", errTokenMissing, name, key)
	}
	return token, secret.ResourceVersion, nil
}

var (
	errTokenMissing = errors.New("token missing")
)

func (r *NetBoxEndpointReconciler) buildConfig(ctx context.Context, e *netboxv1alpha1.NetBoxEndpoint, token string) (netbox.Config, error) {
	cfg := netbox.Config{
		URL:     e.Spec.URL,
		Token:   token,
		Mode:    netbox.Mode(e.Spec.Mode),
		Timeout: e.Spec.Timeout.Duration,
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
		return netbox.Config{}, fmt.Errorf("reading ca bundle secret %s: %w", name, err)
	}
	key := ref.Key
	if key == "" {
		key = "ca.crt"
	}
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

	switch reason {
	case netboxv1alpha1.ReasonAuthError, netboxv1alpha1.ReasonTokenMissing, netboxv1alpha1.ReasonSecretMissing:
		setCondition(e, netboxv1alpha1.ConditionAuthenticated, metav1.ConditionFalse, reason, cause.Error())
	case netboxv1alpha1.ReasonVersionUnsupported, netboxv1alpha1.ReasonVersionUnparseable:
		setCondition(e, netboxv1alpha1.ConditionVersionSupported, metav1.ConditionFalse, reason, cause.Error())
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
	case apierrors.IsNotFound(errors.Unwrap(err)):
		return netboxv1alpha1.ReasonSecretMissing
	case errors.As(err, &authErr):
		return netboxv1alpha1.ReasonAuthError
	default:
		return netboxv1alpha1.ReasonProbeFailed
	}
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
			return []string{endpoint.Spec.TokenSecretRef.Name}
		}); err != nil {
		return fmt.Errorf("indexing endpoints by token secret: %w", err)
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&netboxv1alpha1.NetBoxEndpoint{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.endpointsForSecret),
			builder.WithPredicates()).
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
