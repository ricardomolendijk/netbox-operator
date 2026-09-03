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

// The two stub kinds NBO-052's first PR needs, each keyed by the filter its identity leads
// with (docs/netbox-schema.md, both models' meta.constraints).
//
// Neither has a slug. A panel is `(site, name)` and a feed is `(power_panel, name)`, so `name`
// is the scalar half in both cases and the reference half is pinned through refKeys -- without
// which the stub would match a feed of that name on any panel, which is exactly the failure
// the identity exists to prevent.
var (
	powerPanelKind = stubKind{
		endpoint: "dcim/power-panels", key: "name", refKeys: []string{"site_id"},
	}
	powerFeedKind = stubKind{
		endpoint: "dcim/power-feeds", key: "name", refKeys: []string{"power_panel_id"},
	}
)

// powerServerDefaultColumns are the keys that must never appear in a request body when the
// spec does not set them.
//
// The whole of NBO-052's novel rule in one list. Each column's NetBox default is
// `ConfigItem('POWERFEED_DEFAULT_*')`, read from the target installation's configuration rather
// than from the model (docs/netbox-schema.md -> dcim.PowerFeed), so a key the operator sends
// unbidden overwrites whatever that installation was configured for.
//
// `available_power` rides along because it fails the same way and is checked by the same loop:
// NetBox derives it and does not accept a write.
var powerServerDefaultColumns = []string{
	"voltage", "amperage", "max_utilization", "available_power",
}

// newPowerNetBoxStub is a power-family stub fronted by a handler that answers the reads an
// id-mode reference is verified against, the newRackNetBoxStub shape.
//
// A feed points at three other endpoints and the shared stub serves one by design: it is
// parameterised by the kind under test, not by that kind's references. This adds the smallest
// thing that makes an id-mode ref resolvable, and deliberately cannot serve a *write*, so a
// test that accidentally started managing a Site or a Rack through this path fails rather than
// passing quietly.
func newPowerNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, kind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := referencedObjectID(r, kind.endpoint); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

// makePowerPanel applies a NetBoxPowerPanel and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off.
//
// `siteRef` is in `id` mode and set by default, because NetBox's column is `REQ` and the API
// server rejects the object without it. Id mode costs nothing here: what these tests assert is
// what reaches `dcim/power-panels`, and an id-mode ref renders through the same code a
// name-mode one ends up in.
func makePowerPanel(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxPowerPanel)) {
	t.Helper()

	panel := &netboxv1alpha1.NetBoxPowerPanel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxPowerPanelSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			SiteRef:          netboxv1alpha1.SiteRef{ID: idOf(41)},
		},
	}
	if mutate != nil {
		mutate(panel)
	}

	if err := k8sClient.Create(context.Background(), panel); err != nil {
		t.Fatalf("creating power panel %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), panel) })
}

// makePowerFeed applies a NetBoxPowerFeed. `powerPanelRef` is in `id` mode for the reason
// makePowerPanel's `siteRef` is.
func makePowerFeed(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxPowerFeed)) {
	t.Helper()

	feed := &netboxv1alpha1.NetBoxPowerFeed{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxPowerFeedSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			PowerPanelRef:    netboxv1alpha1.PowerPanelRef{ID: idOf(53)},
		},
	}
	if mutate != nil {
		mutate(feed)
	}

	if err := k8sClient.Create(context.Background(), feed); err != nil {
		t.Fatalf("creating power feed %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), feed) })
}

func fetchPowerPanel(ns, name string) *netboxv1alpha1.NetBoxPowerPanel {
	panel := &netboxv1alpha1.NetBoxPowerPanel{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, panel); err != nil {
		return nil
	}

	return panel
}

func fetchPowerFeed(ns, name string) *netboxv1alpha1.NetBoxPowerFeed {
	feed := &netboxv1alpha1.NetBoxPowerFeed{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, feed); err != nil {
		return nil
	}

	return feed
}

func powerPanelIsReady(ns, name string) bool {
	panel := fetchPowerPanel(ns, name)

	return panel != nil && readyIsTrue(panel.Status.Conditions)
}

func powerFeedIsReady(ns, name string) bool {
	feed := fetchPowerFeed(ns, name)

	return feed != nil && readyIsTrue(feed.Status.Conditions)
}

// readyIsTrue reads the Ready condition out of a status block, for the two kinds here.
//
// Not ipam_remainder_controller_test.go's conditionIsTrue: that one takes the condition type as
// a parameter and every call site in the package already passes ConditionReady, so adding two
// more makes `unparam` right about it. This asks the one question these tests have.
func readyIsTrue(conditions []metav1.Condition) bool {
	for _, c := range conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestPowerPanelWritesSiteAndLocationAsForeignKeys is the panel's round trip.
//
// Both references reach the payload as plain ids under NetBox's own column names, which is the
// whole of what this kind does. `powerfeed_count` is asserted absent from every recorded body
// in the same pass: it is a RelatedObjectCountField NetBox maintains from the feeds and drops
// on write, so sending it would not fail -- it would produce a difference the next reconcile
// finds again, and a PATCH forever.
func TestPowerPanelWritesSiteAndLocationAsForeignKeys(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerPanelKind)
	readyEndpoint(t, ns, target)

	makePowerPanel(t, ns, "panel-a", func(p *netboxv1alpha1.NetBoxPowerPanel) {
		p.Spec.LocationRef = &netboxv1alpha1.LocationRef{ID: idOf(7)}
		p.Spec.Description = "Main distribution panel"
	})

	eventually(t, "the power panel to be Ready", func() bool { return powerPanelIsReady(ns, "panel-a") })

	panel := fetchPowerPanel(ns, "panel-a")
	if panel.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready power panel")
	}

	live := stub.get(panel.Status.ID)

	for column, want := range map[string]any{
		"site": float64(41), "location": float64(7),
		"name": "panel-a", "description": "Main distribution panel",
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		if _, present := write.Payload["powerfeed_count"]; present {
			t.Errorf("request %d (%s) carries powerfeed_count: NetBox maintains it and drops "+
				"the key rather than rejecting it: %v", i, write.Method, write.Payload)
		}
	}
}

// TestPowerPanelIsLookedUpBySiteAndName is the identity on the wire, and the reason the natural
// key was verified against the committed IR before it was written down.
//
// `dcim.PowerPanel.meta.constraints` is `UniqueConstraint(fields=('site', 'name'))` and
// `hack/testdata/ir-4.6.8.json.gz -> dcim.PowerPanel.natural_keys` resolves it to the filters
// `site_id` and `name`. A lookup that dropped `site_id` would match every panel of that name
// in the NetBox, and django-filter answers an unrecognised parameter with the *unfiltered* set
// rather than an error (#206), so a misspelling fails exactly the same way.
func TestPowerPanelIsLookedUpBySiteAndName(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerPanelKind)
	readyEndpoint(t, ns, target)

	// A panel of the same name in a *different* site. It must not be adopted.
	stub.seed(netbox.Object{"name": "panel-b", "site": float64(99)})

	makePowerPanel(t, ns, "panel-b", nil)

	eventually(t, "the power panel to be Ready", func() bool { return powerPanelIsReady(ns, "panel-b") })

	panel := fetchPowerPanel(ns, "panel-b")
	if panel.Status.Adopted {
		t.Error("status.adopted is true; the seeded panel is in site 99 and this CR names " +
			"site 41, so `(site, name)` must not have matched it")
	}

	if live := stub.get(panel.Status.ID); live["site"] != float64(41) {
		t.Errorf("netbox site = %v, want 41: the operator adopted the wrong row", live["site"])
	}
}

// TestPowerFeedOmitsTheServerConfiguredDefaults is NBO-052's central acceptance criterion,
// asserted against real admission and the real engine rather than in a unit test.
//
// The CRD carries no `+kubebuilder:default` for `voltage`, `amperage` or `maxUtilization`, so
// the API server must not add one on create -- that is the half a unit test over a Go struct
// cannot see, because a defaulting marker is applied by the API server and not by the Go type.
// And with the fields still unset, no request body may carry the columns.
//
// The four enums are checked in the same pass for the opposite reason: their NetBox defaults
// are model-level constants rather than ConfigItem lookups, so they *are* defaulted by the CRD
// and they *must* be written, or the operator could never correct one a human changed.
func TestPowerFeedOmitsTheServerConfiguredDefaults(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerFeedKind)
	readyEndpoint(t, ns, target)

	makePowerFeed(t, ns, "feed-a1", nil)

	eventually(t, "the power feed to be Ready", func() bool { return powerFeedIsReady(ns, "feed-a1") })

	feed := fetchPowerFeed(ns, "feed-a1")

	// The API server applied no default to any of the three. A `+kubebuilder:default=120` on
	// voltage would show up right here as a non-nil pointer.
	if feed.Spec.Voltage != nil {
		t.Errorf("spec.voltage = %d after admission, want unset: the column's NetBox default "+
			"is ConfigItem('POWERFEED_DEFAULT_VOLTAGE') and the CRD must not guess it",
			*feed.Spec.Voltage)
	}

	if feed.Spec.Amperage != nil {
		t.Errorf("spec.amperage = %d after admission, want unset", *feed.Spec.Amperage)
	}

	if feed.Spec.MaxUtilization != nil {
		t.Errorf("spec.maxUtilization = %d after admission, want unset", *feed.Spec.MaxUtilization)
	}

	// The four constant-defaulted enums did get theirs.
	for got, want := range map[string]string{
		string(feed.Spec.Status): "active", string(feed.Spec.Type): "primary",
		string(feed.Spec.Supply): "ac", string(feed.Spec.Phase): "single-phase",
	} {
		if got != want {
			t.Errorf("a constant-defaulted enum is %q after admission, want %q", got, want)
		}
	}

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range powerServerDefaultColumns {
			if value, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q = %v; the spec sets none of them and "+
					"each is either configured server-side or derived: %v",
					i, write.Method, column, value, write.Payload)
			}
		}
	}

	live := stub.get(feed.Status.ID)
	for column, want := range map[string]any{
		"power_panel": float64(53), "name": "feed-a1",
		// `type`, `supply` and `phase` read back as bare strings from this stub, which renders
		// only `status` as NetBox's {"value","label"} pair (netboxstub_test.go, netboxShape).
		// The nested form of all four is covered where it belongs -- over the real differ --
		// in internal/reconciler/dcim_powerfeed_test.go.
		"type": "primary", "supply": "ac", "phase": "single-phase",
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	if got, _ := live["status"].(map[string]any); got["value"] != "active" {
		t.Errorf("netbox status = %v, want value=active", live["status"])
	}
}

// TestPowerFeedDoesNotDriftAgainstNetBoxOwnDefaults is the half of the rule that regresses
// first, and it can only be observed by letting time pass.
//
// The stub is made to answer as a NetBox configured for 230 V: it fills in the three columns
// the create body never mentioned. A single reconcile finding a spurious difference looks
// identical to one finding a real one, so the assertion is that several resync intervals
// produce no further writes at all. A CRD that defaulted `voltage` to 120 would PATCH this feed
// on every one of them.
func TestPowerFeedDoesNotDriftAgainstNetBoxOwnDefaults(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerFeedKind)
	readyEndpoint(t, ns, target)

	makePowerFeed(t, ns, "feed-a2", nil)

	eventually(t, "the power feed to be Ready", func() bool { return powerFeedIsReady(ns, "feed-a2") })

	feed := fetchPowerFeed(ns, "feed-a2")

	// What a European installation's own configuration produced, plus the derived column.
	stub.setField(feed.Status.ID, "voltage", float64(230))
	stub.setField(feed.Status.ID, "amperage", float64(16))
	stub.setField(feed.Status.ID, "max_utilization", float64(80))
	stub.setField(feed.Status.ID, "available_power", float64(2944))

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: an unset spec field is not a demand to "+
			"write NetBox's own value back", got, writesAfterCreate)
	}
}

// TestPowerFeedWritesAnExplicitVoltage is the complement, and the reason "do not default it" is
// not the same as "do not manage it".
//
// A declared value is an ordinary column: it goes in the create body and a later pass corrects
// it. The drift half is staged by moving NetBox's value away from the spec's.
func TestPowerFeedWritesAnExplicitVoltage(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerFeedKind)
	readyEndpoint(t, ns, target)

	makePowerFeed(t, ns, "feed-a3", func(f *netboxv1alpha1.NetBoxPowerFeed) {
		f.Spec.Voltage = int32Of(230)
		f.Spec.Amperage = int32Of(32)
		f.Spec.MaxUtilization = int32Of(75)
	})

	eventually(t, "the power feed to be Ready", func() bool { return powerFeedIsReady(ns, "feed-a3") })

	feed := fetchPowerFeed(ns, "feed-a3")

	for column, want := range map[string]any{
		"voltage": float64(230), "amperage": float64(32), "max_utilization": float64(75),
	} {
		if live := stub.get(feed.Status.ID); live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	// A human changed it in NetBox. The operator owns the field now, so it goes back.
	stub.setField(feed.Status.ID, "voltage", float64(120))

	eventually(t, "the operator to correct the voltage", func() bool {
		return stub.get(feed.Status.ID)["voltage"] == float64(230)
	})
}

// TestPowerFeedWithAnUnresolvablePanelWritesNothing is NBO-015's shape on this kind, and the
// assertion is on the recorded traffic rather than on the status.
//
// `powerPanelRef` is half the only natural key, so an unresolved one leaves the engine with no
// identity at all. A version that reported the reference and then created the feed anyway would
// look identical in the conditions -- and would have written a feed onto whichever panel NetBox
// defaulted to, or adopted a stranger's feed of the same name.
func TestPowerFeedWithAnUnresolvablePanelWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newPowerNetBoxStub(t, powerFeedKind)
	endpointWithoutResync(t, ns, target)

	makePowerFeed(t, ns, "feed-a4", func(f *netboxv1alpha1.NetBoxPowerFeed) {
		// A NetBoxPowerPanel that does not exist. `name` is the only mode the operator can
		// wait on, which is why an unresolvable reference is tested in it.
		f.Spec.PowerPanelRef = netboxv1alpha1.PowerPanelRef{Name: "nowhere"}
	})

	eventually(t, "the power feed to report that its panel does not exist", func() bool {
		feed := fetchPowerFeed(ns, "feed-a4")
		if feed == nil {
			return false
		}

		for _, c := range feed.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionRefsResolved {
				return c.Reason == netboxv1alpha1.ReasonRefNotFound
			}
		}

		return false
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox received %d writes, want none: `power_panel` is half the only "+
			"natural key, so there is no identity to look the feed up by: %+v", len(got), got)
	}
}

// TestPowerFeedRejectsAnUnknownEnumValue is the admission half, over all four enums at once.
//
// Every one of them is a closed CRD enum: three of the four ChoiceSets declare no
// FIELD_CHOICES `key` at all and cannot be widened by a deployment, and the fourth
// (`PowerFeed.status`) can be but is enumerated anyway, on the same reasoning SiteStatus and
// RackStatus were. A typo caught by `kubectl apply` beats a 400 three seconds later.
func TestPowerFeedRejectsAnUnknownEnumValue(t *testing.T) {
	ns := newNamespace(t)
	_, target := newPowerNetBoxStub(t, powerFeedKind)
	readyEndpoint(t, ns, target)

	for name, mutate := range map[string]func(*netboxv1alpha1.NetBoxPowerFeed){
		"status": func(f *netboxv1alpha1.NetBoxPowerFeed) { f.Spec.Status = "decommissioning" },
		"type":   func(f *netboxv1alpha1.NetBoxPowerFeed) { f.Spec.Type = "tertiary" },
		"supply": func(f *netboxv1alpha1.NetBoxPowerFeed) { f.Spec.Supply = "AC" },
		"phase":  func(f *netboxv1alpha1.NetBoxPowerFeed) { f.Spec.Phase = "single" },
	} {
		t.Run(name, func(t *testing.T) {
			feed := &netboxv1alpha1.NetBoxPowerFeed{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-" + name[:3], Namespace: ns},
				Spec: netboxv1alpha1.NetBoxPowerFeedSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Name:             "bad",
					PowerPanelRef:    netboxv1alpha1.PowerPanelRef{ID: idOf(53)},
				},
			}
			mutate(feed)

			if err := k8sClient.Create(context.Background(), feed); err == nil {
				_ = k8sClient.Delete(context.Background(), feed)

				t.Error("the API server accepted a value outside the enum; every one of " +
					"these columns is NOT NULL with a default, so there is no empty member " +
					"and no extension in the shipped ChoiceSets bar PowerFeed.status")
			}
		})
	}
}

// TestPowerFeedEmptyEnumsAreDefaultedRatherThanRejected is the complement, and the reason none
// of the four enums carries an `""` member.
//
// All four columns are NOT NULL with a default (docs/netbox-schema.md -> dcim.PowerFeed, and
// `nullable: false` on each in hack/testdata/ir-4.6.8.json.gz), so "unspecified" is not a state
// NetBox can hold and there is nothing for an empty member to mean. Combined with `omitempty`,
// writing `status: ""` therefore drops the key and the API server's own default fills it --
// which is the correct outcome, and the opposite of dcim.Rack's `airflow`, where `""` is a real
// state cleared as null.
func TestPowerFeedEmptyEnumsAreDefaultedRatherThanRejected(t *testing.T) {
	ns := newNamespace(t)
	_, target := newPowerNetBoxStub(t, powerFeedKind)
	readyEndpoint(t, ns, target)

	feed := &netboxv1alpha1.NetBoxPowerFeed{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-enums", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxPowerFeedSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "empty-enums",
			PowerPanelRef:    netboxv1alpha1.PowerPanelRef{ID: idOf(53)},
			Status:           "", Type: "", Supply: "", Phase: "",
		},
	}

	if err := k8sClient.Create(context.Background(), feed); err != nil {
		t.Fatalf("the API server rejected four empty enums: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), feed) })

	// k8sClient reads through the manager's cache, so the object is not visible the instant
	// Create returns.
	eventually(t, "the feed to appear in the cache", func() bool {
		return fetchPowerFeed(ns, "empty-enums") != nil
	})

	stored := fetchPowerFeed(ns, "empty-enums")
	for got, want := range map[string]string{
		string(stored.Spec.Status): "active", string(stored.Spec.Type): "primary",
		string(stored.Spec.Supply): "ac", string(stored.Spec.Phase): "single-phase",
	} {
		if got != want {
			t.Errorf("an empty enum stored as %q, want the CRD default %q", got, want)
		}
	}
}

// int32Of is the address of an int32, for the three optional numeric columns. Their whole
// contract is that nil and a value are different instructions, so the tests need to write one.
func int32Of(v int32) *int32 { return &v }
