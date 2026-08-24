package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

var (
	testEnv *envtest.Environment
	scheme  = runtime.NewScheme()
	// k8sClient reads through the manager's caches, so it sees Secrets exactly as the
	// controller does. apiClient bypasses them, which is how a test can show that an
	// unlabelled Secret exists in the API server and is still invisible to the operator.
	k8sClient client.Client
	apiClient client.Client
	clients   *ClientCache
)

func TestMain(m *testing.M) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(netboxv1alpha1.AddToScheme(scheme))
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		// The shipped CRDs, plus the generic-FK union fixture. The fixture is a test-only
		// group carrying the two CEL shapes NBO-019 specifies, because no shipped Kind
		// embeds a polymorphic union until NBO-025 and a CEL rule no CRD carries is never
		// compiled by anything.
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		// envtest assets are downloaded by `make test`. Running `go test` directly
		// without them is a setup problem, not a test failure.
		println("envtest could not start; run via `make test`:", err.Error())
		os.Exit(1)
	}

	// One manager for the whole package, not one per test: controller-runtime enforces
	// globally unique controller names so that two controllers cannot report the same
	// metric, and a manager per test collides on the second one. Tests isolate through a
	// namespace each instead, which is also faster.
	stop, err := startManager(cfg)
	if err != nil {
		println("starting the manager:", err.Error())
		_ = testEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()
	stop()
	_ = testEnv.Stop()
	os.Exit(code)
}

// startManager brings up the single package-wide manager and returns a stop function.
func startManager(cfg *rest.Config) (func(), error) {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		// The same label selector the shipped manager uses, so the tests exercise it
		// rather than a cache more permissive than production's. Cluster-wide in
		// namespaces, unlike the shipped deployment: every test makes a namespace of its
		// own, which a deploy-time namespace list cannot know about. The namespace half of
		// the scoping is exercised against real RBAC in secretcache_test.go instead.
		Cache: cache.Options{ByObject: SecretScope{}.CacheOptions()},
	})
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}

	apiClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("direct client: %w", err)
	}

	clients = NewClientCache()
	reconciler := &NetBoxEndpointReconciler{
		Client: mgr.GetClient(),
		Cache:  clients,
		// The real recorder, so the envtest suite exercises the same path production
		// does; Event *content* is asserted against a FakeRecorder in events_test.go,
		// where one test's Events cannot be another's.
		Recorder: mgr.GetEventRecorderFor("netboxendpoint-controller"),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("setup: %w", err)
	}

	// The same call the shipped manager makes, so the object tests exercise the real
	// registration path rather than a controller wired up by hand for the test.
	if err := SetupObjectControllers(mgr, clients); err != nil {
		return nil, fmt.Errorf("object controllers: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			println("manager stopped:", err.Error())
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		cancel()
		return nil, fmt.Errorf("cache did not sync")
	}
	k8sClient = mgr.GetClient()
	return cancel, nil
}

// newNamespace gives a test its own namespace, which is how tests stay independent now
// that they share one manager.
func newNamespace(t *testing.T) string {
	t.Helper()
	return newNamespaceSuffixed(t, "")
}

// nsSeq numbers every namespace this process hands out. The test name alone is not unique
// across iterations, so `go test -count=2` used to fail every test on "namespaces ... already
// exists" -- and a suite that cannot be run twice cannot be used to hunt a flake, which is
// the whole instrument NBO-091 needed.
var nsSeq atomic.Uint64

// newNamespaceSuffixed is newNamespace for a test that needs more than one. A NetBox slug
// is unique globally while a CRD is namespaced, so "the same object claimed from two
// namespaces" is a case that cannot be written with one namespace per test.
func newNamespaceSuffixed(t *testing.T, suffix string) string {
	t.Helper()
	seq := fmt.Sprintf("-%d", nsSeq.Add(1))
	name := "nbtest-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	// A namespace name is a DNS label, so the whole thing has to fit in 63 characters; the
	// suffix and the sequence are the parts that carry meaning, so the test name is what
	// gives way.
	if budget := 63 - len(suffix) - len(seq); len(name) > budget {
		name = name[:budget]
	}
	name += suffix + seq
	if err := k8sClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}); err != nil {
		t.Fatalf("creating namespace %s: %v", name, err)
	}
	return name
}

// eventually polls until check passes or the deadline expires. Simpler than pulling in
// gomega for the handful of assertions here.
//
// 20 seconds is not a tight budget and raising it fixes nothing. Every wait in this package
// was measured (NBO-091): the slowest is the drift-correction chain at ~1s, everything else
// lands on the first or second poll, and neither `-cpu 1` nor a load average of 78 on 11
// cores moves any of them or produces a single timeout. A test here that fails is failing
// for a reason, and a longer deadline would only make it slower to find out.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func conditionOf(e *netboxv1alpha1.NetBoxEndpoint, condType string) *metav1.Condition {
	for i := range e.Status.Conditions {
		if e.Status.Conditions[i].Type == condType {
			return &e.Status.Conditions[i]
		}
	}
	return nil
}
