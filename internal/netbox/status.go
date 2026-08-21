package netbox

import (
	"context"
	"fmt"
	"net/http"
	"slices"
)

// ServerStatus is the useful part of GET /api/status/.
type ServerStatus struct {
	// Version is NetBox's own version, e.g. "4.6.8".
	Version string
	// Plugins are the installed plugin names. Worth recording: a plugin that adds a
	// required custom field is an otherwise baffling source of 400s.
	Plugins []string
}

// Status probes GET /api/status/. It doubles as the authentication check, because NetBox
// requires a valid token for it -- so one call answers "can we reach it", "is the token
// good" and "what version is it".
func (c *Client) Status(ctx context.Context) (ServerStatus, error) {
	body, err := c.do(ctx, http.MethodGet, c.base+"/status/", "status", nil)
	if err != nil {
		return ServerStatus{}, err
	}

	status := ServerStatus{Version: asString(body["netbox-version"])}
	if status.Version == "" {
		return ServerStatus{}, &ValidationError{
			Body: fmt.Sprintf("no netbox-version in the status response from %s", c.base),
		}
	}
	for name := range toMap(body["plugins"]) {
		status.Plugins = append(status.Plugins, name)
	}
	// Go randomises map iteration, and this list is written straight to an endpoint's
	// status: unsorted, the same server yields a different order every probe and every
	// resync becomes a real status write. See NBO-078.
	slices.Sort(status.Plugins)
	return status, nil
}

func toMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}
