package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// command runs one external program and returns its combined output.
//
// Every external call the harness makes goes through here so that a failure carries the
// command line and the output rather than "exit status 1", which is the whole difference
// between a diagnosable CI log and a re-run.
type command struct {
	out   io.Writer
	env   []string
	dir   string
	name  string
	args  []string
	stdin string
}

func run(ctx context.Context, out io.Writer, name string, args ...string) (string, error) {
	return command{out: out, name: name, args: args}.run(ctx)
}

func (c command) run(ctx context.Context) (string, error) {
	logf(c.out, "$ %s %s", c.name, strings.Join(c.args, " "))

	cmd := exec.CommandContext(ctx, c.name, c.args...)
	cmd.Dir = c.dir
	if len(c.env) > 0 {
		cmd.Env = append(os.Environ(), c.env...)
	}
	if c.stdin != "" {
		cmd.Stdin = strings.NewReader(c.stdin)
	}

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		return combined.String(), fmt.Errorf("%s %s: %w\n%s",
			c.name, strings.Join(c.args, " "), err, combined.String())
	}
	return combined.String(), nil
}

// toolPath resolves one of the pinned binaries the Makefile installs into ./bin, falling
// back to PATH. bin/ first and deliberately: a contributor's globally installed kind is
// exactly the thing the Makefile's version pins exist to keep out of a run.
func toolPath(root, name string) (string, error) {
	local := filepath.Join(root, "bin", name)
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, nil
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is not in %s/bin nor on PATH: %w", name, root, err)
	}
	return found, nil
}

// dockerAvailable reports whether there is a Docker daemon to talk to. `docker info` and
// not `docker version`, because the latter succeeds against a dead daemon.
func dockerAvailable(ctx context.Context) error {
	if _, err := run(ctx, nil, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("no usable docker daemon: %w", err)
	}
	return nil
}

// composeAvailable reports whether the Docker Compose v2 plugin is present. The harness
// uses `docker compose`, not the standalone `docker-compose`.
func composeAvailable(ctx context.Context) error {
	if _, err := run(ctx, nil, "docker", "compose", "version"); err != nil {
		return fmt.Errorf("the docker compose plugin is not installed: %w", err)
	}
	return nil
}

func joinErrors(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	return errors.Join(failures...)
}
