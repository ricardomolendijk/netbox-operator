package controller

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// `create` and `delete` on netboxipaddressclaims, because an inline address that says
// `claimFrom` materialises one as an owned child of its NetBoxVirtualMachine -- the inline form
// is sugar over a real claim rather than a second allocation path
// (docs/decisions/0004-claims-first-allocation.md, NBO-033). Which is the only reason this
// Kind's Kubernetes side is writable: the claim is still the one kind whose *NetBox* object the
// operator creates without a name, and nothing here changes that.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddressclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddressclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddressclaims/finalizers,verbs=update

// NetBoxIPAddressClaim's controller is one line, the same one NetBoxPrefix's is. Everything
// that is specific to allocating an address out of a prefix -- which advisory-locked
// sub-path, which field of the answer is the result, which pool states are a refusal -- is
// data on its ClaimDescriptor (internal/registry/claim_ipam_ipaddress.go). This file exists
// to name the kind and to carry its RBAC.
func init() { registerClaimKind(&netboxv1alpha1.NetBoxIPAddressClaim{}) }

// claimKinds are the claim kinds to build a controller for, filled by one init() per kind,
// for the same reason objectKinds is: adding a kind must be new files and no edits.
var claimKinds []reconciler.Claim

// registerClaimKind declares that proto's claim kind gets a controller.
func registerClaimKind(proto reconciler.Claim) {
	claimKinds = append(claimKinds, proto)
}

// SetupClaimControllers registers one controller per claim kind that registered itself.
//
// Separate from SetupObjectControllers because a claim is not an object the declarative
// engine drives -- it has no natural key, no drift and no desired state -- and folding the
// two loops together would mean one list holding two kinds of thing. It shares everything
// that can be shared: the descriptor validation (registry.Validate covers claim descriptors
// too), the endpoint provider, the guarded client and the reference indexes.
func SetupClaimControllers(mgr ctrl.Manager, clients *ClientCache) error {
	kinds, err := namedClaimKinds(mgr.GetScheme())
	if err != nil {
		return err
	}

	// The same indexes every other referring kind gets, over the claim's one reference. It
	// has to happen before the cache starts, and it is a second call rather than a wider
	// first one because a claim's Descriptor-shaped view is derived rather than registered.
	if err := resolver.AddIndexes(context.Background(), mgr.GetFieldIndexer(),
		mgr.GetScheme(), claimRefDescriptors()); err != nil {
		return fmt.Errorf("registering the claim reference indexes: %w", err)
	}

	endpoints := &endpointProvider{reader: mgr.GetClient(), clients: clients}

	for _, kind := range kinds {
		if err := newClaimController(mgr, endpoints, kind).setup(mgr, kind); err != nil {
			return err
		}
	}

	return nil
}

// claimRefDescriptors are the registered claim kinds as reference-carrying Descriptors.
func claimRefDescriptors() []registry.Descriptor {
	claims := registry.Claims()
	out := make([]registry.Descriptor, 0, len(claims))

	for _, claim := range claims {
		out = append(out, claim.RefDescriptor())
	}

	return out
}

// namedClaim is one registered claim kind and the name its controller runs under.
type namedClaim struct {
	name  string
	gvk   schema.GroupVersionKind
	proto reconciler.Claim
}

// namedClaimKinds pairs each registered claim kind with its controller name, ordered by
// name -- controller-runtime enforces globally unique controller names, so a collision has
// to fail identically on every boot.
func namedClaimKinds(scheme *runtime.Scheme) ([]namedClaim, error) {
	kinds := make([]namedClaim, 0, len(claimKinds))

	for _, proto := range claimKinds {
		gvk, err := apiutil.GVKForObject(proto, scheme)
		if err != nil {
			return nil, fmt.Errorf("resolving the group-version-kind of %T: %w", proto, err)
		}

		kinds = append(kinds, namedClaim{name: strings.ToLower(gvk.Kind), gvk: gvk, proto: proto})
	}

	slices.SortFunc(kinds, func(a, b namedClaim) int { return cmp.Compare(a.name, b.name) })

	return kinds, nil
}

// claimController is the wiring every claim kind shares: a CR event in, one allocation pass
// out.
type claimController struct {
	client.Client

	proto  reconciler.Claim
	engine *reconciler.ClaimEngine
}

// newClaimController assembles one claim kind's controller and the allocation engine behind
// it.
//
// Every route to the API server goes through specGuard, exactly as the object controller's
// does. A claim has more reason to want that guard than most kinds: it is the one controller
// that will eventually create CRs of its own (NBO-032's materialised NetBoxIPAddress), so
// "the operator writes no spec it did not create" is the invariant with the narrowest margin
// here.
func newClaimController(
	mgr ctrl.Manager, endpoints reconciler.Endpoints, kind namedClaim,
) *claimController {
	writer := specGuard{client.WithFieldOwner(mgr.GetClient(), reconciler.FieldManager)}

	return &claimController{
		Client: writer,
		proto:  kind.proto,
		engine: &reconciler.ClaimEngine{
			Endpoints:  endpoints,
			Refs:       &resolver.Resolver{Objects: writer, Grants: writer},
			Status:     statusWriter{writer},
			Finalizers: finalizerWriter{writer},
			Events:     mgr.GetEventRecorder(kind.name),
			Scheme:     mgr.GetScheme(),
			// Claims and Pools are left nil deliberately: the engine then reads the
			// package-level registries, which the per-kind init()s filled.
		},
	}
}

// Reconcile hands one claim to the allocation engine, and does nothing else.
func (c *claimController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	claim, ok := c.proto.DeepCopyObject().(reconciler.Claim)
	if !ok {
		return ctrl.Result{}, fmt.Errorf("%T does not deep-copy into a reconciler.Claim", c.proto)
	}

	if err := c.Get(ctx, req.NamespacedName, claim); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted, and the engine already released its finalizer. Whatever it allocated
			// in NetBox is still there and was reported as retained on the way out; there is
			// nothing left here to reconcile.
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("fetching %s: %w", req.NamespacedName, err)
	}

	result, err := c.engine.Reconcile(ctx, claim)
	if err != nil {
		return result, fmt.Errorf("reconciling %s: %w", req.NamespacedName, err)
	}

	return result, nil
}

// setup registers this controller and the watch on its pool.
//
// The pool watch is what makes an exhausted claim converge on a widened prefix instead of up
// to ten minutes later
// (https://github.com/ricardomolendijk/netbox-operator/issues/178). It is the ordinary ref
// watch every kind gets, with one extra predicate: see PoolChanged.
func (c *claimController) setup(mgr ctrl.Manager, kind namedClaim) error {
	b := ctrl.NewControllerManagedBy(mgr).For(c.proto).Named(kind.name)

	desc, known := registry.Claim(kind.gvk)
	if !known {
		// The engine reports an unregistered claim kind on the object's own reconcile, which
		// names it. Refusing to boot here would take the whole operator down over one kind.
		return c.complete(b, kind)
	}

	reader := specGuard{client.WithFieldOwner(mgr.GetClient(), reconciler.FieldManager)}
	refs := desc.RefDescriptor()

	if err := WatchRefs(b, reader, mgr.GetScheme(), refs, PoolChanged()); err != nil {
		return fmt.Errorf("watching the pool of %s: %w", kind.name, err)
	}

	WatchGrants(b, reader, mgr.GetScheme(), refs)

	return c.complete(b, kind)
}

func (c *claimController) complete(b *builder.Builder, kind namedClaim) error {
	if err := b.Complete(c); err != nil {
		return fmt.Errorf("building the %s controller: %w", kind.name, err)
	}

	return nil
}
