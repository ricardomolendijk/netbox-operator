package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// The adapter is only useful if it satisfies the seam the engine defines.
var _ reconciler.Endpoints = (*endpointProvider)(nil)

// errReadRefused stands in for an API-server or cache read that did not answer.
var errReadRefused = errors.New("endpoint read refused")

// probeKey marks a context so a test can prove the one it passed in is the one that reached
// the read, rather than a context.Background() built on the way.
type probeKey struct{}

// recordingReader is a client.Reader that records the context it was handed and answers with
// whatever the test asked for. Only Get is exercised; List is here to satisfy the interface.
type recordingReader struct {
	// got is the context the read was handed, kept to assert on rather than to use.
	got context.Context
	err error
	obj *netboxv1alpha1.NetBoxEndpoint
}

func (r *recordingReader) Get(ctx context.Context, _ client.ObjectKey, obj client.Object,
	_ ...client.GetOption,
) error {
	r.got = ctx
	if r.err != nil {
		return r.err
	}

	// One caller, one type: a failed assertion here is a broken test, not a runtime state.
	*(obj.(*netboxv1alpha1.NetBoxEndpoint)) = *r.obj

	return nil
}

func (r *recordingReader) List(ctx context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	r.got = ctx

	return nil
}

// cachedEndpoint returns a provider whose cache holds a client for team-a/homelab, which is
// the precondition for the CR read happening at all.
func cachedEndpoint(t *testing.T, reader client.Reader) *endpointProvider {
	t.Helper()

	nbClient, err := netbox.New(netbox.Config{URL: "https://netbox.invalid"})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}

	cache := NewClientCache()
	// A zero Stamp: this fixture exercises the provider's context and read-failure
	// behaviour, not provenance, and an unstamped endpoint is a real state (spec.managedBy
	// is unset by default).
	cache.put(clientKey{namespace: "team-a", name: "homelab"}, nbClient, provenance.Stamp{})

	return &endpointProvider{reader: reader, clients: cache}
}

// TestEndpointProviderUsesTheReconcileContext is the adapter half of NBO-080: the context
// the engine hands in has to be the one the read runs under, or cancelling a reconcile
// cancels nothing and the request-scoped logger is not the one this code logs through.
func TestEndpointProviderUsesTheReconcileContext(t *testing.T) {
	reader := &recordingReader{obj: &netboxv1alpha1.NetBoxEndpoint{}}
	provider := cachedEndpoint(t, reader)

	ctx := context.WithValue(context.Background(), probeKey{}, "reconcile")
	if _, ok := provider.Endpoint(ctx, "team-a", "homelab"); !ok {
		t.Fatal("Endpoint() = not ready, want the cached client")
	}

	if reader.got == nil {
		t.Fatal("the reader was never called")
	}

	if got := reader.got.Value(probeKey{}); got != "reconcile" {
		t.Errorf("the read ran under a context carrying %v, want the reconcile's own", got)
	}
}

// TestEndpointProviderReadsTheSpecPerPass states why the CR is read at all: the resync
// period and the drift mode come off the CR, not off the cached client, and reading them per
// pass is what makes an edit take effect on the next object pass.
func TestEndpointProviderReadsTheSpecPerPass(t *testing.T) {
	cr := &netboxv1alpha1.NetBoxEndpoint{}
	cr.Spec.ResyncPeriod.Duration = 90 * time.Second
	cr.Spec.DriftMode = netboxv1alpha1.DriftReport

	provider := cachedEndpoint(t, &recordingReader{obj: cr})

	endpoint, ok := provider.Endpoint(context.Background(), "team-a", "homelab")
	if !ok {
		t.Fatal("Endpoint() = not ready, want the cached client")
	}

	if endpoint.Resync != 90*time.Second || endpoint.DriftMode != netboxv1alpha1.DriftReport {
		t.Errorf("Endpoint() = resync %s, driftMode %q; want 1m30s and %q",
			endpoint.Resync, endpoint.DriftMode, netboxv1alpha1.DriftReport)
	}
}

// TestEndpointProviderKeepsTheClientWhenTheReadFails pins the decision NBO-080 had to make
// about the swallowed error, so that changing it is a deliberate act rather than a tidy-up.
//
// A failed read is not turned into "the endpoint is not ready". The cached client is proof
// that the endpoint reconciled successfully, and the only thing the read contributes is the
// resync period and the drift mode -- both of which have defaults that match the CRD's, and
// neither of which can let this endpoint write when it should not, since DryRun and Report
// are enforced by the client's own mode. Refusing the endpoint instead would stall every
// object in the namespace over a cache that catches up in milliseconds.
//
// So the two states stay distinguishable where it matters -- a cache miss returns false, a
// failed read returns the endpoint -- and the failure is reported in the log rather than in
// the return type. See the provider's own comment for the rest of the reasoning.
func TestEndpointProviderKeepsTheClientWhenTheReadFails(t *testing.T) {
	provider := cachedEndpoint(t, &recordingReader{err: errReadRefused})

	endpoint, ok := provider.Endpoint(context.Background(), "team-a", "homelab")
	if !ok {
		t.Fatal("Endpoint() = not ready after a failed read; a cached client is still a client")
	}

	if endpoint.Client == nil {
		t.Error("Endpoint().Client = nil, want the cached client")
	}

	// Zero and empty are the engine's own defaults, which are the CRD's defaults too.
	if endpoint.Resync != 0 || endpoint.DriftMode != "" {
		t.Errorf("Endpoint() = resync %s, driftMode %q; want the defaults after a failed read",
			endpoint.Resync, endpoint.DriftMode)
	}
}

// TestEndpointProviderMissIsNotReady is the other half of the pair above: no client at all is
// the state the engine reports as WaitingForEndpoint.
func TestEndpointProviderMissIsNotReady(t *testing.T) {
	reader := &recordingReader{obj: &netboxv1alpha1.NetBoxEndpoint{}}
	provider := cachedEndpoint(t, reader)

	if _, ok := provider.Endpoint(context.Background(), "team-a", "absent"); ok {
		t.Error("Endpoint() = ready for an endpoint with no cached client")
	}

	// The CR is never read for an endpoint that has no client: there is nothing the resync
	// period could be attached to.
	if reader.got != nil {
		t.Error("the CR was read for an endpoint with no cached client")
	}
}
