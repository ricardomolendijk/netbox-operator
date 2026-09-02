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

// The three stub kinds NBO-057's tests need, each keyed by the filter its identity leads with
// (docs/netbox-schema.md, the five models' meta.constraints and column-level uniques).
//
// A circuit and a provider account are the first two kinds in this package whose identity is a
// reference *plus* a scalar and whose scalar is not globally unique, so both declare a
// `refKeys` entry: matching on `cid` alone would match a circuit sold by a different provider,
// which is precisely the mistake TestCircuitIsNotAdoptedAcrossProviders exists to catch. Get
// that wrong in the stub and the test passes while the operator is broken (#206).
var (
	providerKind        = stubKind{endpoint: "circuits/providers", key: "slug"}
	providerAccountKind = stubKind{
		endpoint: "circuits/provider-accounts", key: "account", refKeys: []string{"provider_id"},
	}
	circuitKind = stubKind{
		endpoint: "circuits/circuits", key: "cid", refKeys: []string{"provider_id"},
	}
)

// circuitTerminationPointers are keys that must never appear in a request body sent to
// `circuits/circuits`.
//
// The whole point of NBO-057's read-only decision, asserted in the negative. Both columns are
// real foreign keys back to circuits.CircuitTermination and both are `read_only` in the IR
// while still appearing in the serializer's write path, so DRF accepts a payload carrying one
// and drops it -- a difference the engine would find again on every reconcile.
var circuitTerminationPointers = []string{"termination_a", "termination_z", "_abs_distance"}

// newCircuitsNetBoxStub is a circuits-family stub fronted by a handler that answers the reads an
// id-mode reference is verified against, the newRackNetBoxStub shape.
//
// A circuit points at four other endpoints and the shared stub serves one by design: it is
// parameterised by the kind under test, not by that kind's references. This adds the smallest
// thing that makes an id-mode ref resolvable, and deliberately cannot serve a *write*, so a
// test that accidentally started managing a Provider through this path fails rather than
// passing quietly.
func newCircuitsNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
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

// makeCircuit applies a NetBoxCircuit and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off.
//
// `providerRef` and `typeRef` are in `id` mode and set by default, because both columns are
// `REQ` and the API server rejects the object without them. Id mode costs nothing here: what
// these tests assert is what reaches `circuits/circuits`, and an id-mode ref renders through
// the same code a name-mode one ends up in.
func makeCircuit(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxCircuit)) {
	t.Helper()

	circuit := &netboxv1alpha1.NetBoxCircuit{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxCircuitSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			CID:              name,
			ProviderRef:      netboxv1alpha1.ProviderRef{ID: idOf(41)},
			TypeRef:          netboxv1alpha1.CircuitTypeRef{ID: idOf(43)},
		},
	}
	if mutate != nil {
		mutate(circuit)
	}

	if err := k8sClient.Create(context.Background(), circuit); err != nil {
		t.Fatalf("creating circuit %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), circuit) })
}

func fetchCircuit(ns, name string) *netboxv1alpha1.NetBoxCircuit {
	circuit := &netboxv1alpha1.NetBoxCircuit{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, circuit); err != nil {
		return nil
	}

	return circuit
}

func circuitIsReady(ns, name string) bool {
	circuit := fetchCircuit(ns, name)
	if circuit == nil {
		return false
	}

	for _, c := range circuit.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestCircuitWritesItsColumnsAndNeverTheTerminationPointers is the round trip, and NBO-057's
// read-only acceptance criterion asserted on recorded requests rather than on a comment.
//
// The four foreign keys reaching the payload under NetBox's own names is the first half: a
// `providerAccountRef` sent as `providerAccountRef` would be dropped silently, which is why the
// field map is a table rather than a convention. The second half is the negative -- no request
// body may contain `termination_a` or `termination_z`, because NetBox writes those from the
// termination's side and two writers for one relationship is how you get flapping.
func TestCircuitWritesItsColumnsAndNeverTheTerminationPointers(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, circuitKind)
	readyEndpoint(t, ns, target)

	makeCircuit(t, ns, "100000123", func(c *netboxv1alpha1.NetBoxCircuit) {
		c.Spec.ProviderAccountRef = &netboxv1alpha1.ProviderAccountRef{ID: idOf(45)}
		c.Spec.TenantRef = &netboxv1alpha1.TenantRef{ID: idOf(47)}
		c.Spec.InstallDate = "2026-01-15"
		c.Spec.CommitRate = ptrTo(int32(1000000))
		c.Spec.Distance = "12.5"
		c.Spec.DistanceUnit = netboxv1alpha1.DistanceUnitKilometer
		c.Spec.Description = "Primary transit into AMS"
	})

	eventually(t, "the circuit to be Ready", func() bool { return circuitIsReady(ns, "100000123") })

	circuit := fetchCircuit(ns, "100000123")
	if circuit.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready circuit")
	}

	live := stub.get(circuit.Status.ID)

	for column, want := range map[string]any{
		"provider": float64(41), "type": float64(43),
		"provider_account": float64(45), "tenant": float64(47),
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	for column, want := range map[string]any{
		"cid": "100000123", "install_date": "2026-01-15",
		"commit_rate": float64(1000000), "distance": "12.5", "distance_unit": "km",
	} {
		if live[column] != want {
			t.Errorf("netbox %s = %v, want %v", column, live[column], want)
		}
	}

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	// The CRD default reaching the payload. Asserted on the request rather than on the stored
	// object because the stub reproduces NetBox's read shape for a choice column --
	// `{"value","label"}` -- and what is under test here is what the operator *sent*: a
	// defaulted field that never reaches a payload is a field the operator can never correct.
	if got := writes[0].Payload["status"]; got != "active" {
		t.Errorf("the create carried status = %v, want the CRD default \"active\"", got)
	}

	for i, write := range writes {
		for _, column := range circuitTerminationPointers {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: circuits.Circuit writes it read-only from "+
					"the termination's side, and DRF drops the key rather than rejecting it: %v",
					i, write.Method, column, write.Payload)
			}
		}
	}
}

// TestCircuitIsAdoptedByProviderAndCID is the identity, taken over rather than duplicated.
//
// `(provider, cid)` is the one candidate this kind declares, and it is a real UniqueConstraint
// rather than a convention (docs/netbox-schema.md -> circuits.Circuit.meta.constraints), so a
// pre-existing row with the same pair is the same circuit.
func TestCircuitIsAdoptedByProviderAndCID(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, circuitKind)
	readyEndpoint(t, ns, target)

	seeded := stub.seed(netbox.Object{
		"cid": "100000200", "provider": float64(41), "type": float64(43),
	})

	makeCircuit(t, ns, "100000200", func(c *netboxv1alpha1.NetBoxCircuit) {
		c.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the circuit to be Ready", func() bool { return circuitIsReady(ns, "100000200") })

	circuit := fetchCircuit(ns, "100000200")
	if !circuit.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if circuit.Status.ID != seeded {
		t.Errorf("status.id = %d, want the seeded %d: the pair is a UniqueConstraint, so a "+
			"second row would be refused by NetBox", circuit.Status.ID, seeded)
	}
}

// TestCircuitIsNotAdoptedAcrossProviders is the reason this kind declares one candidate and not
// the two its `meta.constraints` offer.
//
// `(provider_account, cid)` is a real UniqueConstraint and the IR calls it usable, so nothing
// forced the narrowing. What decided it is that candidates are tried in order: it could only
// ever fire after `(provider, cid)` matched nothing, and the object it would find then is by
// construction a circuit sold by a **different provider**. Adopting that and PATCHing
// `provider` silently repoints somebody else's circuit -- the class of defect behind #206 and
// #216.
//
// The seeded row here shares the account and the cid and differs on the provider, which is
// exactly the state a second candidate would have matched. The operator must create instead.
func TestCircuitIsNotAdoptedAcrossProviders(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, circuitKind)
	readyEndpoint(t, ns, target)

	seeded := stub.seed(netbox.Object{
		"cid": "100000300", "provider": float64(99), "provider_account": float64(45),
		"type": float64(43),
	})

	makeCircuit(t, ns, "100000300", func(c *netboxv1alpha1.NetBoxCircuit) {
		c.Spec.ProviderAccountRef = &netboxv1alpha1.ProviderAccountRef{ID: idOf(45)}
		c.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the circuit to be Ready", func() bool { return circuitIsReady(ns, "100000300") })

	circuit := fetchCircuit(ns, "100000300")
	if circuit.Status.ID == seeded {
		t.Fatalf("the circuit adopted object %d, which belongs to provider 99; the "+
			"(provider_account, cid) constraint is deliberately not a natural-key candidate",
			seeded)
	}

	if circuit.Status.Adopted {
		t.Error("status.adopted is true; there was no object with this (provider, cid) to adopt")
	}

	// And the object it did create carries its own provider, not the seeded one.
	if live := stub.get(circuit.Status.ID); live["provider"] != float64(41) {
		t.Errorf("netbox provider = %v, want 41", live["provider"])
	}
}

// TestCircuitDoesNotHotLoopOnItsDecimalDistance is the steady state, and the one field that
// could break it on its own.
//
// `distance` is a DecimalField NetBox returns padded to two places, so a spec that said `"12.5"`
// reads back as `"12.50"`. Compared as strings that is a difference on every pass and a PATCH
// forever; the engine compares two numeric strings numerically
// (internal/netbox/drift.go, scalarEqual), which is what this asserts from the outside.
func TestCircuitDoesNotHotLoopOnItsDecimalDistance(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, circuitKind)
	readyEndpoint(t, ns, target)

	makeCircuit(t, ns, "100000400", func(c *netboxv1alpha1.NetBoxCircuit) {
		c.Spec.Distance = "12.5"
		c.Spec.DistanceUnit = netboxv1alpha1.DistanceUnitKilometer
	})

	eventually(t, "the circuit to be Ready", func() bool { return circuitIsReady(ns, "100000400") })

	circuit := fetchCircuit(ns, "100000400")
	stub.setField(circuit.Status.ID, "distance", "12.50")

	writesAfterCreate := len(stub.recorded())

	// Wait out several resync intervals. There is no way to observe a hot loop other than
	// letting time pass: a single reconcile finding a spurious difference looks identical to
	// one finding a real one.
	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: \"12.5\" and \"12.50\" are the same "+
			"decimal and must not produce a PATCH", got, writesAfterCreate)
	}
}

// TestProviderAccountIsScopedToItsProvider is the `(provider, account)` identity, and the
// assertion that the reference half of it is actually sent.
//
// `account` carries no column-level UNIQUE, so two providers may bill you under the same
// number. A lookup on `account` alone would find the wrong one -- and would find it silently,
// because django-filter answers an unrecognised parameter with the unfiltered set.
func TestProviderAccountIsScopedToItsProvider(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, providerAccountKind)
	readyEndpoint(t, ns, target)

	other := stub.seed(netbox.Object{"account": "EU-4417", "provider": float64(99)})

	account := &netboxv1alpha1.NetBoxProviderAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "ntt-eu", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxProviderAccountSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{
				EndpointRef: "homelab", OnConflict: netboxv1alpha1.ConflictAdopt,
			},
			ProviderRef: netboxv1alpha1.ProviderRef{ID: idOf(41)},
			Account:     "EU-4417",
			Name:        "EU billing account",
		},
	}
	if err := k8sClient.Create(context.Background(), account); err != nil {
		t.Fatalf("creating provider account: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), account) })

	eventually(t, "the provider account to be Ready", func() bool {
		got := &netboxv1alpha1.NetBoxProviderAccount{}
		if err := k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "ntt-eu"}, got); err != nil {
			return false
		}

		for _, c := range got.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionReady {
				return c.Status == metav1.ConditionTrue
			}
		}

		return false
	})

	got := &netboxv1alpha1.NetBoxProviderAccount{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "ntt-eu"}, got); err != nil {
		t.Fatalf("fetching the provider account: %v", err)
	}

	if got.Status.ID == other {
		t.Fatalf("the account adopted object %d, which belongs to provider 99: the identity is "+
			"(provider, account) and `account` alone is not unique", other)
	}

	live := stub.get(got.Status.ID)
	if live["provider"] != float64(41) {
		t.Errorf("netbox provider = %v, want 41", live["provider"])
	}

	// `name` is written even though it is deliberately not part of the identity: unusable as a
	// key is not the same as unmanaged.
	if live["name"] != "EU billing account" {
		t.Errorf("netbox name = %v, want the spec's value", live["name"])
	}
}

// TestProviderWritesItsASNsAsAnIDList covers the one to-many reference in this slice.
//
// `asns` is a ManyToManyField onto ipam.ASN, so it reaches the payload as a list of NetBox ids
// rather than as a nested object, and the comparison is an order-independent set. Getting the
// class wrong is silent in both directions -- an array comparison would PATCH forever, and a
// scalar one would never settle.
func TestProviderWritesItsASNsAsAnIDList(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newCircuitsNetBoxStub(t, providerKind)
	readyEndpoint(t, ns, target)

	provider := &netboxv1alpha1.NetBoxProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ntt", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxProviderSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "NTT Communications",
			Slug:             "ntt",
			ASNs: []netboxv1alpha1.ASNRef{
				{ID: idOf(51)}, {ID: idOf(52)},
			},
		},
	}
	if err := k8sClient.Create(context.Background(), provider); err != nil {
		t.Fatalf("creating provider: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), provider) })

	eventually(t, "the provider to be Ready", func() bool {
		got := &netboxv1alpha1.NetBoxProvider{}
		if err := k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "ntt"}, got); err != nil {
			return false
		}

		for _, c := range got.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionReady {
				return c.Status == metav1.ConditionTrue
			}
		}

		return false
	})

	got := &netboxv1alpha1.NetBoxProvider{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "ntt"}, got); err != nil {
		t.Fatalf("fetching the provider: %v", err)
	}

	live := stub.get(got.Status.ID)

	ids := netbox.IDsOf(live["asns"])
	if len(ids) != 2 || ids[0] != 51 || ids[1] != 52 {
		t.Errorf("netbox asns = %v (ids %v), want the two referenced ids", live["asns"], ids)
	}

	if live["slug"] != "ntt" {
		t.Errorf("netbox slug = %v, want ntt", live["slug"])
	}
}

// TestCircuitWithoutItsRequiredRefsIsRejectedByTheAPIServer keeps two NOT NULL columns from
// becoming a 400 three steps later.
//
// `provider` and `type` are both `REQ` on circuits.Circuit. Applied as unstructured, because the
// Go struct cannot express the case: `ProviderRef` and `TypeRef` are values rather than
// pointers and marshal to `{}`, which the CEL rules on ObjectRef reject for a different reason
// than the one under test.
func TestCircuitWithoutItsRequiredRefsIsRejectedByTheAPIServer(t *testing.T) {
	ns := newNamespace(t)

	for name, spec := range map[string]map[string]any{
		"no providerRef": {
			"endpointRef": "homelab",
			"cid":         "100000500",
			"typeRef":     map[string]any{"name": "transit"},
		},
		"no typeRef": {
			"endpointRef": "homelab",
			"cid":         "100000500",
			"providerRef": map[string]any{"name": "ntt"},
		},
		"no cid": {
			"endpointRef": "homelab",
			"providerRef": map[string]any{"name": "ntt"},
			"typeRef":     map[string]any{"name": "transit"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := apiClient.Create(context.Background(), &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "netbox.kubeforge.org/v1alpha1",
					"kind":       "NetBoxCircuit",
					"metadata":   map[string]any{"name": "c1", "namespace": ns},
					"spec":       spec,
				},
			}, client.DryRunAll)
			if err == nil {
				t.Fatal("the circuit was accepted; all three fields are required")
			}
		})
	}
}

// TestCircuitTypeColorIsNotDefaulted is the one place NBO-057 differs from NBO-051 on a field
// that looks identical, so it is asserted rather than left to a comment.
//
// `dcim.RackRole.color` carries `def=UNRESOLVED:ColorChoices.COLOR_GREY` and its CRD defaults to
// `9e9e9e`. `circuits.BaseCircuitType.color` carries `blank=True` and **no Django default**
// (hack/testdata/ir-4.6.8.json.gz -> circuits.CircuitType, field `color`), so defaulting it here
// would invent a value NetBox does not have and PATCH it onto every adopted circuit type.
func TestCircuitTypeColorIsNotDefaulted(t *testing.T) {
	ns := newNamespace(t)

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "netbox.kubeforge.org/v1alpha1",
		"kind":       "NetBoxCircuitType",
		"metadata":   map[string]any{"name": "transit", "namespace": ns},
		"spec": map[string]any{
			"endpointRef": "homelab",
			"name":        "Transit",
			"slug":        "transit",
		},
	}}

	if err := apiClient.Create(context.Background(), obj, client.DryRunAll); err != nil {
		t.Fatalf("a NetBoxCircuitType with no color was rejected: %v", err)
	}

	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if colour, present := spec["color"]; present {
		t.Errorf("admission defaulted color to %v; BaseCircuitType.color has no Django default, "+
			"so the CRD must not invent one", colour)
	}
}

// TestCircuitTypeRejectsAMalformedColour is the other half of that: undefaulted is not
// unvalidated. The pattern admits six lowercase hex digits, or the empty string that clears the
// column -- a CharField takes the empty string, so there is no EmptyIsNull here.
func TestCircuitTypeRejectsAMalformedColour(t *testing.T) {
	ns := newNamespace(t)

	err := apiClient.Create(context.Background(), &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "netbox.kubeforge.org/v1alpha1",
			"kind":       "NetBoxCircuitType",
			"metadata":   map[string]any{"name": "transit", "namespace": ns},
			"spec": map[string]any{
				"endpointRef": "homelab",
				"name":        "Transit",
				"slug":        "transit",
				"color":       "#2196F3",
			},
		},
	}, client.DryRunAll)
	if err == nil {
		t.Fatal("`#2196F3` was accepted; a ColorField is six lowercase hex digits with no hash")
	}

	if !strings.Contains(err.Error(), "color") {
		t.Errorf("rejection = %v, want it to name color", err)
	}
}

func ptrTo[T any](v T) *T { return &v }
