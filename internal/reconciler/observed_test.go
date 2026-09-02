package reconciler

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// The read-back half: a column NetBox computes for itself, mirrored into a Kind's status
// because it can never be in a spec. `ipam.IPRange.size` is the first, and the engine's whole
// knowledge of it is the ObservedColumns assertion in settle().
//
// The fake Kind here mirrors one, so the assertions are about the engine's half and not about
// a range's.

var observingGVK = schema.GroupVersionKind{
	Group: netboxv1alpha1.GroupName, Version: "v1alpha1", Kind: "NetBoxObservingFake",
}

// observingKind is a fakeKind that mirrors a computed column, which is the whole of what a
// Kind has to do to get the read-back.
type observingKind struct {
	fakeKind

	size  int64
	calls int
}

func (o *observingKind) ObserveColumns(live map[string]any) bool {
	o.calls++

	size, ok := live["size"].(float64)
	if !ok || int64(size) == o.size {
		return false
	}

	o.size = int64(size)

	return true
}

func (o *observingKind) DeepCopyObject() runtime.Object {
	out := &observingKind{size: o.size, calls: o.calls}

	copied, ok := o.fakeKind.DeepCopyObject().(*fakeKind)
	if !ok {
		return nil
	}
	out.fakeKind = *copied

	return out
}

var _ netboxv1alpha1.ObservedColumns = (*observingKind)(nil)

func observingObject() *observingKind {
	obj := &observingKind{fakeKind: *fakeObject()}
	obj.SetGroupVersionKind(observingGVK)

	return obj
}

func observingScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := fakeScheme(t)
	scheme.AddKnownTypeWithName(observingGVK, &observingKind{})

	return scheme
}

// observingEngine wires an engine around one client and one status writer, for the cases
// below.
func observingEngine(t *testing.T, client *fakeClient, status *fakeStatus) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      status,
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      observingScheme(t),
	}
}

// TestObserveRunsOnBothSettlingPaths is why the hook is in settle() and not next to the
// create: a computed column is answered by the POST that created the object *and* by the GET
// of every quiet pass afterwards, and a reader who deletes the value out of the status has to
// get it back without a write to NetBox.
func TestObserveRunsOnBothSettlingPaths(t *testing.T) {
	live := liveTag(7)
	live["size"] = float64(64)

	client := &fakeClient{created: live, get: live}
	obj := observingObject()
	engine := observingEngine(t, client, &fakeStatus{})

	// Pass one creates and settles on the POST's response; pass two finds no drift and
	// settles on the GET's.
	for i := range 2 {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}

		if obj.size != 64 {
			t.Errorf("after pass %d the mirrored size is %d, want 64", i, obj.size)
		}

		// Blanked between passes, so the second assertion is about the second pass and not
		// about what the first one left behind.
		obj.size = 0
	}

	if obj.calls != 2 {
		t.Errorf("ObserveColumns was called %d times over two passes, want 2: the write path "+
			"and the no-drift path both settle", obj.calls)
	}
}

// TestObserveLeavesAStatusAloneWhenTheColumnIsAbsent is the guard the printer column depends
// on. A DryRun write and a 204 both arrive as objects that never mentioned the column, and a
// SIZE that blinked to empty on every dry run would be worse than one reported a pass late.
func TestObserveLeavesAStatusAloneWhenTheColumnIsAbsent(t *testing.T) {
	withSize := liveTag(7)
	withSize["size"] = float64(64)

	client := &fakeClient{created: withSize, get: withSize}
	obj := observingObject()
	engine := observingEngine(t, client, &fakeStatus{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	// The same object, answered by a response that carries no `size` at all.
	client.get = liveTag(7)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.size != 64 {
		t.Errorf("the mirrored size is %d after a response that omitted the column, want the "+
			"64 the previous pass learned", obj.size)
	}
}

// TestObserveIsSkippedByAKindThatMirrorsNothing is the other half of the capability: the
// engine has no branch on Kind, so a plain fakeKind reconciles through the same settle()
// without implementing anything.
func TestObserveIsSkippedByAKindThatMirrorsNothing(t *testing.T) {
	if _, mirrors := any(&fakeKind{}).(netboxv1alpha1.ObservedColumns); mirrors {
		t.Fatal("the plain fake already mirrors columns, so this proves nothing")
	}

	// A nil object is the other arm: settle() is reachable with no live object in the DryRun
	// shapes, and the entry point has to be safe there rather than each implementation.
	netboxv1alpha1.ObserveColumns(observingObject(), nil)

	client := &fakeClient{created: liveTag(7), get: liveTag(7)}
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), fakeObject()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
}

// TestObserveForcesTheStatusWriteAQuietPassWouldSkip is the half that makes the read-back
// durable rather than notional.
//
// finish() skips the status write when this pass's conclusions match the stored status, which
// is what keeps a resync from churning the resourceVersion of every object in the cluster --
// and that comparison is over the shared envelope, which a mirrored column is not part of. So
// a range that already existed, already had its id, and drifts not at all is exactly the case
// where `status.size` would be learned and thrown away, once per resync, forever.
func TestObserveForcesTheStatusWriteAQuietPassWouldSkip(t *testing.T) {
	live := liveTag(7)
	live["size"] = float64(64)

	client := &fakeClient{created: live, get: live}
	status := &fakeStatus{}
	obj := observingObject()
	engine := observingEngine(t, client, status)

	// Three passes to reach the quiet state: the create, the one where Synced settles from
	// DriftCorrected to NoDrift, and then nothing.
	for i := range 3 {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}
	}

	settled := status.writes

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if status.writes != settled {
		t.Fatalf("a quiet pass wrote the status %d times, want 0: the fixture is not quiet, so "+
			"the assertion below would pass for the wrong reason", status.writes-settled)
	}

	// The upgrade case: the object is otherwise settled and its status has never carried the
	// column, because the operator that wrote it did not know about it.
	obj.size = 0

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.size != 64 {
		t.Errorf("the mirrored size is %d, want 64", obj.size)
	}

	if status.writes != settled+1 {
		t.Errorf("status writes after learning a column = %d, want %d: a value learned and not "+
			"written is a value relearned and rethrown away on every resync",
			status.writes-settled, 1)
	}

	// And back to quiet: the column is unchanged now, so the guard is back in force.
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if status.writes != settled+1 {
		t.Errorf("status writes over the pass after = %d, want 0: an unchanged column must not "+
			"force a write of its own", status.writes-settled-1)
	}
}
