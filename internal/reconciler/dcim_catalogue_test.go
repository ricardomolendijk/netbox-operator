package reconciler

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// NBO-027's conditional natural keys, asserted on the request the engine would have put on
// the wire rather than on the outcome.
//
// The two shapes this ticket exists to keep apart are one edit away from each other and NetBox
// answers both with a 200: dcim.DeviceRole scopes its uniqueness by `parent` and dcim.Platform
// by `manufacturer` (docs/netbox-schema.md, both models' meta.constraints). A lookup that pins
// the wrong column, or omits it, finds somebody else's object and adopts it -- so the
// assertion has to be the query, not the fact that something was found.

// catalogueEngine assembles an engine around one registered NBO-027 descriptor.
//
// The registered descriptor rather than a fixture, because "adding a Kind needs no engine
// change" is only tested when the Kind under test is the shipped one.
func catalogueEngine(t *testing.T, kind string, nb NetBoxClient, refs RefResolver) *Engine {
	t.Helper()

	d, ok := registry.Get(netboxv1alpha1.GroupVersion.WithKind(kind))
	if !ok {
		t.Fatalf("%s is not registered", kind)
	}

	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() = %v", err)
	}

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: d, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        refs,
		Status:      &fakeStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      scheme,
	}
}

// blockedOnField is blockedOnParent for a field other than `parentRef`: the resolution the
// resolver hands back when one declared reference did not resolve.
func blockedOnField(field string, cause error) resolver.Resolution {
	err := refError(field, cause)

	return resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{},
		Blocked: []resolver.Blocker{{
			Field: field, Reason: resolver.Classify(err).Reason, Err: err,
		}},
	}
}

// deviceRole is a top-level role unless mutate says otherwise.
func deviceRole(mutate func(*netboxv1alpha1.NetBoxDeviceRole)) *netboxv1alpha1.NetBoxDeviceRole {
	role := &netboxv1alpha1.NetBoxDeviceRole{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "router", Generation: 1},
		Spec: netboxv1alpha1.NetBoxDeviceRoleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Router",
			Slug:             "router",
			Color:            "9e9e9e",
		},
	}
	if mutate != nil {
		mutate(role)
	}

	return role
}

// platform is a vendor-neutral platform unless mutate says otherwise.
func platform(mutate func(*netboxv1alpha1.NetBoxPlatform)) *netboxv1alpha1.NetBoxPlatform {
	p := &netboxv1alpha1.NetBoxPlatform{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "unifi-os", Generation: 1},
		Spec: netboxv1alpha1.NetBoxPlatformSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "UniFi OS",
			Slug:             "unifi-os",
		},
	}
	if mutate != nil {
		mutate(p)
	}

	return p
}

// deviceType is the UCG-Ultra, whose manufacturer is required and whose identity needs it.
//
// No mutate hook, unlike the other two fixtures: every case here uses the same device type, and
// what varies is the resolution handed to the engine rather than the spec.
func deviceType() *netboxv1alpha1.NetBoxDeviceType {
	return &netboxv1alpha1.NetBoxDeviceType{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "ucg-ultra", Generation: 1},
		Spec: netboxv1alpha1.NetBoxDeviceTypeSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			ManufacturerRef:  netboxv1alpha1.ManufacturerRef{Name: "ubiquiti"},
			Model:            "UniFi Cloud Gateway Ultra",
			Slug:             "ucg-ultra",
			UHeight:          "0.5",
		},
	}
}

// TestCatalogueLookupsPinTheirNullFilters is NBO-027's central acceptance criterion, in one
// table: which query each of the four kinds sends, and what decides it.
//
// The `__isnull` rows are the point. A top-level device role looked up with `parent_id` merely
// *omitted* asks "this slug anywhere in the tree", so it would match a nested role of the same
// slug, adopt it, and the follow-up PATCH would pull it out of somebody else's hierarchy. The
// same sentence holds for a vendor-neutral platform and `manufacturer_id`, on a model where the
// pinned column is not `parent` at all.
func TestCatalogueLookupsPinTheirNullFilters(t *testing.T) {
	tests := map[string]struct {
		kind   string
		object Object
		refs   resolver.Resolution
		want   netbox.Params
	}{
		"a manufacturer is keyed on its globally unique slug": {
			kind: "NetBoxManufacturer",
			object: &netboxv1alpha1.NetBoxManufacturer{
				ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "ubiquiti", Generation: 1},
				Spec: netboxv1alpha1.NetBoxManufacturerSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Name:             "Ubiquiti",
					Slug:             "ubiquiti",
				},
			},
			want: netbox.Params{"slug": "ubiquiti"},
		},
		"a top-level device role pins parent_id to null": {
			kind:   "NetBoxDeviceRole",
			object: deviceRole(nil),
			want:   netbox.Params{"slug": "router", "parent_id__isnull": "true"},
		},
		"a nested device role is keyed on the pair": {
			kind: "NetBoxDeviceRole",
			object: deviceRole(func(r *netboxv1alpha1.NetBoxDeviceRole) {
				r.Spec.ParentRef = &netboxv1alpha1.DeviceRoleRef{Name: "network"}
			}),
			refs: resolvedTo("parentRef", 7),
			want: netbox.Params{"parent_id": "7", "slug": "router"},
		},
		"a vendor-neutral platform pins manufacturer_id to null, not parent_id": {
			kind:   "NetBoxPlatform",
			object: platform(nil),
			want:   netbox.Params{"slug": "unifi-os", "manufacturer_id__isnull": "true"},
		},
		"a platform under a manufacturer is keyed on the pair": {
			kind: "NetBoxPlatform",
			object: platform(func(p *netboxv1alpha1.NetBoxPlatform) {
				p.Spec.ManufacturerRef = &netboxv1alpha1.ManufacturerRef{Name: "ubiquiti"}
			}),
			refs: resolvedTo("manufacturerRef", 3),
			want: netbox.Params{"manufacturer_id": "3", "slug": "unifi-os"},
		},
		// A nested platform is still keyed on its slug and its manufacturer: no constraint on
		// dcim.Platform mentions `parent`, which is what makes that reference deferrable here
		// and not on dcim.DeviceRole.
		"a nested platform is keyed as though it were top-level": {
			kind: "NetBoxPlatform",
			object: platform(func(p *netboxv1alpha1.NetBoxPlatform) {
				p.Spec.ParentRef = &netboxv1alpha1.PlatformRef{Name: "unifi"}
			}),
			refs: resolvedTo("parentRef", 9),
			want: netbox.Params{"slug": "unifi-os", "manufacturer_id__isnull": "true"},
		},
		"a device type is keyed on its manufacturer and slug": {
			kind:   "NetBoxDeviceType",
			object: deviceType(),
			refs:   resolvedTo("manufacturerRef", 3),
			want:   netbox.Params{"manufacturer_id": "3", "slug": "ucg-ultra"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			nb := &fakeClient{created: netbox.Object{"id": float64(11)}}
			engine := catalogueEngine(t, tc.kind, nb, &fakeRefs{resolution: tc.refs})

			if _, err := engine.Reconcile(context.Background(), tc.object); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			if len(nb.calls) == 0 {
				t.Fatal("the engine made no request at all; it had an applicable candidate")
			}

			if got := nb.calls[0].params; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lookup params = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCatalogueKindsWithAnUnresolvedIdentityRefWriteNothing is the other half of a conditional
// natural key, and the half that is easy to get wrong in the safe-looking direction.
//
// Every candidate on these three kinds either matches the reference or asserts it was never
// declared, so a *declared but unresolved* reference makes none of them applicable and the
// engine has to wait. Creating the object anyway is the failure: for dcim.DeviceType NetBox
// would refuse it outright (`manufacturer` is REQ), and for the two nested kinds it would
// succeed and produce an object in the wrong place that the next pass adopts and reparents.
func TestCatalogueKindsWithAnUnresolvedIdentityRefWriteNothing(t *testing.T) {
	tests := map[string]struct {
		kind   string
		object Object
		field  string
	}{
		"a device role whose parent does not exist yet": {
			kind: "NetBoxDeviceRole",
			object: deviceRole(func(r *netboxv1alpha1.NetBoxDeviceRole) {
				r.Spec.ParentRef = &netboxv1alpha1.DeviceRoleRef{Name: "network"}
			}),
			field: "parentRef",
		},
		"a platform whose manufacturer does not exist yet": {
			kind: "NetBoxPlatform",
			object: platform(func(p *netboxv1alpha1.NetBoxPlatform) {
				p.Spec.ManufacturerRef = &netboxv1alpha1.ManufacturerRef{Name: "ubiquiti"}
			}),
			field: "manufacturerRef",
		},
		"a device type whose required manufacturer does not exist yet": {
			kind:   "NetBoxDeviceType",
			object: deviceType(),
			field:  "manufacturerRef",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			nb := &fakeClient{created: netbox.Object{"id": float64(11)}}
			refs := &fakeRefs{resolution: blockedOnField(tc.field, resolver.ErrRefNotFound)}
			engine := catalogueEngine(t, tc.kind, nb, refs)

			if _, err := engine.Reconcile(context.Background(), tc.object); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			for _, call := range nb.calls {
				t.Errorf("the engine sent %s %s %v with %s unresolved; it must wait instead",
					call.method, call.endpoint, call.params, tc.field)
			}
		})
	}
}

// TestDeviceTypePayloadIsWhatNetBoxTakes asserts the create body, because three of this kind's
// columns are shapes the engine has to get right and NetBox answers a wrong one with a 200.
//
//   - `u_height` is a DecimalField the API returns padded ("0.50" for a spec that said "0.5").
//     It goes over the wire as the string the spec holds; drift compares numerically.
//   - the eleven CounterCacheFields and `_abs_weight` are caches NetBox maintains itself. A
//     payload carrying one is silently dropped, so the next pass finds the same difference and
//     PATCHes forever.
//   - `manufacturer` is written under its column name, not under its filter name
//     (`manufacturer_id`), which NetBox would ignore.
func TestDeviceTypePayloadIsWhatNetBoxTakes(t *testing.T) {
	nb := &fakeClient{created: netbox.Object{"id": float64(11)}}
	engine := catalogueEngine(t, "NetBoxDeviceType", nb, &fakeRefs{
		resolution: resolvedTo("manufacturerRef", 3),
	})

	if _, err := engine.Reconcile(context.Background(), deviceType()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	body := sentBody(t, nb)
	if body == nil {
		t.Fatal("the engine wrote nothing; it had a resolved manufacturer and an applicable candidate")
	}

	if got := body["u_height"]; got != "0.5" {
		t.Errorf("u_height = %#v, want the string \"0.5\"", got)
	}

	if got := body["manufacturer"]; got != float64(3) {
		t.Errorf("manufacturer = %#v, want the resolved id 3", got)
	}

	if _, present := body["manufacturer_id"]; present {
		t.Error("the payload carries manufacturer_id; that is the filter name, not the column")
	}

	d, _ := registry.Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxDeviceType"))
	for _, column := range d.ReadOnly {
		if _, present := body[column]; present {
			t.Errorf("the payload carries the read-only column %q", column)
		}
	}
}

// TestDeviceTypeUHeightDoesNotDriftAgainstNetBoxPadding is the other half of modelling a
// DecimalField as a string, and the half a stub cannot show: NetBox returns `u_height` padded to
// its decimal_places, so the object the engine reads back never looks like the one it sent.
//
// `"0.5"` and `"0.50"` are the same u_height. A comparison that got that wrong would find a
// difference on every pass and PATCH forever, and the CounterCacheFields the live object carries
// are the second half of the same claim -- they are read-only and must not be diffed either.
func TestDeviceTypeUHeightDoesNotDriftAgainstNetBoxPadding(t *testing.T) {
	live := netbox.Object{
		"id":           float64(11),
		"manufacturer": map[string]any{"id": float64(3), "slug": "ubiquiti"},
		"model":        "UniFi Cloud Gateway Ultra",
		"slug":         "ucg-ultra",
		"u_height":     "0.50",
		"device_count": float64(7),
	}

	nb := &fakeClient{list: []netbox.Object{live}}
	engine := catalogueEngine(t, "NetBoxDeviceType", nb, &fakeRefs{
		resolution: resolvedTo("manufacturerRef", 3),
	})

	if _, err := engine.Reconcile(context.Background(), deviceType()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	for _, call := range nb.calls {
		if call.method == "PATCH" || call.method == "POST" {
			t.Errorf("the engine sent %s %v; the live object already matches the spec",
				call.method, call.payload)
		}
	}
}
