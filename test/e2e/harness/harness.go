// Package harness brings up everything the e2e suites need and tears it down again: a
// kind cluster, a live NetBox with its Postgres and Redis, the operator deployed from this
// repository's Helm chart, and the NetBoxEndpoints and credentials that connect the two.
//
// It exists to be shared. NBO-017 is the first gate to need a real cluster and a real
// NetBox, and it is not the last -- so the bring-up, the readiness helpers and the
// NetBox-side assertions live here rather than inside one test file, and a later gate adds
// a fixture directory and a Describe block instead of a second copy of this.
//
// Nothing here decides whether the suite should run. Preflight reports what is missing and
// the caller skips; see docs/operations/e2e.md.
package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is every knob the harness has, all of them overridable by environment variable so
// a maintainer can point the suite at a cluster they already have.
type Config struct {
	// ClusterName is the kind cluster's name.
	ClusterName string

	// DockerNetwork is the network kind attaches its nodes to, and therefore the network the
	// NetBox containers have to join for the manager's Pods to reach them.
	//
	// "kind" and not the cluster name: kind puts every cluster on one shared network of that
	// fixed name, and only KIND_EXPERIMENTAL_DOCKER_NETWORK changes it. Deriving it from the
	// cluster name looks right and produces a `docker inspect` that finds no such network.
	DockerNetwork string

	// NetBoxImageTag is the netboxcommunity/netbox tag to run. Pinned to the release
	// docs/netbox-schema.md was extracted from; NetBoxEndpoint gates on >=4.2, <5.0.
	NetBoxImageTag string

	// NetBoxHostPort is where NetBox is published on the host, for the test process. The
	// manager reaches the same NetBox by container IP instead.
	NetBoxHostPort int

	// OperatorImage is the image tag built from this checkout and loaded into kind.
	OperatorImage string

	// SkipImageBuild reuses an OperatorImage that is already in the local Docker daemon.
	// For iterating on a test without paying for a rebuild.
	SkipImageBuild bool

	// Retain leaves the cluster and the NetBox containers running after the suite, so a
	// failure can be inspected. The next run reuses them, which is why Up is idempotent.
	Retain bool

	// ReadyTimeout bounds how long any one object may take to converge. 120 s is the
	// figure NBO-017 fixed.
	ReadyTimeout time.Duration

	// RootDir is the repository root, from which the chart and the CRDs are read.
	RootDir string

	// Out is where progress is written. Ginkgo passes GinkgoWriter.
	Out io.Writer
}

// Environment variable names, all in one place so docs/operations/e2e.md and the code
// cannot disagree about the spelling.
const (
	EnvClusterName    = "NBO_E2E_CLUSTER"
	EnvNetBoxTag      = "NBO_E2E_NETBOX_TAG"
	EnvNetBoxPort     = "NBO_E2E_NETBOX_PORT"
	EnvOperatorImage  = "NBO_E2E_IMAGE"
	EnvSkipImageBuild = "NBO_E2E_SKIP_BUILD"
	EnvRetain         = "NBO_E2E_RETAIN"
	EnvReadyTimeout   = "NBO_E2E_READY_TIMEOUT"
	EnvSeed           = "NBO_E2E_SEED"
	EnvPermutations   = "NBO_E2E_PERMUTATIONS"
)

// DefaultConfig reads the environment and fills in the defaults. It does not touch Docker,
// so it is safe to call before Preflight.
func DefaultConfig(out io.Writer) (Config, error) {
	root, err := repoRoot()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		ClusterName:    envOr(EnvClusterName, "nbo-e2e"),
		DockerNetwork:  envOr("KIND_EXPERIMENTAL_DOCKER_NETWORK", "kind"),
		NetBoxImageTag: envOr(EnvNetBoxTag, "v4.6.8"),
		NetBoxHostPort: envInt(EnvNetBoxPort, 18080),
		OperatorImage:  envOr(EnvOperatorImage, "netbox-operator:e2e"),
		SkipImageBuild: envBool(EnvSkipImageBuild),
		Retain:         envBool(EnvRetain),
		ReadyTimeout:   envDuration(EnvReadyTimeout, 120*time.Second),
		RootDir:        root,
		Out:            out,
	}
	return cfg, nil
}

// Harness is one running environment: a cluster, a NetBox, and the operator between them.
// Every field is populated by Up and is nil before it.
type Harness struct {
	Cfg Config

	// Cluster is the kind cluster and the typed client onto it.
	Cluster *Cluster

	// NetBox is the live NetBox: its two addresses, its API token and a typed client.
	NetBox *NetBox

	// Operator is the deployed manager: its logs, its metrics and its restart.
	Operator *Operator
}

// New builds a Harness from cfg without starting anything.
func New(cfg Config) *Harness { return &Harness{Cfg: cfg} }

// Up brings the whole environment to a state the tests can use, and is safe to call
// against one a previous retained run left behind.
//
// The order is forced: NetBox joins the cluster's Docker network, and the operator needs
// both to exist before it can be pointed at either.
func (h *Harness) Up(ctx context.Context) error {
	cluster, err := StartCluster(ctx, h.Cfg)
	if err != nil {
		return fmt.Errorf("starting the kind cluster: %w", err)
	}
	h.Cluster = cluster

	netbox, err := StartNetBox(ctx, h.Cfg)
	if err != nil {
		return fmt.Errorf("starting netbox: %w", err)
	}
	h.NetBox = netbox

	// Before the chart, not alongside the endpoints. The chart renders one Role and
	// RoleBinding per credential namespace and Helm refuses the whole install when one of
	// those namespaces does not exist yet.
	for _, namespace := range FixtureNamespaces {
		if err := cluster.EnsureNamespace(ctx, namespace); err != nil {
			return err
		}
	}

	operator, err := DeployOperator(ctx, h.Cfg, cluster)
	if err != nil {
		return fmt.Errorf("deploying the operator: %w", err)
	}
	h.Operator = operator

	return nil
}

// Down tears the environment down. It is called from a deferred cleanup and therefore
// reports every failure rather than stopping at the first: a NetBox left running because
// the cluster delete failed is the thing that poisons the next run.
func (h *Harness) Down(ctx context.Context) error {
	if h.Cfg.Retain {
		h.Logf("retaining the cluster and netbox (%s=true)", EnvRetain)
		return nil
	}

	var failures []error
	if h.NetBox != nil {
		if err := h.NetBox.Stop(ctx, h.Cfg); err != nil {
			failures = append(failures, fmt.Errorf("stopping netbox: %w", err))
		}
	}
	if h.Cluster != nil {
		if err := h.Cluster.Delete(ctx, h.Cfg); err != nil {
			failures = append(failures, fmt.Errorf("deleting the kind cluster: %w", err))
		}
	}
	return joinErrors(failures)
}

// Logf writes one progress line. Not logr: this is a test harness whose output is read as
// a transcript of a run, and Ginkgo already owns the writer.
func (h *Harness) Logf(format string, args ...any) { logf(h.Cfg.Out, format, args...) }

func logf(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "[harness] "+format+"\n", args...)
}

// repoRoot walks up from the working directory to the directory holding go.mod. The tests
// run in test/e2e and every asset they need -- the chart, the CRDs, the compose file -- is
// addressed from the root, so guessing with "../.." would break the moment a suite moves.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("reading the working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}

func envBool(name string) bool {
	value, err := strconv.ParseBool(os.Getenv(name))
	if err != nil {
		return false
	}
	return value
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}
