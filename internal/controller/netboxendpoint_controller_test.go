package controller

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// netboxStub is a NetBox that answers GET /api/status/ however the test needs.
func netboxStub(t *testing.T, version string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"detail":"Invalid token."}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"netbox-version":%q,"django-version":"5.0.9",
			"plugins":{"netbox_topology_views":"4.1.0"},"rq-workers-running":1}`, version)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func makeSecret(t *testing.T, k8s client.Client, ns, name, token string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{"token": []byte(token)},
	}
	if err := k8s.Create(context.Background(), secret); err != nil {
		t.Fatalf("creating secret: %v", err)
	}
	return secret
}

func makeEndpoint(t *testing.T, k8s client.Client, ns, name, url, secretName string, mode netboxv1alpha1.EndpointMode) *netboxv1alpha1.NetBoxEndpoint {
	t.Helper()
	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            url,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: secretName, Key: "token"},
			Mode:           mode,
		},
	}
	if err := k8s.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	// The manager is package-wide, so an endpoint left behind keeps being requeued after
	// its stub server has closed -- harmless, but it fills the log with connection
	// refused from tests that already passed.
	t.Cleanup(func() {
		_ = k8s.Delete(context.Background(), endpoint)
	})
	return endpoint
}

// fetch reads an endpoint, returning nil when it is not in the manager's cache yet. It
// must not fail the test on a miss: the client reads through an informer cache that lags
// a Create by a moment, and eventually() polls through this function.
func fetch(t *testing.T, k8s client.Client, ns, name string) *netboxv1alpha1.NetBoxEndpoint {
	t.Helper()
	out := &netboxv1alpha1.NetBoxEndpoint{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, out); err != nil {
		return nil
	}
	return out
}

// mustFetch is fetch for assertions after eventually() has already proven the object is
// there.
func mustFetch(t *testing.T, k8s client.Client, ns, name string) *netboxv1alpha1.NetBoxEndpoint {
	t.Helper()
	out := fetch(t, k8s, ns, name)
	if out == nil {
		t.Fatalf("endpoint %s/%s not found", ns, name)
	}
	return out
}

func TestEndpointBecomesReady(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	makeEndpoint(t, k8s, ns, "homelab", srv.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "Ready=True", func() bool {
		e := fetch(t, k8s, ns, "homelab")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionReady)
		return c != nil && c.Status == metav1.ConditionTrue
	})

	e := mustFetch(t, k8s, ns, "homelab")
	if e.Status.NetBoxVersion != "4.6.8" {
		t.Errorf("netboxVersion = %q, want 4.6.8", e.Status.NetBoxVersion)
	}
	if len(e.Status.Plugins) != 1 || e.Status.Plugins[0] != "netbox_topology_views" {
		t.Errorf("plugins = %v, want [netbox_topology_views]", e.Status.Plugins)
	}
	if c := conditionOf(e, netboxv1alpha1.ConditionAuthenticated); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Authenticated = %v", c)
	}
	if _, ok := cache.Lookup(ns, "homelab"); !ok {
		t.Error("no client in the cache for a Ready endpoint")
	}
}

func TestBadTokenHandsOutNoClient(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusUnauthorized)
	makeSecret(t, k8s, ns, "nb-token", "wrong-token")
	makeEndpoint(t, k8s, ns, "badauth", srv.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "Authenticated=False", func() bool {
		e := fetch(t, k8s, ns, "badauth")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionAuthenticated)
		return c != nil && c.Status == metav1.ConditionFalse
	})

	e := mustFetch(t, k8s, ns, "badauth")
	if c := conditionOf(e, netboxv1alpha1.ConditionAuthenticated); c.Reason != netboxv1alpha1.ReasonAuthError {
		t.Errorf("reason = %q, want %q", c.Reason, netboxv1alpha1.ReasonAuthError)
	}
	if c := conditionOf(e, netboxv1alpha1.ConditionReady); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %v, want False", c)
	}
	// A failure before the version probe must not leave a stale answer standing.
	if c := conditionOf(e, netboxv1alpha1.ConditionVersionSupported); c == nil || c.Status != metav1.ConditionUnknown {
		t.Errorf("VersionSupported = %v, want Unknown -- it was never probed", c)
	}
	if _, ok := cache.Lookup(ns, "badauth"); ok {
		t.Error("a client was cached for an endpoint with a bad token")
	}
}

func TestUnsupportedVersionRefusesAClient(t *testing.T) {
	// The guard that matters: NetBox 3.7 wants `site` on a prefix, 4.2+ wants a
	// polymorphic scope and silently ignores `site`. Running against 3.7 would not fail,
	// it would quietly do nothing.
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "3.7.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	makeEndpoint(t, k8s, ns, "ancient", srv.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "VersionSupported=False", func() bool {
		e := fetch(t, k8s, ns, "ancient")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionVersionSupported)
		return c != nil && c.Status == metav1.ConditionFalse
	})

	e := mustFetch(t, k8s, ns, "ancient")
	if c := conditionOf(e, netboxv1alpha1.ConditionVersionSupported); c.Reason != netboxv1alpha1.ReasonVersionUnsupported {
		t.Errorf("reason = %q, want %q", c.Reason, netboxv1alpha1.ReasonVersionUnsupported)
	}
	// The version is still recorded: knowing what it found is how anyone diagnoses this.
	if e.Status.NetBoxVersion != "3.7.8" {
		t.Errorf("netboxVersion = %q, want it recorded even when unsupported", e.Status.NetBoxVersion)
	}
	if _, ok := cache.Lookup(ns, "ancient"); ok {
		t.Error("a client was cached for an unsupported NetBox version")
	}
}

func TestMissingSecretIsReportedNotCrashed(t *testing.T) {
	k8s, ns := k8sClient, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeEndpoint(t, k8s, ns, "nosecret", srv.URL, "absent", netboxv1alpha1.EndpointModeApply)

	eventually(t, "Ready=False", func() bool {
		e := fetch(t, k8s, ns, "nosecret")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionReady)
		return c != nil && c.Status == metav1.ConditionFalse
	})
}

func TestEmptyTokenKeyIsReported(t *testing.T) {
	k8s, ns := k8sClient, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: ns},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	if err := k8s.Create(context.Background(), secret); err != nil {
		t.Fatalf("creating secret: %v", err)
	}
	makeEndpoint(t, k8s, ns, "emptytoken", srv.URL, "empty", netboxv1alpha1.EndpointModeApply)

	eventually(t, "TokenMissing", func() bool {
		e := fetch(t, k8s, ns, "emptytoken")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionReady)
		return c != nil && c.Reason == netboxv1alpha1.ReasonTokenMissing
	})
}

func TestSecretRotationRebuildsTheClientWithoutRestart(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"netbox-version":"4.6.8","plugins":{}}`))
	}))
	t.Cleanup(srv.Close)

	secret := makeSecret(t, k8s, ns, "rotating", "first-token")
	makeEndpoint(t, k8s, ns, "rotate", srv.URL, "rotating", netboxv1alpha1.EndpointModeApply)

	eventually(t, "first token used", func() bool {
		v, _ := seen.Load().(string)
		return v == "Token first-token"
	})
	firstClient, _ := cache.Lookup(ns, "rotate")

	secret.Data["token"] = []byte("second-token")
	if err := k8s.Update(context.Background(), secret); err != nil {
		t.Fatalf("rotating secret: %v", err)
	}

	eventually(t, "second token used", func() bool {
		v, _ := seen.Load().(string)
		return v == "Token second-token"
	})
	// The cache is keyed on the Secret's resourceVersion, so a rotation cannot leave the
	// old client reachable.
	secondClient, ok := cache.Lookup(ns, "rotate")
	if !ok {
		t.Fatal("no client after rotation")
	}
	if firstClient == secondClient {
		t.Error("the same client instance survived a token rotation")
	}
}

// TestCABundleKeyDefaultsToCACrt guards a defaulting bug: SecretKeyRef is shared by
// tokenSecretRef and caBundleSecretRef, so a +kubebuilder:default on its Key field
// applied to both and defaulted the CA bundle's key to "token" -- making the controller's
// own ca.crt fallback unreachable and the endpoint fail with InvalidConfig.
func TestCABundleKeyDefaultsToCACrt(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")

	// A real certificate, taken from a throwaway TLS server rather than hand-written:
	// the client parses the bundle, so an invalid PEM fails for the wrong reason.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tlsSrv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})

	ca := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nb-ca", Namespace: ns},
		// Only ca.crt. If the key defaults to "token" this Secret looks empty.
		Data: map[string][]byte{"ca.crt": caPEM},
	}
	if err := k8s.Create(context.Background(), ca); err != nil {
		t.Fatalf("creating ca secret: %v", err)
	}

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "withca", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            srv.URL,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
			TLSConfig: &netboxv1alpha1.TLSConfig{
				// Key deliberately omitted.
				CABundleSecretRef: &netboxv1alpha1.SecretKeyRef{Name: "nb-ca"},
			},
		},
	}
	if err := k8s.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	t.Cleanup(func() { _ = k8s.Delete(context.Background(), endpoint) })

	eventually(t, "client built with the CA bundle", func() bool {
		_, ok := cache.Lookup(ns, "withca")
		return ok
	})
}

// TestVersionFailureStillReportsAuthenticated pins that every condition describes the
// same reconcile. Reaching the version gate proves the token was accepted, so leaving
// Authenticated unwritten made it absent entirely on a first reconcile that lands
// straight on a version failure.
func TestVersionFailureStillReportsAuthenticated(t *testing.T) {
	k8s, _, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "3.7.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	makeEndpoint(t, k8s, ns, "oldbox", srv.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "VersionSupported=False", func() bool {
		e := fetch(t, k8s, ns, "oldbox")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionVersionSupported)
		return c != nil && c.Status == metav1.ConditionFalse
	})

	e := mustFetch(t, k8s, ns, "oldbox")
	c := conditionOf(e, netboxv1alpha1.ConditionAuthenticated)
	if c == nil {
		t.Fatal("Authenticated is absent; reaching the version gate proves the token was accepted")
	}
	if c.Status != metav1.ConditionTrue {
		t.Errorf("Authenticated = %v, want True", c.Status)
	}
}

// TestMissingCABundleIsNotAnAuthFailure pins that a missing CA bundle does not claim the
// token failed -- the token read fine, and saying otherwise sends the reader to the wrong
// Secret.
func TestMissingCABundleIsNotAnAuthFailure(t *testing.T) {
	k8s, _, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "noca", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            srv.URL,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
			TLSConfig: &netboxv1alpha1.TLSConfig{
				CABundleSecretRef: &netboxv1alpha1.SecretKeyRef{Name: "absent-ca"},
			},
		},
	}
	if err := k8s.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	t.Cleanup(func() { _ = k8s.Delete(context.Background(), endpoint) })

	eventually(t, "CABundleMissing", func() bool {
		e := fetch(t, k8s, ns, "noca")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionReady)
		return c != nil && c.Reason == netboxv1alpha1.ReasonCABundleMissing
	})

	e := mustFetch(t, k8s, ns, "noca")
	if c := conditionOf(e, netboxv1alpha1.ConditionAuthenticated); c == nil || c.Status != metav1.ConditionUnknown {
		t.Errorf("Authenticated = %v, want Unknown -- the token was never rejected", c)
	}
}

func TestDryRunModeReachesTheClient(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	makeEndpoint(t, k8s, ns, "planonly", srv.URL, "nb-token", netboxv1alpha1.EndpointModeDryRun)

	eventually(t, "client cached", func() bool {
		_, ok := cache.Lookup(ns, "planonly")
		return ok
	})
	nbClient, _ := cache.Lookup(ns, "planonly")
	if !nbClient.DryRun() {
		t.Error("mode DryRun did not reach the client")
	}
}

func TestDeletingAnEndpointForgetsItsClient(t *testing.T) {
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	endpoint := makeEndpoint(t, k8s, ns, "transient", srv.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "client cached", func() bool {
		_, ok := cache.Lookup(ns, "transient")
		return ok
	})
	if err := k8s.Delete(context.Background(), endpoint); err != nil {
		t.Fatalf("deleting endpoint: %v", err)
	}
	eventually(t, "client forgotten", func() bool {
		_, ok := cache.Lookup(ns, "transient")
		return !ok
	})
}

func TestTwoEndpointsInOneNamespaceAreIndependent(t *testing.T) {
	// Namespaced endpoints mean lab/staging/prod side by side with no extra machinery.
	k8s, cache, ns := k8sClient, clients, newNamespace(t)
	good := netboxStub(t, "4.6.8", http.StatusOK)
	old := netboxStub(t, "3.7.8", http.StatusOK)
	makeSecret(t, k8s, ns, "nb-token", "valid-token")
	makeEndpoint(t, k8s, ns, "prod", good.URL, "nb-token", netboxv1alpha1.EndpointModeApply)
	makeEndpoint(t, k8s, ns, "lab", old.URL, "nb-token", netboxv1alpha1.EndpointModeApply)

	eventually(t, "prod ready and lab refused", func() bool {
		_, prodOK := cache.Lookup(ns, "prod")
		_, labOK := cache.Lookup(ns, "lab")
		return prodOK && !labOK
	})
}
