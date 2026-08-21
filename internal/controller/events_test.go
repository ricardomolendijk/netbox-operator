package controller

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Events are asserted against a fake client rather than through the package's envtest
// manager, and deliberately so: the manager reconciles on its own schedule, and "the
// second reconcile emitted nothing" cannot be asserted while something else is also
// reconciling the object. Here every pass is one explicit call.

// TestEndpointEvents drives one endpoint through each outcome and asserts what a user
// would see in `kubectl describe`.
func TestEndpointEvents(t *testing.T) {
	tests := []struct {
		name string
		// version and status are what the stubbed NetBox answers GET /api/status/ with;
		// secret is whether the credential Secret exists at all.
		version    string
		status     int
		secret     bool
		wantEvent  string
		wantReason string
	}{
		{
			name: "an endpoint that becomes usable says so", version: "4.6.8",
			status: http.StatusOK, secret: true,
			wantEvent: "Normal", wantReason: netboxv1alpha1.ReasonReady,
		},
		{
			name: "a rejected token is a warning naming the token", version: "4.6.8",
			status: http.StatusUnauthorized, secret: true,
			wantEvent: "Warning", wantReason: netboxv1alpha1.ReasonAuthError,
		},
		{
			name: "a netbox outside the supported range is refused, loudly", version: "4.1.0",
			status: http.StatusOK, secret: true,
			wantEvent: "Warning", wantReason: netboxv1alpha1.ReasonVersionUnsupported,
		},
		{
			name: "a version netbox cannot even spell", version: "not-a-version",
			status: http.StatusOK, secret: true,
			wantEvent: "Warning", wantReason: netboxv1alpha1.ReasonVersionUnparseable,
		},
		{
			name: "a missing credential secret", version: "4.6.8",
			status: http.StatusOK, secret: false,
			wantEvent: "Warning", wantReason: netboxv1alpha1.ReasonSecretMissing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := netboxStub(t, tc.version, tc.status)
			objects := []client.Object{endpointFor(srv.URL)}
			if tc.secret {
				objects = append(objects, credentialSecret())
			}

			reconciler, events := fakeReconciler(t, objects...)
			counted := metrics.EndpointReconcileTotal.WithLabelValues(tc.wantReason)
			before := counterValue(t, counted)

			reconcileOnce(t, reconciler)

			if got := drain(events); !slices.Equal(got, []string{tc.wantEvent + " " + tc.wantReason}) {
				t.Errorf("events = %v, want one %s/%s", got, tc.wantEvent, tc.wantReason)
			}

			if got := counterValue(t, counted) - before; got != 1 {
				t.Errorf("endpoint_reconcile_total{result=%q} moved by %v, want 1", tc.wantReason, got)
			}

			// The assertion this whole file exists for. The endpoint is reconciled again
			// with nothing changed, exactly as the resync does every ten minutes, and it
			// must be silent: an Event per resync fills a namespace with the same line
			// and buries whatever actually happened.
			reconcileOnce(t, reconciler)

			if got := drain(events); len(got) != 0 {
				t.Errorf("a resync that changed nothing emitted %v, want no events", got)
			}
		})
	}
}

// TestEndpointEventOnRecovery is the other half: a transition *out* of a failure has to
// be reported too, or an endpoint that fixed itself looks broken forever.
func TestEndpointEventOnRecovery(t *testing.T) {
	broken := netboxStub(t, "4.6.8", http.StatusUnauthorized)
	endpoint := endpointFor(broken.URL)

	reconciler, events := fakeReconciler(t, endpoint, credentialSecret())
	reconcileOnce(t, reconciler)

	if got := drain(events); !slices.Equal(got, []string{"Warning " + netboxv1alpha1.ReasonAuthError}) {
		t.Fatalf("events = %v, want one Warning/AuthError", got)
	}

	// Point the same endpoint at a NetBox that accepts the token, as rotating the token
	// would.
	fixed := netboxStub(t, "4.6.8", http.StatusOK)
	current := &netboxv1alpha1.NetBoxEndpoint{}
	key := client.ObjectKeyFromObject(endpoint)
	if err := reconciler.Get(context.Background(), key, current); err != nil {
		t.Fatalf("re-reading the endpoint: %v", err)
	}
	current.Spec.URL = fixed.URL
	if err := reconciler.Update(context.Background(), current); err != nil {
		t.Fatalf("updating the endpoint: %v", err)
	}

	reconcileOnce(t, reconciler)

	if got := drain(events); !slices.Equal(got, []string{"Normal " + netboxv1alpha1.ReasonReady}) {
		t.Errorf("events = %v, want one Normal/Ready", got)
	}
}

// TestEndpointWithNoRecorder guards the nil case: the Recorder is optional, and a
// reconcile must not panic without one.
func TestEndpointWithNoRecorder(t *testing.T) {
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	reconciler, _ := fakeReconciler(t, endpointFor(srv.URL), credentialSecret())
	reconciler.Recorder = nil

	reconcileOnce(t, reconciler)
}

// TestClientCacheSizeGauge covers the one gauge the operator exports. It is the earliest
// operator-side signal that writes have stopped: an object reconcile whose endpoint is
// not in the cache can only wait.
func TestClientCacheSizeGauge(t *testing.T) {
	cache := NewClientCache()

	client, err := netbox.New(netbox.Config{URL: "https://netbox.invalid", Token: "t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cache.put(clientKey{namespace: "default", name: "homelab"}, client)
	if got := testutil.ToFloat64(metrics.ClientCacheSize); got != 1 {
		t.Errorf("client_cache_size = %v after one put, want 1", got)
	}

	// A second client for the same endpoint replaces the first rather than adding to it,
	// which is the property that makes this gauge a count of endpoints.
	cache.put(clientKey{namespace: "default", name: "homelab", secretVersion: "2"}, client)
	if got := testutil.ToFloat64(metrics.ClientCacheSize); got != 1 {
		t.Errorf("client_cache_size = %v after a rotation, want 1", got)
	}

	cache.Forget("default", "homelab")
	if got := testutil.ToFloat64(metrics.ClientCacheSize); got != 0 {
		t.Errorf("client_cache_size = %v after Forget, want 0", got)
	}
}

// fakeReconciler builds a reconciler over an in-memory API server. The status subresource
// is declared, or every condition write would be silently dropped and every case would
// look like a first reconcile.
func fakeReconciler(t *testing.T, objects ...client.Object) (*NetBoxEndpointReconciler, *record.FakeRecorder) {
	t.Helper()

	recorder := record.NewFakeRecorder(16)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&netboxv1alpha1.NetBoxEndpoint{}).
		Build()

	return &NetBoxEndpointReconciler{
		Client:   fakeClient,
		Cache:    NewClientCache(),
		Recorder: recorder,
	}, recorder
}

func reconcileOnce(t *testing.T, r *NetBoxEndpointReconciler) {
	t.Helper()

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: "default", Name: "homelab"},
	})
	if err != nil {
		t.Fatalf("Reconcile() = %v; netbox availability is a condition, not a controller failure", err)
	}
}

func endpointFor(url string) *netboxv1alpha1.NetBoxEndpoint {
	return &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: "default"},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            url,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token", Key: "token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
		},
	}
}

func credentialSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nb-token",
			Namespace: "default",
			Labels:    map[string]string{CredentialLabel: CredentialLabelValue},
		},
		Data: map[string][]byte{"token": []byte("valid-token")},
	}
}

// drain collects the Events the recorder holds as "Type Reason", without blocking on an
// empty channel. The message is deliberately dropped: its wording is not the contract,
// and asserting on it would make every reworded error a failing test.
func drain(recorder *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case event := <-recorder.Events:
			// FakeRecorder formats as "Type Reason Message".
			fields := strings.SplitN(event, " ", 3)
			if len(fields) < 2 {
				out = append(out, event)
				continue
			}
			out = append(out, fields[0]+" "+fields[1])
		default:
			return out
		}
	}
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()

	return testutil.ToFloat64(counter)
}
