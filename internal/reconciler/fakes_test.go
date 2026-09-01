package reconciler

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The engine's collaborator interfaces exist to be faked, but they are only worth having if
// the real thing satisfies them too. These are the second implementations.
var (
	_ NetBoxClient = (*netbox.Client)(nil)
	_ Recorder     = record.EventRecorder(nil)
	_ Object       = (*fakeKind)(nil)
	_ Endpoints    = fakeEndpoints{}
	_ Endpoints    = (*blockingEndpoints)(nil)
	_ StatusReader = (*fakeLiveStatus)(nil)
)

var fakeGVK = schema.GroupVersionKind{Group: "netbox.kubeforge.org", Version: "v1alpha1", Kind: "NetBoxFake"}

// fakeRef stands in for the ObjectRef that NBO-011 defines. Its only job here is to be a
// spec value the engine cannot render into a payload.
type fakeRef struct {
	Name string `json:"name,omitempty"`
}

// fakeSpec is a generated kind's spec: the shared envelope inline, plus one field of each
// shape the engine has to handle.
//
// One field per shape NBO-079 has to answer for -- string, int, bool, slice, map -- and
// `omitempty` on all of them, because that is what a real kind carries and it is precisely
// what makes an empty value invisible: `description: ""` marshals to nothing at all. The
// engine puts the value back from metadata.managedFields, so the fixture models the problem
// rather than being edited around it. Weight is the pointer shape as well, which needs no
// ownership metadata: a nil pointer is already a state of its own.
type fakeSpec struct {
	netboxv1alpha1.NetBoxObjectSpec `json:",inline"`

	Name         string            `json:"name,omitempty"`
	Slug         string            `json:"slug,omitempty"`
	Color        string            `json:"color,omitempty"`
	Description  string            `json:"description,omitempty"`
	Priority     int64             `json:"priority,omitempty"`
	Enabled      bool              `json:"enabled,omitempty"`
	Weight       *int64            `json:"weight,omitempty"`
	ObjectTypes  []string          `json:"objectTypes,omitempty"`
	LocalContext map[string]string `json:"localContextData,omitempty"`
	ParentRef    *fakeRef          `json:"parentRef,omitempty"`

	// ASNRefs is the to-many reference NBO-088 added: a list of references written as a list
	// of NetBox ids, which is what dcim.Site's `asns`, ipam.VRF's `import_targets` and
	// dcim.Interface's `wireless_lans` all are.
	//
	// Deliberately not `tags`. That column is the engine's own, stamped from
	// Descriptor.Taggable rather than mapped from a spec field, and using it here would make
	// the provenance tests unable to tell the two sources apart.
	ASNRefs []fakeRef `json:"asnRefs,omitempty"`

	// PrimaryIP4Ref is the deferred reference NBO-015 is about: dcim.Device's
	// `primary_ip4` needs an address that needs an interface that needs the Device, so no
	// apply order sets it at create time.
	PrimaryIP4Ref *fakeRef `json:"primaryIP4Ref,omitempty"`

	// Scope is the polymorphic reference NBO-018 and NBO-019 share: one spec field that
	// writes the two columns of a generic foreign key.
	//
	// The real v1alpha1.ScopeRef rather than a fake union, because it is the only
	// generic-FK union any Kind carries today and a second fake of the same shape is how the
	// two implementations of it got built in the first place.
	Scope *netboxv1alpha1.ScopeRef `json:"scope,omitempty"`

	Unmapped string `json:"unmapped,omitempty"`
}

// fakeKind is a stand-in for a generated kind.
//
// The engine has to be provable before any real kind exists -- NBO-008 is the first -- and
// a fake spec can carry combinations no single real kind has, which is the point of testing
// the engine rather than testing a kind.
type fakeKind struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   fakeSpec                          `json:"spec,omitempty"`
	Status netboxv1alpha1.NetBoxObjectStatus `json:"status,omitempty"`
}

func (f *fakeKind) NetBoxSpec() *netboxv1alpha1.NetBoxObjectSpec     { return &f.Spec.NetBoxObjectSpec }
func (f *fakeKind) NetBoxStatus() *netboxv1alpha1.NetBoxObjectStatus { return &f.Status }

func (f *fakeKind) DeepCopyObject() runtime.Object {
	out := *f
	f.DeepCopyInto(&out.ObjectMeta)
	out.Status = *f.Status.DeepCopy()
	out.Spec.ObjectTypes = slices.Clone(f.Spec.ObjectTypes)
	out.Spec.LocalContext = maps.Clone(f.Spec.LocalContext)
	out.Spec.ASNRefs = slices.Clone(f.Spec.ASNRefs)
	out.Spec.Scope = f.Spec.Scope.DeepCopy()

	return &out
}

func fakeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(fakeGVK, &fakeKind{})

	return scheme
}

// fakeObject is a NetBoxFake with the fields every case needs set.
func fakeObject() *fakeKind {
	return &fakeKind{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "managed", Generation: 3},
		Spec: fakeSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Managed",
			Slug:             "managed",
			Color:            "9e9e9e",
		},
	}
}

// deletingObject is a NetBoxFake the way the API server hands one back after
// `kubectl delete`: a deletion timestamp, the finalizer still on, and a status.id proving
// the engine created the NetBox object it is now responsible for removing.
func deletingObject() *fakeKind {
	obj := fakeObject()
	obj.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	obj.Finalizers = []string{netboxv1alpha1.Finalizer}
	obj.Status.ID = 9

	return obj
}

// fakeDescriptor is extras.Tag's shape: one scalar natural key, an object-type list, and a
// reference the engine cannot resolve yet.
func fakeDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK:        fakeGVK,
		Endpoint:   "extras/tags",
		ObjectType: "extras.tag",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []registry.Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "description", API: "description"},
			{Spec: "priority", API: "priority"},
			{Spec: "enabled", API: "enabled"},
			{Spec: "weight", API: "weight"},
			{Spec: "objectTypes", API: "object_types", Class: registry.ClassObjectTypeList},
			// The map shape for the three-state table (NBO-079). A map is compared as a value,
			// so it is ClassValue; only its clearability is what NBO-079 added.
			{Spec: "localContextData", API: "local_context_data"},
			// CascadeOnDelete, so this field can stand in for a containment parent: a
			// descriptor naming a non-cascading ref fails registry validation
			// (docs/decisions/0003-ownership-and-references.md rule 4). Target too, as every
			// shipped reference carries one -- it is what the resolver dispatches on, and what
			// tells owners.go which owner references occupy the containment slot and may
			// therefore be removed when the reference moves (#214).
			{
				Spec: "parentRef", API: "parent", Class: registry.ClassRefOne,
				Target: fakeGVK, CascadeOnDelete: true,
			},
			{Spec: "asnRefs", API: "asns", Class: registry.ClassRefMany},
		},
		NaturalKeys:    []registry.NaturalKey{{Fields: []registry.KeyField{{Filter: "slug", Spec: "slug"}}}},
		UpdateStrategy: registry.UpdatePatch,
		ReadOnly:       []string{"created", "last_updated", "url", "display"},
	}
}

// customFieldableDescriptor is a kind whose NetBox model mixes in CustomFieldsMixin, which
// fakeDescriptor's extras.Tag deliberately does not.
func customFieldableDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.CustomFieldable = true

	return d
}

// parentedDescriptor keys on its parent, like dcim.Region: with the parent declared and
// unresolvable, no candidate applies at all.
func parentedDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.NaturalKeys = []registry.NaturalKey{{Fields: []registry.KeyField{
		{Filter: "parent_id", Spec: "parentRef"},
		{Filter: "name", Spec: "name"},
	}}}
	d.ContainmentRef = "parentRef"

	return d
}

// recreateDescriptor has an identity-bearing field, like dcim.Cable's terminations.
func recreateDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.UpdateStrategy = registry.UpdateRecreate
	d.RecreateOn = []string{"slug"}

	return d
}

// call is one request the engine made, in the order it made them. Recording the payload
// too, because "PATCHed exactly one field" is the assertion that matters.
type call struct {
	method   string
	endpoint string
	id       int
	params   netbox.Params
	payload  netbox.Object
}

// fakeClient is a NetBox that answers from canned state and records what it was asked.
type fakeClient struct {
	calls []call

	get       netbox.Object
	getErr    error
	list      []netbox.Object
	listErr   error
	created   netbox.Object
	createErr error
	patched   netbox.Object
	patchErr  error
	deleteErr error

	// dryRun delegates writes to a real client in DryRun mode, so the suppressed shape the
	// engine has to recognise comes from the code that produces it and not from a copy of
	// its marker.
	dryRun *netbox.Client
}

func (f *fakeClient) GetByID(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "GET", endpoint: endpoint, id: id})

	return f.get, f.getErr
}

// GetOne answers out of the canned list, classifying it with netbox.One rather than with a
// copy of the rule -- a fake that decides for itself when a lookup is ambiguous can
// disagree with the client about the one thing the ambiguity cases assert.
func (f *fakeClient) GetOne(_ context.Context, endpoint string, params netbox.Params) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "GETONE", endpoint: endpoint, params: params})

	if f.listErr != nil {
		return nil, f.listErr
	}

	return netbox.One(endpoint, params, f.list)
}

func (f *fakeClient) Create(ctx context.Context, endpoint string, payload netbox.Object) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "POST", endpoint: endpoint, payload: payload})

	if f.dryRun != nil {
		return f.dryRun.Create(ctx, endpoint, payload)
	}

	return f.created, f.createErr
}

func (f *fakeClient) Patch(ctx context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "PATCH", endpoint: endpoint, id: id, payload: payload})

	if f.dryRun != nil {
		return f.dryRun.Patch(ctx, endpoint, id, payload)
	}

	return f.patched, f.patchErr
}

func (f *fakeClient) Delete(ctx context.Context, endpoint string, id int) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "DELETE", endpoint: endpoint, id: id})

	if f.dryRun != nil {
		return f.dryRun.Delete(ctx, endpoint, id)
	}

	// nil is what a real 204 decodes to, so an Apply delete cannot look suppressed.
	return nil, f.deleteErr
}

// methods is the sequence of requests the engine made, which is what "zero writes" and
// "exactly one POST" are asserted against.
func (f *fakeClient) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}

	return out
}

func (f *fakeClient) lastPayload() netbox.Object {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].payload != nil {
			return f.calls[i].payload
		}
	}

	return nil
}

// dryRunClient is a real client that suppresses every write.
func dryRunClient(t *testing.T) *netbox.Client {
	t.Helper()

	client, err := netbox.New(netbox.Config{URL: "https://netbox.invalid", Mode: netbox.ModeDryRun})
	if err != nil {
		t.Fatalf("building a dry-run client: %v", err)
	}

	return client
}

// fakeEndpoints resolves to one endpoint, or to none at all.
type fakeEndpoints struct {
	endpoint Endpoint
	ready    bool
}

func (f fakeEndpoints) Endpoint(_ context.Context, _, _ string) (Endpoint, bool) {
	return f.endpoint, f.ready
}

// blockingEndpoints is an endpoint provider that does not answer until its context is
// cancelled. It stands in for the only implementation this seam has ever wanted -- one that
// reads Kubernetes objects -- with the informer cache taken away, which is the state a cold
// cache or a direct API read puts it in.
type blockingEndpoints struct {
	// entered is closed once Endpoint has been called, so a test can cancel at the point the
	// provider is actually blocked rather than racing it.
	entered chan struct{}

	// cancelled records that the provider observed the reconcile's cancellation. Before
	// NBO-080 it could not: there was no context to observe.
	cancelled bool
}

func (b *blockingEndpoints) Endpoint(ctx context.Context, _, _ string) (Endpoint, bool) {
	close(b.entered)
	<-ctx.Done()
	b.cancelled = true

	return Endpoint{}, false
}

// fakeDescriptors serves one descriptor for the fake kind.
type fakeDescriptors struct {
	descriptor registry.Descriptor
	registered bool
}

func (f fakeDescriptors) Get(_ schema.GroupVersionKind) (registry.Descriptor, bool) {
	return f.descriptor, f.registered
}

// fakeStatus counts status writes. The count is an assertion in its own right: a reconcile
// that changed nothing must not write.
type fakeStatus struct {
	writes int
	err    error
}

func (f *fakeStatus) UpdateStatus(_ context.Context, _ client.Object) error {
	f.writes++

	return f.err
}

// fakeLiveStatus is the API server's own copy of an object's status, which is what the engine
// asks for rather than trusting the cached one it was handed (issue #252).
//
// The zero value answers with the status the pass is holding, which is the same answer a
// caught-up cache would give: a case that is not about a stale read then behaves exactly as it
// did before the live read existed, and the cases that *are* about one say so by setting
// `status`.
type fakeLiveStatus struct {
	status *netboxv1alpha1.NetBoxObjectStatus
	err    error
	reads  int
}

func (f *fakeLiveStatus) LiveStatus(_ context.Context, obj Object) (netboxv1alpha1.NetBoxObjectStatus, error) {
	f.reads++

	if f.err != nil {
		return netboxv1alpha1.NetBoxObjectStatus{}, f.err
	}

	if f.status != nil {
		return *f.status, nil
	}

	return *obj.NetBoxStatus(), nil
}

// errLiveStatusRead stands in for an API server that would not answer a live status read.
var errLiveStatusRead = errors.New("live status read refused")

// fakeFinalizers records what the finalizer list looked like at each write, which is how a
// test asserts the ordering that matters: the finalizer has to be persisted before the
// first NetBox write and removed only after the last one.
type fakeFinalizers struct {
	writes [][]string
	err    error
}

func (f *fakeFinalizers) UpdateFinalizers(_ context.Context, obj client.Object) error {
	if f.err != nil {
		return f.err
	}

	f.writes = append(f.writes, slices.Clone(obj.GetFinalizers()))

	return nil
}

// errFinalizerWrite stands in for an API-server rejection of a finalizer update.
var errFinalizerWrite = errors.New("finalizer update rejected")

// fakeRecorder collects Events as "Type/Reason".
type fakeRecorder struct {
	events []string
}

func (f *fakeRecorder) Eventf(_ runtime.Object, eventtype, reason, _ string, _ ...any) {
	f.events = append(f.events, eventtype+"/"+reason)
}

// errStatusWrite stands in for an API-server rejection of a status update.
var errStatusWrite = errors.New("status update rejected")

// conditionOf returns one condition, or the zero value when it was never set.
func conditionOf(obj *fakeKind, condType string) metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}
