// Command manager runs the NetBox operator's controller manager.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(netboxv1alpha1.AddToScheme(scheme))
}

// options is every flag the manager accepts, kept in one struct so main() stays a
// sequence of named steps rather than a paragraph.
type options struct {
	metricsAddr          string
	probeAddr            string
	enableLeaderElection bool
	secureMetrics        bool
	enableHTTP2          bool
}

func parseFlags() (options, zap.Options) {
	var opts options
	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", "0",
		"Address the metrics endpoint binds to. 0 disables it; :8443 serves HTTPS.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081",
		"Address the health probe endpoint binds to.")
	flag.BoolVar(&opts.enableLeaderElection, "leader-elect", false,
		"Enable leader election, so only one manager instance is active.")
	flag.BoolVar(&opts.secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS with authn/authz.")
	flag.BoolVar(&opts.enableHTTP2, "enable-http2", false,
		"Enable HTTP/2 on the metrics and webhook servers.")

	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	return opts, zapOpts
}

// tlsOpts disables HTTP/2 unless it was explicitly asked for. HTTP/2 has a history
// of denial-of-service issues (CVE-2023-44487, CVE-2023-39325) and neither the
// metrics endpoint nor the webhook server needs it.
func tlsOpts(enableHTTP2 bool) []func(*tls.Config) {
	if enableHTTP2 {
		return nil
	}
	return []func(*tls.Config){
		func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} },
	}
}

func run() error {
	opts, zapOpts := parseFlags()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   opts.metricsAddr,
			SecureServing: opts.secureMetrics,
			TLSOpts:       tlsOpts(opts.enableHTTP2),
		},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.enableLeaderElection,
		LeaderElectionID:       "netbox-operator.kubeforge.org",
		// Secrets are the operator's only cluster-wide read, and an unscoped informer
		// would cache every one of them.
		Cache: cache.Options{ByObject: controller.SecretCacheOptions()},
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	// The client cache is shared: the endpoint controller fills it, object controllers
	// read from it. A miss means the endpoint is not Ready.
	clients := controller.NewClientCache()

	if err := (&controller.NetBoxEndpointReconciler{
		Client:   mgr.GetClient(),
		Cache:    clients,
		Recorder: mgr.GetEventRecorderFor("netboxendpoint-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up the NetBoxEndpoint controller: %w", err)
	}

	// Every object kind at once, from the set its init() functions registered. This call
	// does not change when a kind is added, which is the point: a new kind is three new
	// files and no edit here (CONTRIBUTING.md, "Extensibility").
	if err := controller.SetupObjectControllers(mgr, clients); err != nil {
		return fmt.Errorf("setting up the object controllers: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("adding healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("adding readyz check: %w", err)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		ctrl.Log.Error(err, "manager exited")
		os.Exit(1)
	}
}
