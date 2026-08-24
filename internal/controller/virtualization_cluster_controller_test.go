package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// clusterKind points the shared stub at virtualization.Cluster. Keyed on `name`, because a
// cluster has no slug: its identity is `(group, name)` and the stub honours the one filter it
// is parameterised with.
var clusterKind = stubKind{endpoint: "virtualization/clusters", key: "name"}

// clusterSitedColumns are the keys that must never appear in a request body sent to
// `virtualization/clusters`, and the reason differs between the two halves.
//
// `site` and `site_id` are not columns on virtualization.Cluster since NetBox 4.2, and NetBox's
// ClusterSerializer has no such member -- DRF drops an unknown key rather than rejecting it, so
// a write containing one returns 201, creates an *unscoped* cluster, and never drifts. That is
// netbox-populator's ../reconcile.go:270, and this list is its regression test.
//
// The four `_`-prefixed columns are the scope caches NetBox maintains from
// `(scope_type, scope_id)`. An attempt to set one is dropped exactly like `site`, so the next
// read finds it unchanged and the operator PATCHes it again on every resync, forever.
var clusterSitedColumns = []string{
	"site", "site_id",
	"_site", "_region", "_site_group", "_location",
}

// newClusterNetBoxStub is the cluster stub fronted by a handler that also answers the reads an
// id-mode reference is verified against.
//
// A cluster points at four other endpoints -- cluster types, cluster groups, tenants, and the
// four dcim targets of the scope union -- and the shared stub serves one endpoint by design:
// it is parameterised by the kind under test, not by that kind's references. This fronts it
// with the smallest thing that makes an id-mode ref resolvable, and deliberately cannot serve a
// *write*, so a test that accidentally started managing a Site or a ClusterType through this
// path would fail rather than pass quietly.
func newClusterNetBoxStub(t *testing.T) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, clusterKind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := referencedObjectID(r, clusterKind.endpoint); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

// referencedObjectID reports the primary key of a `GET /api/<app>/<collection>/<id>/` for any
// collection other than the one under test, and false for anything else -- including every
// request to the kind's own endpoint, which the real stub has to answer.
func referencedObjectID(r *http.Request, own string) (int64, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}

	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if strings.HasPrefix(path, own+"/") {
		return 0, false
	}

	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		return 0, false
	}

	id, err := strconv.ParseInt(parts[2], 10, 64)

	return id, err == nil
}

// makeCluster applies a NetBoxCluster and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off.
//
// `typeRef` is in `id` mode and set by default, because NetBox's column is `REQ` and the API
// server rejects the object without it. Id mode costs nothing here: what these tests assert is
// what reaches `virtualization/clusters`, and an id-mode ref renders through the same code a
// name-mode one ends up in.
func makeCluster(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxCluster)) {
	t.Helper()

	cluster := &netboxv1alpha1.NetBoxCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxClusterSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			TypeRef:          netboxv1alpha1.ClusterTypeRef{ID: idOf(3)},
			Status:           netboxv1alpha1.ClusterStatusActive,
		},
	}
	if mutate != nil {
		mutate(cluster)
	}
	if err := k8sClient.Create(context.Background(), cluster); err != nil {
		t.Fatalf("creating cluster %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), cluster) })
}

func fetchCluster(ns, name string) *netboxv1alpha1.NetBoxCluster {
	cluster := &netboxv1alpha1.NetBoxCluster{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, cluster); err != nil {
		return nil
	}

	return cluster
}

func clusterIsReady(ns, name string) bool {
	cluster := fetchCluster(ns, name)
	if cluster == nil {
		return false
	}
	for _, c := range cluster.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// assertNoClusterSited is the acceptance criterion asserted against what was actually sent,
// across every request the engine made, rather than by reading the descriptor.
func assertNoClusterSited(t *testing.T, stub *netboxStubServer) {
	t.Helper()

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range clusterSitedColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: %v", i, write.Method, column, write.Payload)
			}
		}
	}
}

// TestClusterIsScopedNeverSited is the populator regression, end to end. A site-scoped cluster
// POSTs the polymorphic pair, a read-back of NetBox shows the scope actually set, and no
// request body anywhere mentions `site` or a scope cache.
func TestClusterIsScopedNeverSited(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newClusterNetBoxStub(t)
	readyEndpoint(t, ns, target)

	siteID := int64(41)
	makeCluster(t, ns, "proxmox-ams", func(c *netboxv1alpha1.NetBoxCluster) {
		c.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: &siteID}}
		c.Spec.GroupRef = &netboxv1alpha1.ClusterGroupRef{ID: idOf(5)}
		c.Spec.TenantRef = &netboxv1alpha1.TenantRef{ID: idOf(7)}
		c.Spec.Description = "Proxmox, Amsterdam"
	})

	eventually(t, "the cluster to be Ready", func() bool { return clusterIsReady(ns, "proxmox-ams") })

	cluster := fetchCluster(ns, "proxmox-ams")
	if cluster.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready cluster")
	}

	live := stub.get(cluster.Status.ID)
	if live["scope_type"] != "dcim.site" {
		t.Errorf("scope_type = %v, want dcim.site -- the cluster is unscoped, which is the populator bug",
			live["scope_type"])
	}
	if live["scope_id"] != float64(siteID) {
		t.Errorf("scope_id = %v, want %d", live["scope_id"], siteID)
	}

	// The three ordinary foreign keys are written under NetBox's own names. `typeRef` sent as
	// `typeRef` would be dropped and the create would 400 on a missing required field, which
	// is the loud failure; `groupRef` sent as `groupRef` would be dropped silently, which is
	// not, and is why the field map is a table rather than a naming convention.
	for column, want := range map[string]any{
		"type": float64(3), "group": float64(5), "tenant": float64(7),
	} {
		if live[column] != want {
			t.Errorf("%s = %v, want %v", column, live[column], want)
		}
	}

	assertNoClusterSited(t, stub)
}

// TestClusterScopeMovesAsOnePair is the acceptance criterion that a half-changed scope is never
// sent. Moving a cluster from a Region to a Site is one change to one reference, so it must be
// one PATCH carrying both columns -- not two independent diffs a partial write could apply
// inconsistently, leaving `scope_type: dcim.site` against a Region's id.
func TestClusterScopeMovesAsOnePair(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newClusterNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makeCluster(t, ns, "moving", func(c *netboxv1alpha1.NetBoxCluster) {
		c.Spec.Scope = &netboxv1alpha1.ScopeRef{RegionRef: &netboxv1alpha1.RegionRef{ID: idOf(11)}}
	})
	eventually(t, "the cluster to be Ready", func() bool { return clusterIsReady(ns, "moving") })

	writesBefore := len(stub.recorded())

	cluster := fetchCluster(ns, "moving")
	cluster.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: idOf(41)}}
	if err := k8sClient.Update(context.Background(), cluster); err != nil {
		t.Fatalf("moving the scope: %v", err)
	}

	eventually(t, "the move to reach NetBox", func() bool {
		return stub.get(cluster.Status.ID)["scope_type"] == "dcim.site"
	})

	patches := stub.recorded()[writesBefore:]
	if len(patches) != 1 {
		t.Fatalf("the move took %d writes, want exactly 1: %+v", len(patches), patches)
	}

	for _, column := range []string{"scope_type", "scope_id"} {
		if _, present := patches[0].Payload[column]; !present {
			t.Errorf("the PATCH does not carry %q: %v", column, patches[0].Payload)
		}
	}

	assertNoClusterSited(t, stub)
}

// TestClusterIgnoresWhatNetBoxMaintains is the "zero writes on an unchanged scoped cluster"
// criterion, and it is the assertion that proves drift is keyed on `(scope_type, scope_id)`
// rather than confused by what NetBox returns alongside them.
//
// The cache and the counts are injected server-side, as a NetBox that maintains them does. If
// `_site` were not declared read-only the operator would find a difference on every resync and
// PATCH a column NetBox discards -- forever, and invisibly, because the PATCH succeeds.
func TestClusterIgnoresWhatNetBoxMaintains(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newClusterNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makeCluster(t, ns, "steady", func(c *netboxv1alpha1.NetBoxCluster) {
		c.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: idOf(41)}}
	})
	eventually(t, "the cluster to be Ready", func() bool { return clusterIsReady(ns, "steady") })

	id := fetchCluster(ns, "steady").Status.ID

	// What a real 4.6.8 answers a read with, and what the operator must not react to: the
	// scope cache NetBox derives from the pair, the resolved `scope` object, the two
	// related-object counts and the three allocation sums.
	stub.setField(id, "_site", map[string]any{"id": float64(41), "name": "Home"})
	stub.setField(id, "scope", map[string]any{"id": float64(41), "name": "Home"})
	stub.setField(id, "device_count", float64(4))
	stub.setField(id, "virtualmachine_count", float64(17))
	stub.setField(id, "allocated_vcpus", "12.00")

	writesBefore := len(stub.recorded())

	// There is no way to observe a hot loop other than letting time pass: one reconcile
	// finding a spurious difference looks identical to one finding a real one.
	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesBefore {
		t.Errorf("netbox received %d writes, want %d: a column NetBox maintains is being diffed",
			got, writesBefore)
	}

	if live := stub.get(id); live["_site"] == nil {
		t.Error("_site was cleared, so the operator wrote a cached column it must never touch")
	}

	assertNoClusterSited(t, stub)
}

// TestClusterRequiresItsType is the admission half: `type ForeignKey REQ` on the NetBox side
// means the CRD's `typeRef` is required, so a cluster without one is rejected by the API server
// rather than reaching NetBox and coming back as a 400.
//
// A server-side dry run, so admission runs in full and nothing is stored.
func TestClusterRequiresItsType(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		name       string
		mutate     func(*netboxv1alpha1.NetBoxCluster)
		wantReject string
	}{
		{
			name:       "no typeRef at all",
			mutate:     func(c *netboxv1alpha1.NetBoxCluster) { c.Spec.TypeRef = netboxv1alpha1.ClusterTypeRef{} },
			wantReject: "typeRef",
		},
		{
			name: "typeRef in two modes at once",
			mutate: func(c *netboxv1alpha1.NetBoxCluster) {
				c.Spec.TypeRef = netboxv1alpha1.ClusterTypeRef{Name: "proxmox", ID: idOf(3)}
			},
			wantReject: "exactly one of name, slug, lookup or id",
		},
		{
			name: "two members of the scope union",
			mutate: func(c *netboxv1alpha1.NetBoxCluster) {
				c.Spec.Scope = &netboxv1alpha1.ScopeRef{
					SiteRef:   &netboxv1alpha1.SiteRef{ID: idOf(41)},
					RegionRef: &netboxv1alpha1.RegionRef{ID: idOf(11)},
				}
			},
			wantReject: "at most one of regionRef, siteGroupRef, siteRef or locationRef",
		},
		{
			name:   "a type and nothing else is enough",
			mutate: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &netboxv1alpha1.NetBoxCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "cel-"},
				Spec: netboxv1alpha1.NetBoxClusterSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Name:             "proxmox",
					TypeRef:          netboxv1alpha1.ClusterTypeRef{ID: idOf(3)},
				},
			}
			if tc.mutate != nil {
				tc.mutate(cluster)
			}

			err := apiClient.Create(context.Background(), cluster, client.DryRunAll)

			if tc.wantReject == "" {
				if err != nil {
					t.Fatalf("Create was rejected: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Create was accepted, want a rejection naming %q", tc.wantReject)
			}
			if !strings.Contains(err.Error(), tc.wantReject) {
				t.Errorf("rejection %q does not name %q", err, tc.wantReject)
			}
		})
	}
}

// TestClusterCatalogueKindsRoundTrip is the apply/update/delete criterion for the two kinds
// that carry nothing but name, slug and description. They are keyed on `slug`, so the shared
// stub serves them unchanged -- which is the point worth asserting: a kind whose descriptor is
// three fields needs no test scaffolding of its own either.
func TestClusterCatalogueKindsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		stub   stubKind
		object func(ns string) reconciler.Object
	}{
		{
			kind: "NetBoxClusterType",
			stub: stubKind{endpoint: "virtualization/cluster-types", key: "slug"},
			object: func(ns string) reconciler.Object {
				return &netboxv1alpha1.NetBoxClusterType{
					ObjectMeta: metav1.ObjectMeta{Name: "proxmox", Namespace: ns},
					Spec: netboxv1alpha1.NetBoxClusterTypeSpec{
						NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
						Name:             "Proxmox VE",
						Slug:             "proxmox",
					},
				}
			},
		},
		{
			kind: "NetBoxClusterGroup",
			stub: stubKind{endpoint: "virtualization/cluster-groups", key: "slug"},
			object: func(ns string) reconciler.Object {
				return &netboxv1alpha1.NetBoxClusterGroup{
					ObjectMeta: metav1.ObjectMeta{Name: "production", Namespace: ns},
					Spec: netboxv1alpha1.NetBoxClusterGroupSpec{
						NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
						Name:             "Production",
						Slug:             "production",
					},
				}
			},
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ns := newNamespace(t)
			stub, target := newNetBoxStub(t, tc.stub)
			readyEndpoint(t, ns, target)

			obj := tc.object(ns)
			if err := k8sClient.Create(context.Background(), obj); err != nil {
				t.Fatalf("creating %s: %v", tc.kind, err)
			}

			key := client.ObjectKeyFromObject(obj)
			eventually(t, tc.kind+" to be Ready", func() bool {
				fresh := tc.object(ns)
				if k8sClient.Get(context.Background(), key, fresh) != nil {
					return false
				}
				for _, c := range fresh.NetBoxStatus().Conditions {
					if c.Type == netboxv1alpha1.ConditionReady {
						return c.Status == metav1.ConditionTrue
					}
				}

				return false
			})

			fresh := tc.object(ns)
			if err := k8sClient.Get(context.Background(), key, fresh); err != nil {
				t.Fatalf("reading %s back: %v", tc.kind, err)
			}

			id := fresh.NetBoxStatus().ID
			if id == 0 {
				t.Fatal("status.id is unset on a Ready object")
			}

			if live := stub.get(id); live["slug"] != key.Name {
				t.Errorf("slug = %v, want %q", live["slug"], key.Name)
			}

			// Delete, and the object leaves NetBox: these kinds are not IPAM, so the default
			// deletionPolicy is Delete (#176, #186).
			if err := k8sClient.Delete(context.Background(), obj); err != nil {
				t.Fatalf("deleting %s: %v", tc.kind, err)
			}

			eventually(t, tc.kind+" to be removed from NetBox", func() bool {
				return stub.get(id) == nil
			})
		})
	}
}
