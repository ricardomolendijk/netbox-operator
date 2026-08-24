package controller

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// managedBy is the spec block every test here sets, with an explicit cluster identifier
// because there is deliberately no default for one.
func managedBy(mutate func(*netboxv1alpha1.ManagedBy)) *netboxv1alpha1.ManagedBy {
	spec := &netboxv1alpha1.ManagedBy{
		ClusterID:               "prod-eu",
		Tag:                     provenance.DefaultTag,
		UIDField:                provenance.DefaultUIDField,
		ClusterField:            provenance.DefaultClusterField,
		OwnerField:              provenance.DefaultOwnerField,
		AllocationIdentityField: provenance.DefaultAllocationIdentityField,
	}
	if mutate != nil {
		mutate(spec)
	}

	return spec
}

// stampingEndpoint is readyEndpoint with provenance configured. It deliberately does not wait
// for a cached client: the failure cases here never get one.
func stampingEndpoint(t *testing.T, ns, target string, spec *netboxv1alpha1.ManagedBy) {
	t.Helper()

	makeSecret(t, k8sClient, ns, "nb-token", "valid-token")

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            target,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
			DriftMode:      netboxv1alpha1.DriftCorrect,
			ManagedBy:      spec,
		},
	}
	if err := k8sClient.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint in %s: %v", ns, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), endpoint) })
}

// awaitProvenance waits for the ProvenanceReady condition to reach one status and reason, and
// returns the endpoint as it then stood.
func awaitProvenance(t *testing.T, ns string, status metav1.ConditionStatus, reason string,
) *netboxv1alpha1.NetBoxEndpoint {
	t.Helper()

	eventually(t, fmt.Sprintf("ProvenanceReady=%s/%s in %s", status, reason, ns), func() bool {
		e := fetch(t, k8sClient, ns, "homelab")
		if e == nil {
			return false
		}
		c := conditionOf(e, netboxv1alpha1.ConditionProvenanceReady)

		return c != nil && c.Status == status && c.Reason == reason
	})

	return mustFetch(t, k8sClient, ns, "homelab")
}

// TestEndpointBootstrapsProvenance is the acceptance criterion pair: the definitions are
// created if absent, and re-running changes nothing.
func TestEndpointBootstrapsProvenance(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))

	e := awaitProvenance(t, ns, metav1.ConditionTrue, netboxv1alpha1.ReasonProvisioned)

	if e.Status.ManagedBy == nil {
		t.Fatal("status.managedBy is unset on a provisioned endpoint")
	}
	if e.Status.ManagedBy.TagID == 0 {
		t.Error("status.managedBy.tagID is zero; an object is tagged by id, so nothing could be stamped")
	}
	if e.Status.ManagedBy.ClusterID != "prod-eu" {
		t.Errorf("status.managedBy.clusterID = %q, want prod-eu", e.Status.ManagedBy.ClusterID)
	}

	want := []string{"k8s_allocation_identity", "k8s_cluster", "k8s_owner", "k8s_uid"}
	if got := e.Status.ManagedBy.CustomFields; !slices.Equal(got, want) {
		t.Errorf("status.managedBy.customFields = %v, want %v", got, want)
	}

	// The endpoint has to be usable: a bootstrap that succeeded must not withhold the
	// client.
	if _, stamp, ok := clients.Lookup(ns, "homelab"); !ok || !stamp.Applicable() {
		t.Errorf("cached client ok = %v, stamp applicable = %v; want both", ok, stamp.Applicable())
	}

	// object_types on every created definition, derived from the registry rather than
	// listed. Both stampable kinds must be in it and extras.tag must not.
	fields := 0

	for _, write := range stub.recordedExtras() {
		if write.Endpoint != "extras/custom-fields" || write.Method != http.MethodPost {
			continue
		}
		fields++

		types := netbox.ObjectTypesOf(write.Payload["object_types"])
		for _, required := range []string{"dcim.site", "dcim.region"} {
			if !slices.Contains(types, required) {
				t.Errorf("custom field %v declares object_types %v, which omits %s",
					write.Payload["name"], types, required)
			}
		}
		if slices.Contains(types, "extras.tag") {
			t.Errorf("custom field %v declares extras.tag, which carries no custom_fields column",
				write.Payload["name"])
		}
	}

	if fields != 4 {
		t.Errorf("created %d custom fields, want 4", fields)
	}

	// Re-running changes nothing. The endpoint re-probes on its own schedule, so editing the
	// spec is what makes a second pass observable without waiting out a resync period.
	before := len(stub.recordedExtras())
	bump(t, ns)

	eventually(t, "a second endpoint reconcile in "+ns, func() bool {
		e := fetch(t, k8sClient, ns, "homelab")

		return e != nil && e.Status.ObservedGeneration == e.Generation && e.Generation > 1
	})

	if got := len(stub.recordedExtras()); got != before {
		t.Errorf("the second pass wrote %d more provenance requests, want none",
			got-before)
	}
}

// TestEndpointAdoptsDefinitionsMadeByHand: a NetBox admin created the tag and the fields
// already, and the operator resolves rather than duplicates them.
func TestEndpointAdoptsDefinitionsMadeByHand(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()

	stub.seedExtras("extras/tags", netbox.Object{"name": "k8s-managed", "slug": "k8s-managed"})

	// The seeded object_types come from the registry rather than from a list written here.
	// A hand-written pair was correct until the next custom-fieldable kind landed, at which
	// point the bootstrap widened the definitions -- correctly -- and this test read the
	// resulting PATCH as a duplicate. provenance.ObjectTypes' own comment says exactly that
	// about hand-maintained lists; a test is not an exception to it.
	types := make([]any, 0, len(registry.List()))
	for _, objectType := range provenance.ObjectTypes(registry.List()) {
		types = append(types, objectType)
	}

	for _, name := range []string{"k8s_uid", "k8s_cluster", "k8s_owner", "k8s_allocation_identity"} {
		stub.seedExtras("extras/custom-fields", netbox.Object{
			"name": name, "object_types": types,
		})
	}

	stampingEndpoint(t, ns, target, managedBy(nil))
	awaitProvenance(t, ns, metav1.ConditionTrue, netboxv1alpha1.ReasonProvisioned)

	for _, write := range stub.recordedExtras() {
		t.Errorf("bootstrap wrote %s %s against a netbox that already had everything",
			write.Method, write.Endpoint)
	}
}

// TestEndpointBootstrapDisabled is the opt-out: nothing is created, the condition names what
// a human has to create, and the endpoint is refused so no object discovers it one 400 at a
// time.
func TestEndpointBootstrapDisabled(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()

	off := false
	stampingEndpoint(t, ns, target, managedBy(func(m *netboxv1alpha1.ManagedBy) { m.Bootstrap = &off }))

	e := awaitProvenance(t, ns, metav1.ConditionFalse, netboxv1alpha1.ReasonBootstrapDisabled)

	for _, write := range stub.recordedExtras() {
		t.Errorf("bootstrap wrote %s %s with bootstrap disabled", write.Method, write.Endpoint)
	}

	ready := conditionOf(e, netboxv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse ||
		ready.Reason != netboxv1alpha1.ReasonBootstrapDisabled {
		t.Errorf("Ready = %+v, want False/BootstrapDisabled: a stamp that cannot be written "+
			"has to fail the endpoint rather than every object", ready)
	}

	// Reaching the bootstrap means both earlier gates passed, and retracting those answers
	// would send the reader to the token or the version.
	for _, condType := range []string{
		netboxv1alpha1.ConditionAuthenticated, netboxv1alpha1.ConditionVersionSupported,
	} {
		if c := conditionOf(e, condType); c == nil || c.Status != metav1.ConditionTrue {
			t.Errorf("%s = %+v, want True", condType, c)
		}
	}

	if _, _, ok := clients.Lookup(ns, "homelab"); ok {
		t.Error("an endpoint whose provenance is incomplete was handed a client")
	}

	if message := conditionOf(e, netboxv1alpha1.ConditionProvenanceReady).Message; message == "" {
		t.Error("the ProvenanceReady message is empty; it has to name what is missing")
	}
}

// TestEndpointBootstrapRefused: the token can read but not create. Same gate, different
// reason, because the fix is a NetBox permission rather than a definition.
func TestEndpointBootstrapRefused(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stub.extrasStatus = http.StatusForbidden

	stampingEndpoint(t, ns, target, managedBy(nil))

	e := awaitProvenance(t, ns, metav1.ConditionFalse, netboxv1alpha1.ReasonBootstrapFailed)

	ready := conditionOf(e, netboxv1alpha1.ConditionReady)
	if ready == nil || ready.Reason != netboxv1alpha1.ReasonBootstrapFailed {
		t.Errorf("Ready = %+v, want False/BootstrapFailed", ready)
	}
	if _, _, ok := clients.Lookup(ns, "homelab"); ok {
		t.Error("an endpoint whose bootstrap was refused was handed a client")
	}
}

// TestEndpointWithoutManagedByReportsNothing is the pre-NBO-075 behaviour: no condition, no
// status block, and not one request to the extras endpoints.
func TestEndpointWithoutManagedByReportsNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	readyEndpoint(t, ns, target)

	e := mustFetch(t, k8sClient, ns, "homelab")

	if c := conditionOf(e, netboxv1alpha1.ConditionProvenanceReady); c != nil {
		t.Errorf("ProvenanceReady = %+v, want absent: nothing was asked for", c)
	}
	if e.Status.ManagedBy != nil {
		t.Errorf("status.managedBy = %+v, want nil", e.Status.ManagedBy)
	}
	if writes := stub.recordedExtras(); len(writes) != 0 {
		t.Errorf("an endpoint with no spec.managedBy wrote %v", writes)
	}
}

// TestSiteIsStamped is the end-to-end criterion: an object the operator created carries the
// tag and the identifying custom fields, and status records what it stamped.
func TestSiteIsStamped(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))

	e := awaitProvenance(t, ns, metav1.ConditionTrue, netboxv1alpha1.ReasonProvisioned)
	tagID := e.Status.ManagedBy.TagID

	site := makeSite(t, ns, "stamped", nil)
	eventually(t, "the site to be ready", func() bool { return siteIsReady(ns, "stamped") })

	stored := stub.get(int64(fetchSite(ns, "stamped").Status.ID))
	if stored == nil {
		t.Fatal("the site is ready but netbox holds no object for it")
	}

	if ids := netbox.IDsOf(stored["tags"]); !slices.Contains(ids, int(tagID)) {
		t.Errorf("netbox tags = %v, want the managed-by tag %d", ids, tagID)
	}

	fields, ok := stored["custom_fields"].(map[string]any)
	if !ok {
		t.Fatalf("netbox custom_fields = %#v, want a map", stored["custom_fields"])
	}

	want := map[string]any{
		"k8s_uid":     string(site.UID),
		"k8s_cluster": "prod-eu",
		"k8s_owner":   "netboxsite/" + ns + "/stamped",
	}
	for name, value := range want {
		if fields[name] != value {
			t.Errorf("custom field %s = %v, want %v", name, fields[name], value)
		}
	}

	// The allocation-identity definition exists and is deliberately not written: its value
	// is the allocation engine's to compute (NBO-036).
	if _, written := fields["k8s_allocation_identity"]; written {
		t.Error("the engine wrote k8s_allocation_identity, which is NBO-036's to fill")
	}

	stamped := fetchSite(ns, "stamped").Status.Provenance
	if stamped == nil || stamped.Tag != "k8s-managed" || stamped.ClusterID != "prod-eu" {
		t.Errorf("status.provenance = %+v, want the stamp that was written", stamped)
	}

	// The stamp must not become permanent drift: a second pass finds NetBox already correct
	// and sends nothing.
	writes := len(stub.recorded())
	eventually(t, "a resync that writes nothing", func() bool {
		return len(stub.recorded()) == writes
	})
}

// TestTagIsNotStamped is extras.Tag: its NetBox model carries neither `tags` nor
// `custom_fields`, so a NetBoxTag is a managed object with no stamp -- and that is the state
// NetBoxSweep (NBO-046) must report rather than delete.
func TestTagIsNotStamped(t *testing.T) {
	ns := newNamespace(t)
	// The kind under test *is* extras.Tag, so its own store is the tag store the bootstrap
	// resolves against -- which is what a real NetBox looks like, and the reason the
	// provenance store never claims the kind's endpoint.
	stub, target := newNetBoxStub(t, stubKind{endpoint: "extras/tags", key: "slug"})
	stub.withProvenance()

	stampingEndpoint(t, ns, target, managedBy(nil))
	awaitProvenance(t, ns, metav1.ConditionTrue, netboxv1alpha1.ReasonProvisioned)

	makeTag(t, ns, "unstamped", nil)
	eventually(t, "the tag to be ready", func() bool { return tagIsReady(ns, "unstamped") })

	tag := fetchTag(ns, "unstamped")
	if tag.Status.Provenance != nil {
		t.Errorf("status.provenance = %+v, want nil on a kind that cannot carry a stamp",
			tag.Status.Provenance)
	}

	stored := stub.get(tag.Status.ID)
	if _, tagged := stored["tags"]; tagged {
		t.Error("the operator wrote `tags` onto extras.Tag, a model with no such column")
	}
}

// bump forces a reconcile by editing the endpoint's spec, which is the only way to observe a
// second pass without waiting out a resync period.
func bump(t *testing.T, ns string) {
	t.Helper()

	e := mustFetch(t, k8sClient, ns, "homelab")
	e.Spec.Timeout = metav1.Duration{Duration: e.Spec.Timeout.Duration + 1}

	if err := k8sClient.Update(context.Background(), e); err != nil {
		t.Fatalf("bumping endpoint %s/homelab: %v", ns, err)
	}
}
