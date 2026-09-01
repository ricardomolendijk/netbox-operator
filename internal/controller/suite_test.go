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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
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

	if err := SetupClaimControllers(mgr, clients); err != nil {
		return nil, fmt.Errorf("claim controllers: %w", err)
	}

	// The sweep takes the manager's plain client: it reads NetBox and the CRs of every kind it
	// is asked about, and writes to neither (NBO-046).
	sweeps := &NetBoxSweepReconciler{
		Client:   mgr.GetClient(),
		Clients:  clients,
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("netboxsweep-controller"),
	}
	if err := sweeps.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("sweep controller: %w", err)
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
//
// A namespace per test is not what makes this suite slow, which is what #215 suspected. Timed
// at the end of a full run, with all 138 of them and everything the suite created still in the
// API server, and the control plane confined to 0.6 of one core, a namespace Create costs
// 0.7-2.0ms. Pooling them would buy nothing and cost every test its isolation.
//
// If a Create here ever does take tens of seconds, the only client-side deadline of that order
// on this path is client-go's HTTP/2 keepalive -- ReadIdleTimeout 30s plus PingTimeout 15s, in
// k8s.io/apimachinery/pkg/util/net -- which closes the connection and fails whatever was in
// flight on it. That is a transport failure rather than a queue, so no test deadline reaches it
// and no amount of namespace pooling would either.
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
//
// Re-measured for #215 with the whole control plane -- test binary, kube-apiserver and etcd --
// confined to a cgroup holding 0.6 of one core: 259 waits over a full run, none timed out, the
// slowest 906ms. The margin is twenty-fold, so a timeout here is never the deadline's fault.
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

// editSpec re-reads obj, applies mutate and updates it, and returns the object as the API
// server stored it. It is how a test edits a CR the operator is already reconciling.
//
// Two things make the obvious `fetchX(); x.Spec.Y = z; k8sClient.Update(x)` lose under load.
// An Update carries the resourceVersion it was read at as a precondition; and every fetch
// helper here reads through the manager's cache, which lags the API server by however long the
// manager is busy -- while the operator is concurrently patching finalizers, owner references
// and status onto that same object on its one-second resync. The two together turn a spec edit
// into a 409, and a test that treats 409 as fatal then fails for a reason with nothing to do
// with what it asserts. Reproduced in TestClusterScopeMovesAsOnePair with the control plane
// held to a fifth of a core.
//
// So the read and the write both go through apiClient: an optimistic-concurrency write must not
// be based on a version that is stale before it is even sent, and retrying a *cached* read
// cannot help, because the retry sees the same stale version. The retry that remains covers
// only the operator writing in the moment between this Get and this Update, which is the
// protocol working as designed rather than a slow operation being waited out -- and is why
// three claim tests already spelled it out by hand before this helper collected it.
func editSpec[T client.Object](t *testing.T, obj T, mutate func(T)) T {
	t.Helper()

	ctx := context.Background()
	key := client.ObjectKeyFromObject(obj)
	stored := obj

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, ok := obj.DeepCopyObject().(T)
		if !ok {
			return fmt.Errorf("%T does not deep-copy into its own type", obj)
		}

		if err := apiClient.Get(ctx, key, fresh); err != nil {
			return err
		}

		mutate(fresh)

		if err := apiClient.Update(ctx, fresh); err != nil {
			return err
		}

		stored = fresh

		return nil
	})
	if err != nil {
		t.Fatalf("editing %s: %v", key, err)
	}

	return stored
}

// removeObject deletes obj and does not return until the API server has dropped it. It is
// the cleanup every helper that applies a NetBox-backed CR registers.
//
// The waiting is the whole point. Releasing the engine's finalizer costs a NetBox DELETE, so
// it needs the namespace's NetBoxEndpoint and the httptest stub behind it -- both of which
// t.Cleanup is about to take away, in that order, because it runs LIFO and the object was
// registered last. A Delete that returns while the finalizer is still on gives that ordering
// away: the endpoint goes next, and the object is left Terminating holding a finalizer it can
// never release, because the client it would need is gone for good. It is then requeued every
// endpointRetry for the remainder of the run, in a package where all ~190 tests share one
// manager and one API server.
//
// Measured on the full suite before this existed: 106 deletions refused with
// WaitingForEndpoint and 6 to 9 objects left permanently Terminating per run, a different set
// each time -- which is the shape of a cross-test flake rather than a tidiness problem.
//
// It gives up rather than failing the test. A cleanup that fails reports the test that just
// passed instead of the one that leaked, and an object whose deletion is deliberately blocked
// (deletionPolicy: Retain) is a case some tests mean to create.
func removeObject(t *testing.T, obj client.Object) {
	t.Helper()

	ctx := context.Background()
	key := client.ObjectKeyFromObject(obj)

	if err := k8sClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return
	}

	for range 100 {
		if err := k8sClient.Get(ctx, key, obj); apierrors.IsNotFound(err) {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}
}

// endpointIsReady is the endpoint-side tagIsReady, and the gate to use before reading
// anything one of its reconciles produced.
//
// Reconcile calls Cache.put before it writes status, so a client cache read taken after
// Ready=True cannot be racing a pass still in flight. A gate on the cache itself can:
// nothing stops a later failing pass calling Cache.Forget between the gate and the read,
// and a gate on a request arriving at the stub releases earlier still (NBO-091, #159).
func endpointIsReady(ns, name string) bool {
	e := &netboxv1alpha1.NetBoxEndpoint{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, e); err != nil {
		return false
	}

	c := conditionOf(e, netboxv1alpha1.ConditionReady)

	return c != nil && c.Status == metav1.ConditionTrue
}

func conditionOf(e *netboxv1alpha1.NetBoxEndpoint, condType string) *metav1.Condition {
	for i := range e.Status.Conditions {
		if e.Status.Conditions[i].Type == condType {
			return &e.Status.Conditions[i]
		}
	}
	return nil
}
