package resolver

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
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

	// routeTargetGVK is what the to-many reference points at. It has no typed alias, because
	// ipam.RouteTarget is an M3 kind (NBO-022) -- which is also the ordinary shape for a
	// reference declared before its target's alias exists.
	routeTargetGVK = netboxv1alpha1.GroupVersion.WithKind("NetBoxRouteTarget")
)

// regionField is a reference to a NetBoxRegion, written the way a Descriptor writes one.
func regionField() registry.Field {
	return registry.Field{Spec: "regionRef", API: "region", Class: registry.ClassRefOne, Target: regionGVK}
}

// tenantField is a reference to a Kind that has no descriptor in these tests, which is the
// shape of every ref declared before its target Kind exists.
func tenantField() registry.Field {
	return registry.Field{Spec: "tenantRef", API: "tenant", Class: registry.ClassRefOne, Target: tenantGVK}
}

// importTargetsField is the to-many reference NBO-088 was filed for. ipam.VRF's
// `import_targets` is a ManyToManyField onto ipam.RouteTarget (docs/netbox-schema.md ->
// ipam.VRF), so it arrives as a JSON list and resolves to a list of ids.
func importTargetsField() registry.Field {
	return registry.Field{
		Spec: "importTargets", API: "import_targets",
		Class: registry.ClassRefMany, Target: routeTargetGVK,
	}
}

// kinds is the descriptor source: the Kinds the resolver can dispatch on.
func kinds() fakeDescriptors {
	return fakeDescriptors{byGVK: map[schema.GroupVersionKind]registry.Descriptor{
		regionGVK:      {GVK: regionGVK, Endpoint: "dcim/regions", ObjectType: "dcim.region"},
		siteGVK:        {GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site"},
		routeTargetGVK: {GVK: routeTargetGVK, Endpoint: "ipam/route-targets", ObjectType: "ipam.routetarget"},
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

// ByObjectType is the reverse index over the same fixed set, so a test can exercise the
// AllowedTypes -> Kind lookup without registering a kind into the package-level registry.
func (f fakeDescriptors) ByObjectType(objectType string) (registry.Descriptor, bool) {
	for _, d := range f.byGVK {
		if d.ObjectType == objectType {
			return d, true
		}
	}

	return registry.Descriptor{}, false
}

// scopePair is a polymorphic pair in the shape ipam.Prefix's `scope` and ipam.IPAddress's
// `assignedObject` both have: two members whose Kinds are registered here and one whose Kind
// is not, which is the M3/M4 ordering every real union is in.
func scopePair() registry.GenericFKSpec {
	return registry.GenericFKSpec{
		TypeField:    "scope_type",
		IDField:      "scope_id",
		Spec:         "scope",
		AllowedTypes: []string{"dcim.region", "dcim.site"},
		Members: []registry.GenericFKMember{
			{Spec: "regionRef", Target: regionGVK},
			{Spec: "siteRef", Target: siteGVK},
			{Spec: "tenantRef", Target: tenantGVK},
		},
	}
}

// genericDescriptor is a referrer carrying one polymorphic pair and nothing else, so a test
// asserting on the pair is not also asserting on the ordinary references.
func genericDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site",
		Fields:     []registry.Field{{Spec: "name", API: "name"}},
		GenericFKs: []registry.GenericFKSpec{scopePair()},
	}
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

	// spec is the object's own spec, for the tests that read it: the cycle walk follows a
	// target's references, so a target in those tests is a referrer too.
	spec map[string]any
}

// object renders the target as the API server would hand it back.
func (t target) object() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": t.namespace, "name": t.name},
		"status":   map[string]any{},
	}}
	obj.SetGroupVersionKind(t.gvk)

	if t.spec != nil {
		obj.Object["spec"] = t.spec
	}

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

// GetOne answers out of the canned list, classifying it with netbox.One rather than with a
// copy of the rule -- a fake that decides for itself when a lookup is ambiguous can
// disagree with the client about the one thing the ambiguity cases assert.
func (f *fakeNetBox) GetOne(_ context.Context, endpoint string, params netbox.Params) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "GETONE", endpoint: endpoint, params: params})

	if f.listErr != nil {
		return nil, f.listErr
	}

	return netbox.One(endpoint, params, f.list)
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

// siteDescriptor is a referrer's descriptor: a to-one reference, a to-one reference to a Kind
// that does not exist yet, a to-many reference, plus a scalar that must be left alone.
func siteDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site",
		Fields: []registry.Field{
			{Spec: "name", API: "name"},
			regionField(),
			tenantField(),
			importTargetsField(),
		},
	}
}

// routeTarget is one NetBoxRouteTarget the to-many reference can point at.
func routeTarget(name string, id int64) target {
	return target{
		gvk: routeTargetGVK, namespace: "team-a", name: name, id: id,
		ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
	}
}

// refList renders a to-many reference the way a spec carries one: a JSON array of ObjectRefs.
func refList(names ...string) []any {
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name})
	}

	return out
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

// fakeGrants answers grant lists and namespace label reads from canned state, and counts
// both -- because "a same-namespace reference reads nothing" is an assertion about the counts
// and not about the answers.
type fakeGrants struct {
	grants []netboxv1alpha1.NetBoxRefGrant

	// labels are the extra labels per namespace. Every namespace also carries
	// kubernetes.io/metadata.name, exactly as one the API server created does, so a grant
	// selecting by name needs no fixture.
	labels map[string]map[string]string

	// listErr and nsErr are the reads failing for reasons of their own, which must not be
	// reported as a denial.
	listErr error
	nsErr   error

	lists   int
	nsReads int
}

func (f *fakeGrants) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	f.lists++

	if f.listErr != nil {
		return f.listErr
	}

	grants, ok := list.(*netboxv1alpha1.NetBoxRefGrantList)
	if !ok {
		return fmt.Errorf("fakeGrants was handed a %T rather than a NetBoxRefGrantList", list)
	}

	options := &client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(options)
	}

	for _, candidate := range f.grants {
		if options.Namespace != "" && candidate.Namespace != options.Namespace {
			continue
		}

		grants.Items = append(grants.Items, candidate)
	}

	return nil
}

func (f *fakeGrants) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	f.nsReads++

	if f.nsErr != nil {
		return f.nsErr
	}

	live, ok := obj.(*corev1.Namespace)
	if !ok {
		return fmt.Errorf("fakeGrants was handed a %T rather than a Namespace", obj)
	}

	live.SetName(key.Name)
	live.SetLabels(namespaceLabels(key.Name, f.labels[key.Name]))

	return nil
}

// namespaceLabels is what a real Namespace carries: whatever was set on it, plus the name
// label the API server adds to every namespace.
func namespaceLabels(name string, extra map[string]string) map[string]string {
	set := map[string]string{corev1.LabelMetadataName: name}
	for key, value := range extra {
		set[key] = value
	}

	return set
}

// grantIn is one NetBoxRefGrant in the namespace being referenced, which is the only
// namespace a grant is ever read from.
func grantIn(
	namespace, name string, from []netboxv1alpha1.RefGrantFrom, to []netboxv1alpha1.RefGrantTo,
) netboxv1alpha1.NetBoxRefGrant {
	return netboxv1alpha1.NetBoxRefGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       netboxv1alpha1.NetBoxRefGrantSpec{From: from, To: to},
	}
}

// catalogueGrant is the shape ADR-0002 asks for: one object, no `to`, readable by every
// namespace. It is the fixture most of these tests want, because it is the one most clusters
// will actually hold.
func catalogueGrant(namespace string) netboxv1alpha1.NetBoxRefGrant {
	return grantIn(namespace, "readable-by-all", []netboxv1alpha1.RefGrantFrom{fromAll()}, nil)
}

// fromAll admits every namespace in the cluster.
func fromAll() netboxv1alpha1.RefGrantFrom {
	return netboxv1alpha1.RefGrantFrom{Namespaces: netboxv1alpha1.NamespacesAll}
}

// fromLabelled selects the referring namespaces by an ordinary label.
func fromLabelled(key, value string) netboxv1alpha1.RefGrantFrom {
	return netboxv1alpha1.RefGrantFrom{
		Namespaces: netboxv1alpha1.NamespacesSelector,
		Selector:   &metav1.LabelSelector{MatchLabels: map[string]string{key: value}},
	}
}

// fromNamespaceNamed selects one namespace by name, which needs no extra field: every
// Namespace carries kubernetes.io/metadata.name.
func fromNamespaceNamed(name string) netboxv1alpha1.RefGrantFrom {
	return fromLabelled(corev1.LabelMetadataName, name)
}
