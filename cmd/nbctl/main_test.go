package main

import (
	"io"
	"strings"
	"testing"
)

// TestRequiredFlags covers the guards on the flags that cannot be defaulted. --endpoint is
// the one worth a test of its own: it names a NetBoxEndpoint in the destination cluster,
// which no NetBox URL can be turned into, so a silently-derived default would produce
// manifests that apply and then wait forever.
func TestRequiredFlags(t *testing.T) {
	t.Setenv(tokenEnv, "token")

	base := []string{"export", "--url", "https://nb", "--endpoint", "homelab", "-n", "ns", "-o", "out"}

	tests := map[string]struct {
		args []string
		want string
	}{
		"no subcommand":  {args: nil, want: "export subcommand"},
		"wrong command":  {args: []string{"plan"}, want: "export subcommand"},
		"missing url":    {args: without(base, "--url"), want: "--url is required"},
		"missing ns":     {args: without(base, "-n"), want: "--namespace is required"},
		"missing output": {args: without(base, "-o"), want: "-o is required"},
		"missing endpoint": {
			args: without(base, "--endpoint"),
			want: "--endpoint is required",
		},
		"bad split": {args: append(append([]string{}, base...), "--split", "app"), want: "--split must be"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := run(test.args, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("run(%v) error = %v, want it to mention %q", test.args, err, test.want)
			}
		})
	}
}

func TestMissingTokenIsRefused(t *testing.T) {
	t.Setenv(tokenEnv, "")

	args := []string{"export", "--url", "https://nb", "--endpoint", "e", "-n", "ns", "-o", "out"}
	if err := run(args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), tokenEnv) {
		t.Errorf("run error = %v, want it to name %s", err, tokenEnv)
	}
}

// without drops a flag and its value from an argument list.
func without(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++

			continue
		}
		out = append(out, args[i])
	}

	return out
}
