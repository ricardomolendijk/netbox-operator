package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// locationKind points the shared stub at dcim.Location.
//
// Keyed by `name` like dcim.Region rather than by `slug`: every constraint on dcim.Location
// is `(site, ..., name)` (docs/netbox-schema.md -> dcim.Location.meta.constraints), so `name`
// is the filter the engine sends.
var locationKind = stubKind{endpoint: "dcim/locations", key: "name"}

// TestLocationWithAnUnresolvableSiteWritesNothing is NBO-066's acceptance criterion for the
// first kind with a *required* reference, and it is a stronger claim than "it reports the
// problem".
//
// `site` is in both natural-key candidates, so an unresolved `siteRef` leaves the engine with
// no identity to look up: it cannot create, and it must not fall back to a lookup with
// `site_id` omitted -- that filter matches a location of the same name in *any* site, and
// adopting one would PATCH somebody else's location into this site.
//
// The assertion is therefore on the recorded traffic and not only on the conditions: a
// version that reported the reference and then created the object anyway would look identical
// in the status.
func TestLocationWithAnUnresolvableSiteWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, locationKind)
	endpointWithoutResync(t, ns, target)

	location := &netboxv1alpha1.NetBoxLocation{
		ObjectMeta: metav1.ObjectMeta{Name: "ground-floor", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxLocationSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Ground floor",
			Slug:             "ground-floor",
			// A NetBoxSite that does not exist. `name` is the only mode the operator can
			// wait on, which is why it is the one an unresolvable reference is tested in.
			SiteRef: netboxv1alpha1.SiteRef{Name: "nowhere"},
		},
	}
	if err := k8sClient.Create(context.Background(), location); err != nil {
		t.Fatalf("creating location: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), location) })

	eventually(t, "the location to report that its site does not exist", func() bool {
		return locationRefsReason(ns, "ground-floor") == netboxv1alpha1.ReasonRefNotFound
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox writes = %v, want none: no candidate is applicable without site_id", got)
	}

	fetched := &netboxv1alpha1.NetBoxLocation{}
	key := client.ObjectKey{Namespace: ns, Name: "ground-floor"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching location: %v", err)
	}

	if fetched.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: nothing was created", fetched.Status.ID)
	}

	for _, c := range fetched.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
			t.Error("Ready=True on a location whose site does not exist")
		}
	}
}

// locationRefsReason is the reason on a location's RefsResolved condition, or "" when it has
// not reported one yet.
func locationRefsReason(ns, name string) string {
	location := &netboxv1alpha1.NetBoxLocation{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, location); err != nil {
		return ""
	}

	for _, c := range location.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionRefsResolved {
			return c.Reason
		}
	}

	return ""
}
