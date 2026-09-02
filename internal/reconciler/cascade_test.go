package reconciler

import (
	"context"
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// errReferrers stands in for the API server refusing the referrer query.
var errReferrers = errors.New("listing referrers failed")

// fakeReferrers is the reverse index as far as a cascading delete is concerned: a canned
// answer, and a record of what was asked.
type fakeReferrers struct {
	found []client.Object
	err   error
	asked []string
}

func (f *fakeReferrers) Referring(_ context.Context, obj client.Object) ([]client.Object, error) {
	f.asked = append(f.asked, obj.GetNamespace()+"/"+obj.GetName())

	if f.err != nil {
		return nil, f.err
	}

	return f.found, nil
}

// referringCR is a CR of some other kind pointing at the object being deleted. Unstructured
// because what the cascade does with it -- name it, delete it -- needs no Go type, which is
// also true of the ~120 real kinds it has to work for.
func referringCR(kind, name string, deleting bool) client.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(netboxv1alpha1.GroupVersion.WithKind(kind))
	obj.SetNamespace("team-a")
	obj.SetName(name)

	if deleting {
		now := metav1.Now()
		obj.SetDeletionTimestamp(&now)
	}

	return obj
}

// cascadingObject is a CR being deleted whose NetBox object NetBox refuses to remove, with
// the annotation that says to take the CRs in the way with it.
func cascadingObject() *fakeKind {
	obj := deletingObject()
	obj.Annotations = map[string]string{netboxv1alpha1.CascadeDeleteAnnotation: "true"}

	return obj
}

// cascadeEngine wires an engine whose DELETE is refused and whose referrers are canned.
func cascadeEngine(
	t *testing.T, refs *fakeReferrers, children ChildWriter, events *fakeRecorder,
) (*Engine, *fakeClient) {
	t.Helper()

	client := &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Children:    children,
		Referrers:   refs,
		Events:      events,
		Scheme:      fakeScheme(t),
	}, client
}

// TestCascadeDeletesTheCRsInTheWay is #304's headline: a delete NetBox refuses because
// something still points at the object takes those somethings with it, so tearing down a
// graph is one `kubectl delete` rather than a manual topological sort read out of NetBox's
// error message.
func TestCascadeDeletesTheCRsInTheWay(t *testing.T) {
	refs := &fakeReferrers{found: []client.Object{
		referringCR("NetBoxVLAN", "vlan-1301", false),
		referringCR("NetBoxPrefix", "prefix-10-18", false),
	}}
	children := newFakeChildren()
	children.plant(netboxv1alpha1.GroupVersion.WithKind("NetBoxVLAN"), "vlan-1301", nil, nil)
	children.plant(netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefix"), "prefix-10-18", nil, nil)

	events := &fakeRecorder{}
	engine, _ := cascadeEngine(t, refs, children, events)

	obj := cascadingObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := []string{"NetBoxPrefix/prefix-10-18", "NetBoxVLAN/vlan-1301"}
	got := slices.Clone(children.deleted)
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Errorf("deleted CRs = %v, want %v", got, want)
	}

	// The finalizer stays on. The NetBox object is still there -- the referrers' own
	// finalizers have not run yet -- so releasing here would orphan exactly the object this
	// cascade exists to be able to delete.
	if !slices.Contains(obj.GetFinalizers(), netboxv1alpha1.Finalizer) {
		t.Error("the finalizer came off while the cascade was still in flight")
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionDeleting).Reason; got != netboxv1alpha1.ReasonCascading {
		t.Errorf("Deleting reason = %q, want %q", got, netboxv1alpha1.ReasonCascading)
	}

	// A Warning, because deleting Kubernetes objects the user did not name is not something
	// to find out about from a log line.
	if !slices.Equal(events.events, []string{"Warning/CascadeDeleted"}) {
		t.Errorf("events = %v, want Warning/CascadeDeleted", events.events)
	}
}

// TestCascadeNeedsTheAnnotation is the default, and the one that must not regress: without
// the annotation a refused delete is reported and nothing else is touched.
func TestCascadeNeedsTheAnnotation(t *testing.T) {
	refs := &fakeReferrers{found: []client.Object{referringCR("NetBoxVLAN", "vlan-1301", false)}}
	children := newFakeChildren()
	children.plant(netboxv1alpha1.GroupVersion.WithKind("NetBoxVLAN"), "vlan-1301", nil, nil)

	events := &fakeRecorder{}
	engine, _ := cascadeEngine(t, refs, children, events)

	obj := deletingObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(children.deleted) != 0 {
		t.Errorf("deleted %v without the annotation; a refused delete must touch nothing", children.deleted)
	}

	if len(refs.asked) != 0 {
		t.Errorf("looked for referrers %v without the annotation; the query is the cascade", refs.asked)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionDeleting).Reason; got != netboxv1alpha1.ReasonProtected {
		t.Errorf("Deleting reason = %q, want %q", got, netboxv1alpha1.ReasonProtected)
	}
}

// TestCascadeWithNothingToDeleteReportsProtected is the case that keeps the annotation from
// meaning "keep going until something gives": the blocker is an object this cluster does not
// manage, there is no CR to delete, and the honest answer is the one it always was.
func TestCascadeWithNothingToDeleteReportsProtected(t *testing.T) {
	refs := &fakeReferrers{}
	events := &fakeRecorder{}
	engine, _ := cascadeEngine(t, refs, newFakeChildren(), events)

	obj := cascadingObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionDeleting).Reason; got != netboxv1alpha1.ReasonProtected {
		t.Errorf("Deleting reason = %q, want %q: nothing was in the way that this cluster owns",
			got, netboxv1alpha1.ReasonProtected)
	}

	if len(events.events) != 0 {
		t.Errorf("events = %v, want none: nothing was deleted", events.events)
	}
}

// TestCascadeDoesNotRedeleteWhatIsAlreadyGoing keeps the retry loop quiet. This path runs
// again on every backed-off attempt for as long as the referrers take to go, and re-issuing a
// DELETE per referrer per attempt would be API calls for a state that resolves itself -- and
// an Event each time, which is how the Events somebody needed get evicted.
func TestCascadeDoesNotRedeleteWhatIsAlreadyGoing(t *testing.T) {
	refs := &fakeReferrers{found: []client.Object{referringCR("NetBoxVLAN", "vlan-1301", true)}}
	children := newFakeChildren()
	children.plant(netboxv1alpha1.GroupVersion.WithKind("NetBoxVLAN"), "vlan-1301", nil, nil)

	events := &fakeRecorder{}
	engine, _ := cascadeEngine(t, refs, children, events)

	obj := cascadingObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(children.deleted) != 0 {
		t.Errorf("deleted %v, want nothing: it already carries a deletion timestamp", children.deleted)
	}

	// Still Cascading rather than Protected: something *is* happening, and the two states
	// need different things from whoever is reading the condition.
	if got := conditionOf(obj, netboxv1alpha1.ConditionDeleting).Reason; got != netboxv1alpha1.ReasonCascading {
		t.Errorf("Deleting reason = %q, want %q", got, netboxv1alpha1.ReasonCascading)
	}

	if len(events.events) != 0 {
		t.Errorf("events = %v, want none: this pass deleted nothing", events.events)
	}
}

// TestCascadeFailureIsAnError checks the direction a partial answer must not go. A failed
// referrer query that returned "nothing refers to this" would make the caller conclude the
// blocker is somebody else's and give up, on a deletion that is actually recoverable.
func TestCascadeFailureIsAnError(t *testing.T) {
	refs := &fakeReferrers{err: errReferrers}
	engine, _ := cascadeEngine(t, refs, newFakeChildren(), &fakeRecorder{})

	obj := cascadingObject()

	_, err := engine.Reconcile(context.Background(), obj)
	if !errors.Is(err, errReferrers) {
		t.Fatalf("Reconcile() = %v, want the referrer query's error", err)
	}

	if !slices.Contains(obj.GetFinalizers(), netboxv1alpha1.Finalizer) {
		t.Error("the finalizer came off after a failed cascade")
	}
}
