package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

var (
	testEnv   *envtest.Environment
	scheme    = runtime.NewScheme()
	k8sClient client.Client
	clients   *ClientCache
)

func TestMain(m *testing.M) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(netboxv1alpha1.AddToScheme(scheme))
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
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
	})
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}

	clients = NewClientCache()
	reconciler := &NetBoxEndpointReconciler{Client: mgr.GetClient(), Cache: clients}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("setup: %w", err)
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
	name := "nbtest-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if len(name) > 60 {
		name = name[:60]
	}
	if err := k8sClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}); err != nil {
		t.Fatalf("creating namespace %s: %v", name, err)
	}
	return name
}

// eventually polls until check passes or the deadline expires. Simpler than pulling in
// gomega for the handful of assertions here.
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
