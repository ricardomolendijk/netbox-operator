package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// ErrSpecWriteForbidden is a write the operator is structurally not allowed to make: a
// change to something other than the status of a CR it did not create.
//
// Git owns a CR's spec. If the operator writes one, the GitOps tool sees the live object
// diverge from the manifest, reverts it, and the operator writes it again -- a fight at the
// shorter of the two resync intervals, for as long as both are running
// (docs/decisions/0005-gitops-coexistence.md §1).
var ErrSpecWriteForbidden = errors.New("the operator may not write anything but the status of a CR it did not create")

// specGuard is the client the object controllers write through: every method of a
// client.Client, with the ones that could reach a spec refusing to.
//
// The invariant it enforces is already structural -- the engine holds a StatusWriter and a
// FinalizerWriter and no client at all, so there is nothing for it to call. This is the
// third layer, and it is deliberately cheap: it costs one registry lookup per write and it
// converts "somebody adds a client.Client to the engine in eighteen months" from a silent
// GitOps fight into a returned error with this sentinel in it. It is not a substitute for
// review, and the registry-wide test in internal/reconciler is not a substitute for it:
// the test proves today's code does not write a spec, the guard proves tomorrow's cannot.
type specGuard struct{ client.Client }

// Update refuses to replace a guarded object. An Update sends the whole object, spec
// included, so there is no such thing as a status-only Update -- that is what
// Status().Update() is, and it goes to a different subresource through the embedded
// client.
func (g specGuard) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := g.check(obj, "update"); err != nil {
		return err
	}

	if err := g.Client.Update(ctx, obj, opts...); err != nil {
		return fmt.Errorf("updating %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// Patch refuses a patch that reaches outside metadata.
//
// Scoped rather than refused outright because the finalizer is written this way: a merge
// patch carrying only metadata.finalizers and the resourceVersion, precisely so that
// keeping a CR alive until its NetBox object is dealt with does not require sending the
// spec back (objectcontroller.go, finalizerWriter). Anything whose shape this cannot read
// is refused rather than guessed at.
func (g specGuard) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if !metadataOnly(obj, patch) {
		if err := g.check(obj, "patch"); err != nil {
			return err
		}
	}

	if err := g.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return fmt.Errorf("patching %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// check reports whether writing obj is forbidden.
func (g specGuard) check(obj client.Object, verb string) error {
	gvk, err := apiutil.GVKForObject(obj, g.Scheme())
	if err != nil {
		// A type the scheme does not know cannot be one of ours, and refusing a write over
		// a failed lookup would break every controller that shares this client.
		return nil //nolint:nilerr // an unknown type is not a registered kind
	}

	if _, registered := registry.Get(gvk); !registered {
		return nil
	}

	// The operator may write what it created: an inline child or a claim's resulting
	// resource is the operator's own output, not Git's input, so nothing reverts it
	// (NBO-032, NBO-036). Keyed on the owner reference the operator itself set rather than
	// on a list of generated kinds, because a list is the thing that goes stale.
	if generated(obj) {
		return nil
	}

	return fmt.Errorf("%w: %s on %s %s/%s", ErrSpecWriteForbidden,
		verb, gvk.Kind, obj.GetNamespace(), obj.GetName())
}

// generated reports whether the operator materialised obj, which it can tell from an owner
// reference to one of its own kinds -- the operator is the only thing that sets one.
func generated(obj client.Object) bool {
	for _, owner := range obj.GetOwnerReferences() {
		group, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil && group.Group == netboxv1alpha1.GroupName {
			return true
		}
	}

	return false
}

// metadataOnly reports whether patch changes nothing outside metadata.
//
// It reads the rendered patch body rather than trusting the patch type, because the type
// says how the body is merged and not what is in it. A body that is not a JSON object --
// a JSON Patch operation list, say -- is reported as not metadata-only: the operator sends
// exactly one kind of patch, and a shape this does not recognise is one nobody has thought
// about.
func metadataOnly(obj client.Object, patch client.Patch) bool {
	body, err := patch.Data(obj)
	if err != nil {
		return false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return false
	}

	for name := range fields {
		if name != "metadata" {
			return false
		}
	}

	return true
}
