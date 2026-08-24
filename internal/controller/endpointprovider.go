package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
// The context is the reconcile's own (NBO-080). Both reads are in-memory today -- the
// client cache is a map and the CR comes from the manager's informer cache -- so neither
// blocks. It is threaded anyway, because "cannot block" is a property of today's
// implementation and not of the signature: the moment either read reaches the API server,
// a pass whose context is already cancelled would go on waiting, and an object controller
// runs a single worker by default, so one uninterruptible read stalls every object of the
// kind. It also carries the logger, which is the only reason the read failure below is
// reportable at all (CONTRIBUTING.md, "Logging": take the logger from the context).
func (p *endpointProvider) Endpoint(ctx context.Context, namespace, name string) (reconciler.Endpoint, bool) {
	nbClient, stamp, ok := p.clients.Lookup(namespace, name)
	if !ok {
		return reconciler.Endpoint{}, false
	}

	// No DryRun field to populate: the client reports suppression on the return value of
	// every mutating call, so the engine reads it from the answer rather than from a copy
	// of the mode that an adapter could forget to set. See NBO-076. driftMode below is
	// carried for the opposite reason -- it decides what the engine *says* and when it
	// comes back, neither of which is readable off a write that never happened.
	// The stamp comes from the cache rather than from the CR below, and that is deliberate:
	// it is what the endpoint controller *proved* exists in NetBox, whereas spec.managedBy
	// is only what was asked for. Reading it off the spec would let an edit start stamping a
	// tag whose definition has not been created yet -- a 400 per object of every kind.
	// Allocator is the same *netbox.Client, handed over a second time under the narrower
	// interface the allocation engine holds. Not a type assertion inside that engine: an
	// assertion that stops matching after a refactor fails as "this endpoint cannot
	// allocate", at the one moment nobody is watching for it.
	endpoint := reconciler.Endpoint{Client: nbClient, Allocator: nbClient, Provenance: stamp}

	cr := &netboxv1alpha1.NetBoxEndpoint{}
	if err := p.reader.Get(ctx,
		types.NamespacedName{Namespace: namespace, Name: name}, cr); err != nil {
		// A cached client means the endpoint reconciled successfully and has not been
		// forgotten, so this is the narrow window where the CR has gone but the cache has
		// not caught up. The only thing missing is the resync period; refusing the
		// endpoint over it would stall every object in the namespace for a state that
		// resolves itself in milliseconds. Endpoint.Resync of zero means the engine's own
		// default, which is the same ten minutes the CRD defaults to, and an empty
		// DriftMode means Correct, which is the CRD's default too -- and neither default can
		// let this endpoint write when it should not, because DryRun and driftMode: Report
		// are enforced by the client's own mode rather than by this struct (NBO-076).
		//
		// Logged rather than swallowed, and at debug rather than error: falling back on the
		// defaults is the deliberate answer, so nobody has to act on it, and this is the
		// periodic path for every object of every kind -- at error, an informer cache a
		// minute behind is a flood (docs/concepts/reconciliation.md, the transition rule).
		// Said at all because "the endpoint has no client yet" and "the endpoint could not
		// be read" have different fixes, and this is the one place that can tell them apart.
		//
		// endpointRef rather than name, because the logger from the context already carries
		// the object's own name and the two are not the same thing.
		logf.FromContext(ctx).V(1).Info("could not read the netboxendpoint; using its defaults",
			"endpointRef", name, "action", "endpoint", "err", err.Error())

		return endpoint, true
	}

	endpoint.Resync, endpoint.DriftMode = cr.Spec.ResyncPeriod.Duration, cr.Spec.DriftMode

	return endpoint, true
}
