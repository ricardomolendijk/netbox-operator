package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// wirelessLANKind points the shared stub at wireless.WirelessLAN. Keyed by `ssid`, which is
// the first natural-key field that is neither a slug nor a network address.
var wirelessLANKind = stubKind{endpoint: "wireless/wireless-lans", key: "ssid"}

// forbiddenSSIDColumns are the keys that must never appear in a request body sent to
// `wireless/wireless-lans`, for three different reasons.
//
// `site` and `site_id` do not exist on wireless.WirelessLAN -- it is on CachedScopeMixin, so
// the foreign key is `(scope_type, scope_id)` -- and NetBox drops a field it does not know
// rather than rejecting it. A write containing one returns 201, creates an *unscoped* SSID, and
// never drifts, because the spec's `site` is compared against a column that does not exist.
// That is the netbox-populator bug, and the reason this Kind has no siteRef.
//
// The four underscore-prefixed columns are the caches NetBox maintains from the pair
// (netbox/dcim/models/mixins.py:63-89). An attempt to set one is dropped exactly like `site`,
// so the next read finds it unchanged and the operator PATCHes it again on every resync,
// forever.
//
// `auth_psk` is the third reason and a different kind of failure: a pre-shared key that reached
// a request body from a spec field would mean the key had been stored in plain text in the
// cluster's etcd and in whatever git repository the manifest lives in. No spec field maps onto
// it, and this is the assertion that says so from the outside.
var forbiddenSSIDColumns = append(
	[]string{"site", "site_id", "auth_psk"},
	"_site", "_region", "_site_group", "_location",
)

// newWirelessStub is the wireless-LAN stub fronted by a handler that also answers the reads an
// id-mode scope reference is verified against, exactly as newScopedNetBoxStub does for
// ipam/prefixes. The scope union's four targets live at four dcim endpoints and the shared stub
// serves one endpoint by design.
//
// It deliberately cannot serve a *write*, so a test that accidentally started managing a Site
// through this path would fail rather than pass quietly.
func newWirelessStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, kind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := dcimObjectID(r); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

func makeWirelessLAN(t *testing.T, ns, name, ssid string, mutate func(*netboxv1alpha1.NetBoxWirelessLAN)) {
	t.Helper()

	lan := &netboxv1alpha1.NetBoxWirelessLAN{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxWirelessLANSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			SSID:             ssid,
			Status:           netboxv1alpha1.WirelessLANStatusActive,
		},
	}
	if mutate != nil {
		mutate(lan)
	}
	if err := k8sClient.Create(context.Background(), lan); err != nil {
		t.Fatalf("creating wireless LAN %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), lan) })
}

func fetchWirelessLAN(ns, name string) *netboxv1alpha1.NetBoxWirelessLAN {
	lan := &netboxv1alpha1.NetBoxWirelessLAN{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, lan); err != nil {
		return nil
	}

	return lan
}

func wirelessLANCondition(ns, name, kind string) metav1.Condition {
	lan := fetchWirelessLAN(ns, name)
	if lan == nil {
		return metav1.Condition{}
	}
	for _, c := range lan.Status.Conditions {
		if c.Type == kind {
			return c
		}
	}

	return metav1.Condition{}
}

func wirelessLANIsReady(ns, name string) bool {
	return wirelessLANCondition(ns, name, netboxv1alpha1.ConditionReady).Status == metav1.ConditionTrue
}

// assertNothingSitedOrKeyed is the acceptance criterion asserted against what was actually
// sent, across every request the engine made, rather than by reading the descriptor.
func assertNothingSitedOrKeyed(t *testing.T, stub *netboxStubServer) {
	t.Helper()

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range forbiddenSSIDColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: %v", i, write.Method, column, write.Payload)
			}
		}
	}
}

// TestWirelessLANIsScopedNeverSited is the populator regression on the kind the ticket names it
// for: a site-scoped SSID POSTs the polymorphic pair, a read-back of NetBox shows the scope
// actually set, and no request body anywhere mentions `site` -- or `auth_psk`.
//
// Asserted on the recorded body rather than on a condition, deliberately. A resolved scope that
// never reaches the payload is invisible from `RefsResolved=True`: the operator would report
// every reference resolved and send NetBox an unscoped SSID that agrees with its spec forever.
func TestWirelessLANIsScopedNeverSited(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLANKind)
	readyEndpoint(t, ns, target)

	siteID := int64(41)
	makeWirelessLAN(t, ns, "donkersloot", "Donkersloot", func(l *netboxv1alpha1.NetBoxWirelessLAN) {
		l.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: &siteID}}
		l.Spec.AuthType = netboxv1alpha1.WirelessAuthTypeWPAPersonal
		l.Spec.AuthCipher = netboxv1alpha1.WirelessAuthCipherAES
	})

	eventually(t, "the SSID to be Ready", func() bool { return wirelessLANIsReady(ns, "donkersloot") })

	lan := fetchWirelessLAN(ns, "donkersloot")
	if lan.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready wireless LAN")
	}

	live := stub.get(lan.Status.ID)
	if live["scope_type"] != "dcim.site" {
		t.Errorf("scope_type = %v, want dcim.site -- the SSID is unscoped, which is the populator bug",
			live["scope_type"])
	}
	if live["scope_id"] != float64(siteID) {
		t.Errorf("scope_id = %v, want %d", live["scope_id"], siteID)
	}

	// The auth fields the operator *does* manage still arrive, so the absence of `auth_psk`
	// above is a targeted omission rather than the whole auth block having been dropped.
	body := onlyPost(t, stub)
	if body["auth_type"] != "wpa-personal" || body["auth_cipher"] != "aes" {
		t.Errorf("POST auth_type = %v, auth_cipher = %v, want wpa-personal and aes",
			body["auth_type"], body["auth_cipher"])
	}

	assertNothingSitedOrKeyed(t, stub)
}

// TestWirelessLANScopeMovesAsOnePair is the atomic-pair property on a kind where the pair is
// also part of the identity: moving the SSID from a Region to a Site is one change and must be
// one PATCH carrying both columns.
//
// A `scope_id` sent without its `scope_type` is rejected by NetBox at best and interpreted
// against the old type at worst -- the object would point at whatever row of the *previous*
// model happens to share that primary key.
func TestWirelessLANScopeMovesAsOnePair(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLANKind)
	readyEndpoint(t, ns, target)

	makeWirelessLAN(t, ns, "moving", "Moving", func(l *netboxv1alpha1.NetBoxWirelessLAN) {
		l.Spec.Scope = &netboxv1alpha1.ScopeRef{RegionRef: &netboxv1alpha1.RegionRef{ID: idOf(11)}}
	})
	eventually(t, "the SSID to be Ready", func() bool { return wirelessLANIsReady(ns, "moving") })

	writesBefore := len(stub.recorded())

	lan := fetchWirelessLAN(ns, "moving")
	lan.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: idOf(41)}}
	if err := k8sClient.Update(context.Background(), lan); err != nil {
		t.Fatalf("moving the scope: %v", err)
	}

	eventually(t, "the move to reach NetBox", func() bool {
		return stub.get(lan.Status.ID)["scope_type"] == "dcim.site"
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

	assertNothingSitedOrKeyed(t, stub)
}

// TestWirelessLANAmbiguousSSIDIsAConflictAndWritesNothing is the acceptance criterion for an
// identity NetBox does not enforce.
//
// wireless.WirelessLAN declares no meta.constraints at all (netbox/wireless/models.py:118-125),
// so two rows with one SSID is a state NetBox permits and will hand back on a lookup. The
// operator refuses to guess which one was meant: `Conflict`, nothing written, and the message
// names the candidates. Adopting an arbitrary match would silently attach this CR to one of two
// networks and start correcting it towards a spec that was never about it.
func TestWirelessLANAmbiguousSSIDIsAConflictAndWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLANKind)
	readyEndpoint(t, ns, target)

	// Two rows NetBox is perfectly happy to hold, neither created by this operator.
	stub.seed(netbox.Object{"ssid": "Donkersloot", "description": "first"})
	stub.seed(netbox.Object{"ssid": "Donkersloot", "description": "second"})

	writesBefore := len(stub.recorded())

	makeWirelessLAN(t, ns, "ambiguous", "Donkersloot", nil)

	eventually(t, "the SSID to report Conflict", func() bool {
		c := wirelessLANCondition(ns, "ambiguous", netboxv1alpha1.ConditionReady)

		return c.Status == metav1.ConditionFalse && c.Reason == netboxv1alpha1.ReasonConflict
	})

	if got := stub.recorded()[writesBefore:]; len(got) != 0 {
		t.Errorf("the engine wrote %d times on an ambiguous lookup, want none: %+v", len(got), got)
	}

	if lan := fetchWirelessLAN(ns, "ambiguous"); lan.Status.ID != 0 {
		t.Errorf("status.id = %d on a Conflict, want 0: nothing was adopted", lan.Status.ID)
	}
}
