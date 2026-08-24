package controller

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// grantKind is the `targetKind` a grant-triggered enqueue is counted under. A literal
// rather than a GVK from the scheme: NetBoxRefGrant has no Descriptor -- nothing about a
// grant is reconciled into NetBox -- so there is no per-kind data to read it off.
const grantKind = "NetBoxRefGrant"

// WatchRefs adds the reverse edge of every reference d declares: one watch per distinct
// target Kind, mapping a target event back to the objects that point at it.
//
// This is the whole of event-driven convergence. Without it a reference that does not
// resolve waits for the referrer's resync, so a four-deep chain applied in reverse order
// takes four resync intervals -- forty minutes at the default -- to converge. With it the
// same apply converges in the time four reconciles take.
//
// Nothing here branches on Kind, and adding a kind adds no code: the targets come off the
// descriptor's field map, the typed object for each comes from the scheme, and the referrers
// come from a field index whose keys are computed from the same field map (resolver.RefIndex).
//
// Called once from the shared controller shell rather than from each kind's own file. A
// per-kind registration would be ~120 identical calls and ~120 chances to leave one out,
// and a missing one fails as "that kind converges only on its resync" -- which the resync
// then hides.
func WatchRefs(b *builder.Builder, c client.Client, s *runtime.Scheme, d registry.Descriptor) error {
	for _, gvk := range resolver.RefTargets(d) {
		made, err := s.New(gvk)
		if err != nil {
			// No Go type for that Kind, so there is no informer to watch and no object that
			// could exist to be watched. The resolver already reports such a reference as
			// RefKindUnavailable -- a correct manifest waiting for an operator upgrade --
			// and failing the boot here would take the whole operator down over one
			// descriptor naming a Kind this build does not carry yet.
			continue
		}

		target, ok := made.(client.Object)
		if !ok {
			return fmt.Errorf("watching %s for %s: the scheme holds it as %T, which is not a client.Object",
				gvk.Kind, d.GVK.Kind, made)
		}

		b.Watches(target,
			handler.EnqueueRequestsFromMapFunc(EnqueueReferrers(c, s, d, gvk)),
			builder.WithPredicates(TargetUsable()))
	}

	return nil
}

// WatchGrants adds the watch that makes a NetBoxRefGrant take effect when it is written
// rather than on the next resync.
//
// A grant is the fix for a denied reference, so a grant that changes nothing until a timer
// expires is a feature that reads as broken: the operator has just told the user exactly
// which object to create, and creating it appears to do nothing. Deferred from NBO-014 to
// here on purpose -- it is the same reverse edge as a target's, taken by namespace instead
// of by object.
//
// Only kinds that declare a reference get this watch. A grant cannot unblock a kind that
// has nothing to authorise.
func WatchGrants(b *builder.Builder, c client.Client, s *runtime.Scheme, d registry.Descriptor) {
	b.Watches(&netboxv1alpha1.NetBoxRefGrant{},
		handler.EnqueueRequestsFromMapFunc(EnqueueGrantedReferrers(c, s, d)),
		// A grant carries no status, so a generation change is exactly "somebody edited who
		// may refer in here". Create and Delete both pass, which is what they should: one
		// authorises references that are being denied right now, the other revokes
		// references that must stop resolving.
		builder.WithPredicates(predicate.GenerationChangedPredicate{}))
}

// EnqueueReferrers maps an event on a target of Kind target to reconcile requests for the
// objects of d's Kind that reference it.
func EnqueueReferrers(
	c client.Client, s *runtime.Scheme, d registry.Descriptor, target schema.GroupVersionKind,
) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		// Across every namespace, with no InNamespace option. A team namespace pointing at a
		// shared catalogue is the ordinary shape of this API rather than an edge case
		// (docs/decisions/0002-crd-scoping.md), so the cross-namespace referrer is the
		// common case -- and whether it is *allowed* to resolve is the resolver's decision
		// at resolve time, not a reason to leave it asleep.
		match := client.MatchingFields{
			resolver.RefIndex: resolver.IndexValue(target, obj.GetNamespace(), obj.GetName()),
		}

		// A self-referential Kind -- NetBoxRegion.parentRef -- would otherwise enqueue the
		// target itself if it referenced itself, which the resolver reports as a one-hop
		// cycle anyway. For() already covers the object's own events.
		skip := types.NamespacedName{}
		if d.GVK == target {
			skip = client.ObjectKeyFromObject(obj)
		}

		return enqueue(ctx, c, s, d, target.Kind, obj, match, skip)
	}
}

// EnqueueGrantedReferrers maps a NetBoxRefGrant event to the objects of d's Kind whose
// references reach into the grant's namespace.
//
// By namespace rather than by object, because that is what a grant is about: it says who may
// refer *into here*, and its `to` entries narrow that by Kind and name in ways an index
// cannot cheaply mirror. Enqueuing every referrer that crosses into the namespace and
// letting each one re-evaluate the grant is both correct and cheap -- a reconcile that finds
// itself still denied writes nothing.
func EnqueueGrantedReferrers(c client.Client, s *runtime.Scheme, d registry.Descriptor) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		match := client.MatchingFields{resolver.RefNamespaceIndex: obj.GetNamespace()}

		return enqueue(ctx, c, s, d, grantKind, obj, match, types.NamespacedName{})
	}
}

// enqueue lists the referrers one index query matches and turns them into requests.
func enqueue(
	ctx context.Context, c client.Client, s *runtime.Scheme, d registry.Descriptor,
	targetKind string, target client.Object, match client.MatchingFields, skip types.NamespacedName,
) []reconcile.Request {
	log := logf.FromContext(ctx).WithValues(
		"kind", d.GVK.Kind, "targetKind", targetKind, "action", "map",
		"namespace", target.GetNamespace(), "name", target.GetName())

	list, err := newList(s, d.GVK)
	if err != nil {
		log.Error(err, "listing referrers")

		return nil
	}

	if err := c.List(ctx, list, match); err != nil {
		log.Error(err, "listing referrers")

		return nil
	}

	requests, err := requestsFor(list, skip)
	if err != nil {
		log.Error(err, "reading the referrers out of a list")

		return nil
	}

	metrics.RefEnqueueTotal.WithLabelValues(targetKind, d.GVK.Kind).Add(float64(len(requests)))

	// Debug, and never info. This fires for every admitted event on every target of every
	// kind, and a log where nothing means anything is a log nobody reads
	// (CONTRIBUTING.md, "Logging").
	log.V(1).Info("re-enqueued referrers", "referrers", len(requests))

	return requests
}

// requestsFor turns a listed page of referrers into requests, leaving out skip.
func requestsFor(list client.ObjectList, skip types.NamespacedName) ([]reconcile.Request, error) {
	items, err := apimeta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("extracting the items of %T: %w", list, err)
	}

	requests := make([]reconcile.Request, 0, len(items))

	for _, item := range items {
		referrer, ok := item.(client.Object)
		if !ok {
			return nil, fmt.Errorf("%T is not a client.Object", item)
		}

		key := client.ObjectKeyFromObject(referrer)
		if key == skip {
			continue
		}

		// No deduplication here: the index function returns one key per distinct target, so
		// an object holding two references to one target is matched once (resolver.RefIndex).
		requests = append(requests, reconcile.Request{NamespacedName: key})
	}

	return requests, nil
}

// newList returns an empty typed list for gvk.
//
// Typed rather than unstructured, because the field index is registered against the typed
// informer: an unstructured List would query a second cache that has no such index and
// would answer that nothing refers to anything.
func newList(s *runtime.Scheme, gvk schema.GroupVersionKind) (client.ObjectList, error) {
	made, err := s.New(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err != nil {
		return nil, fmt.Errorf("resolving the list type of %s: %w", gvk, err)
	}

	list, ok := made.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("the list type of %s is %T, which is not a client.ObjectList", gvk, made)
	}

	return list, nil
}
