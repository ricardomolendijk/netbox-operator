package controller

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// referrers answers the engine's "which CRs point at this one", for a cascading delete
// (reconciler.Referrers, #304).
//
// It is the same query the reference watches make, run from the other end. WatchRefs walks
// *forwards* -- for this kind's targets, watch them and map an event back -- and this walks
// backwards: for one object, ask every kind that could point at it whether any does. Both go
// through resolver.RefIndex, so a referrer this operator would re-enqueue on a change is
// exactly one it can find here, and there is no second definition of "refers to" to keep in
// step with the first.
type referrers struct {
	client client.Client
	scheme *runtime.Scheme
}

// Referring lists every CR of every registered kind that names obj in a reference.
//
// The candidate kinds are computed rather than looked up: a kind can refer to this object
// only if its descriptor declares a reference whose target is this object's GVK, which
// resolver.RefTargets already reports for the forward walk. Kinds that cannot refer here are
// never listed at all, so the cost is one indexed List per kind that genuinely could -- two
// or three for most objects, not one per kind in the API.
//
// Across every namespace, with no InNamespace option, because a reference may cross one: a
// team's VLAN pointing at a shared site is the ordinary shape of this API (ADR-0002), and it
// is precisely the CR whose NetBox object is in the way.
//
// A partial answer is not returned. This drives deletion, and a list that quietly missed a
// kind would report "nothing references this" to a caller whose next move is to conclude the
// blocker is somebody else's and stop -- so a failed List fails the whole call and the
// deletion retries.
func (r referrers) Referring(ctx context.Context, obj client.Object) ([]client.Object, error) {
	gvk, err := apiutil.GVKForObject(obj, r.scheme)
	if err != nil {
		return nil, fmt.Errorf("resolving the GVK of %T: %w", obj, err)
	}

	match := client.MatchingFields{
		resolver.RefIndex: resolver.IndexValue(gvk, obj.GetNamespace(), obj.GetName()),
	}

	self := client.ObjectKeyFromObject(obj)
	found := make([]client.Object, 0, 4)

	for _, descriptor := range registry.List() {
		if !refersTo(descriptor, gvk) {
			continue
		}

		// Typed rather than unstructured, for the reason newList carries: the field index is
		// registered against the typed informer, and an unstructured List queries a second
		// cache that has no such index and would answer that nothing refers to anything.
		list, err := newList(r.scheme, descriptor.GVK)
		if err != nil {
			return nil, err
		}

		if err := r.client.List(ctx, list, match); err != nil {
			return nil, fmt.Errorf("listing %s referring to %s %s/%s: %w",
				descriptor.GVK.Kind, gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
		}

		items, err := apimeta.ExtractList(list)
		if err != nil {
			return nil, fmt.Errorf("reading the %s referrers out of a list: %w", descriptor.GVK.Kind, err)
		}

		for _, item := range items {
			referrer, ok := item.(client.Object)
			if !ok {
				return nil, fmt.Errorf("a %s referrer is %T, which is not a client.Object",
					descriptor.GVK.Kind, item)
			}

			// A self-referential kind -- NetBoxRegion.parentRef -- can index itself. Deleting
			// the object that is already being deleted would be this cascade eating its own
			// caller, and the finalizer it would be waiting on is its own.
			if descriptor.GVK == gvk && client.ObjectKeyFromObject(referrer) == self {
				continue
			}

			// The Kind is what makes the Event readable, and a typed object listed out of
			// the cache does not carry one: TypeMeta is cleared on decode. Set from the
			// descriptor that produced the list, which is where it is known for certain.
			referrer.GetObjectKind().SetGroupVersionKind(descriptor.GVK)

			found = append(found, referrer)
		}
	}

	return found, nil
}

// refersTo reports whether a kind's descriptor declares any reference pointing at gvk.
func refersTo(d registry.Descriptor, gvk schema.GroupVersionKind) bool {
	for _, target := range resolver.RefTargets(d) {
		if target == gvk {
			return true
		}
	}

	return false
}
