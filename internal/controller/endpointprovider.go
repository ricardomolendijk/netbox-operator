package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// endpointProvider resolves a spec.endpointRef into the engine's Endpoint.
//
// It needs both the cache and the CR, and that is the point of the type. The cache holds
// the client -- the artefact the endpoint controller has proved is authenticated and
// version-checked -- but reconciler.Endpoint also carries Resync, which comes off
// NetBoxEndpoint.spec.resyncPeriod, and a *netbox.Client does not know its own resync
// period (NBO-006). So the provider reads both, and reading the CR per pass rather than
// snapshotting its resync period into the cache is what makes an edit to
// spec.resyncPeriod take effect on the next object pass instead of on the endpoint's next
// reconcile.
type endpointProvider struct {
	// reader reads NetBoxEndpoints through the manager's informer cache, which the
	// endpoint controller already populates. So resolving an endpointRef is an in-memory
	// lookup rather than an API call per object per pass.
	reader client.Reader

	// clients is filled by the NetBoxEndpoint controller and emptied the moment an
	// endpoint stops being Ready. A miss here is the engine's WaitingForEndpoint, which
	// is a wait rather than a failure.
	clients *ClientCache
}

// Endpoint returns the client for one NetBoxEndpoint by namespace and name.
//
// Endpoints.Endpoint takes no context, so there is nothing to cancel a read against and
// nothing to carry the reconcile's logger. That is survivable only because both reads are
// in-memory: an informer cache lookup cannot block, and a blocking read behind this
// signature would stall a reconcile worker with no way to interrupt it.
func (p *endpointProvider) Endpoint(namespace, name string) (reconciler.Endpoint, bool) {
	nbClient, ok := p.clients.Lookup(namespace, name)
	if !ok {
		return reconciler.Endpoint{}, false
	}

	// No DryRun field to populate: the client reports suppression on the return value of
	// every mutating call, so the engine reads it from the answer rather than from a copy
	// of the mode that an adapter could forget to set. See NBO-076. driftMode below is
	// carried for the opposite reason -- it decides what the engine *says* and when it
	// comes back, neither of which is readable off a write that never happened.
	endpoint := reconciler.Endpoint{Client: nbClient}

	cr := &netboxv1alpha1.NetBoxEndpoint{}
	if err := p.reader.Get(context.Background(),
		types.NamespacedName{Namespace: namespace, Name: name}, cr); err != nil {
		// A cached client means the endpoint reconciled successfully and has not been
		// forgotten, so this is the narrow window where the CR has gone but the cache has
		// not caught up. The only thing missing is the resync period; refusing the
		// endpoint over it would stall every object in the namespace for a state that
		// resolves itself in milliseconds. Endpoint.Resync of zero means the engine's own
		// default, which is the same ten minutes the CRD defaults to, and an empty
		// DriftMode means Correct, which is the CRD's default too.
		return endpoint, true
	}

	endpoint.Resync, endpoint.DriftMode = cr.Spec.ResyncPeriod.Duration, cr.Spec.DriftMode

	return endpoint, true
}
