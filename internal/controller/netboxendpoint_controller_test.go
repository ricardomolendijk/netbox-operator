package controller

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// makeSecret creates a credential Secret, labelled -- the operator's cache selects on
// that label, so an unlabelled Secret here would be invisible to the controller.
func makeSecret(t *testing.T, k8s client.Client, ns, name, token string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{CredentialLabel: CredentialLabelValue},
		},
		Data: map[string][]byte{"token": []byte(token)},
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty",
			Namespace: ns,
			Labels:    map[string]string{CredentialLabel: CredentialLabelValue},
		},
		Data: map[string][]byte{"other": []byte("x")},
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

// TestUnlabelledSecretIsInvisibleAndNamesTheLabel is the price of the label-selected
// cache, made explicit: the Secret is in the API server, the name on the endpoint is
// right, and the operator still cannot read it. Both halves are asserted -- that the
// cache does not hold it (the privilege and memory win) and that the condition says why
// (the usability cost, which is only acceptable while the message is this specific).
func TestUnlabelledSecretIsInvisibleAndNamesTheLabel(t *testing.T) {
	ctx, k8s, ns := context.Background(), k8sClient, newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)

	unlabelled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "unlabelled", Namespace: ns},
		Data:       map[string][]byte{"token": []byte("valid-token")},
	}
	if err := apiClient.Create(ctx, unlabelled); err != nil {
		t.Fatalf("creating unlabelled secret: %v", err)
	}

	key := client.ObjectKey{Namespace: ns, Name: "unlabelled"}
	if err := apiClient.Get(ctx, key, &corev1.Secret{}); err != nil {
		t.Fatalf("the unlabelled secret should exist in the API server: %v", err)
	}
	if err := k8s.Get(ctx, key, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Errorf("reading an unlabelled secret through the manager: err = %v, want NotFound", err)
	}
	// A labelled Secret in the same namespace is readable, so the assertion above is the
	// selector working rather than an empty cache.
	makeSecret(t, k8s, ns, "labelled", "valid-token")
	eventually(t, "labelled secret visible", func() bool {
		return k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: "labelled"}, &corev1.Secret{}) == nil
	})

	makeEndpoint(t, k8s, ns, "invisible", srv.URL, "unlabelled", netboxv1alpha1.EndpointModeApply)
	eventually(t, "SecretMissing", func() bool {
		e := fetch(t, k8s, ns, "invisible")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionReady)
		return c != nil && c.Reason == netboxv1alpha1.ReasonSecretMissing
	})

	c := conditionOf(mustFetch(t, k8s, ns, "invisible"), netboxv1alpha1.ConditionReady)
	if !strings.Contains(c.Message, CredentialLabel) {
		t.Errorf("Ready message = %q, want it to name the %s label", c.Message, CredentialLabel)
	}
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nb-ca",
			Namespace: ns,
			Labels:    map[string]string{CredentialLabel: CredentialLabelValue},
		},
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

// TestUnchangedReconcileWritesNoStatus is the endpoint controller's half of the property
// the engine already has: a resync that found the endpoint exactly as it left it performs
// no status write. Without it every endpoint bumps its resourceVersion every resyncPeriod
// forever, waking every watcher -- an Argo CD refresh and an audit entry for a change that
// is not one.
//
// Writes are counted at the client seam rather than inferred from resourceVersion under the
// package's shared manager, for the reason events_test.go states: the manager reconciles on
// its own schedule, so "nothing happened" cannot be asserted while something else may also
// be reconciling the object. Counting is the same technique internal/reconciler's tests use
// (fakeStatus), which is the point -- both components are held to one standard.
func TestUnchangedReconcileWritesNoStatus(t *testing.T) {
	// Six plugins rather than the shared stub's one, deliberately: NetBox returns them as
	// a JSON object and the client builds the list by ranging a map, so with one plugin an
	// unsorted list is indistinguishable from a sorted one. It takes both defects to
	// produce the churn, so the fixture has to be able to expose both.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"netbox-version":"4.6.8","plugins":{
			"netbox_topology_views":"4.1.0","netbox_bgp":"0.14.0","netbox_dns":"1.1.0",
			"netbox_secrets":"2.1.0","netbox_qrcode":"0.0.13","netbox_attachments":"7.0.0"}}`)
	}))
	t.Cleanup(srv.Close)

	reconciler, _ := fakeReconciler(t, endpointFor(srv.URL), credentialSecret())
	counter := &countingClient{Client: reconciler.Client}
	reconciler.Client = counter

	reconcileOnce(t, reconciler)
	if counter.statusWrites != 1 {
		t.Fatalf("status writes on the first reconcile = %d, want 1: an endpoint that has "+
			"never been reconciled must report something", counter.statusWrites)
	}

	const resyncs = 10
	for range resyncs {
		reconcileOnce(t, reconciler)
	}
	if counter.statusWrites != 1 {
		t.Errorf("status writes after %d unchanged resyncs = %d, want 1",
			resyncs, counter.statusWrites)
	}

	// The other half: suppression must not extend to a status that genuinely differs, or
	// the object stops reporting reality. Repointing the endpoint at a NetBox outside the
	// supported range is a real change to every condition.
	old := netboxStub(t, "3.7.8", http.StatusOK)
	current := &netboxv1alpha1.NetBoxEndpoint{}
	key := client.ObjectKey{Namespace: "default", Name: "homelab"}
	if err := reconciler.Get(context.Background(), key, current); err != nil {
		t.Fatalf("re-reading the endpoint: %v", err)
	}
	current.Spec.URL = old.URL
	// Set by hand because the fake client does not bump it: a real API server does on a
	// spec change, and a zero on both sides would make the observedGeneration assertion
	// below true without asserting anything.
	current.Generation = 7
	if err := reconciler.Update(context.Background(), current); err != nil {
		t.Fatalf("updating the endpoint: %v", err)
	}

	reconcileOnce(t, reconciler)
	if counter.statusWrites != 2 {
		t.Errorf("status writes after a genuine change = %d, want 2", counter.statusWrites)
	}

	// And skipping a write must not mean skipping observedGeneration: it is what
	// `kubectl wait` reads, and the pass that writes has to stamp the generation it saw.
	after := mustFetch(t, reconciler.Client, "default", "homelab")
	if after.Status.ObservedGeneration != after.Generation {
		t.Errorf("status.observedGeneration = %d, want %d",
			after.Status.ObservedGeneration, after.Generation)
	}
}

// countingClient counts status writes, so a test can assert that a reconcile issued none.
// The engine's tests count the same thing through its Status collaborator; the endpoint
// controller writes through client.Client directly, so the count goes here instead.
type countingClient struct {
	client.Client
	statusWrites int
}

func (c *countingClient) Status() client.SubResourceWriter {
	return &countingStatusWriter{SubResourceWriter: c.Client.Status(), owner: c}
}

type countingStatusWriter struct {
	client.SubResourceWriter
	owner *countingClient
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object,
	opts ...client.SubResourceUpdateOption,
) error {
	w.owner.statusWrites++

	return w.SubResourceWriter.Update(ctx, obj, opts...)
}
