package controller

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// regionKind points the shared stub at dcim.Region.
//
// Keyed by `name` and not by `slug`, unlike the other two kinds: dcim.Region's identity is
// `(parent, name)` with a second candidate on `name` alone
// (docs/netbox-schema.md -> dcim.Region.meta.constraints), so `name` is the filter the
// engine actually sends and therefore the one the stub has to answer on.
var regionKind = stubKind{endpoint: "dcim/regions", key: "name"}

// noResync is an endpoint resync long enough that nothing in a test can be an accident of
// polling. Every convergence assertion below is therefore an assertion about the watch: with
// the resync an hour away, an object that becomes Ready did so because an event woke it.
const noResync = time.Hour

// makeRegion applies a NetBoxRegion whose slug and NetBox name are both its CR name, and
// removes it afterwards so the finalizer does not outlive the stub it needs to come off.
func makeRegion(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxRegion)) *netboxv1alpha1.NetBoxRegion {
	t.Helper()

	region := &netboxv1alpha1.NetBoxRegion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRegionSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			Slug:             name,
		},
	}
	if mutate != nil {
		mutate(region)
	}
	if err := k8sClient.Create(context.Background(), region); err != nil {
		t.Fatalf("creating region %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), region) })

	return region
}

func fetchRegion(ns, name string) *netboxv1alpha1.NetBoxRegion {
	region := &netboxv1alpha1.NetBoxRegion{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, region); err != nil {
		return nil
	}

	return region
}

func regionIsReady(ns, name string) bool {
	region := fetchRegion(ns, name)
	if region == nil {
		return false
	}
	for _, c := range region.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// refsReason is the reason on a region's RefsResolved condition, or "" when it has not
// reported one yet. It is the condition every test here waits on: it names what the object
// is waiting for, one reference at a time.
func refsReason(ns, name string) string {
	region := fetchRegion(ns, name)
	if region == nil {
		return ""
	}
	for _, c := range region.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionRefsResolved {
			return c.Reason
		}
	}

	return ""
}

// endpointWithoutResync is the endpoint every test here uses: the same one every other test
// in this package uses, with the resync an hour out.
func endpointWithoutResync(t *testing.T, ns, target string) {
	t.Helper()
	readyEndpointWith(t, ns, target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.ResyncPeriod = metav1.Duration{Duration: noResync}
	})
}

// TestReferrerConvergesWhenItsTargetArrives is the whole ticket in one test: the reverse
// order apply.
//
// The child region is applied first, so its `parentRef` cannot resolve and no natural-key
// candidate is applicable -- the engine refuses to create it and waits. The parent is applied
// second, and the child has to converge off the parent's event rather than off a resync. The
// endpoint's resyncPeriod is an hour, so there is no timer that could produce a pass: a
// passing test cannot be an accident of polling, and the elapsed time says how far from the
// resync the convergence was.
func TestReferrerConvergesWhenItsTargetArrives(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, regionKind)
	endpointWithoutResync(t, ns, target)

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
	})

	// Blocked first, and proven blocked: without this the test could pass on a child that
	// simply had not been reconciled yet when the parent appeared.
	eventually(t, "the child to report that it is waiting for its parent", func() bool {
		return refsReason(ns, "child") == netboxv1alpha1.ReasonRefNotFound
	})
	if regionIsReady(ns, "child") {
		t.Fatal("the child is Ready with no parent; its parentRef is part of its identity")
	}

	started := time.Now()
	makeRegion(t, ns, "parent", nil)

	eventually(t, "the child to become Ready once its parent exists", func() bool {
		return regionIsReady(ns, "child")
	})

	elapsed := time.Since(started)
	if elapsed > 15*time.Second {
		t.Errorf("the child took %s to converge; the endpoint's resync is %s, so this is the "+
			"watch being slow rather than absent", elapsed, noResync)
	}
	t.Logf("converged in %s, against a resyncPeriod of %s", elapsed, noResync)

	child := fetchRegion(ns, "child")
	if child.Status.ID == 0 {
		t.Error("the child is Ready with no status.id")
	}

	// The reference reached NetBox rather than merely being reported as resolved: a watch
	// that woke the object without the parent id landing on the payload would look identical
	// in the conditions.
	live := stub.get(child.Status.ID)
	if live["parent"] != float64(fetchRegion(ns, "parent").Status.ID) {
		t.Errorf("netbox holds parent=%v, want the parent's own id %d",
			live["parent"], fetchRegion(ns, "parent").Status.ID)
	}
}

// TestCrossNamespaceReferrerConvergesWhenItsTargetArrives is the same reverse-order apply
// across a namespace boundary, which is the *common* path rather than an edge case: every
// kind is namespaced, so a team namespace pointing at a shared catalogue is the ordinary
// shape (docs/decisions/0002-crd-scoping.md).
//
// The map function therefore lists referrers across every namespace, and this is the test
// that fails if it is ever scoped to the target's own.
func TestCrossNamespaceReferrerConvergesWhenItsTargetArrives(t *testing.T) {
	catalogue := newNamespaceSuffixed(t, "-c")
	team := newNamespaceSuffixed(t, "-t")
	stub, target := newNetBoxStub(t, regionKind)
	endpointWithoutResync(t, catalogue, target)
	endpointWithoutResync(t, team, target)

	// The grant first, so this test is about the namespace boundary and not about the grant.
	makeGrant(t, catalogue, "readable-by-all")

	makeRegion(t, team, "xchild", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "xparent", Namespace: catalogue}
	})
	eventually(t, "the child to report that its parent does not exist", func() bool {
		return refsReason(team, "xchild") == netboxv1alpha1.ReasonRefNotFound
	})

	started := time.Now()
	makeRegion(t, catalogue, "xparent", nil)

	eventually(t, "the cross-namespace child to become Ready", func() bool {
		return regionIsReady(team, "xchild")
	})
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("the cross-namespace child took %s to converge, against a resyncPeriod of %s",
			elapsed, noResync)
	}

	if got := stub.countByKey("xchild"); got != 1 {
		t.Errorf("netbox holds %d regions named xchild, want 1", got)
	}
}

// TestAGrantWakesADeniedReferrer is the piece NBO-014 deferred to this ticket.
//
// A denied reference names the exact object to create in its condition message. If creating
// it does nothing until the next resync -- ten minutes at the default, an hour here -- the
// operator has given an instruction that appears not to work, which is worse than not
// offering the remedy at all.
func TestAGrantWakesADeniedReferrer(t *testing.T) {
	catalogue := newNamespaceSuffixed(t, "-c")
	team := newNamespaceSuffixed(t, "-t")
	_, target := newNetBoxStub(t, regionKind)
	endpointWithoutResync(t, catalogue, target)
	endpointWithoutResync(t, team, target)

	makeRegion(t, catalogue, "gparent", nil)
	eventually(t, "the catalogue parent to be Ready", func() bool {
		return regionIsReady(catalogue, "gparent")
	})

	makeRegion(t, team, "gchild", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "gparent", Namespace: catalogue}
	})
	eventually(t, "the child to be denied", func() bool {
		return refsReason(team, "gchild") == netboxv1alpha1.ReasonRefDenied
	})

	started := time.Now()
	makeGrant(t, catalogue, "readable-by-all")

	eventually(t, "the grant to unblock the child", func() bool { return regionIsReady(team, "gchild") })
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("the grant took %s to take effect, against a resyncPeriod of %s", elapsed, noResync)
	}
}

// TestDeletingATargetTellsItsReferrers covers the other direction. A referrer whose target
// went away has to hear about it: it stops resolving the reference and reports why, rather
// than standing on a stale id until a resync it may not have.
func TestDeletingATargetTellsItsReferrers(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, regionKind)
	endpointWithoutResync(t, ns, target)

	parent := makeRegion(t, ns, "dparent", nil)
	makeRegion(t, ns, "dchild", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "dparent"}
	})
	eventually(t, "the child to be Ready", func() bool { return regionIsReady(ns, "dchild") })

	started := time.Now()
	if err := k8sClient.Delete(context.Background(), parent); err != nil {
		t.Fatalf("deleting the parent: %v", err)
	}

	eventually(t, "the child to report its parent is gone", func() bool {
		return refsReason(ns, "dchild") == netboxv1alpha1.ReasonRefNotFound
	})
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("the child took %s to notice, against a resyncPeriod of %s", elapsed, noResync)
	}
}

// TestAnUnrelatedTargetUpdateEnqueuesNothing is the cost half of the ticket, and the one that
// is asserted by counting rather than by timing.
//
// Every object in the cluster writes its status as it reconciles. If a target update that
// changes only `status.lastSyncTime` fanned out to that target's referrers, the operator
// would enqueue one reconcile per reference per resync per object, forever -- at ~120 kinds,
// a storm it inflicts on itself. The paired assertion is what makes the zero meaningful: the
// same test then changes `status.id` and requires the counter to move, so a zero above cannot
// be a metric that was never wired up.
func TestAnUnrelatedTargetUpdateEnqueuesNothing(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, regionKind)
	endpointWithoutResync(t, ns, target)

	makeRegion(t, ns, "sparent", nil)
	makeRegion(t, ns, "schild", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "sparent"}
	})
	eventually(t, "the child to be Ready", func() bool { return regionIsReady(ns, "schild") })

	// Nothing else may be reconciling while the counter is being watched, and with the
	// resync an hour out the only remaining traffic is this test's own settling.
	waitResyncs(t, 2)
	before := refEnqueues(t)

	writeRegionStatus(t, ns, "sparent", func(s *netboxv1alpha1.NetBoxObjectStatus) {
		now := metav1.NewTime(time.Now())
		s.LastSyncTime = &now
	})
	waitResyncs(t, 2)

	if got := refEnqueues(t) - before; got != 0 {
		t.Errorf("a lastSyncTime-only update enqueued %v referrers, want 0: every object in "+
			"the cluster writes that field, so this is one reconcile per reference per resync", got)
	}

	// The positive control. Without it, a counter that is never incremented at all would
	// pass the assertion above.
	writeRegionStatus(t, ns, "sparent", func(s *netboxv1alpha1.NetBoxObjectStatus) { s.ID += 1000 })

	eventually(t, "an id change to enqueue the referrer", func() bool { return refEnqueues(t) > before })
}

// refEnqueues is the enqueue counter for the one pair these tests produce: a NetBoxRegion
// woken by another NetBoxRegion.
func refEnqueues(t *testing.T) float64 {
	t.Helper()

	return testutil.ToFloat64(metrics.RefEnqueueTotal.WithLabelValues("NetBoxRegion", "NetBoxRegion"))
}

// writeRegionStatus edits one region's status directly, which is how a test produces an
// update the operator itself would produce without waiting for the conditions that cause it.
func writeRegionStatus(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxObjectStatus)) {
	t.Helper()

	region := &netboxv1alpha1.NetBoxRegion{}
	key := client.ObjectKey{Namespace: ns, Name: name}
	if err := apiClient.Get(context.Background(), key, region); err != nil {
		t.Fatalf("reading region %s: %v", key, err)
	}

	mutate(&region.Status)

	if err := apiClient.Status().Update(context.Background(), region); err != nil {
		t.Fatalf("updating the status of region %s: %v", key, err)
	}
}

// makeGrant admits every namespace to this one, which is the shape ADR-0002 asks for.
func makeGrant(t *testing.T, ns, name string) {
	t.Helper()

	grant := &netboxv1alpha1.NetBoxRefGrant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRefGrantSpec{
			From: []netboxv1alpha1.RefGrantFrom{{Namespaces: netboxv1alpha1.NamespacesAll}},
		},
	}
	if err := k8sClient.Create(context.Background(), grant); err != nil {
		t.Fatalf("creating grant %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), grant) })
}

// TestEnqueueReferrersListsEveryNamespaceAndNeverTheTarget drives the map function over the
// production index functions, against a fake client rather than a cluster.
//
// The index is built by resolver.AddIndexes itself, so this exercises the same key encoding
// the manager registers rather than a copy of it written for the test.
func TestEnqueueReferrersListsEveryNamespaceAndNeverTheTarget(t *testing.T) {
	regionGVK := netboxv1alpha1.RegionRef{}.TargetGVK()
	d, ok := registry.Get(regionGVK)
	if !ok {
		t.Fatal("no descriptor for NetBoxRegion")
	}

	c := indexedClient(t,
		// Two teams referring into the catalogue, which is the shape the watch exists for.
		refRegion("team-a", "a", "emea", "catalogue"),
		refRegion("team-b", "b", "emea", "catalogue"),
		// Same name, wrong namespace: the key carries the namespace, so this must not match.
		refRegion("team-c", "c", "emea", "elsewhere"),
		// A reference to another object entirely.
		refRegion("team-a", "d", "apac", "catalogue"),
		// The target's own self-reference. A self-referential Kind is the common case here
		// (dcim.Region.parentRef), and enqueuing the target from its own event is work For()
		// has already done.
		refRegion("catalogue", "emea", "emea", "catalogue"),
	)

	target := &netboxv1alpha1.NetBoxRegion{
		ObjectMeta: metav1.ObjectMeta{Namespace: "catalogue", Name: "emea"},
	}

	got := EnqueueReferrers(c, scheme, d, regionGVK)(context.Background(), target)

	want := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "a"}},
		{NamespacedName: types.NamespacedName{Namespace: "team-b", Name: "b"}},
	}
	if !sameRequests(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// TestARepointedReferenceStopsMatchingTheOldTarget is the stale-edge case, and the reason
// the index is a field index rather than a map the operator maintains.
//
// The index is recomputed from the object on every write, so editing a reference is all it
// takes: there is no bookkeeping to get wrong and no old key to clean up.
func TestARepointedReferenceStopsMatchingTheOldTarget(t *testing.T) {
	regionGVK := netboxv1alpha1.RegionRef{}.TargetGVK()
	d, ok := registry.Get(regionGVK)
	if !ok {
		t.Fatal("no descriptor for NetBoxRegion")
	}

	child := refRegion("team-a", "a", "emea", "catalogue")
	c := indexedClient(t, child)

	emea := &netboxv1alpha1.NetBoxRegion{
		ObjectMeta: metav1.ObjectMeta{Namespace: "catalogue", Name: "emea"},
	}
	enqueue := EnqueueReferrers(c, scheme, d, regionGVK)

	if got := enqueue(context.Background(), emea); len(got) != 1 {
		t.Fatalf("requests = %v, want the one referrer before the edit", got)
	}

	child.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "apac", Namespace: "catalogue"}
	if err := c.Update(context.Background(), child); err != nil {
		t.Fatalf("repointing the reference: %v", err)
	}

	if got := enqueue(context.Background(), emea); len(got) != 0 {
		t.Errorf("requests = %v, want none: the reference points somewhere else now", got)
	}
}

// TestEnqueueGrantedReferrersFindsWhatAGrantUnblocks covers the grant half: the query is by
// namespace, and a reference that never leaves its own namespace is not something a grant can
// unblock.
func TestEnqueueGrantedReferrersFindsWhatAGrantUnblocks(t *testing.T) {
	d, ok := registry.Get(netboxv1alpha1.RegionRef{}.TargetGVK())
	if !ok {
		t.Fatal("no descriptor for NetBoxRegion")
	}

	c := indexedClient(t,
		refRegion("team-a", "a", "emea", "catalogue"),
		refRegion("team-b", "b", "apac", "catalogue"),
		refRegion("team-c", "c", "emea", "elsewhere"),
		// Inside the granted namespace, referring within it: free, never authorised against
		// anything, and so never woken by a grant.
		refRegion("catalogue", "local", "emea", ""),
	)

	grant := &netboxv1alpha1.NetBoxRefGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "catalogue", Name: "readable-by-all"},
	}

	got := EnqueueGrantedReferrers(c, scheme, d)(context.Background(), grant)

	want := []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "a"}},
		{NamespacedName: types.NamespacedName{Namespace: "team-b", Name: "b"}},
	}
	if !sameRequests(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// indexedClient is a fake client carrying the operator's real reference indexes.
func indexedClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range objects {
		builder = builder.WithObjects(obj)
	}

	// The production registration path, so the keys under test are the keys the manager
	// stores rather than a second implementation of the same encoding.
	indexes := &builderIndexer{builder: builder}
	if err := resolver.AddIndexes(context.Background(), indexes, scheme, registry.List()); err != nil {
		t.Fatalf("adding the reference indexes: %v", err)
	}

	return indexes.builder.Build()
}

// builderIndexer feeds resolver.AddIndexes into a fake client builder.
type builderIndexer struct{ builder *fake.ClientBuilder }

func (b *builderIndexer) IndexField(
	_ context.Context, obj client.Object, field string, extract client.IndexerFunc,
) error {
	b.builder = b.builder.WithIndex(obj, field, extract)

	return nil
}

// refRegion is one referring region: its own namespace and name, and the parent it points at.
// An empty target namespace means its own.
func refRegion(ns, name, parent, parentNamespace string) *netboxv1alpha1.NetBoxRegion {
	return &netboxv1alpha1.NetBoxRegion{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: netboxv1alpha1.NetBoxRegionSpec{
			Name: name, Slug: name,
			ParentRef: &netboxv1alpha1.RegionRef{Name: parent, Namespace: parentNamespace},
		},
	}
}

// sameRequests compares two request lists ignoring order: the order a List returns objects in
// is not something the map function promises.
func sameRequests(got, want []reconcile.Request) bool {
	if len(got) != len(want) {
		return false
	}

	for _, request := range want {
		found := false
		for _, candidate := range got {
			if candidate == request {
				found = true

				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
