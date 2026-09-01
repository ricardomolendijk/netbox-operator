package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Cluster is the kind cluster the suite runs against.
type Cluster struct {
	// Name is the kind cluster name, and also the name of the Docker network its nodes
	// join -- which is what lets NetBox share an L3 domain with the manager's Pods.
	Name string

	// Kubeconfig is the path kind exported. Written under the OS temp directory rather
	// than merged into ~/.kube/config, so a run cannot change the contributor's current
	// context and a failed run leaves nothing behind that a later `kubectl` would pick up.
	Kubeconfig string

	// RESTConfig and Client talk to the cluster. The client knows the operator's scheme,
	// so tests deal in typed CRs.
	RESTConfig *rest.Config
	Client     client.Client
}

// Scheme is the scheme the harness's client uses: core Kubernetes, the CRD API (for
// installing the CRDs) and the operator's own group.
func Scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(netboxv1alpha1.AddToScheme(scheme))
	return scheme
}

// StartCluster creates the kind cluster if it is not already there, and returns a client
// onto it either way.
//
// Idempotent on purpose. A retained cluster from a previous run is reused rather than
// recreated, which is what makes iterating on a test bearable -- the cluster is the
// expensive part.
func StartCluster(ctx context.Context, cfg Config) (*Cluster, error) {
	kind, err := toolPath(cfg.RootDir, "kind")
	if err != nil {
		return nil, err
	}

	existing, err := run(ctx, cfg.Out, kind, "get", "clusters")
	if err != nil {
		return nil, fmt.Errorf("listing kind clusters: %w", err)
	}

	if !hasLine(existing, cfg.ClusterName) {
		logf(cfg.Out, "creating kind cluster %q", cfg.ClusterName)
		configPath := filepath.Join(cfg.RootDir, "test", "e2e", "kind", "cluster.yaml")
		if _, err := run(ctx, cfg.Out, kind, "create", "cluster",
			"--name", cfg.ClusterName, "--config", configPath, "--wait", "180s"); err != nil {
			return nil, fmt.Errorf("creating kind cluster %q: %w", cfg.ClusterName, err)
		}
	}

	cluster := &Cluster{Name: cfg.ClusterName}
	if err := cluster.exportKubeconfig(ctx, cfg, kind); err != nil {
		return nil, err
	}
	if err := cluster.connect(); err != nil {
		return nil, err
	}
	return cluster, nil
}

func (c *Cluster) exportKubeconfig(ctx context.Context, cfg Config, kind string) error {
	path := filepath.Join(os.TempDir(), "nbo-e2e-"+c.Name+".kubeconfig")
	if _, err := run(ctx, cfg.Out, kind, "export", "kubeconfig",
		"--name", c.Name, "--kubeconfig", path); err != nil {
		return fmt.Errorf("exporting the kubeconfig for %q: %w", c.Name, err)
	}
	c.Kubeconfig = path
	return nil
}

func (c *Cluster) connect() error {
	restCfg, err := clientcmd.BuildConfigFromFlags("", c.Kubeconfig)
	if err != nil {
		return fmt.Errorf("reading kubeconfig %s: %w", c.Kubeconfig, err)
	}
	// The suite applies a few hundred objects in bursts and then polls them. The
	// client-go defaults (5 QPS) turn that into minutes of client-side throttling that
	// looks exactly like a slow operator.
	restCfg.QPS = 100
	restCfg.Burst = 200

	typed, err := client.New(restCfg, client.Options{Scheme: Scheme()})
	if err != nil {
		return fmt.Errorf("building a client for %s: %w", c.Kubeconfig, err)
	}
	c.RESTConfig = restCfg
	c.Client = typed
	return nil
}

// containerAddress reads a container's address on the kind network.
//
// It is how the manager is told where NetBox is: both are on that network, so the address is
// routable between them. It is deliberately not used the other way round -- a container IP is
// only reachable from the *host* on plain Linux Docker, so everything the test process talks
// to is a published port instead.
func containerAddress(ctx context.Context, cfg Config, container string) (string, error) {
	format := fmt.Sprintf("{{ (index .NetworkSettings.Networks %q).IPAddress }}", cfg.DockerNetwork)
	out, err := run(ctx, cfg.Out, "docker", "inspect", "-f", format, container)
	if err != nil {
		return "", fmt.Errorf("reading the %s address of container %s: %w",
			cfg.DockerNetwork, container, err)
	}
	address := strings.TrimSpace(out)
	if address == "" {
		return "", fmt.Errorf("container %s has no address on the %s network",
			container, cfg.DockerNetwork)
	}
	return address, nil
}

// Delete removes the cluster. Removing the cluster removes its Docker network, which is
// why NetBox has to be stopped first.
func (c *Cluster) Delete(ctx context.Context, cfg Config) error {
	kind, err := toolPath(cfg.RootDir, "kind")
	if err != nil {
		return err
	}
	if _, err := run(ctx, cfg.Out, kind, "delete", "cluster", "--name", c.Name); err != nil {
		return fmt.Errorf("deleting kind cluster %q: %w", c.Name, err)
	}
	_ = os.Remove(c.Kubeconfig)
	return nil
}

// EnsureNamespace creates ns if it is not there. Used by every suite, so it lives here.
func (c *Cluster) EnsureNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{}
	ns.Name = name
	if err := c.Client.Create(ctx, ns); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", name, err)
	}
	return nil
}

// WaitFor polls condition until it returns true, or until the deadline. The error names
// what was being waited for, because a bare "timed out" in a CI log is worthless.
func WaitFor(ctx context.Context, what string, timeout time.Duration, condition func(context.Context) (bool, string, error)) error {
	deadline := time.Now().Add(timeout)
	var lastDetail string

	for {
		done, detail, err := condition(ctx)
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		if done {
			return nil
		}
		lastDetail = detail

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s: %s", timeout, what, lastDetail)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// transient renders err as polling detail rather than as a failure.
//
// A Get that failed once while the API server was busy is not a reason to stop waiting;
// WaitFor's deadline is what ends the wait, and the last detail is what the timeout prints.
// A genuine failure therefore shows up as a timeout carrying the error's own message.
func transient(err error) (bool, string, error) {
	return false, err.Error(), nil
}

func hasLine(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
