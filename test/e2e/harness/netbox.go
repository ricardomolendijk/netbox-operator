package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// APIToken is the token the harness mints in the test NetBox and hands to the operator.
//
// A constant, and not a secret: it exists for the lifetime of one throwaway database that
// is published on localhost. See test/e2e/netbox/mint-token.py for why the harness mints it
// rather than letting the image do it.
const APIToken = "e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0"

// NetBox is the live NetBox the suite runs against.
type NetBox struct {
	// Project is the compose project name, derived from the cluster name so two clusters
	// on one machine do not fight over one NetBox.
	Project string

	// HostURL is where the *test process* reaches NetBox: the published port on localhost.
	HostURL string

	// InClusterURL is where the *manager* reaches NetBox: the container's address on the
	// cluster's Docker network. Two addresses for one NetBox, because the manager is
	// inside the cluster and the assertions are not.
	InClusterURL string

	// Client is a typed client onto HostURL, for asserting NetBox-side state. It is the
	// operator's own client from internal/netbox rather than raw HTTP, so a test cannot
	// disagree with the operator about pagination, ambiguity or error classification.
	Client *netbox.Client

	composeFile string
	container   string
}

// StartNetBox brings up NetBox, Postgres and Redis on the cluster's Docker network, waits
// for the API to answer, and mints the operator's token.
//
// Idempotent: `docker compose up` against a running project is a no-op, and the token
// script checks before it writes.
func StartNetBox(ctx context.Context, cfg Config) (*NetBox, error) {
	nb := &NetBox{
		Project:      cfg.ClusterName + "-netbox",
		composeFile:  filepath.Join(cfg.RootDir, "test", "e2e", "netbox", "docker-compose.yaml"),
		HostURL:      fmt.Sprintf("http://127.0.0.1:%d", cfg.NetBoxHostPort),
		InClusterURL: "",
	}
	nb.container = nb.Project + "-netbox-1"

	logf(cfg.Out, "bringing up netbox %s (project %s)", cfg.NetBoxImageTag, nb.Project)
	if _, err := nb.compose(ctx, cfg, "up", "-d", "--wait"); err != nil {
		return nil, fmt.Errorf("bringing up the netbox compose project: %w", err)
	}

	address, err := containerAddress(ctx, cfg, nb.container)
	if err != nil {
		return nil, err
	}
	nb.InClusterURL = "http://" + address + ":8080"

	if err := nb.mintToken(ctx, cfg); err != nil {
		return nil, err
	}

	client, err := netbox.New(netbox.Config{URL: nb.HostURL, Token: APIToken})
	if err != nil {
		return nil, fmt.Errorf("building a netbox client for %s: %w", nb.HostURL, err)
	}
	nb.Client = client

	if err := nb.waitForAPI(ctx, cfg); err != nil {
		return nil, err
	}
	return nb, nil
}

// Stop tears the compose project down, volumes and all. Called before the cluster is
// deleted, because deleting the cluster removes the network these containers are attached
// to and Docker will not remove a network with endpoints on it.
func (nb *NetBox) Stop(ctx context.Context, cfg Config) error {
	if _, err := nb.compose(ctx, cfg, "down", "--volumes", "--remove-orphans"); err != nil {
		return fmt.Errorf("tearing down the netbox compose project %s: %w", nb.Project, err)
	}
	return nil
}

func (nb *NetBox) compose(ctx context.Context, cfg Config, args ...string) (string, error) {
	full := append([]string{"compose", "-p", nb.Project, "-f", nb.composeFile}, args...)
	return command{
		out:  cfg.Out,
		name: "docker",
		args: full,
		env: []string{
			"NETBOX_IMAGE_TAG=" + cfg.NetBoxImageTag,
			"NETBOX_HOST_PORT=" + strconv.Itoa(cfg.NetBoxHostPort),
			"NETBOX_DOCKER_NETWORK=" + cfg.DockerNetwork,
		},
	}.run(ctx)
}

// mintToken runs test/e2e/netbox/mint-token.py inside the NetBox container.
//
// Piped into `manage.py shell` rather than copied in and executed, so the script is a file
// in the repository that a maintainer can read and run by hand.
func (nb *NetBox) mintToken(ctx context.Context, cfg Config) error {
	script := filepath.Join(cfg.RootDir, "test", "e2e", "netbox", "mint-token.py")
	body, err := os.ReadFile(script)
	if err != nil {
		return fmt.Errorf("reading %s: %w", script, err)
	}

	cmd := command{
		out:  cfg.Out,
		name: "docker",
		args: []string{
			"exec", "-i", nb.container,
			"/opt/netbox/venv/bin/python", "/opt/netbox/netbox/manage.py",
			"shell", "--no-startup", "--no-imports", "--interface", "python",
		},
		stdin: string(body),
	}
	out, err := cmd.run(ctx)
	if err != nil {
		return fmt.Errorf("minting the netbox api token: %w", err)
	}
	if !strings.Contains(out, "token-created") && !strings.Contains(out, "token-exists") {
		return fmt.Errorf("mint-token.py reported neither outcome:\n%s", out)
	}
	return nil
}

// waitForAPI polls /api/status/ with the token, which is the first request that proves all
// three of "gunicorn is up", "migrations are done" and "the token authenticates".
func (nb *NetBox) waitForAPI(ctx context.Context, cfg Config) error {
	return WaitFor(ctx, "netbox /api/status/ to answer", 5*time.Minute,
		func(ctx context.Context) (bool, string, error) {
			status, err := nb.Client.Status(ctx)
			if err != nil {
				return transient(err)
			}
			logf(cfg.Out, "netbox %s is up at %s (in-cluster %s)",
				status.Version, nb.HostURL, nb.InClusterURL)
			return true, "", nil
		})
}
