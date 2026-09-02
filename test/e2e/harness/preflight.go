package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Preflight reports every reason this machine cannot run the e2e suite, as sentences a
// maintainer can act on.
//
// It returns all of them rather than the first, because "install kind" followed by "start
// Docker" followed by "the compose plugin is missing" is three CI runs to learn one thing.
// An empty slice means the suite can run.
//
// It deliberately reports rather than decides. A suite that silently passed because its
// prerequisites were absent would be worse than one that does not run, so the caller skips
// loudly with these reasons attached.
func Preflight(ctx context.Context, cfg Config) []string {
	var reasons []string

	if err := dockerAvailable(ctx); err != nil {
		reasons = append(reasons, fmt.Sprintf(
			"docker: %v -- the suite needs a Docker daemon for kind and for NetBox", err))
	}
	if err := composeAvailable(ctx); err != nil {
		reasons = append(reasons, fmt.Sprintf(
			"docker compose: %v -- NetBox, Postgres and Redis come up as a compose project", err))
	}
	if _, err := toolPath(cfg.RootDir, "kind"); err != nil {
		reasons = append(reasons, fmt.Sprintf(
			"kind: %v -- run `make kind` to install the pinned version into ./bin", err))
	}
	if _, err := toolPath(cfg.RootDir, "helm"); err != nil {
		reasons = append(reasons, fmt.Sprintf(
			"helm: %v -- run `make helm-bin` to install the pinned version into ./bin", err))
	}

	reasons = append(reasons, missingAssets(cfg)...)
	return reasons
}

// missingAssets checks the files the harness reads out of the checkout. A missing chart is
// not an environment problem, so it is worth telling apart from a missing binary.
//
// A slice rather than a map: these reasons are printed in a skip message and a map's
// iteration order would reorder them between runs.
func missingAssets(cfg Config) []string {
	assets := []struct {
		what string
		path string
	}{
		{"the operator's Helm chart", filepath.Join(cfg.RootDir, "charts", "netbox-operator", "Chart.yaml")},
		{"the CRD directory", filepath.Join(append([]string{cfg.RootDir}, crdDir...)...)},
		{"the kind cluster config", filepath.Join(cfg.RootDir, "test", "e2e", "kind", "cluster.yaml")},
		{"the NetBox compose project", filepath.Join(cfg.RootDir, "test", "e2e", "netbox", "docker-compose.yaml")},
	}

	var reasons []string
	for _, asset := range assets {
		if _, err := os.Stat(asset.path); err != nil {
			reasons = append(reasons, fmt.Sprintf("%s is missing at %s: %v", asset.what, asset.path, err))
		}
	}
	return reasons
}
