package reconciler

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// dcim.PowerFeed through the engine, over the **real** Descriptor rather than a fake one.
//
// One rule is under test here and it is the only genuinely new behaviour in NBO-052:
// `voltage`, `amperage` and `max_utilization` do not default to a model constant, they default
// to `ConfigItem('POWERFEED_DEFAULT_VOLTAGE')` and friends -- read from the *target NetBox's*
// own configuration at write time (docs/netbox-schema.md -> dcim.PowerFeed;
// `default_unresolved: true` on all three, hack/testdata/ir-4.6.8.json.gz). So an omitted
// field must mean "whatever this NetBox is configured for", end to end:
//
//  1. it must not reach the create body, or a NetBox configured for 230 V is silently
//     reconfigured to whatever the CRD guessed;
//  2. it must not be compared on a later pass, or the operator finds a difference against the
//     value NetBox itself supplied and PATCHes it away -- and then finds it again next resync,
//     which is the hot loop docs/concepts/drift.md opens by warning about;
//  3. field ownership must not put a zero back when somebody else claims the field, which is
//     the mechanism that *does* restore an emptied string and would be wrong here.
//
// None of the three needs engine code. They fall out of the fields being optional pointers
// with no `+kubebuilder:default`: `payload.desired` skips a spec key with no value,
// `netbox.Drift` considers only fields present in desired, and `specFields.restoreEmpty`
// deliberately has no empty form for a pointer type. That is exactly why each is asserted
// separately below rather than only end to end -- the behaviour is four existing properties
// lining up, and any one of them changing would break it silently.

var (
	powerFeedGVK  = netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerFeed")
	powerPanelGVK = netboxv1alpha1.PowerPanelRef{}.TargetGVK()
)

// powerPanelID is the NetBox id the feed's panel resolves to. It appears in every payload
// below, because `power_panel` is REQ.
const powerPanelID = 7

// adoptedFeedID is the NetBox id of the feed the update cases reconcile against.
const adoptedFeedID = 23

// powerFeedEngine is the engine wired to the shipped Descriptor and to a cluster holding one
// ready NetBoxPowerPanel for `powerPanelRef` to resolve against.
//
// The shipped Descriptor rather than a fixture, because "adding a Kind needs no engine change"
// is only tested if the Kind under test is the one that ships. A fixture that happened to
// declare `voltage` differently would pass while NetBoxPowerFeed was broken.
func powerFeedEngine(t *testing.T, nb NetBoxClient) *Engine {
	t.Helper()

	reg := registry.New()

	for _, gvk := range []schema.GroupVersionKind{
		powerFeedGVK, powerPanelGVK,
		netboxv1alpha1.RackRef{}.TargetGVK(), netboxv1alpha1.TenantRef{}.TargetGVK(),
	} {
		d, ok := registry.Get(gvk)
		if !ok {
			t.Fatalf("no descriptor for %s; this test needs the shipped one", gvk)
		}

		if err := reg.Add(d); err != nil {
			t.Fatalf("registering %s: %v", gvk, err)
		}
	}

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(powerFeedGVK, &netboxv1alpha1.NetBoxPowerFeed{})

	feed, _ := registry.Get(powerFeedGVK)

	panel := readyTarget(powerPanelGVK, "team-a", "panel-a", powerPanelID)

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: feed, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs: &resolver.Resolver{
			Objects: fakeCluster{objects: []*unstructured.Unstructured{panel}},
			Kinds:   reg,
		},
		Status:     &fakeStatus{},
		LiveStatus: &fakeLiveStatus{},
		Finalizers: &fakeFinalizers{},
		Scheme:     scheme,
	}
}

// powerFeedObject is one feed on `panel-a`, with the four constant-defaulted enums set the way
// the CRD defaults them and none of the three ConfigItem fields set.
//
// `claimed` names spec fields somebody other than the operator owns, in the shape
// metadata.managedFields records them. It is how the field-ownership case below claims
// `voltage` without setting it -- which is precisely the state that would break if
// restoreEmpty ever grew an empty form for pointers.
func powerFeedObject(claimed ...string) *netboxv1alpha1.NetBoxPowerFeed {
	feed := &netboxv1alpha1.NetBoxPowerFeed{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "feed-a1", Generation: 1},
		Spec: netboxv1alpha1.NetBoxPowerFeedSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Feed A1",
			PowerPanelRef:    netboxv1alpha1.PowerPanelRef{Name: "panel-a"},
			Status:           netboxv1alpha1.PowerFeedStatusActive,
			Type:             netboxv1alpha1.PowerFeedTypePrimary,
			Supply:           netboxv1alpha1.PowerFeedSupplyAC,
			Phase:            netboxv1alpha1.PowerFeedPhaseSingle,
		},
	}

	if len(claimed) > 0 {
		fields := make(map[string]any, len(claimed))
		for _, name := range claimed {
			fields["f:"+name] = map[string]any{}
		}

		raw, _ := json.Marshal(map[string]any{"f:spec": fields})
		feed.ManagedFields = []metav1.ManagedFieldsEntry{{
			Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply,
			FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(string(raw)),
		}}
	}

	return feed
}

// liveFeedAsNetBoxDefaultedIt is what a NetBox configured for 230 V answers a create with: the
// three ConfigItem columns filled in server-side with values the spec never asked for, and
// `available_power` derived from two of them.
//
// 230/16/80 rather than NetBox's shipped 120/15/80 on purpose. If any of the three ever leaked
// a CRD default onto the payload, the values here are what the operator would find itself
// PATCHing away -- and the difference is visible in the assertion rather than hidden behind a
// coincidence.
func liveFeedAsNetBoxDefaultedIt() netbox.Object {
	return netbox.Object{
		"id":              float64(adoptedFeedID),
		"name":            "Feed A1",
		"power_panel":     map[string]any{"id": float64(powerPanelID), "name": "Panel A"},
		"status":          map[string]any{"value": "active", "label": "Active"},
		"type":            map[string]any{"value": "primary", "label": "Primary"},
		"supply":          map[string]any{"value": "ac", "label": "AC"},
		"phase":           map[string]any{"value": "single-phase", "label": "Single phase"},
		"voltage":         float64(230),
		"amperage":        float64(16),
		"max_utilization": float64(80),
		"available_power": float64(2944),
		"mark_connected":  false,
	}
}

// TestPowerFeedOmittedServerDefaultsAreNotInTheCreateBody is NBO-052's first acceptance
// criterion: "voltage omitted: the POST body contains no voltage key".
//
// Asserted on the encoded request body rather than on the Go payload, because a `*int32` that
// somehow reached the map as a typed nil would marshal to `null` -- which is a *value* NetBox
// would try to store, not an omission, and `voltage: null` on a NOT NULL column is a 400.
// Only the encoded form tells the two apart.
//
// The four enums are asserted in the same pass and for the opposite reason. Their defaults are
// model-level constants rather than ConfigItem lookups, so they *are* defaulted in the CRD and
// they *must* reach the payload: a defaulted field that never reaches a body is a field the
// operator can never correct.
func TestPowerFeedOmittedServerDefaultsAreNotInTheCreateBody(t *testing.T) {
	nb := &fakeClient{created: liveFeedAsNetBoxDefaultedIt()}
	engine := powerFeedEngine(t, nb)

	if _, err := engine.Reconcile(context.Background(), powerFeedObject()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); !slices.Contains(got, "POST") {
		t.Fatalf("methods() = %v, want a POST; this assertion proves nothing without one", got)
	}

	body := sentBody(t, nb)

	for _, key := range []string{"voltage", "amperage", "max_utilization"} {
		if value, present := body[key]; present {
			t.Errorf("body carries %s = %#v; the column defaults to a ConfigItem read from "+
				"the target NetBox's configuration, so an unset field must send no key at all",
				key, value)
		}
	}

	// `available_power` is derived by NetBox and is not in the serializer at 4.6.8. It must
	// never appear in a request body -- the third acceptance criterion.
	if value, present := body["available_power"]; present {
		t.Errorf("body carries available_power = %#v; NetBox derives it", value)
	}

	for key, want := range map[string]any{
		"name": "Feed A1", "power_panel": float64(powerPanelID),
		"status": "active", "type": "primary", "supply": "ac", "phase": "single-phase",
	} {
		if body[key] != want {
			t.Errorf("body[%s] = %#v, want %#v", key, body[key], want)
		}
	}
}

// TestPowerFeedOmittedServerDefaultsDoNotDrift is the second half of the same criterion, and
// the half that regresses first: "a subsequent reconcile reports no drift against whatever
// NetBox defaulted to".
//
// The live object here is a feed NetBox filled in at 230/16/80. Nothing in the spec mentions
// any of the three, so nothing may be compared against them and no PATCH may be sent. A
// version of this Kind that carried `+kubebuilder:default=120` would send `voltage: 120` on
// every single resync, forever, against a European installation.
func TestPowerFeedOmittedServerDefaultsDoNotDrift(t *testing.T) {
	nb := &fakeClient{get: liveFeedAsNetBoxDefaultedIt()}
	engine := powerFeedEngine(t, nb)

	feed := powerFeedObject()
	feed.Status.ID = adoptedFeedID

	if _, err := engine.Reconcile(context.Background(), feed); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	for _, method := range nb.methods() {
		if method == "PATCH" || method == "POST" {
			t.Errorf("methods() = %v, want reads only: the spec sets none of voltage, "+
				"amperage or max_utilization, so there is nothing to correct. Payload: %#v",
				nb.methods(), nb.lastPayload())

			break
		}
	}
}

// TestPowerFeedSetServerDefaultsAreWrittenAndCorrected is the third acceptance criterion:
// "voltage: 230 set explicitly is written and drift-corrected".
//
// The complement of the two above, and the reason "do not default it" is not the same as "do
// not manage it". A declared value is an ordinary column: it goes in the create body, and a
// later pass that finds NetBox holding something else PATCHes it back.
func TestPowerFeedSetServerDefaultsAreWrittenAndCorrected(t *testing.T) {
	t.Run("written on create", func(t *testing.T) {
		nb := &fakeClient{created: liveFeedAsNetBoxDefaultedIt()}
		engine := powerFeedEngine(t, nb)

		feed := powerFeedObject()
		feed.Spec.Voltage = ptrTo(int32(230))
		feed.Spec.Amperage = ptrTo(int32(32))
		feed.Spec.MaxUtilization = ptrTo(int32(75))

		if _, err := engine.Reconcile(context.Background(), feed); err != nil {
			t.Fatalf("Reconcile() = %v", err)
		}

		body := sentBody(t, nb)
		for key, want := range map[string]any{
			"voltage": float64(230), "amperage": float64(32), "max_utilization": float64(75),
		} {
			if body[key] != want {
				t.Errorf("body[%s] = %#v, want %#v", key, body[key], want)
			}
		}
	})

	t.Run("corrected on a later pass", func(t *testing.T) {
		// NetBox holds the values it defaulted; the spec now says otherwise.
		nb := &fakeClient{get: liveFeedAsNetBoxDefaultedIt(), patched: liveFeedAsNetBoxDefaultedIt()}
		engine := powerFeedEngine(t, nb)

		feed := powerFeedObject()
		feed.Status.ID = adoptedFeedID
		feed.Spec.Voltage = ptrTo(int32(400))

		if _, err := engine.Reconcile(context.Background(), feed); err != nil {
			t.Fatalf("Reconcile() = %v", err)
		}

		if got := nb.methods(); !slices.Contains(got, "PATCH") {
			t.Fatalf("methods() = %v, want a PATCH: the spec sets voltage=400 and NetBox "+
				"holds 230", got)
		}

		body := sentBody(t, nb)
		if body["voltage"] != float64(400) {
			t.Errorf("body[voltage] = %#v, want 400", body["voltage"])
		}

		// The two the spec still does not mention stay out of the correction. A PATCH that
		// carried them would write NetBox's own values back to it, which is a no-op today and
		// a silent reconfiguration the moment the installation's configuration changes.
		for _, key := range []string{"amperage", "max_utilization"} {
			if value, present := body[key]; present {
				t.Errorf("PATCH body carries %s = %#v; the spec does not set it", key, value)
			}
		}
	})
}

// TestPowerFeedAClaimedButUnsetVoltageIsStillUnmanaged is the field-ownership case, and it is
// the one that would break if the "unset means server default" rule were implemented anywhere
// other than in the Go type.
//
// NBO-079's mechanism puts a *claimed* spec field back at its empty value when `omitempty`
// dropped it, so that `description: ""` clears a NetBox description. Applied to `voltage` that
// would put a `0` in the payload -- and 0 is a value NetBox stores, not an absence, so the
// feed would be reconfigured to nothing on the first apply by anything that claims the field.
//
// It does not happen, and specFields.restoreEmpty says why in its own comment: it has no empty
// form for a pointer type, because a nil pointer is already a state of its own. This test is
// what holds that sentence to its consequence for this Kind. It is deliberately *not* a test
// of ownership.go -- it is a test that this Kind's choice of `*int32` over `int32` is what
// makes the rule true.
func TestPowerFeedAClaimedButUnsetVoltageIsStillUnmanaged(t *testing.T) {
	nb := &fakeClient{created: liveFeedAsNetBoxDefaultedIt()}
	engine := powerFeedEngine(t, nb)

	// Every ConfigItem field claimed by another manager, and none of them set.
	feed := powerFeedObject("voltage", "amperage", "maxUtilization", "description")

	if _, err := engine.Reconcile(context.Background(), feed); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	body := sentBody(t, nb)

	for _, key := range []string{"voltage", "amperage", "max_utilization"} {
		if value, present := body[key]; present {
			t.Errorf("body carries %s = %#v; a claimed *int32 that was never set is not an "+
				"emptied field, and 0 is a value NetBox would store", key, value)
		}
	}

	// The contrast, in the same body: `description` is a claimed string, so it *is* restored
	// at its empty value and does clear NetBox's own. That is the behaviour the three above
	// are deliberately exempt from, and asserting both here is what makes the exemption a
	// claim about one code path rather than two.
	if value, present := body["description"]; !present || value != "" {
		t.Errorf("body[description] = %#v (present=%v), want \"\": a claimed string is "+
			"restored at its empty value (docs/concepts/field-ownership.md)", value, present)
	}
}

// TestPowerFeedIsLookedUpByPanelAndName pins the identity on the wire.
//
// `name` alone is unique nowhere in `dcim/power-feeds`, so a lookup missing `power_panel_id`
// would match every feed of that name in the NetBox and adopt one -- the failure behind #206
// and #216. The filter name is `power_panel_id` and not `power_panel`, which is the kind of
// detail django-filter answers by dropping the parameter and returning the *unfiltered* set.
func TestPowerFeedIsLookedUpByPanelAndName(t *testing.T) {
	nb := &fakeClient{list: []netbox.Object{liveFeedAsNetBoxDefaultedIt()}}
	engine := powerFeedEngine(t, nb)

	if _, err := engine.Reconcile(context.Background(), powerFeedObject()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	var found bool

	for _, c := range nb.calls {
		if c.method != "GETONE" {
			continue
		}

		found = true

		got := sortedPairs(c.params)
		want := []string{"name=Feed A1", "power_panel_id=7"}

		if !slices.Equal(got, want) {
			t.Errorf("lookup params = %v, want %v", got, want)
		}
	}

	if !found {
		t.Fatal("no lookup was made, so this assertion proves nothing")
	}
}

// TestPowerFeedWaitsRatherThanCreatingWhenThePanelDoesNotResolve is the other half of the same
// identity argument.
//
// `powerPanelRef` is in the natural key, so a feed whose panel has not reconciled yet has no
// applicable candidate at all. The engine must write nothing: a create would put a second feed
// in NetBox once the panel appeared, and a lookup with the panel dropped would adopt a
// stranger's.
func TestPowerFeedWaitsRatherThanCreatingWhenThePanelDoesNotResolve(t *testing.T) {
	nb := &fakeClient{}
	engine := powerFeedEngine(t, nb)

	feed := powerFeedObject()
	feed.Spec.PowerPanelRef = netboxv1alpha1.PowerPanelRef{Name: "not-applied-yet"}

	if _, err := engine.Reconcile(context.Background(), feed); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); len(got) != 0 {
		t.Errorf("methods() = %v, want none: the panel is unresolved and it is half the "+
			"natural key", got)
	}
}
