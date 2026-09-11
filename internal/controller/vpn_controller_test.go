package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The three stub kinds #59's tests need, each keyed by the filter its identity leads with
// (docs/netbox-schema.md, the eight models' constraints and column-level uniques).
//
// A tunnel is keyed by `name`, because both of its constraints lead with the name-bearing
// column and the group is the discriminator around it. An IKE policy has no slug at all -- it
// declares no meta.constraints and its only unique column is `name`. An L2VPN has both, and
// `slug` is the candidate.
var (
	tunnelKind    = stubKind{endpoint: "vpn/tunnels", key: "name"}
	ikePolicyKind = stubKind{endpoint: "vpn/ike-policies", key: "name"}
	l2vpnKind     = stubKind{endpoint: "vpn/l2vpns", key: "slug"}
)

// queryLog records the collection GETs the engine sends, which is the only way to see a
// natural-key lookup from the outside.
//
// The shared stub records writes and not reads, and for most kinds that is the right split:
// what a kind writes is what a user sees in NetBox. It is not enough here. The whole hazard on
// vpn.Tunnel is the *spelling of a filter* -- `?group_id=null` against `?group_id__empty=true`
// against the filter simply omitted -- and all three produce identical writes against a stub
// that answers whatever it is asked. #206 is exactly that bug shipped, so the query string is
// the assertion.
type queryLog struct {
	mu   sync.Mutex
	seen []url.Values
}

func (q *queryLog) record(values url.Values) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seen = append(q.seen, values)
}

// lookups returns the recorded queries with the paging parameters dropped, so an assertion
// reads as the filter set the engine chose.
func (q *queryLog) lookups() []url.Values {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]url.Values, 0, len(q.seen))

	for _, values := range q.seen {
		trimmed := url.Values{}

		for name, v := range values {
			if name == "limit" || name == "offset" {
				continue
			}

			trimmed[name] = v
		}

		out = append(out, trimmed)
	}

	return out
}

// sent reports whether any recorded lookup carried this parameter with this value.
func (q *queryLog) sent(param, value string) bool {
	for _, values := range q.lookups() {
		if values.Get(param) == value {
			return true
		}
	}

	return false
}

// sentAny reports whether any recorded lookup carried this parameter at all, whatever the
// value. Used in the negative, for the spellings that must never appear.
func (q *queryLog) sentAny(param string) bool {
	for _, values := range q.lookups() {
		if _, ok := values[param]; ok {
			return true
		}
	}

	return false
}

// newVPNNetBoxStub is a vpn-family stub fronted by a handler that records collection GETs and
// answers the reads an id-mode reference is verified against -- the newRackNetBoxStub shape,
// plus the query log.
//
// The reference reads are the smallest thing that makes an id-mode ref resolvable, and
// deliberately cannot serve a *write*: a test that accidentally started managing a
// NetBoxTunnelGroup or a NetBoxRouteTarget through this path fails rather than passing quietly.
func newVPNNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, *queryLog, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, kind)
	log := &queryLog{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		collection := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"+kind.endpoint), "/")
		if r.Method == http.MethodGet && collection == "" {
			log.record(r.URL.Query())
		}

		if id, ok := referencedObjectID(r, kind.endpoint); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, log, srv.URL
}

// makeTunnel applies a NetBoxTunnel and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off.
//
// `encapsulation` is set by default because NetBox's column is `REQ` and the API server
// rejects the object without it. `groupRef` is left unset by default, because the groupless
// tunnel is the case the null pin exists for and a test that wants a group says so.
func makeTunnel(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxTunnel)) {
	t.Helper()

	tunnel := &netboxv1alpha1.NetBoxTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxTunnelSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			Encapsulation:    netboxv1alpha1.TunnelEncapsulationIPSecTunnel,
			Status:           netboxv1alpha1.TunnelStatusActive,
		},
	}
	if mutate != nil {
		mutate(tunnel)
	}

	if err := k8sClient.Create(context.Background(), tunnel); err != nil {
		t.Fatalf("creating tunnel %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), tunnel) })
}

func fetchTunnel(ns, name string) *netboxv1alpha1.NetBoxTunnel {
	tunnel := &netboxv1alpha1.NetBoxTunnel{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, tunnel); err != nil {
		return nil
	}

	return tunnel
}

func tunnelIsReady(ns, name string) bool {
	tunnel := fetchTunnel(ns, name)
	if tunnel == nil {
		return false
	}

	for _, c := range tunnel.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestGrouplessTunnelPinsGroupIDToNull is #59's acceptance criterion "a groupless NetBoxTunnel
// is looked up with `group_id=null`", asserted on the wire rather than on the outcome.
//
// vpn.Tunnel's second constraint is `unique(name) WHERE group IS NULL`, so the lookup for a
// tunnel in no group has to *say* that the group is null. An omitted filter would match a
// tunnel of that name inside somebody else's group and the follow-up PATCH would move it out;
// `?group_id__empty=true` would be dropped by NetBox's BaseFilterSet, which registers no
// `empty` lookup for a ModelMultipleChoiceFilter, leaving the same unfiltered match (#206,
// #216). All three spellings look identical in the recorded writes, which is why this test
// reads the query string.
func TestGrouplessTunnelPinsGroupIDToNull(t *testing.T) {
	ns := newNamespace(t)
	stub, queries, target := newVPNNetBoxStub(t, tunnelKind)
	readyEndpoint(t, ns, target)

	makeTunnel(t, ns, "t1", nil)

	eventually(t, "the tunnel to be Ready", func() bool { return tunnelIsReady(ns, "t1") })

	if !queries.sent("group_id", "null") {
		t.Errorf("lookups = %v, want one carrying ?group_id=null: without the pin a groupless "+
			"tunnel adopts a tunnel of the same name in somebody else's group (#206)",
			queries.lookups())
	}

	if queries.sentAny("group_id__empty") || queries.sentAny("group_id__isnull") {
		t.Errorf("lookups = %v, want the null sentinel: NetBox registers no `empty` lookup for "+
			"a ModelMultipleChoiceFilter, so the suffixed spellings are dropped and the "+
			"request matches everything (#206, registry.NullColumnRef)", queries.lookups())
	}

	if !queries.sent("name", "t1") {
		t.Errorf("lookups = %v, want one carrying ?name=t1", queries.lookups())
	}

	// The write half, because a lookup that found nothing has to be followed by a create that
	// carries no group at all rather than one carrying the literal string "null".
	writes := stub.recorded()
	if len(writes) != 1 || writes[0].Method != http.MethodPost {
		t.Fatalf("netbox writes = %v, want one POST", writes)
	}

	if group, ok := writes[0].Payload["group"]; ok {
		t.Errorf("the create payload carries group = %v; a groupless tunnel writes no group "+
			"column at all", group)
	}
}

// TestTunnelInAGroupIsLookedUpByTheConstraint is the other half of the conditional identity.
//
// With `groupRef` declared and resolved, the applicable candidate is `(group_id, name)` -- the
// database constraint -- and the null pin must be gone: a tunnel that names a group and is
// looked up with `?group_id=null` would never find itself, and would create a second row on
// every reconcile.
func TestTunnelInAGroupIsLookedUpByTheConstraint(t *testing.T) {
	ns := newNamespace(t)
	stub, queries, target := newVPNNetBoxStub(t, tunnelKind)
	readyEndpoint(t, ns, target)

	makeTunnel(t, ns, "t2", func(tunnel *netboxv1alpha1.NetBoxTunnel) {
		tunnel.Spec.GroupRef = &netboxv1alpha1.TunnelGroupRef{ID: idOf(7)}
		tunnel.Spec.IPSecProfileRef = &netboxv1alpha1.IPSecProfileRef{ID: idOf(9)}
		tunnel.Spec.TenantRef = &netboxv1alpha1.TenantRef{ID: idOf(11)}
		tunnel.Spec.TunnelID = idOf(4242)
	})

	eventually(t, "the tunnel to be Ready", func() bool { return tunnelIsReady(ns, "t2") })

	if !queries.sent("group_id", "7") {
		t.Errorf("lookups = %v, want one carrying ?group_id=7", queries.lookups())
	}

	if queries.sent("group_id", "null") {
		t.Errorf("lookups = %v, want no null pin: this tunnel names a group, so the pinned "+
			"candidate is inapplicable and would never find it", queries.lookups())
	}

	writes := stub.recorded()
	if len(writes) != 1 {
		t.Fatalf("netbox writes = %v, want one", writes)
	}

	// The three references are written as plain foreign keys under the column name, not under
	// the `_id` filter spelling: NetBox ignores a field name it does not know rather than
	// rejecting it, so `group_id` in a payload would write nothing and report success.
	for column, want := range map[string]float64{
		"group": 7, "ipsec_profile": 9, "tenant": 11, "tunnel_id": 4242,
	} {
		if got := writes[0].Payload[column]; got != want {
			t.Errorf("payload[%q] = %v, want %v", column, got, want)
		}
	}

	for _, absent := range []string{"group_id", "ipsec_profile_id", "tenant_id"} {
		if _, ok := writes[0].Payload[absent]; ok {
			t.Errorf("payload carries %q; a reference is written under its column name", absent)
		}
	}
}

// TestGrouplessTunnelIsAdoptedNotDuplicated is the same criterion from the outcome side: the
// pinned lookup has to find the row that is already there.
func TestGrouplessTunnelIsAdoptedNotDuplicated(t *testing.T) {
	ns := newNamespace(t)
	stub, _, target := newVPNNetBoxStub(t, tunnelKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{
		"name": "t3", "group": nil, "encapsulation": "ipsec-tunnel", "status": "active",
	})

	makeTunnel(t, ns, "t3", func(tunnel *netboxv1alpha1.NetBoxTunnel) {
		tunnel.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the tunnel to be Ready", func() bool { return tunnelIsReady(ns, "t3") })

	tunnel := fetchTunnel(ns, "t3")
	if !tunnel.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if n := stub.countByKey("t3"); n != 1 {
		t.Errorf("%d tunnels named t3, want 1: it was duplicated rather than adopted", n)
	}
}

// TestTunnelWithAnUnresolvableGroupWritesNothing is NBO-015's shape on the conditional
// identity, and it is the row #59's acceptance criteria do not mention.
//
// A tunnel whose `groupRef` names a NetBoxTunnelGroup that does not exist yet has *no*
// applicable candidate: the constraint candidate needs `group_id` resolved, and the pinned one
// is offered only while `groupRef` is undeclared. Falling through to the pin would find the
// groupless tunnel of the same name and PATCH this group onto it -- somebody else's tunnel,
// moved. With nothing applicable the engine waits, and the assertion is on the recorded
// traffic rather than on the status, because a version that reported the reference and created
// the tunnel anyway would look identical in the conditions.
func TestTunnelWithAnUnresolvableGroupWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, queries, target := newVPNNetBoxStub(t, tunnelKind)
	endpointWithoutResync(t, ns, target)

	makeTunnel(t, ns, "t4", func(tunnel *netboxv1alpha1.NetBoxTunnel) {
		// A NetBoxTunnelGroup that does not exist. `name` is the only mode the operator can
		// wait on, which is why an unresolvable reference is tested in it.
		tunnel.Spec.GroupRef = &netboxv1alpha1.TunnelGroupRef{Name: "nowhere"}
	})

	eventually(t, "the tunnel to report that its group does not exist", func() bool {
		tunnel := fetchTunnel(ns, "t4")
		if tunnel == nil {
			return false
		}

		for _, c := range tunnel.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionRefsResolved {
				return c.Reason == netboxv1alpha1.ReasonRefNotFound
			}
		}

		return false
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox writes = %v, want none: no candidate is applicable while groupRef is "+
			"declared and unresolved", got)
	}

	if queries.sent("group_id", "null") {
		t.Errorf("lookups = %v, want no null pin: the group is declared, so asserting it is "+
			"null would adopt an unrelated tunnel", queries.lookups())
	}

	if tunnel := fetchTunnel(ns, "t4"); tunnel.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: nothing was created", tunnel.Status.ID)
	}
}

// presharedKey is the value the stubbed NetBox holds and returns on every read of the IKE
// policy. Distinctive on purpose: the assertions below are substring scans, so the value has
// to be one that cannot occur by accident.
const presharedKey = "s3cr3t-preshared-key-do-not-leak"

// TestIKEPolicyNeverTouchesThePresharedKey is the rule this whole app is shaped around,
// asserted end to end against a NetBox that *does* hold a key.
//
// `vpn.IKEPolicy.preshared_key` is writable and is deliberately not mapped: a secret may never
// be inline in a spec, the only permitted shape is a SecretRef, and the engine has no
// FieldClass that reads a Secret into a payload (#241). Four things follow, and all four are
// checked here, because three of them would look like success:
//
//  1. The key NetBox already holds survives. An unmapped column cannot reach a payload, so
//     adopting the policy neither writes nor clears it.
//  2. No request the operator sends carries the column -- not the create, not a PATCH.
//  3. The value never reaches the CR: not `status`, not a condition message, not an
//     annotation. The operator reads the whole object back from NetBox, so the value *is* in
//     the process; what matters is that nothing puts it somewhere a namespace reader can see.
//  4. It reaches no Event either.
//
// The drift half is the subtle one. If `preshared_key` were compared, the engine would see a
// spec that does not declare it, decide it differs, and PATCH -- forever. Zero writes after
// adoption is what says it is not compared.
func TestIKEPolicyNeverTouchesThePresharedKey(t *testing.T) {
	ns := newNamespace(t)
	stub, _, target := newVPNNetBoxStub(t, ikePolicyKind)
	readyEndpoint(t, ns, target)

	// Seeded with every mapped field already agreeing with the spec below, so that "zero
	// writes" means what it says: the only thing that could still provoke one is a column the
	// spec does not declare, and `preshared_key` is the only such column here.
	// Seeded with every mapped field already agreeing with the spec below, so that "zero
	// writes" means what it says: the only thing that could still provoke one is a column the
	// spec does not declare, and `preshared_key` is the only such column here.
	//
	// `proposals` is seeded in the *nested* shape NetBox reads a many-to-many back as --
	// `[{"id": 21, ...}]` rather than `[21]` -- because that is what the drift comparison has
	// to normalise, and a seed in the write shape would be testing a NetBox that does not
	// exist (internal/reconciler/ipam_vrf_test.go, liveVRFWith).
	id := stub.seed(netbox.Object{
		"name": "p1", "version": float64(2),
		"proposals":     []any{map[string]any{"id": float64(21), "name": "prop"}},
		"preshared_key": presharedKey,
	})

	policy := &netboxv1alpha1.NetBoxIKEPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxIKEPolicySpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{
				EndpointRef: "homelab", OnConflict: netboxv1alpha1.ConflictAdopt,
			},
			Name:      "p1",
			Version:   netboxv1alpha1.IKEVersion2,
			Proposals: []netboxv1alpha1.IKEProposalRef{{ID: idOf(21)}},
		},
	}
	if err := k8sClient.Create(context.Background(), policy); err != nil {
		t.Fatalf("creating IKE policy: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), policy) })

	eventually(t, "the IKE policy to be Ready", func() bool {
		got := &netboxv1alpha1.NetBoxIKEPolicy{}
		if err := k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "p1"}, got); err != nil {
			return false
		}

		for _, c := range got.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionReady {
				return c.Status == metav1.ConditionTrue
			}
		}

		return false
	})

	// (1) NetBox still holds the key it held.
	if got := stub.get(id)["preshared_key"]; got != presharedKey {
		t.Errorf("netbox preshared_key = %v, want it untouched: an unmapped column is never "+
			"written and never cleared", got)
	}

	// (2) and the drift half of (2): nothing the operator sent mentions the column, and after
	// adoption it sends nothing at all.
	for _, write := range stub.recorded() {
		if _, ok := write.Payload["preshared_key"]; ok {
			t.Errorf("%s payload carries preshared_key; the column is deliberately unmapped "+
				"(#241, api/v1alpha1/vpn_ikepolicy.go)", write.Method)
		}
	}

	if writes := stub.recorded(); len(writes) != 0 {
		t.Errorf("netbox writes = %v, want none: the policy was adopted and every mapped field "+
			"already agrees, so a write here means preshared_key is being compared and "+
			"PATCHed forever", writes)
	}

	// (3) the CR carries no copy of it, anywhere.
	got := &netboxv1alpha1.NetBoxIKEPolicy{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "p1"}, got); err != nil {
		t.Fatalf("reading the IKE policy back: %v", err)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the IKE policy: %v", err)
	}

	if strings.Contains(string(encoded), presharedKey) {
		t.Errorf("the CR contains the pre-shared key: %s", encoded)
	}

	// (4) and neither does anything the operator said out loud about it.
	events := &corev1.EventList{}
	if err := k8sClient.List(context.Background(), events, client.InNamespace(ns)); err != nil {
		t.Fatalf("listing events: %v", err)
	}

	for _, event := range events.Items {
		if strings.Contains(event.Message, presharedKey) {
			t.Errorf("event %s/%s carries the pre-shared key: %s",
				event.Namespace, event.Name, event.Message)
		}
	}
}

// makeL2VPN applies a NetBoxL2VPN and removes it afterwards so the finalizer does not outlive
// the stub it needs in order to come off.
func makeL2VPN(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxL2VPN)) {
	t.Helper()

	l2vpn := &netboxv1alpha1.NetBoxL2VPN{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxL2VPNSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Campus EVPN",
			Slug:             name,
			Type:             netboxv1alpha1.L2VPNTypeVXLANEVPN,
			Status:           netboxv1alpha1.L2VPNStatusActive,
		},
	}
	if mutate != nil {
		mutate(l2vpn)
	}

	if err := k8sClient.Create(context.Background(), l2vpn); err != nil {
		t.Fatalf("creating L2VPN %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), l2vpn) })
}

func l2vpnIsReady(ns, name string) bool {
	got := &netboxv1alpha1.NetBoxL2VPN{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, got); err != nil {
		return false
	}

	for _, c := range got.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestL2VPNWritesRouteTargetsAsASortedIDList is the write half of the ipam.VRF relation, on the
// second kind that carries it.
//
// NetBox replaces a many-to-many wholesale on PATCH and takes bare ids, so the payload is the
// whole set as a list of ids -- sorted, because NetBox does not preserve the order and a
// comparison that did would find drift on every reconcile after a user tidied their YAML.
func TestL2VPNWritesRouteTargetsAsASortedIDList(t *testing.T) {
	ns := newNamespace(t)
	stub, _, target := newVPNNetBoxStub(t, l2vpnKind)
	readyEndpoint(t, ns, target)

	makeL2VPN(t, ns, "campus-evpn", func(l2vpn *netboxv1alpha1.NetBoxL2VPN) {
		l2vpn.Spec.ImportTargets = []netboxv1alpha1.RouteTargetRef{{ID: idOf(31)}, {ID: idOf(17)}}
		l2vpn.Spec.ExportTargets = []netboxv1alpha1.RouteTargetRef{{ID: idOf(17)}}
		l2vpn.Spec.TenantRef = &netboxv1alpha1.TenantRef{ID: idOf(5)}
		l2vpn.Spec.Identifier = idOf(65000)
	})

	eventually(t, "the L2VPN to be Ready", func() bool { return l2vpnIsReady(ns, "campus-evpn") })

	writes := stub.recorded()
	if len(writes) == 0 || writes[0].Method != http.MethodPost {
		t.Fatalf("netbox writes = %v, want a POST first", writes)
	}

	// Written declared-order-independently: 31 was listed first and 17 leaves first.
	got, ok := writes[0].Payload["import_targets"].([]any)
	if !ok {
		t.Fatalf("payload[import_targets] = %v, want a list", writes[0].Payload["import_targets"])
	}

	want := []any{float64(17), float64(31)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("payload[import_targets] = %v, want %v sorted", got, want)
	}

	// The two relations are independent: the same route target may be in both, and each is
	// written on its own.
	if export := writes[0].Payload["export_targets"]; !reflect.DeepEqual(export, []any{float64(17)}) {
		t.Errorf("payload[export_targets] = %v, want [17]", export)
	}

	// `tenant` is a plain foreign key alongside them, and `identifier` a nullable integer.
	if tenant := writes[0].Payload["tenant"]; tenant != float64(5) {
		t.Errorf("payload[tenant] = %v, want 5", tenant)
	}

	if id := writes[0].Payload["identifier"]; id != float64(65000) {
		t.Errorf("payload[identifier] = %v, want 65000", id)
	}
}

// TestL2VPNReorderingRouteTargetsWritesNothing is #59's acceptance criterion "importTargets
// reordering produces zero API writes", and it runs against an *adopted* object because that
// is the only way to put NetBox's own read shape on the live side.
//
// NetBox reads a many-to-many back as a list of nested objects and takes bare ids on write
// (internal/reconciler/ipam_vrf_test.go, liveVRFWith), so the comparison has to normalise both
// and sort. A comparison that respected order would PATCH the same list forever the moment
// somebody reordered their YAML -- drift the operator invented rather than found.
func TestL2VPNReorderingRouteTargetsWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, _, target := newVPNNetBoxStub(t, l2vpnKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{
		"name": "Campus EVPN", "slug": "campus-live", "type": "vxlan-evpn", "status": "active",
		"import_targets": []any{
			map[string]any{"id": float64(17), "name": "65000:17"},
			map[string]any{"id": float64(31), "name": "65000:31"},
		},
	})

	makeL2VPN(t, ns, "campus-live", func(l2vpn *netboxv1alpha1.NetBoxL2VPN) {
		l2vpn.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
		l2vpn.Spec.ImportTargets = []netboxv1alpha1.RouteTargetRef{{ID: idOf(31)}, {ID: idOf(17)}}
	})

	eventually(t, "the L2VPN to be Ready", func() bool { return l2vpnIsReady(ns, "campus-live") })

	if writes := stub.recorded(); len(writes) != 0 {
		t.Fatalf("netbox writes = %v, want none: the declared set is the live set, in a "+
			"different order", writes)
	}

	current := &netboxv1alpha1.NetBoxL2VPN{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "campus-live"}, current); err != nil {
		t.Fatalf("reading the L2VPN back: %v", err)
	}

	current.Spec.ImportTargets = []netboxv1alpha1.RouteTargetRef{{ID: idOf(17)}, {ID: idOf(31)}}
	if err := k8sClient.Update(context.Background(), current); err != nil {
		t.Fatalf("reordering the import targets: %v", err)
	}

	eventually(t, "the L2VPN to be Ready again", func() bool {
		return l2vpnIsReady(ns, "campus-live")
	})

	if writes := stub.recorded(); len(writes) != 0 {
		t.Errorf("netbox writes = %v after a reorder, want none: a to-many reference is "+
			"compared as an unordered set", writes)
	}
}
