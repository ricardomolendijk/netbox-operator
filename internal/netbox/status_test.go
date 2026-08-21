package netbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestStatusPluginsAreSorted pins the ordering, which is not cosmetic: the list is written
// straight to a NetBoxEndpoint's status, and Go randomises map iteration -- so an unsorted
// list makes every probe of an unchanged server produce a different status and turns every
// resync into a real write. See NBO-078.
func TestStatusPluginsAreSorted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"netbox-version":"4.6.8","plugins":{
			"netbox_topology_views":"4.1.0","netbox_bgp":"0.14.0","netbox_dns":"1.1.0",
			"netbox_secrets":"2.1.0","netbox_qrcode":"0.0.13","netbox_attachments":"7.0.0"}}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, nil)
	want := []string{
		"netbox_attachments", "netbox_bgp", "netbox_dns",
		"netbox_qrcode", "netbox_secrets", "netbox_topology_views",
	}

	// Repeated because one call cannot fail: with six keys, map order happens to come out
	// sorted often enough that a single probe is not evidence of anything.
	for i := range 20 {
		got, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("Status() probe %d: %v", i, err)
		}
		if !slices.Equal(got.Plugins, want) {
			t.Fatalf("probe %d plugins = %v, want %v", i, got.Plugins, want)
		}
	}
}

// TestStatusWithoutPlugins covers a NetBox with none installed: sorting must not turn an
// absent list into an empty one, because [] and null are different status values and
// alternating between them would be its own churn.
func TestStatusWithoutPlugins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"netbox-version":"4.6.8","plugins":{}}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv, nil).Status(context.Background())
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if got.Plugins != nil {
		t.Errorf("plugins = %v, want nil", got.Plugins)
	}
}
