package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The three stub kinds NBO-051 needs, each keyed by the filter its identity leads with
// (docs/netbox-schema.md, the five models' meta.constraints).
//
// A rack has no slug: both of its constraints are `(location, ...)` and the name-bearing one is
// the candidate that leads, so `name` is the filter the engine sends. A reservation has neither
// slug nor name -- it has no constraint at all -- and `description` is the one required scalar
// its convention key can carry.
var (
	rackRoleKind        = stubKind{endpoint: "dcim/rack-roles", key: "slug"}
	rackKind            = stubKind{endpoint: "dcim/racks", key: "name"}
	rackReservationKind = stubKind{endpoint: "dcim/rack-reservations", key: "description"}
)

// rackScopeColumns are keys that must never appear in a request body sent to `dcim/racks`.
//
// The NetBox 4.2 trap, asserted in the negative on the kind it did *not* happen to. ipam.Prefix
// and virtualization.Cluster moved to `(scope_type, scope_id)` with cached `site`/`location`
// columns; `dcim.Rack` kept both as real foreign keys (docs/netbox-schema.md -> dcim.Rack). So
// the mistake available here is the opposite one -- sending a scope pair a rack has no columns
// for, which DRF drops rather than rejecting, leaving a rack with no site and no drift ever.
var rackScopeColumns = []string{"scope_type", "scope_id", "_site", "_location"}

// newRackNetBoxStub is a rack-family stub fronted by a handler that answers the reads an
// id-mode reference is verified against, the newClusterNetBoxStub shape.
//
// A rack points at six other endpoints and the shared stub serves one by design: it is
// parameterised by the kind under test, not by that kind's references. This adds the smallest
// thing that makes an id-mode ref resolvable, and deliberately cannot serve a *write*, so a
// test that accidentally started managing a Site or a RackType through this path fails rather
// than passing quietly.
func newRackNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
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

// makeRack applies a NetBoxRack and removes it afterwards so the finalizer does not outlive the
// stub it needs in order to come off.
//
// `siteRef` is in `id` mode and set by default, because NetBox's column is `REQ` and the API
// server rejects the object without it. Id mode costs nothing here: what these tests assert is
// what reaches `dcim/racks`, and an id-mode ref renders through the same code a name-mode one
// ends up in.
func makeRack(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxRack)) {
	t.Helper()

	rack := &netboxv1alpha1.NetBoxRack{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRackSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			SiteRef:          netboxv1alpha1.SiteRef{ID: idOf(41)},
			Status:           netboxv1alpha1.RackStatusActive,
		},
	}
	if mutate != nil {
		mutate(rack)
	}

	if err := k8sClient.Create(context.Background(), rack); err != nil {
		t.Fatalf("creating rack %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rack) })
}

func fetchRack(ns, name string) *netboxv1alpha1.NetBoxRack {
	rack := &netboxv1alpha1.NetBoxRack{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, rack); err != nil {
		return nil
	}

	return rack
}

func rackIsReady(ns, name string) bool {
	rack := fetchRack(ns, name)
	if rack == nil {
		return false
	}

	for _, c := range rack.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestRackWritesSiteAsAForeignKeyAndNoScopePair is the round trip, and the acceptance criterion
// that this Kind is *not* the one NetBox 4.2 changed.
//
// `site` reaching the payload as a plain id is the whole point: had dcim.Rack been mixed with
// CachedScopeMixin, that key would be a read-only cache NetBox answers 201 for and never sets.
// The dimension defaults are checked in the same pass because a defaulted field that never
// reaches a payload is a field the operator can never correct.
func TestRackWritesSiteAsAForeignKeyAndNoScopePair(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackKind)
	readyEndpoint(t, ns, target)

	makeRack(t, ns, "r1", func(r *netboxv1alpha1.NetBoxRack) {
		r.Spec.LocationRef = &netboxv1alpha1.LocationRef{ID: idOf(7)}
		r.Spec.RoleRef = &netboxv1alpha1.RackRoleRef{ID: idOf(9)}
		r.Spec.RackTypeRef = &netboxv1alpha1.RackTypeRef{ID: idOf(11)}
		r.Spec.GroupRef = &netboxv1alpha1.RackGroupRef{ID: idOf(13)}
		r.Spec.TenantRef = &netboxv1alpha1.TenantRef{ID: idOf(15)}
		r.Spec.FacilityID = "C3.14"
		r.Spec.AssetTag = "ASSET-0001"
		r.Spec.FormFactor = netboxv1alpha1.RackFormFactorFourPostCabinet
		r.Spec.Weight = "18.5"
		r.Spec.WeightUnit = netboxv1alpha1.WeightUnitKilograms
	})

	eventually(t, "the rack to be Ready", func() bool { return rackIsReady(ns, "r1") })

	rack := fetchRack(ns, "r1")
	if rack.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready rack")
	}

	live := stub.get(rack.Status.ID)

	// The six foreign keys under NetBox's own names. `rackTypeRef` sent as `rackTypeRef` would
	// be dropped silently, which is why the field map is a table rather than a convention.
	for column, want := range map[string]any{
		"site": float64(41), "location": float64(7), "role": float64(9),
		"rack_type": float64(11), "group": float64(13), "tenant": float64(15),
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	// The three defaulted RackBase dimensions, from v1alpha1.RackDimensions.
	for column, want := range map[string]any{
		"width": float64(19), "u_height": float64(42), "starting_unit": float64(1),
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want the CRD default %v reaching the payload",
				column, live[column], want)
		}
	}

	for column, want := range map[string]any{
		"facility_id": "C3.14", "asset_tag": "ASSET-0001",
		"form_factor": "4-post-cabinet", "weight": "18.5", "weight_unit": "kg",
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
		for _, column := range rackScopeColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: dcim.Rack has no scope pair, and DRF "+
					"drops the key rather than rejecting it: %v", i, write.Method, column,
					write.Payload)
			}
		}
	}
}

// TestRackDoesNotHotLoopOnItsDecimalWeight is the steady state, and the one field that could
// break it on its own.
//
// `weight` is a DecimalField NetBox returns padded to two places, so a spec that said `"18.5"`
// reads back as `"18.50"`. Compared as strings that is a difference on every pass and a PATCH
// forever; the engine compares two numeric strings numerically
// (internal/netbox/drift.go, scalarEqual), which is what this asserts from the outside.
func TestRackDoesNotHotLoopOnItsDecimalWeight(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackKind)
	readyEndpoint(t, ns, target)

	makeRack(t, ns, "r2", func(r *netboxv1alpha1.NetBoxRack) {
		r.Spec.Weight = "18.5"
		r.Spec.WeightUnit = netboxv1alpha1.WeightUnitKilograms
	})

	eventually(t, "the rack to be Ready", func() bool { return rackIsReady(ns, "r2") })

	rack := fetchRack(ns, "r2")
	stub.setField(rack.Status.ID, "weight", "18.50")

	writesAfterCreate := len(stub.recorded())

	// Wait out several resync intervals. There is no way to observe a hot loop other than
	// letting time pass: a single reconcile finding a spurious difference looks identical to
	// one finding a real one.
	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: \"18.5\" and \"18.50\" are the same "+
			"decimal and must not produce a PATCH", got, writesAfterCreate)
	}
}

// TestLocationlessRackIsAdoptedNotDuplicated is the acceptance criterion for the identity NetBox
// does not enforce.
//
// A rack with no `locationRef` satisfies neither of dcim.Rack's constraints -- both are keyed on
// `location`, and Postgres treats NULLs as distinct -- so nothing on the server side stops a
// second row. The `(site, name)` candidate with `location_id` pinned null is the convention that
// does, and this is what holds it: the pre-existing row is taken over rather than duplicated.
func TestLocationlessRackIsAdoptedNotDuplicated(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{"name": "r3", "site": float64(41), "location": nil})

	makeRack(t, ns, "r3", func(r *netboxv1alpha1.NetBoxRack) {
		r.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the rack to be Ready", func() bool { return rackIsReady(ns, "r3") })

	rack := fetchRack(ns, "r3")
	if !rack.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if n := stub.countByKey("r3"); n != 1 {
		t.Errorf("%d racks named r3, want 1: it was duplicated rather than adopted", n)
	}
}

// TestRackWithAnUnresolvableSiteWritesNothing is NBO-015's shape on this kind, and the
// assertion is on the recorded traffic rather than on the status.
//
// Every candidate reads `site_id` or `location_id`, so an unresolved `siteRef` leaves the engine
// with no identity at all. A version that reported the reference and then created the rack
// anyway would look identical in the conditions -- and would have written a rack into whichever
// site NetBox defaulted to.
func TestRackWithAnUnresolvableSiteWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackKind)
	endpointWithoutResync(t, ns, target)

	makeRack(t, ns, "r4", func(r *netboxv1alpha1.NetBoxRack) {
		// A NetBoxSite that does not exist. `name` is the only mode the operator can wait on,
		// which is why an unresolvable reference is tested in it.
		r.Spec.SiteRef = netboxv1alpha1.SiteRef{Name: "nowhere"}
	})

	eventually(t, "the rack to report that its site does not exist", func() bool {
		rack := fetchRack(ns, "r4")
		if rack == nil {
			return false
		}

		for _, c := range rack.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionRefsResolved {
				return c.Reason == netboxv1alpha1.ReasonRefNotFound
			}
		}

		return false
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox writes = %v, want none: no candidate is applicable without site_id "+
			"or location_id", got)
	}

	if rack := fetchRack(ns, "r4"); rack.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: nothing was created", rack.Status.ID)
	}
}

// TestRackAirflowIsClearedWithNull is the EmptyIsNull half of the descriptor, on the wire.
//
// `airflow` is `blank=True, null=True` and NetBox's serializer returns `null` rather than `""`
// for an unset choice (docs/netbox-schema.md -> dcim.Rack). Sent as `""` the value would differ
// from the value read back on every pass, which is a PATCH loop rather than an error -- so the
// empty spec value has to leave as JSON null (#170, registry.Field.EmptyIsNull).
func TestRackAirflowIsClearedWithNull(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackKind)
	readyEndpoint(t, ns, target)

	makeRack(t, ns, "r5", func(r *netboxv1alpha1.NetBoxRack) {
		r.Spec.Airflow = ""
		r.Spec.FormFactor = ""
	})

	eventually(t, "the rack to be Ready", func() bool { return rackIsReady(ns, "r5") })

	create := stub.recorded()[0]
	for _, column := range []string{"airflow", "form_factor"} {
		value, present := create.Payload[column]
		if !present {
			// Absent is legitimate: an empty string the user never claimed is not managed at
			// all, and field ownership is what tells the two apart. What must not happen is
			// the column going out as "".
			continue
		}

		if value != nil {
			t.Errorf("create payload %s = %#v, want nil: an unset choice is cleared with null",
				column, value)
		}
	}
}

// TestRackRoleRoundTripsAndDoesNotHotLoop is the apply round trip for the plainest kind in
// NBO-051, plus the steady state that proves its `slug` lookup finds what it just created.
//
// `color` is the shape worth watching: it is defaulted by the CRD so it reaches every payload,
// and a defaulted field compared wrongly is a difference found on every pass.
func TestRackRoleRoundTripsAndDoesNotHotLoop(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, rackRoleKind)
	readyEndpoint(t, ns, target)

	role := &netboxv1alpha1.NetBoxRackRole{
		ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRackRoleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Compute",
			Slug:             "compute",
		},
	}
	if err := k8sClient.Create(context.Background(), role); err != nil {
		t.Fatalf("creating rack role: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), role) })

	eventually(t, "the rack role to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxRackRole{}, ns, "compute")
	})

	fetched := &netboxv1alpha1.NetBoxRackRole{}
	key := client.ObjectKey{Namespace: ns, Name: "compute"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching rack role: %v", err)
	}

	if live := stub.get(fetched.Status.ID); live["color"] != "9e9e9e" {
		t.Errorf("netbox color = %v, want the CRD default 9e9e9e reaching the payload",
			live["color"])
	}

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d", got, writesAfterCreate)
	}
}

// TestRackTypeWithoutAManufacturerIsRejectedByTheAPIServer is the acceptance criterion that the
// requirement is schema, not condition.
//
// `manufacturer` is `REQ` on dcim.RackType and both natural keys start at it, so a rack type
// without one has no identity at all. Rejecting it at admission is what turns that into a
// message on `kubectl apply` instead of an object that sits at Ready=False forever.
//
// Applied as unstructured, because the Go struct cannot express the case: `ManufacturerRef` is
// a value rather than a pointer and marshals to `{}`, which the CEL rules reject for a
// different reason than the one under test.
func TestRackTypeWithoutAManufacturerIsRejectedByTheAPIServer(t *testing.T) {
	ns := newNamespace(t)

	err := apiClient.Create(context.Background(), rackTypeManifest(ns, map[string]any{
		"endpointRef": "homelab",
		"model":       "MCS 42U",
		"slug":        "mcs-42u",
		"formFactor":  "4-post-cabinet",
	}), client.DryRunAll)
	if err == nil {
		t.Fatal("a NetBoxRackType with no manufacturerRef was accepted; the field is required")
	}

	if !strings.Contains(err.Error(), "manufacturerRef") {
		t.Errorf("rejection = %v, want it to name manufacturerRef", err)
	}
}

// TestRackTypeWithAnEmptyFormFactorIsRejected is the other half of that requirement, and the
// reason it needs a CEL rule of its own.
//
// `dcim.RackType.form_factor` is NOT NULL with no default while `dcim.Rack.form_factor` is
// `blank=True, null=True` (docs/netbox-schema.md), so RackFormFactor carries `""` as a member
// for the rack's sake. Requiring the *field* therefore is not enough: `formFactor: ""` would
// satisfy the enum, pass admission, and be refused by Postgres on the INSERT.
func TestRackTypeWithAnEmptyFormFactorIsRejected(t *testing.T) {
	ns := newNamespace(t)

	err := apiClient.Create(context.Background(), rackTypeManifest(ns, map[string]any{
		"endpointRef":     "homelab",
		"manufacturerRef": map[string]any{"name": "minkels"},
		"model":           "MCS 42U",
		"slug":            "mcs-42u",
		"formFactor":      "",
	}), client.DryRunAll)
	if err == nil {
		t.Fatal("a NetBoxRackType with an empty formFactor was accepted")
	}

	if !strings.Contains(err.Error(), "formFactor") {
		t.Errorf("rejection = %v, want it to name formFactor", err)
	}
}

func rackTypeManifest(ns string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "netbox.kubeforge.org/v1alpha1",
		"kind":       "NetBoxRackType",
		"metadata":   map[string]any{"name": "mcs-42u", "namespace": ns},
		"spec":       spec,
	}}
}

// TestReservationWithoutAUserIsRejectedByTheAPIServer is the engine gap made visible at
// admission.
//
// `user` is `ForeignKey REQ -> settings.AUTH_USER_MODEL` and the `users` app has no Kind, so
// `spec.userID` is a literal NetBox primary key. It is required for a reason worth stating: the
// operator must never guess the token's own user, because that would silently book every
// reservation to its service account, and NetBox would refuse the create anyway.
func TestReservationWithoutAUserIsRejectedByTheAPIServer(t *testing.T) {
	ns := newNamespace(t)

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "netbox.kubeforge.org/v1alpha1",
		"kind":       "NetBoxRackReservation",
		"metadata":   map[string]any{"name": "cage-3", "namespace": ns},
		"spec": map[string]any{
			"endpointRef": "homelab",
			"rackRef":     map[string]any{"id": 1},
			"units":       []any{1, 2, 3},
			"description": "Reserved for the network team",
		},
	}}

	err := apiClient.Create(context.Background(), obj, client.DryRunAll)
	if err == nil {
		t.Fatal("a NetBoxRackReservation with no userID was accepted; the field is required")
	}

	if !strings.Contains(err.Error(), "userID") {
		t.Errorf("rejection = %v, want it to name userID", err)
	}
}

// TestReservationUnitsRoundTripInOrder is the units criterion, and it is where NBO-051's ticket
// and the engine disagree.
//
// The ticket asks for a set, so that reordering `units` produces no write. `units` is a Postgres
// ArrayField and NetBox returns it in stored order (docs/netbox-schema.md ->
// dcim.RackReservation), so registry.ClassArray is what ships: the array round-trips unchanged
// and unchanged input produces no PATCH, which is the half of the criterion that is true of the
// column. Comparing it order-independently would report two genuinely different server states
// as equal -- so a manifest should list the units sorted, and the reference page says so.
func TestReservationUnitsRoundTripInOrder(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newRackNetBoxStub(t, rackReservationKind)
	readyEndpoint(t, ns, target)

	reservation := &netboxv1alpha1.NetBoxRackReservation{
		ObjectMeta: metav1.ObjectMeta{Name: "cage-3", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRackReservationSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			RackRef:          netboxv1alpha1.RackRef{ID: idOf(41)},
			Units:            []int32{20, 21, 22},
			UserID:           4,
			Description:      "Reserved for the network team",
			Status:           netboxv1alpha1.RackReservationStatusActive,
		},
	}
	if err := k8sClient.Create(context.Background(), reservation); err != nil {
		t.Fatalf("creating reservation: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), reservation) })

	eventually(t, "the reservation to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxRackReservation{}, ns, "cage-3")
	})

	fetched := &netboxv1alpha1.NetBoxRackReservation{}
	key := client.ObjectKey{Namespace: ns, Name: "cage-3"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching reservation: %v", err)
	}

	live := stub.get(fetched.Status.ID)

	// `user` under NetBox's own name, from a plain value field rather than from a resolved
	// reference: there is no NetBoxUser Kind for the resolver to dispatch against.
	if live["user"] != float64(4) {
		t.Errorf("netbox user = %v, want 4", live["user"])
	}

	if live["rack"] != float64(41) {
		t.Errorf("netbox rack = %v, want 41", live["rack"])
	}

	want := []any{float64(20), float64(21), float64(22)}
	got, _ := live["units"].([]any)

	if len(got) != len(want) {
		t.Fatalf("netbox units = %v, want %v", live["units"], want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("netbox units[%d] = %v, want %v (the array is written in spec order)",
				i, got[i], want[i])
		}
	}

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if n := len(stub.recorded()); n != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: an unchanged array must not PATCH",
			n, writesAfterCreate)
	}
}
