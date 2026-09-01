package harness

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/controller"
)

// credentialSecret is the Secret name every fixture namespace holds its token in.
const credentialSecret = "netbox-e2e-token"

// SeedEndpoints creates each fixture namespace, its token Secret and its NetBoxEndpoint,
// and waits for every endpoint to report Ready.
//
// The endpoints are not fixtures. Their content is the address of a NetBox that exists only
// while the suite runs, and they are the one thing the graph is entitled to assume: the gate
// is about ordering between *objects*, not about an object racing its endpoint.
func (h *Harness) SeedEndpoints(ctx context.Context, resyncPeriod time.Duration) error {
	for _, namespace := range FixtureNamespaces {
		if err := h.Cluster.EnsureNamespace(ctx, namespace); err != nil {
			return err
		}
		if err := h.seedCredential(ctx, namespace); err != nil {
			return err
		}
		if err := h.seedEndpoint(ctx, namespace, resyncPeriod); err != nil {
			return err
		}
	}
	return h.WaitEndpointsReady(ctx)
}

func (h *Harness) seedCredential(ctx context.Context, namespace string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialSecret,
			Namespace: namespace,
			// The operator's Secret cache selects on this label, so a Secret without it is
			// invisible to the manager even when the endpoint names it correctly.
			Labels: map[string]string{controller.CredentialLabel: "true"},
		},
		StringData: map[string]string{"token": APIToken},
	}
	if err := h.Cluster.Client.Create(ctx, secret); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("creating the credential secret in %s: %w", namespace, err)
	}
	return nil
}

func (h *Harness) seedEndpoint(ctx context.Context, namespace string, resyncPeriod time.Duration) error {
	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: EndpointNames[namespace], Namespace: namespace},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			// The in-cluster address: the manager reaches NetBox over the kind network,
			// not through the port published on the test process's localhost.
			URL:            h.NetBox.InClusterURL,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: credentialSecret, Key: "token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
			DriftMode:      netboxv1alpha1.DriftCorrect,
			Timeout:        metav1.Duration{Duration: 30 * time.Second},
			ResyncPeriod:   metav1.Duration{Duration: resyncPeriod},
		},
	}
	if err := h.Cluster.Client.Create(ctx, endpoint); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("creating the endpoint in %s: %w", namespace, err)
	}
	return nil
}

// SetResyncPeriod rewrites every endpoint's spec.resyncPeriod.
//
// Two runs want different values and the endpoints outlive both: the quiescence assertion
// needs a period short enough that two of them fit in a test, and the grant-last run needs
// one long enough that a resync cannot be the thing that made the referrers converge.
func (h *Harness) SetResyncPeriod(ctx context.Context, period time.Duration) error {
	for _, namespace := range FixtureNamespaces {
		key := types.NamespacedName{Namespace: namespace, Name: EndpointNames[namespace]}
		endpoint := &netboxv1alpha1.NetBoxEndpoint{}
		if err := h.Cluster.Client.Get(ctx, key, endpoint); err != nil {
			return fmt.Errorf("getting endpoint %s: %w", key, err)
		}
		patch := client.MergeFrom(endpoint.DeepCopy())
		endpoint.Spec.ResyncPeriod = metav1.Duration{Duration: period}
		if err := h.Cluster.Client.Patch(ctx, endpoint, patch); err != nil {
			return fmt.Errorf("setting resyncPeriod on %s: %w", key, err)
		}
	}
	return nil
}

// WaitEndpointsReady waits for every seeded endpoint to have a usable client.
func (h *Harness) WaitEndpointsReady(ctx context.Context) error {
	return WaitFor(ctx, "every NetBoxEndpoint to be Ready", h.Cfg.ReadyTimeout,
		func(ctx context.Context) (bool, string, error) {
			for _, namespace := range FixtureNamespaces {
				ready, detail, err := h.endpointReady(ctx, namespace)
				if err != nil {
					return false, "", err
				}
				if !ready {
					return false, detail, nil
				}
			}
			return true, "", nil
		})
}

func (h *Harness) endpointReady(ctx context.Context, namespace string) (bool, string, error) {
	key := types.NamespacedName{Namespace: namespace, Name: EndpointNames[namespace]}
	endpoint := &netboxv1alpha1.NetBoxEndpoint{}
	if err := h.Cluster.Client.Get(ctx, key, endpoint); err != nil {
		return false, "", fmt.Errorf("getting endpoint %s: %w", key, err)
	}

	for i := range endpoint.Status.Conditions {
		condition := endpoint.Status.Conditions[i]
		if condition.Type != netboxv1alpha1.ConditionReady {
			continue
		}
		if condition.Status == metav1.ConditionTrue {
			return true, "", nil
		}
		return false, fmt.Sprintf("%s: Ready=%s Reason=%s %q",
			key, condition.Status, condition.Reason, condition.Message), nil
	}
	return false, fmt.Sprintf("%s: no Ready condition yet", key), nil
}
