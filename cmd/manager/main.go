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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/controller"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
	webhookadmission "github.com/ricardomolendijk/netbox-operator/internal/webhook/admission"
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
	webhookPort          int
	webhookCertDir       string
	enableWebhooks       bool
}

func parseFlags() (options, zap.Options) {
	var opts options
	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", "0",
		"Address the metrics endpoint binds to. 0 disables it; :8443 serves HTTPS.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081",
		"Address the health probe endpoint binds to.")
	flag.BoolVar(&opts.enableLeaderElection, "leader-elect", false,
		"Enable leader election, so only one manager instance is active.")
	// "with authn/authz" is what this said, and it was not true: controller-runtime only
	// runs a TokenReview/SubjectAccessReview on the metrics endpoint when the server is
	// given a FilterProvider, and this one is not (see NewManager below). The flag has only
	// ever chosen HTTPS-with-a-self-signed-certificate over plain HTTP, so that is what it
	// now says -- a scraper needs no credential either way, and anybody reading the old
	// string had reason to believe the port was protected when it is not.
	//
	// Wiring the filter is the stronger answer and is deliberately not done here: it needs
	// `create` on tokenreviews and subjectaccessreviews in the ClusterRole and a bearer
	// token on every existing scraper, so it is a breaking change for a deployment rather
	// than a bug fix. Until then the port must not be reachable from outside the cluster --
	// docs/operations/observability.md and the chart's own comment say so.
	flag.BoolVar(&opts.secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS with a self-signed certificate. There is no authn/authz "+
			"filter on the endpoint either way: do not expose it outside the cluster.")
	flag.BoolVar(&opts.enableHTTP2, "enable-http2", false,
		"Enable HTTP/2 on the metrics and webhook servers.")
	flag.IntVar(&opts.webhookPort, "webhook-port", 9443,
		"Port the admission webhook server binds to.")
	flag.StringVar(&opts.webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs",
		"Directory holding the webhook serving certificate, as tls.crt and tls.key.")
	// On by default, because a webhook configuration installed with no server behind it is
	// the failure mode failurePolicy: Ignore quietly absorbs -- every apply admitted
	// unchecked, with nothing in any log to say so. The flag exists so that a deployment
	// that has not installed the configuration can turn the server off rather than serve on
	// a port nothing calls.
	flag.BoolVar(&opts.enableWebhooks, "enable-webhooks", true,
		"Serve the validating admission webhook.")

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

// credentialNamespacesEnv names the namespaces the operator may read credential Secrets
// in, comma-separated, or `*` for every namespace.
//
// An environment variable rather than a flag because the value is written by a kustomize
// patch: env is a keyed list, so a component can add one entry without restating the
// container's other settings, where a patch to `args` replaces the whole list and would
// have to repeat --leader-elect and --health-probe-bind-address to add one flag.
const credentialNamespacesEnv = "NETBOX_CREDENTIAL_NAMESPACES"

// secretScope reads the deploy-time credential namespace list.
//
// Unset is fatal, not cluster-wide. The manager cannot read Secrets it has no Role for,
// and the two ways that goes wrong are both bad: a cluster-scoped informer would fail its
// LIST with a `Forbidden` that stalls startup with no explanation, and an operator who did
// once hold a cluster-wide grant would silently keep using it. Refusing to start names the
// problem at the only moment anybody is looking.
func secretScope() (controller.SecretScope, error) {
	value := os.Getenv(credentialNamespacesEnv)
	scope, err := controller.ParseSecretScope(value)
	if err != nil {
		// Both install paths are named for the reason SecretScope.Check names both: this
		// message is the first thing a failed rollout shows, and a Helm install has no
		// config/rbac to edit (#300).
		return controller.SecretScope{}, fmt.Errorf("reading %s=%q: %w; list the namespaces "+
			"holding endpoint credential Secrets -- Helm: `--set credentialNamespaces={ns}`; "+
			"kustomize: config/rbac/credential-namespaces/namespaces.txt, then "+
			"`make manifests` -- or set it to %q to read Secrets cluster-wide, which needs a "+
			"cluster-wide Secret grant neither the chart nor config/rbac ships "+
			"(see docs/operations/rbac.md)",
			credentialNamespacesEnv, value, err, controller.AllNamespaces)
	}
	return scope, nil
}

func run() error {
	opts, zapOpts := parseFlags()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	secrets, err := secretScope()
	if err != nil {
		return err
	}
	ctrl.Log.Info("scoped secret access", "namespaces", secrets.String(),
		"credentialLabel", controller.CredentialLabel)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   opts.metricsAddr,
			SecureServing: opts.secureMetrics,
			TLSOpts:       tlsOpts(opts.enableHTTP2),
		},
		HealthProbeBindAddress: opts.probeAddr,
		// Both replicas serve the webhook, and it is deliberately outside the leader-election
		// gate below: a webhook served only by the leader is served by one pod, so half the
		// admission requests reach a replica that answers 404. See internal/webhook/admission.
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    opts.webhookPort,
			CertDir: opts.webhookCertDir,
			TLSOpts: tlsOpts(opts.enableHTTP2),
		}),
		LeaderElection:   opts.enableLeaderElection,
		LeaderElectionID: "netbox-operator.kubeforge.org",
		// One informer per granted namespace, label-selected: an unscoped informer would
		// cache every Secret in the cluster, and a cluster-scoped one would need a
		// cluster-wide grant this deployment does not have.
		Cache: cache.Options{ByObject: secrets.CacheOptions()},
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	// The client cache is shared: the endpoint controller fills it, object controllers
	// read from it. A miss means the endpoint is not Ready.
	clients := controller.NewClientCache()

	// Every write the operator makes carries one field manager name, the endpoint
	// controller's included: the engine reads metadata.managedFields to tell a user's spec
	// fields from its own writes, and it identifies its own by elimination (NBO-079).
	if err := (&controller.NetBoxEndpointReconciler{
		Client:   client.WithFieldOwner(mgr.GetClient(), reconciler.FieldManager),
		Cache:    clients,
		Recorder: mgr.GetEventRecorderFor("netboxendpoint-controller"), //nolint:staticcheck // SA1019: the events-API migration is #294 group 1
		Secrets:  secrets,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up the NetBoxEndpoint controller: %w", err)
	}

	// Every object kind at once, from the set its init() functions registered. This call
	// does not change when a kind is added, which is the point: a new kind is three new
	// files and no edit here (CONTRIBUTING.md, "Extensibility").
	if err := controller.SetupObjectControllers(mgr, clients); err != nil {
		return fmt.Errorf("setting up the object controllers: %w", err)
	}

	// And every claim kind, from the set its own init() functions registered. A second call
	// rather than a wider first one because a claim is not an object the declarative engine
	// drives -- see SetupClaimControllers.
	if err := controller.SetupClaimControllers(mgr, clients); err != nil {
		return fmt.Errorf("setting up the claim controllers: %w", err)
	}

	// The sweep reads NetBox and the CRs of every kind it is asked about, and writes
	// nothing to either -- so it takes the manager's plain client rather than the
	// field-owned one every writer above uses (NBO-046).
	if err := (&controller.NetBoxSweepReconciler{
		Client:   mgr.GetClient(),
		Clients:  clients,
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("netboxsweep-controller"), //nolint:staticcheck // SA1019: the events-API migration is #294 group 1
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up the NetBoxSweep controller: %w", err)
	}

	if opts.enableWebhooks {
		webhookadmission.Setup(mgr)

		// Readiness gated on the webhook server having started, which it cannot do until the
		// serving certificate is on disk. Without it a replica joins the Service before it can
		// answer a TLS handshake, and with failurePolicy: Ignore that is not an error anybody
		// sees -- it is a window in which a fraction of applies go unchecked.
		if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
			return fmt.Errorf("adding the webhook readiness check: %w", err)
		}
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
