package admission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	_ "github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The suite runs the real webhook behind a real API server, which is the only way the
// assertions worth making can be made: a rejection message is produced by the API server
// from the webhook's response, and a warning arrives as an HTTP header on the response to
// the write. Neither is observable from a handler called directly.
var (
	testEnv *envtest.Environment
	scheme  = runtime.NewScheme()

	// k8sClient reads and writes through the API server, so every create it makes passes
	// through the installed webhook configuration.
	k8sClient client.Client

	// cached is the very reader the handler holds. A test gates on this rather than on the
	// API server, so "the sibling is visible to the webhook" is asserted rather than slept
	// for -- the two are different facts and only one of them decides the verdict.
	cached client.Reader

	// warned is the warning collector k8sClient's responses feed.
	warned = &warningLog{}
)

func TestMain(m *testing.M) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(netboxv1alpha1.AddToScheme(scheme))
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		// The shipped configuration, not a copy: `resources: ['*']` and
		// `failurePolicy: Ignore` are asserted against this same file by
		// TestShippedWebhookConfiguration, so the suite cannot pass against a webhook
		// scoped differently from the one that is deployed.
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "config", "webhook", "manifests.yaml")},
		},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		// envtest assets are downloaded by `make test`. Running `go test` directly without
		// them is a setup problem, not a test failure.
		println("envtest could not start; run via `make test`:", err.Error())
		os.Exit(1)
	}

	stop, err := serve(cfg)
	if err != nil {
		println("starting the webhook server:", err.Error())
		_ = testEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()
	stop()
	_ = testEnv.Stop()
	os.Exit(code)
}

// serve brings up a manager serving the webhook on the port envtest rewrote the
// configuration to point at, and waits until the API server can reach it.
func serve(cfg *rest.Config) (func(), error) {
	install := &testEnv.WebhookInstallOptions

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    install.LocalServingHost,
			Port:    install.LocalServingPort,
			CertDir: install.LocalServingCertDir,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}

	// The same call cmd/manager makes. A suite that wired a handler up by hand would not be
	// testing the registration.
	Setup(mgr)

	// Warnings arrive as HTTP headers on the response to the write, so a client that wants
	// to see them has to say so before it makes one.
	warnCfg := rest.CopyConfig(cfg)
	warnCfg.WarningHandler = warned

	client, err := client.New(warnCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}

	k8sClient = client
	cached = mgr.GetClient()

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

	// The API server cannot be told to wait for a webhook that was not serving when it was
	// installed, so the suite waits for the server instead. StartedChecker is the same
	// readiness gate cmd/manager wires to /readyz, and it does not report ready until the
	// serving certificate has been read off disk.
	if err := awaitServing(ctx, mgr.GetWebhookServer().StartedChecker()); err != nil {
		cancel()

		return nil, err
	}

	return cancel, nil
}

// awaitServing polls until the webhook server answers its own readiness check.
func awaitServing(ctx context.Context, started healthz.Checker) error {
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if err := started(nil); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the webhook server: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}

	return fmt.Errorf("the webhook server did not start")
}

// warningLog collects the warning headers the API server relays from the webhook.
//
// Concurrent because client-go calls it from whichever goroutine made the request, and the
// suite makes requests from several tests.
type warningLog struct {
	mu       sync.Mutex
	messages []string
}

// HandleWarningHeader records one warning.
func (w *warningLog) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.messages = append(w.messages, text)
}

// reset drops everything collected so far, so one test cannot read another's warnings.
func (w *warningLog) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.messages = nil
}

// contains reports whether any collected warning mentions substr.
func (w *warningLog) contains(substr string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, message := range w.messages {
		if strings.Contains(message, substr) {
			return true
		}
	}

	return false
}

// all is every warning collected, for a failure message.
func (w *warningLog) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.messages...)
}

// nsSeq numbers the namespaces this process hands out, so `go test -count=2` does not fail
// every test on a name that already exists.
var nsSeq atomic.Uint64

// newNamespace gives a test its own namespace, which is how tests that create objects of the
// same Kind stay independent of each other's natural keys.
func newNamespace(t *testing.T) string {
	t.Helper()

	seq := fmt.Sprintf("-%d", nsSeq.Add(1))
	name := "nbwh-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))

	if budget := 63 - len(seq); len(name) > budget {
		name = name[:budget]
	}

	name += seq

	if err := k8sClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}); err != nil {
		t.Fatalf("creating namespace %s: %v", name, err)
	}

	return name
}

// mustCreate creates obj and fails the test if the webhook or the API server refused it.
func mustCreate(t *testing.T, obj client.Object) {
	t.Helper()

	if err := k8sClient.Create(context.Background(), obj); err != nil {
		t.Fatalf("creating %s %s/%s: %v", obj.GetObjectKind().GroupVersionKind().Kind,
			obj.GetNamespace(), obj.GetName(), err)
	}
}

// refuses creates obj, requires the write to be refused, and returns the message.
func refuses(t *testing.T, obj client.Object) string {
	t.Helper()

	err := k8sClient.Create(context.Background(), obj)
	if err == nil {
		t.Fatalf("%s/%s was admitted; expected a rejection", obj.GetNamespace(), obj.GetName())
	}

	return err.Error()
}

// awaitCached blocks until the webhook's own reader can see something in namespace.
//
// The handler reads the manager's informer cache, which lags the API server by however long a
// watch event takes to arrive. Gating on that exact reader is what keeps the collision and
// cycle tests honest: without it they would be racing the lag, and a miss there is not a bug
// -- the next apply catches it, and the reconcile-time backstop catches it regardless.
func awaitCached(t *testing.T, list client.ObjectList, namespace string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		if err := cached.List(context.Background(), list, client.InNamespace(namespace)); err != nil {
			t.Fatalf("listing through the webhook's reader: %v", err)
		}

		items, err := apimeta.ExtractList(list)
		if err != nil {
			t.Fatalf("reading the listed items: %v", err)
		}

		if len(items) > 0 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for the webhook's reader to see anything in %s", namespace)
}
