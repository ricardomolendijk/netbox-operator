package controller

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// objectKinds are the kinds to build a controller for, filled by one init() per kind.
//
// It exists for the same reason the descriptor registry does: adding a kind must be new
// files and no edits (CONTRIBUTING.md, "Extensibility"), and the alternative -- five lines
// in main() per kind -- is six hundred lines of wiring by the time the catalogue is
// complete, and one hundred and twenty chances to hand a controller the wrong client.
var objectKinds []reconciler.Object

// registerObjectKind declares that proto's kind gets a controller. Called from one init()
// per kind, long before a manager exists, so it takes nothing that has to be built first.
func registerObjectKind(proto reconciler.Object) {
	objectKinds = append(objectKinds, proto)
}

// SetupObjectControllers registers one controller per object kind that registered itself,
// and validates every descriptor before any of them starts.
//
// The registry check is here rather than in main() because this is the function that makes
// descriptors load-bearing: a malformed one must fail the boot, not one reconcile hours
// later (docs/concepts/descriptor.md).
func SetupObjectControllers(mgr ctrl.Manager, clients *ClientCache) error {
	if err := registry.Validate(); err != nil {
		return fmt.Errorf("validating the descriptor registry: %w", err)
	}

	kinds, err := namedKinds(mgr.GetScheme())
	if err != nil {
		return err
	}

	// Before any controller is built, so every index exists before the cache it belongs to
	// starts: an index added afterwards is an error, and one that is silently missing turns
	// every ref watch below into a reconcile that never happens.
	//
	// Here rather than in main() so that the one call covering all ~120 kinds is next to the
	// one watch registration covering all ~120 kinds, and so the envtest suite -- which
	// calls this function and not main() -- exercises the real wiring.
	if err := resolver.AddIndexes(context.Background(), mgr.GetFieldIndexer(),
		mgr.GetScheme(), registry.List()); err != nil {
		return fmt.Errorf("registering the reference indexes: %w", err)
	}

	// One provider for every kind: it is stateless beyond the two caches it reads, and a
	// copy per kind would only multiply the pointers.
	endpoints := &endpointProvider{reader: mgr.GetClient(), clients: clients}

	for _, kind := range kinds {
		if err := newObjectController(mgr, endpoints, kind).setup(mgr, kind); err != nil {
			return err
		}
	}

	return nil
}

// namedKind is one registered kind and the name its controller runs under.
type namedKind struct {
	// name is the lowercased Kind, which is what controller-runtime would derive anyway.
	// Written down because it also names the Event recorder and every error about the
	// controller, and those must agree.
	name string

	// gvk is the kind's group-version-kind, which the scheme already had to resolve in
	// order to name the controller. Kept because the descriptor -- and through it every
	// reference this kind declares -- is keyed on it.
	gvk schema.GroupVersionKind

	// proto is deep-copied to get an empty object of this kind. A prototype rather than a
	// constructor function, because it is the same value controller-runtime's For() needs:
	// one value cannot disagree with itself.
	proto reconciler.Object
}

// namedKinds pairs each registered kind with its controller name, ordered by name.
//
// Ordered because callers log and validate from it, and because controller-runtime
// enforces globally unique controller names -- a collision has to fail identically on
// every boot, not on the boot where the map iterated the other way. Same reason
// registry.List() is ordered.
func namedKinds(scheme *runtime.Scheme) ([]namedKind, error) {
	kinds := make([]namedKind, 0, len(objectKinds))

	for _, proto := range objectKinds {
		gvk, err := apiutil.GVKForObject(proto, scheme)
		if err != nil {
			// The type was never added to the scheme. Nothing about it will work, and a
			// boot failure names it far more clearly than the first reconcile would.
			return nil, fmt.Errorf("resolving the group-version-kind of %T: %w", proto, err)
		}

		kinds = append(kinds, namedKind{name: strings.ToLower(gvk.Kind), gvk: gvk, proto: proto})
	}

	slices.SortFunc(kinds, func(a, b namedKind) int { return cmp.Compare(a.name, b.name) })

	return kinds, nil
}

// objectController is the wiring every object kind shares: a CR event in, one engine pass
// out.
//
// There is one instance per kind and the only field that differs between them is the
// prototype. Everything else the engine needs to know about a kind is data on its
// Descriptor, which is why this is the only Reconcile any object kind has.
type objectController struct {
	client.Client

	// proto is the empty object this controller reconciles, deep-copied per pass.
	proto reconciler.Object

	engine *reconciler.Engine
}

// newObjectController assembles one kind's controller and the engine behind it.
//
// Every route from this controller to the API server goes through specGuard, the Get
// included: one wrapper rather than one per writer, so a future collaborator wired from
// mgr.GetClient() directly is the visible odd one out.
func newObjectController(mgr ctrl.Manager, endpoints reconciler.Endpoints, kind namedKind) *objectController {
	// Field-owned before it is guarded, so every write the operator makes -- the status
	// update, the finalizer patch -- is attributed to one stable manager name. The engine
	// reads metadata.managedFields to learn which spec fields a user set and identifies its
	// own entries by elimination, which needs a name that does not change with the binary
	// (reconciler.FieldManager, NBO-079).
	writer := specGuard{client.WithFieldOwner(mgr.GetClient(), reconciler.FieldManager)}

	return &objectController{
		Client: writer,
		proto:  kind.proto,
		engine: &reconciler.Engine{
			Endpoints: endpoints,

			// The resolver only ever reads, and it reads through the same guarded client as
			// everything else here: the reference target it fetches is another kind's CR, and
			// the one route to the API server is what makes "the operator never writes a spec"
			// checkable rather than merely intended.
			// Grants is the same client for the same reason: authorising a cross-namespace
			// reference is two more reads -- the grants in the target namespace, and the
			// referring namespace's labels -- and both go through the one guarded route.
			Refs:       &resolver.Resolver{Objects: writer, Grants: writer},
			Status:     statusWriter{writer},
			IDs:        idWriter{writer},
			Finalizers: finalizerWriter{writer},
			Owners:     ownerWriter{writer},

			// The one collaborator deliberately *not* wired from the cached client: it exists
			// so that the engine never concludes "somebody else's NetBox object" from a
			// status.id the informer cache is behind on (issue #252). Reading it through the
			// cache would answer with the very copy that is stale.
			//
			// Not wrapped in specGuard either, and it is the one route here that needs no
			// wrapping: an APIReader is a client.Reader with no write method to guard, which
			// is a stronger guarantee than the guard's own.
			LiveStatus: liveStatus{mgr.GetAPIReader()},

			// Through specGuard like everything else, and it passes for a reason rather
			// than by accident: specGuard.generated() keys on the *controller* owner
			// reference, and every object handed to this writer carries one naming the
			// parent that built it. A hand-written CR does not, which is why the guard is
			// still a real backstop on this path -- if the non-hijacking check in
			// children.go were ever removed, the write would be refused here.
			Children: childWriter{writer},
			GitOps:   gitOpsDefaults(),

			Events: mgr.GetEventRecorderFor(kind.name), //nolint:staticcheck // SA1019: the events-API migration is #294 group 1
			Scheme: mgr.GetScheme(),
			// Descriptors is left nil deliberately: the engine then reads the
			// package-level registry, which is the one every kind's init() filled.
		},
	}
}

// Reconcile hands one object to the engine, and does nothing else.
//
// A controller containing business logic has taken work that belongs to the engine
// (CONTRIBUTING.md). The only decision made here is what a missing object means.
func (c *objectController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj, ok := c.proto.DeepCopyObject().(reconciler.Object)
	if !ok {
		return ctrl.Result{}, fmt.Errorf("%T does not deep-copy into a reconciler.Object", c.proto)
	}

	if err := c.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted, and the engine already released its finalizer -- otherwise the
			// object would still be here carrying it. There is nothing left to reconcile
			// and no NetBox object this controller can still prove it owns, so requeueing
			// would be a busy loop against an object that is gone.
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("fetching %s: %w", req.NamespacedName, err)
	}

	result, err := c.engine.Reconcile(ctx, obj)
	if err != nil {
		// The engine returns an error for exactly three things -- a kind with no registered
		// descriptor, a status write that failed for anything but losing a race, and a live
		// status read the API server would not answer -- and none of them is about NetBox.
		// All are worth naming the object for, since they are the only failures that reach
		// controller-runtime's backoff rather than the object's own conditions.
		return result, fmt.Errorf("reconciling %s: %w", req.NamespacedName, err)
	}

	return result, nil
}

// setup registers this controller with the manager, together with the watches that make a
// reference converge on an event rather than on a resync.
//
// The ref watches are registered here, once, for every kind: they are computed from the
// kind's Descriptor (internal/controller/refwatch.go), so this call does not change when a
// kind is added and a kind cannot be added having forgotten them.
//
// No watch on the NetBoxEndpoint: an object whose endpoint is not Ready requeues on the
// engine's own short interval, and a watch that fans one endpoint out to every object in
// its namespace is a thundering herd worth designing rather than adding here.
func (c *objectController) setup(mgr ctrl.Manager, kind namedKind) error {
	b := ctrl.NewControllerManagedBy(mgr).For(c.proto).Named(kind.name)

	if err := c.watchRefs(mgr, b, kind); err != nil {
		return err
	}

	if err := b.Complete(c); err != nil {
		return fmt.Errorf("building the %s controller: %w", kind.name, err)
	}

	return nil
}

// watchRefs adds this kind's reference watches, and none if it declares no reference.
//
// A kind with no descriptor is left without them rather than failing the boot. The engine
// already reports that as an error on the object's own reconcile, which names the kind and
// the object; refusing to start would turn one unregistered kind into a manager that never
// reconciles any of the other hundred and nineteen.
func (c *objectController) watchRefs(mgr ctrl.Manager, b *builder.Builder, kind namedKind) error {
	d, known := registry.Get(kind.gvk)
	if !known || len(resolver.RefTargets(d)) == 0 {
		return nil
	}

	// Through specGuard like every other route to the API server from this controller, even
	// though a map function only ever lists: one wrapper rather than one per caller is what
	// keeps a future collaborator wired from mgr.GetClient() directly the visible odd one out.
	reader := specGuard{client.WithFieldOwner(mgr.GetClient(), reconciler.FieldManager)}

	if err := WatchRefs(b, reader, mgr.GetScheme(), d); err != nil {
		return fmt.Errorf("watching the reference targets of %s: %w", kind.name, err)
	}

	WatchGrants(b, reader, mgr.GetScheme(), d)

	return nil
}

// statusWriter persists an object's status subresource, and is the engine's only route to
// the API server for it.
//
// Deliberately not the same type as finalizerWriter. Status and metadata.finalizers are
// different subresources written by different calls, and keeping the two writers apart is
// what makes "the engine never writes a spec"
// (docs/decisions/0005-gitops-coexistence.md) checkable rather than merely intended.
type statusWriter struct{ client.Client }

// UpdateStatus writes obj's status, and only its status.
func (w statusWriter) UpdateStatus(ctx context.Context, obj client.Object) error {
	if err := w.Status().Update(ctx, obj); err != nil {
		return fmt.Errorf("updating the status of %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// idWriter persists status.id on its own, for the pass that has just created the NetBox object
// it names.
//
// A merge patch on the status subresource with no resourceVersion in it, which is the whole
// point: statusWriter's Update is optimistically concurrent by design and a create whose status
// write lost that race left NetBox holding an object no CR could ever claim again (issues #289
// and #291, reconciler.recordID). An id is not a conclusion drawn from a possibly-stale read --
// it is a fact NetBox minted for a POST that has already happened -- so there is no fresher
// value for a concurrent write to hold, and nothing this could clobber.
//
// It patches a copy and reads nothing back, for the reason patchMetadata does: a PATCH is
// answered with the whole object, and decoding that answer into the caller's object would
// replace the status the pass is still building (issue #243).
type idWriter struct{ client.Client }

// RecordID writes obj's status.id, and nothing else.
func (w idWriter) RecordID(ctx context.Context, obj client.Object, id int64) error {
	patch, err := json.Marshal(map[string]any{"status": map[string]any{"id": id}})
	if err != nil {
		return fmt.Errorf("encoding the id patch for %s/%s: %w",
			obj.GetNamespace(), obj.GetName(), err)
	}

	patched, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("%T does not deep-copy into a client.Object", obj)
	}

	if err := w.Status().Patch(ctx, patched, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patching the netbox id of %s/%s: %w",
			obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// liveStatus reads an object's engine-owned status straight from the API server.
//
// A client.Reader rather than a client.Client, and the shipped one is the manager's
// APIReader: the whole value of this seam is that it does not answer from the informer cache
// the reconcile was fed from, and a type that cannot write is also a type that cannot be
// mistaken for a route to one.
type liveStatus struct{ reader client.Reader }

// LiveStatus returns the status the API server currently holds for obj.
//
// Into a fresh copy rather than into obj: the caller is mid-reconcile and has already written
// to the status it is holding, so reading over it would silently discard the pass's own work.
func (r liveStatus) LiveStatus(ctx context.Context, obj reconciler.Object) (
	netboxv1alpha1.NetBoxObjectStatus, error,
) {
	fresh, ok := obj.DeepCopyObject().(reconciler.Object)
	if !ok {
		return netboxv1alpha1.NetBoxObjectStatus{},
			fmt.Errorf("%T does not deep-copy into a reconciler.Object", obj)
	}

	if err := r.reader.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
		return netboxv1alpha1.NetBoxObjectStatus{}, fmt.Errorf("reading %s/%s past the cache: %w",
			obj.GetNamespace(), obj.GetName(), err)
	}

	return *fresh.NetBoxStatus(), nil
}

// finalizerWriter persists an object's metadata.finalizers.
type finalizerWriter struct{ client.Client }

// UpdateFinalizers writes obj's finalizer list, and nothing else.
//
// A nil list marshals to `null`, which a merge patch reads as "remove the key". That is
// the same outcome as an empty list and is what the deletion path wants.
func (w finalizerWriter) UpdateFinalizers(ctx context.Context, obj client.Object) error {
	return patchMetadata(ctx, w.Client, obj, "finalizers", obj.GetFinalizers())
}

// ownerWriter persists an object's metadata.ownerReferences.
//
// A separate type from finalizerWriter even though both patch one metadata field, because
// the engine holds them as separate interfaces for exactly that reason: what a writer is
// named is what it may write (reconciler/owners.go, and the same argument in the comment on
// statusWriter).
type ownerWriter struct{ client.Client }

// UpdateOwnerReferences writes obj's owner references, and nothing else.
//
// The whole list, because a merge patch replaces a list rather than merging it -- there is no
// per-entry patch for an array of objects with no merge key. That is safe here only because
// the list being sent was built by appending to the one this object was read with
// (reconciler.addOwner), so an owner reference somebody else set is carried back verbatim
// rather than dropped. The resourceVersion below is the other half of that: a list computed
// from a stale read is rejected outright rather than applied over a concurrent addition.
func (w ownerWriter) UpdateOwnerReferences(ctx context.Context, obj client.Object) error {
	return patchMetadata(ctx, w.Client, obj, "ownerReferences", obj.GetOwnerReferences())
}

// childWriter creates, updates and deletes the child CRs a parent's inline lists declare.
//
// The one writer in this file that is not narrowed to a single metadata field, because
// materialising a child means bringing a whole object into existence and there is no smaller
// shape that does it. What keeps ADR-0005 §1 intact is not the interface here but what the
// materialiser hands it: every object carries a controller owner reference naming the parent
// that built it, so it is the operator's own output rather than Git's input -- and specGuard,
// which this is wrapped in, refuses anything that is not.
//
// The `create` and `delete` this needs are granted by each kind's own RBAC marker, in its own
// controller file, rather than by one rule here: controller-gen does not accept a wildcard
// resource, so the alternative is a hand-maintained list of every kind -- exactly the thing
// that goes stale when a kind is added.
type childWriter struct{ client.Client }

// Apply server-side-applies obj under reconciler.ChildFieldManager.
//
// A field manager of its own, overriding the one on the wrapped client: server-side apply
// records ownership per field per manager, so the separate name is what lets the materialiser
// own the fields it sets on a child and leave the rest to whoever set them. It also keeps the
// invariant readable from outside -- `f:spec` under netbox-operator/children is the
// materialiser's own output, `f:spec` under netbox-operator would be a broken promise.
func (w childWriter) Apply(ctx context.Context, obj client.Object, opts ...client.ApplyOption) error {
	opts = append(opts, client.FieldOwner(reconciler.ChildFieldManager))

	// Client.Apply takes an apply configuration rather than an object, and a CRD kind here
	// has none generated for it, so the object goes through the unstructured route the
	// client provides for exactly that case. What is sent does not change: the deprecated
	// client.Apply patch this replaces used json.Marshal of this same object as its body,
	// which is what ToUnstructured produces. Force is still unset unless the caller passes
	// client.ForceOwnership, so the unforced-first apply in reconciler's write() still gets
	// the conflict that names the fields.
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return fmt.Errorf("encoding %s/%s to apply: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	applied := &unstructured.Unstructured{Object: content}

	if err := w.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(applied), opts...); err != nil {
		return fmt.Errorf("applying %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	// The server's answer, back into obj. The patch this replaces did that for free, and the
	// materialiser reads it: the applied child's status is how it decides the child is ready
	// (reconciler/children.go).
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(applied.Object, obj); err != nil {
		return fmt.Errorf("decoding the apply of %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

// gitOpsDefaults is the GitOps annotation set the shipped manager uses.
//
// A function rather than a package variable, so that two controllers cannot end up sharing --
// and one of them mutating -- a single Extra map. The chart values that will populate it are
// NBO-061; until then this is ADR-0005 §5's documented default, Argo CD on and Flux off.
func gitOpsDefaults() *reconciler.GitOps {
	defaults := reconciler.DefaultGitOps()

	return &defaults
}

// patchMetadata writes one field of obj's metadata and nothing else.
//
// A merge patch scoped to that field rather than an Update, because an Update sends the whole
// object back -- spec included -- and the engine must never write a spec. The patch carries
// the object's resourceVersion, so it is still optimistically concurrent: a value computed
// from a stale read is rejected rather than applied over somebody else's.
//
// One function for both fields so that the shape stays one decision. specGuard.Patch admits a
// patch whose body is nothing but `metadata`, and a second hand-rolled patch body is a second
// chance to put something else in there.
//
// It patches a copy and carries back nothing but the resourceVersion, because a PATCH is
// answered with the *whole* object and controller-runtime decodes that answer into whatever
// object it was handed -- status included. Patching obj directly therefore replaced the status
// the engine had built up in memory with the one the API server still held, silently erasing
// every condition an earlier step of the same pass had set. That is how a referrer reached
// Ready=Synced carrying no RefsResolved at all: resolveRefs sets it during build(), the
// containment owner reference is patched on afterwards, and finish() then wrote the status the
// answer had brought back (issue #243).
func patchMetadata(ctx context.Context, c client.Client, obj client.Object, field string, value any) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		field:             value,
		"resourceVersion": obj.GetResourceVersion(),
	}})
	if err != nil {
		return fmt.Errorf("encoding the %s patch for %s/%s: %w",
			field, obj.GetNamespace(), obj.GetName(), err)
	}

	patched, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("%T does not deep-copy into a client.Object", obj)
	}

	if err := c.Patch(ctx, patched, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patching the %s of %s/%s: %w",
			field, obj.GetNamespace(), obj.GetName(), err)
	}

	// The resourceVersion is the one thing the answer carries that the caller does not already
	// have, and it is load-bearing: the status update at the end of the pass sends it as a
	// precondition, so a pass that patched metadata and then wrote its status would otherwise
	// be rejected as stale. The metadata the patch itself moved is what the caller sent, and
	// the only other entry the API server touches is this manager's own managedFields entry --
	// which ownershipOf skips by name, so leaving it a pass behind changes nothing.
	obj.SetResourceVersion(patched.GetResourceVersion())

	return nil
}
