package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The four stub kinds #54's module block needs, each keyed by the filter its identity leads
// with (docs/netbox-schema.md, the four models' meta.constraints -- or, twice over, the absence
// of one).
//
// A module type is keyed by `model` rather than by a slug, because `dcim.ModuleType` has no
// slug column. A module has neither: `dcim.Module` has no constraint at all and its lookup is
// `?module_bay_id=`, so `asset_tag` here serves only countByKey and `module_bay_id` is declared
// as a ref key so the stub matches the filter the engine actually sends.
var (
	moduleTypeProfileKind = stubKind{endpoint: "dcim/module-type-profiles", key: "name"}
	moduleTypeKind        = stubKind{endpoint: "dcim/module-types", key: "model"}
	moduleBayKind         = stubKind{endpoint: "dcim/module-bays", key: "name"}
	moduleKind            = stubKind{
		endpoint: "dcim/modules", key: "asset_tag", refKeys: []string{"module_bay_id"},
	}
)

// newModuleNetBoxStub is a module-family stub fronted by a handler that answers the reads an
// id-mode reference is verified against, the newRackNetBoxStub shape.
//
// A module points at three other endpoints and the shared stub serves one by design: it is
// parameterised by the kind under test, not by that kind's references. This adds the smallest
// thing that makes an id-mode ref resolvable, and deliberately cannot serve a *write*, so a
// test that accidentally started managing a Device or a ModuleType through this path fails
// rather than passing quietly.
func newModuleNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
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

// makeModuleBay applies a NetBoxModuleBay and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off.
//
// `deviceRef` is in `id` mode and set by default, because NetBox's column is `REQ` and the API
// server rejects the object without it. Id mode costs nothing here: what these tests assert is
// what reaches `dcim/module-bays`, and an id-mode ref renders through the same code a name-mode
// one ends up in.
func makeModuleBay(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxModuleBay)) {
	t.Helper()

	bay := &netboxv1alpha1.NetBoxModuleBay{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxModuleBaySpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			DeviceRef:        netboxv1alpha1.DeviceRef{ID: idOf(41)},
			Name:             name,
		},
	}
	if mutate != nil {
		mutate(bay)
	}

	if err := k8sClient.Create(context.Background(), bay); err != nil {
		t.Fatalf("creating module bay %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), bay) })
}

func fetchModuleBay(ns, name string) *netboxv1alpha1.NetBoxModuleBay {
	bay := &netboxv1alpha1.NetBoxModuleBay{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, bay); err != nil {
		return nil
	}

	return bay
}

func moduleBayIsReady(ns, name string) bool {
	bay := fetchModuleBay(ns, name)
	if bay == nil {
		return false
	}

	for _, c := range bay.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestModuleTypeWritesAttributesUnderTheSerializerName is the round trip for the catalogue
// kind, and the assertion is on a column name rather than on a value.
//
// The model column is `attribute_data` and the serializer's field is `attributes`
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleTypeSerializer). NetBox drops a field name
// it does not know rather than rejecting it, so a payload carrying `attribute_data` would come
// back 201 with the attributes unset, the next reconcile would find the same difference, and
// nothing in the conditions would say so. Both halves are asserted: `attributes` present and
// carrying the document, `attribute_data` absent from every request.
//
// The document's `value` key is the point rather than decoration: it is what the scalar
// comparison would unwrap the whole document down to, which is why the column is ClassJSON.
func TestModuleTypeWritesAttributesUnderTheSerializerName(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleTypeKind)
	readyEndpoint(t, ns, target)

	moduleType := &netboxv1alpha1.NetBoxModuleType{
		ObjectMeta: metav1.ObjectMeta{Name: "sfp-10g-lr", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxModuleTypeSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			ManufacturerRef:  netboxv1alpha1.ManufacturerRef{ID: idOf(41)},
			Model:            "SFP-10G-LR",
			ProfileRef:       &netboxv1alpha1.ModuleTypeProfileRef{ID: idOf(7)},
			PartNumber:       "SFP-10G-LR=",
			Airflow:          netboxv1alpha1.ModuleAirflowPassive,
			Attributes: &netboxv1alpha1.JSONDocument{
				Raw: []byte(`{"form_factor":"SFP+","value":10,"optics":{"reach_km":10}}`),
			},
			Weight:     "0.08",
			WeightUnit: netboxv1alpha1.WeightUnitKilograms,
		},
	}
	if err := k8sClient.Create(context.Background(), moduleType); err != nil {
		t.Fatalf("creating module type: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), moduleType) })

	eventually(t, "the module type to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxModuleType{}, ns, "sfp-10g-lr")
	})

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	post := writes[0]
	if post.Method != http.MethodPost {
		t.Fatalf("the first request was %s, want POST", post.Method)
	}

	document, ok := post.Payload["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("attributes = %#v, want the JSON document itself", post.Payload["attributes"])
	}

	if document["form_factor"] != "SFP+" {
		t.Errorf("attributes.form_factor = %#v, want \"SFP+\": the key survived neither "+
			"admission nor the payload build", document["form_factor"])
	}

	for i, write := range writes {
		if _, present := write.Payload["attribute_data"]; present {
			t.Errorf("request %d (%s) carries `attribute_data`: that is the model column, and "+
				"the serializer's field is `attributes` -- NetBox drops the key rather than "+
				"rejecting it: %v", i, write.Method, write.Payload)
		}
	}

	fetched := &netboxv1alpha1.NetBoxModuleType{}
	key := client.ObjectKey{Namespace: ns, Name: "sfp-10g-lr"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching module type: %v", err)
	}

	// The lookup NetBox was actually asked, recorded on the object. `(manufacturer, model)` is
	// the only candidate this kind has -- there is no slug column to fall back to.
	want := map[string]string{"manufacturer_id": "41", "model": "SFP-10G-LR"}
	if !reflect.DeepEqual(fetched.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v", fetched.Status.NaturalKey, want)
	}

	live := stub.get(fetched.Status.ID)
	for column, wantValue := range map[string]any{
		"manufacturer": float64(41), "profile": float64(7),
		"part_number": "SFP-10G-LR=", "airflow": "passive",
		"weight": "0.08", "weight_unit": "kg",
	} {
		if live[column] != wantValue {
			t.Errorf("netbox %s = %v, want %v", column, live[column], wantValue)
		}
	}

	// The steady state. `weight` is a DecimalField NetBox returns padded, and `attributes` is
	// a document whose `value` key the scalar rule would unwrap -- either compared wrongly is
	// a PATCH on every pass rather than an error.
	stub.setField(fetched.Status.ID, "weight", "0.08")

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: neither the padded decimal nor the "+
			"JSON document is a difference", got, writesAfterCreate)
	}
}

// TestChassisModuleBayPinsModuleIDToNull is the identity NetBox declares and does not enforce,
// observed on the wire.
//
// `dcim.ModuleBay`'s only constraint is `(device, module, name)` and `module` is nullable, so
// Postgres does not stop two identically named bays on one chassis. The `(device, name)`
// candidate with `module_id` pinned null is the convention that does -- and the pin is what
// makes it safe, because without it the lookup would also match a bay of that name on some
// module of the same device.
//
// `?module_id=null` rather than an omitted filter is the whole assertion: an omitted filter is
// not a narrower query, it is a wider one, and that is the class of defect behind #206 and
// #216.
func TestChassisModuleBayPinsModuleIDToNull(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleBayKind)
	readyEndpoint(t, ns, target)

	makeModuleBay(t, ns, "slot-1", func(b *netboxv1alpha1.NetBoxModuleBay) {
		b.Spec.Position = "1"
		b.Spec.Label = "SLOT1"
	})

	eventually(t, "the module bay to be Ready", func() bool { return moduleBayIsReady(ns, "slot-1") })

	bay := fetchModuleBay(ns, "slot-1")

	want := map[string]string{"device_id": "41", "name": "slot-1", "module_id": "null"}
	if !reflect.DeepEqual(bay.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v: a chassis bay is looked up with module_id "+
			"pinned null, not with module_id omitted", bay.Status.NaturalKey, want)
	}

	live := stub.get(bay.Status.ID)
	for column, wantValue := range map[string]any{
		"device": float64(41), "name": "slot-1", "position": "1", "label": "SLOT1",
	} {
		if live[column] != wantValue {
			t.Errorf("netbox %s = %v, want %v", column, live[column], wantValue)
		}
	}

	// Absent from every payload: NetBox derives `parent` from `module.module_bay` and does not
	// accept it, `installed_module` is the reverse accessor NetBoxModule owns, and `_occupied`
	// and the three ComponentModel caches are computed. Writing any of them is a key NetBox
	// drops in silence.
	for _, write := range stub.recorded() {
		for _, column := range []string{
			"parent", "installed_module", "_occupied", "_site", "_location", "_rack",
		} {
			if _, present := write.Payload[column]; present {
				t.Errorf("a %s request carries %q, which NetBox derives rather than accepts: %v",
					write.Method, column, write.Payload)
			}
		}
	}
}

// TestModuleBayOnAModuleUsesTheThreeColumnConstraint is the other half of the same identity.
//
// With `moduleRef` set the database constraint applies verbatim and the null pin must not: a
// bay on a line card is looked up by all three columns, and a query that pinned `module_id` to
// null as well would match nothing and create a duplicate on every reconcile.
func TestModuleBayOnAModuleUsesTheThreeColumnConstraint(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleBayKind)
	readyEndpoint(t, ns, target)

	makeModuleBay(t, ns, "sub-1", func(b *netboxv1alpha1.NetBoxModuleBay) {
		b.Spec.ModuleRef = &netboxv1alpha1.ModuleRef{ID: idOf(88)}
	})

	eventually(t, "the module bay to be Ready", func() bool { return moduleBayIsReady(ns, "sub-1") })

	bay := fetchModuleBay(ns, "sub-1")

	want := map[string]string{"device_id": "41", "module_id": "88", "name": "sub-1"}
	if !reflect.DeepEqual(bay.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v", bay.Status.NaturalKey, want)
	}

	if live := stub.get(bay.Status.ID); live["module"] != float64(88) {
		t.Errorf("netbox module = %v, want 88: `module` is the module that provides the bay, "+
			"and it is a writable forward foreign key", live["module"])
	}
}

// TestModuleBayWhoseModuleIsUnresolvableWritesNothing is NBO-015's shape on this kind, and the
// assertion is on the recorded traffic rather than on the status.
//
// A bay whose `moduleRef` names a NetBoxModule that does not exist has *no* applicable
// candidate: candidate 1 needs the reference resolved and candidate 2 needs it undeclared. The
// engine has to wait. A version that fell through to the null-pinned convention would look
// identical in the conditions -- and would have adopted the chassis bay of the same name and
// then PATCHed it off the card.
func TestModuleBayWhoseModuleIsUnresolvableWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleBayKind)
	endpointWithoutResync(t, ns, target)

	// Seed the chassis bay the wrong answer would find and adopt.
	stub.seed(netbox.Object{"name": "slot-2", "device": float64(41), "module": nil})

	makeModuleBay(t, ns, "slot-2", func(b *netboxv1alpha1.NetBoxModuleBay) {
		// A NetBoxModule that does not exist. `name` is the only mode the operator can wait
		// on, which is why an unresolvable reference is tested in it.
		b.Spec.ModuleRef = &netboxv1alpha1.ModuleRef{Name: "nowhere"}
	})

	eventually(t, "the module bay to report that its module does not exist", func() bool {
		bay := fetchModuleBay(ns, "slot-2")
		if bay == nil {
			return false
		}

		for _, c := range bay.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionRefsResolved {
				return c.Reason == netboxv1alpha1.ReasonRefNotFound
			}
		}

		return false
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox writes = %v, want none: no candidate is applicable while moduleRef "+
			"is declared and unresolved", got)
	}

	if bay := fetchModuleBay(ns, "slot-2"); bay.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: the seeded chassis bay must not be adopted",
			bay.Status.ID)
	}
}

// TestChassisModuleBayIsAdoptedNotDuplicated is the acceptance criterion for the identity NetBox
// does not enforce, and the one #54 calls the most likely bug in the ticket.
//
// NetBox instantiates a device type's module-bay templates when a device is created from it, so
// the rows a manifest describes routinely exist already and are not CRs. Adopting one rather
// than creating a second is what stops two `slot-3`s on one switch.
func TestChassisModuleBayIsAdoptedNotDuplicated(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleBayKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{"name": "slot-3", "device": float64(41), "module": nil})

	makeModuleBay(t, ns, "slot-3", func(b *netboxv1alpha1.NetBoxModuleBay) {
		b.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the module bay to be Ready", func() bool { return moduleBayIsReady(ns, "slot-3") })

	bay := fetchModuleBay(ns, "slot-3")
	if !bay.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if n := stub.countByKey("slot-3"); n != 1 {
		t.Errorf("%d module bays named slot-3, want 1: it was duplicated rather than adopted", n)
	}
}

// TestModuleIsKeyedOnItsBayAlone is the OneToOneField identity, end to end.
//
// `dcim.Module` declares no `meta.constraints` and needs none: `module_bay` is a
// `OneToOneField`, which is a `ForeignKey` with `unique=True`, so the database already holds
// at most one module per bay. The recorded lookup is the assertion -- one filter and no others.
// Adding `device_id` would narrow the query below what the database enforces, and `asset_tag`
// is globally unique and deliberately not a candidate.
func TestModuleIsKeyedOnItsBayAlone(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleKind)
	readyEndpoint(t, ns, target)

	module := &netboxv1alpha1.NetBoxModule{
		ObjectMeta: metav1.ObjectMeta{Name: "slot-1-optic", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxModuleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			DeviceRef:        netboxv1alpha1.DeviceRef{ID: idOf(41)},
			ModuleBayRef:     netboxv1alpha1.ModuleBayRef{ID: idOf(7)},
			ModuleTypeRef:    netboxv1alpha1.ModuleTypeRef{ID: idOf(9)},
			Status:           netboxv1alpha1.ModuleStatusActive,
			Serial:           "FNS12345678",
			AssetTag:         "ASSET-SFP-0001",
		},
	}
	if err := k8sClient.Create(context.Background(), module); err != nil {
		t.Fatalf("creating module: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), module) })

	eventually(t, "the module to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxModule{}, ns, "slot-1-optic")
	})

	fetched := &netboxv1alpha1.NetBoxModule{}
	key := client.ObjectKey{Namespace: ns, Name: "slot-1-optic"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching module: %v", err)
	}

	want := map[string]string{"module_bay_id": "7"}
	if !reflect.DeepEqual(fetched.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v: the bay is the whole key",
			fetched.Status.NaturalKey, want)
	}

	live := stub.get(fetched.Status.ID)
	for column, wantValue := range map[string]any{
		"device": float64(41), "module_bay": float64(7), "module_type": float64(9),
		"serial": "FNS12345678", "asset_tag": "ASSET-SFP-0001",
	} {
		if live[column] != wantValue {
			t.Errorf("netbox %s = %v, want %v", column, live[column], wantValue)
		}
	}

	// `status` is asserted on the payload rather than on the stored object, because the stub
	// reads a choice back the way NetBox does -- `{"value": ..., "label": ...}` -- which is
	// exactly the nesting internal/netbox/drift.go's unwrapNested reduces on the way in. What
	// matters here is that the bare value goes out.
	if create := stub.recorded()[0]; create.Payload["status"] != "active" {
		t.Errorf("create payload status = %#v, want \"active\": NetBox takes the bare value",
			create.Payload["status"])
	}

	// The two write-only action flags are never sent. Neither can be read back, so either one
	// in the payload would be a key that never appears in the response.
	for i, write := range stub.recorded() {
		for _, column := range []string{"replicate_components", "adopt_components"} {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q, which is write-only and cannot be "+
					"diffed: %v", i, write.Method, column, write.Payload)
			}
		}
	}

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d", got, writesAfterCreate)
	}
}

// TestModuleAssetTagIsClearedWithNullOnTheWire is the EmptyIsNull half of the descriptor,
// observed rather than reasoned about.
//
// `asset_tag` is `UNIQUE` and `null=True` (docs/netbox-schema.md -> dcim.Module). Cleared to
// `""` rather than to null, two modules whose tag was removed would collide on the unique index
// -- and NetBox's serializer returns `null` for an unset one, so the value would differ from
// the value read back on every pass as well (#170).
func TestModuleAssetTagIsClearedWithNullOnTheWire(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newModuleNetBoxStub(t, moduleKind)
	readyEndpoint(t, ns, target)

	module := &netboxv1alpha1.NetBoxModule{
		ObjectMeta: metav1.ObjectMeta{Name: "slot-2-optic", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxModuleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			DeviceRef:        netboxv1alpha1.DeviceRef{ID: idOf(41)},
			ModuleBayRef:     netboxv1alpha1.ModuleBayRef{ID: idOf(8)},
			ModuleTypeRef:    netboxv1alpha1.ModuleTypeRef{ID: idOf(9)},
			AssetTag:         "",
		},
	}
	if err := k8sClient.Create(context.Background(), module); err != nil {
		t.Fatalf("creating module: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), module) })

	eventually(t, "the module to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxModule{}, ns, "slot-2-optic")
	})

	create := stub.recorded()[0]

	value, present := create.Payload["asset_tag"]
	if !present {
		// Absent is legitimate: an empty string the user never claimed is not managed at all,
		// and field ownership is what tells the two apart. What must not happen is the column
		// going out as "".
		return
	}

	if value != nil {
		t.Errorf("create payload asset_tag = %#v, want nil: the column is UNIQUE and "+
			"null=True, so an emptied one is cleared with null", value)
	}
}

// TestModuleWithoutItsRequiredReferencesIsRejectedByTheAPIServer is the acceptance criterion
// that the requirement is schema, not condition.
//
// All three of `device`, `module_bay` and `module_type` are `REQ` on dcim.Module, and
// `module_bay` is the natural key on top of that -- a module without one has no identity at
// all. Rejecting it at admission is what turns that into a message on `kubectl apply` instead
// of an object that sits at Ready=False forever.
//
// Applied as unstructured, because the Go struct cannot express the case: the three ref fields
// are values rather than pointers and marshal to `{}`, which the CEL rules reject for a
// different reason than the one under test.
func TestModuleWithoutItsRequiredReferencesIsRejectedByTheAPIServer(t *testing.T) {
	for _, missing := range []string{"deviceRef", "moduleBayRef", "moduleTypeRef"} {
		t.Run(missing, func(t *testing.T) {
			ns := newNamespace(t)

			spec := map[string]any{
				"endpointRef":   "homelab",
				"deviceRef":     map[string]any{"id": 1},
				"moduleBayRef":  map[string]any{"id": 2},
				"moduleTypeRef": map[string]any{"id": 3},
			}
			delete(spec, missing)

			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "netbox.kubeforge.org/v1alpha1",
				"kind":       "NetBoxModule",
				"metadata":   map[string]any{"name": "optic", "namespace": ns},
				"spec":       spec,
			}}

			err := apiClient.Create(context.Background(), obj, client.DryRunAll)
			if err == nil {
				t.Fatalf("a NetBoxModule with no %s was accepted; the field is required", missing)
			}

			if !strings.Contains(err.Error(), missing) {
				t.Errorf("rejection = %v, want it to name %s", err, missing)
			}
		})
	}
}

// TestModuleTypeProfileSchemaRoundTripsAndDoesNotHotLoop is the JSON column on the plainest
// kind in the block, plus the steady state that proves its `name` lookup finds what it created.
//
// A JSON Schema document is the worst case for the scalar comparison on purpose: it is nested,
// it contains a `type` key and it routinely contains `enum` and `properties` objects. ClassJSON
// is what compares it as a whole document, and a second write of a column nobody changed is
// the only way that failing would show.
func TestModuleTypeProfileSchemaRoundTripsAndDoesNotHotLoop(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, moduleTypeProfileKind)
	readyEndpoint(t, ns, target)

	profile := &netboxv1alpha1.NetBoxModuleTypeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "optic", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxModuleTypeProfileSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Optic",
			Description:      "Pluggable optical transceivers",
			Schema: &netboxv1alpha1.JSONDocument{
				Raw: []byte(`{"type":"object","properties":{"form_factor":{"type":"string"}}}`),
			},
		},
	}
	if err := k8sClient.Create(context.Background(), profile); err != nil {
		t.Fatalf("creating module type profile: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), profile) })

	eventually(t, "the module type profile to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxModuleTypeProfile{}, ns, "optic")
	})

	fetched := &netboxv1alpha1.NetBoxModuleTypeProfile{}
	key := client.ObjectKey{Namespace: ns, Name: "optic"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching module type profile: %v", err)
	}

	// `name` and nothing else: the model has no slug column, and its only unique is the
	// column-level one on `name`.
	if want := (map[string]string{"name": "Optic"}); !reflect.DeepEqual(fetched.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v", fetched.Status.NaturalKey, want)
	}

	live := stub.get(fetched.Status.ID)

	document, ok := live["schema"].(map[string]any)
	if !ok {
		t.Fatalf("netbox schema = %#v, want the JSON document itself", live["schema"])
	}

	if document["type"] != "object" {
		t.Errorf("netbox schema.type = %#v, want \"object\"", document["type"])
	}

	writesAfterCreate := len(stub.recorded())

	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: a JSON document compared with the "+
			"scalar rule would be a difference on every pass", got, writesAfterCreate)
	}
}
