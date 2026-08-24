package resolver

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The collaborator interfaces exist to be faked, but they are only worth having if the real
// things satisfy them too. These are the second implementations.
var (
	_ Reader       = client.Client(nil)
	_ LookupClient = (*netbox.Client)(nil)
	_ Descriptors  = (*registry.Registry)(nil)
)

// The two Kinds the tests resolve against: one that exists everywhere, and one whose CRD is
// deliberately missing so that "not installed" is a state the tests can reach.
var (
	regionGVK = netboxv1alpha1.RegionRef{}.TargetGVK()
	siteGVK   = netboxv1alpha1.SiteRef{}.TargetGVK()
	tenantGVK = netboxv1alpha1.TenantRef{}.TargetGVK()
)

// regionField is a reference to a NetBoxRegion, written the way a Descriptor writes one.
func regionField() registry.Field {
	return registry.Field{Spec: "regionRef", API: "region", Ref: true, Target: regionGVK}
}

// tenantField is a reference to a Kind that has no descriptor in these tests, which is the
// shape of every ref declared before its target Kind exists.
func tenantField() registry.Field {
	return registry.Field{Spec: "tenantRef", API: "tenant", Ref: true, Target: tenantGVK}
}

// kinds is the descriptor source: the two Kinds the resolver can dispatch on.
func kinds() fakeDescriptors {
	return fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
		regionGVK: {GVK: regionGVK, Endpoint: "dcim/regions", ObjectType: "dcim.region"},
		siteGVK:   {GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site"},
	}}
}

// fakeDescriptors serves a fixed set of descriptors.
type fakeDescriptors struct {
	byGVK map[schema.GroupVersionKind]registry.Descriptor
}

func (f fakeDescriptors) Get(gvk schema.GroupVersionKind) (registry.Descriptor, bool) {
	d, ok := f.byGVK[gvk]

	return d, ok
}

// target is one CR the resolver may read: the status the engine would have written, in the
// unstructured form the resolver reads it in.
type target struct {
	gvk         schema.GroupVersionKind
	namespace   string
	name        string
	id          int64
	ready       metav1.ConditionStatus
	reason      string
	message     string
	terminating bool
}

// object renders the target as the API server would hand it back.
func (t target) object() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": t.namespace, "name": t.name},
		"status":   map[string]any{},
	}}
	obj.SetGroupVersionKind(t.gvk)

	if t.id != 0 {
		if err := unstructured.SetNestedField(obj.Object, t.id, "status", "id"); err != nil {
			panic(err)
		}
	}

	if t.ready != "" {
		conditions := []any{map[string]any{
			"type": netboxv1alpha1.ConditionReady, "status": string(t.ready),
			"reason": t.reason, "message": t.message,
		}}
		if err := unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions"); err != nil {
			panic(err)
		}
	}

	if t.terminating {
		obj.SetDeletionTimestamp(&metav1.Time{Time: metav1.Now().Time})
		obj.SetFinalizers([]string{netboxv1alpha1.Finalizer})
	}

	return obj
}

// readyTarget is the ordinary case: a CR the engine has already written to NetBox, with the
// one id every table row expects to come back.
func readyTarget() target {
	return target{
		gvk: regionGVK, namespace: "team-a", name: "emea", id: 12,
		ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
	}
}

// fakeReader answers from canned objects, and can fail the way a cluster fails.
type fakeReader struct {
	objects []target

	// err replaces the whole lookup, for the failures that are not about absence: a CRD that
	// is not installed, or an API server that said no.
	err error

	reads int
}

func (f *fakeReader) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	f.reads++

	if f.err != nil {
		return f.err
	}

	live, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("fakeReader was handed a %T rather than an unstructured object", obj)
	}

	for _, candidate := range f.objects {
		if candidate.gvk != live.GroupVersionKind() ||
			candidate.namespace != key.Namespace || candidate.name != key.Name {
			continue
		}

		live.Object = candidate.object().Object

		return nil
	}

	return apierrors.NewNotFound(
		schema.GroupResource{Group: live.GroupVersionKind().Group, Resource: live.GetKind()}, key.Name)
}

// noKindMatch is what a RESTMapper returns for a Kind whose CRD is not installed, which is
// the failure that must not be reported as "not found".
func noKindMatch(gvk schema.GroupVersionKind) error {
	return &apimeta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
}

// errAPIServer stands in for a cluster read that failed for a reason of its own.
var errAPIServer = errors.New("the api server said no")

// call is one NetBox request, in the order it was made.
type call struct {
	method   string
	endpoint string
	id       int
	params   netbox.Params
}

// fakeNetBox answers from canned state and records what it was asked. It has no write
// methods at all, which is how "a blocked resolution issues zero mutations" is asserted.
type fakeNetBox struct {
	calls []call

	list    []netbox.Object
	listErr error
	get     netbox.Object
	getErr  error
}

func (f *fakeNetBox) List(_ context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error) {
	f.calls = append(f.calls, call{method: "LIST", endpoint: endpoint, params: params})

	return f.list, f.listErr
}

func (f *fakeNetBox) GetByID(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "GET", endpoint: endpoint, id: id})

	return f.get, f.getErr
}

// referrer is the object holding the references, in the shape ResolveAll reads: an
// unstructured CR, because the resolver reads a spec through its JSON form and so needs no
// per-kind type at all.
func referrer(name string, refs map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "team-a", "name": name},
		"spec":     refs,
	}}
	obj.SetGroupVersionKind(siteGVK)

	return obj
}

// siteDescriptor is a referrer's descriptor: two references, one of them to a Kind that does
// not exist yet, plus a scalar that must be left alone.
func siteDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site",
		Fields: []registry.Field{
			{Spec: "name", API: "name"},
			regionField(),
			tenantField(),
		},
	}
}

// The four shapes of a reference, so a table row reads as the one thing it is testing.
func objectRef(name string) netboxv1alpha1.ObjectRef {
	return netboxv1alpha1.ObjectRef{Name: name}
}

func slugRef(slug string) netboxv1alpha1.ObjectRef {
	return netboxv1alpha1.ObjectRef{Slug: slug}
}

func lookupRef(filter map[string]string) netboxv1alpha1.ObjectRef {
	return netboxv1alpha1.ObjectRef{Lookup: filter}
}

func idRef(id int64) netboxv1alpha1.ObjectRef {
	return netboxv1alpha1.ObjectRef{ID: &id}
}

func namespacedName(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}
