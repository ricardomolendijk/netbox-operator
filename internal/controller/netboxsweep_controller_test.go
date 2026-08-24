package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// makeSweep applies a NetBoxSweep over NetBoxSite, with the grace period the case wants.
//
// The interval is deliberately long: every test here asserts on the *first* run, and a short
// interval would let a second run land between the assertion and the read.
func makeSweep(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxSweep)) *netboxv1alpha1.NetBoxSweep {
	t.Helper()

	sweep := &netboxv1alpha1.NetBoxSweep{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxSweepSpec{
			EndpointRef: "homelab",
			Kinds:       []string{"NetBoxSite"},
			Interval:    metav1.Duration{Duration: time.Hour},
			GracePeriod: metav1.Duration{Duration: 0},
			MaxFindings: 100,
			Timeout:     metav1.Duration{Duration: time.Minute},
		},
	}
	if mutate != nil {
		mutate(sweep)
	}
	if err := k8sClient.Create(context.Background(), sweep); err != nil {
		t.Fatalf("creating sweep %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), sweep) })

	return sweep
}

func fetchSweep(ns, name string) *netboxv1alpha1.NetBoxSweep {
	sweep := &netboxv1alpha1.NetBoxSweep{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, sweep); err != nil {
		return nil
	}

	return sweep
}

// awaitSweep waits for the sweep's Ready condition to reach one status and reason, and
// returns it as it then stood.
func awaitSweep(t *testing.T, ns, name string, status metav1.ConditionStatus,
	reason string,
) *netboxv1alpha1.NetBoxSweep {
	t.Helper()

	eventually(t, fmt.Sprintf("sweep %s/%s Ready=%s/%s", ns, name, status, reason), func() bool {
		sweep := fetchSweep(ns, name)
		if sweep == nil {
			return false
		}
		for _, c := range sweep.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionSweepReady {
				return c.Status == status && c.Reason == reason
			}
		}

		return false
	})

	return fetchSweep(ns, name)
}

// poke forces a reconcile of a sweep whose next run is an hour away, by writing an
// annotation. There is no other way in: a sweep watches nothing but itself, on purpose.
func poke(t *testing.T, sweep *netboxv1alpha1.NetBoxSweep) {
	t.Helper()

	live := fetchSweep(sweep.Namespace, sweep.Name)
	if live == nil {
		t.Fatalf("sweep %s/%s has gone", sweep.Namespace, sweep.Name)
	}

	live.Annotations = map[string]string{"test.netbox.kubeforge.org/poke": time.Now().Format(time.RFC3339Nano)}
	if err := k8sClient.Update(context.Background(), live); err != nil {
		t.Fatalf("poking sweep %s/%s: %v", sweep.Namespace, sweep.Name, err)
	}
}

// seedSite puts a site into NetBox that the operator did not create, carrying whatever stamp
// the case wants. Returns its NetBox id.
//
// tagID of zero leaves the object untagged, which is how "a hand-made NetBox object" is
// spelled: structurally out of a sweep's reach.
func seedSite(stub *netboxStubServer, tagID int, slug, cluster, owner string) int64 {
	object := netbox.Object{"slug": slug, "name": slug, "status": "active"}
	if tagID != 0 {
		object[provenance.TagsField] = []any{float64(tagID)}
	}

	fields := map[string]any{}
	if cluster != "" {
		fields[provenance.DefaultClusterField] = cluster
	}
	if owner != "" {
		fields[provenance.DefaultOwnerField] = owner
		fields[provenance.DefaultUIDField] = "uid-" + slug
	}
	object[provenance.CustomFieldsField] = fields

	return stub.seed(object)
}

// findingFor returns the status entry for one NetBox id, or nil.
func findingFor(sweep *netboxv1alpha1.NetBoxSweep, id int64) *netboxv1alpha1.SweepFinding {
	for i := range sweep.Status.Findings {
		if sweep.Status.Findings[i].NetBoxID == id {
			return &sweep.Status.Findings[i]
		}
	}

	return nil
}

// TestSweepReportsOrphansAndWritesNothing is the whole feature in one namespace against one
// NetBox, and it asserts the two properties everything else is shaped around:
//
//   - the report is right: an orphan is found, a claimed object is not, another namespace's
//     object is not, another cluster's object is never even fetched, and a hand-made object
//     is structurally out of reach;
//   - **nothing is written**. The stub records every mutating request it receives, and the
//     only ones this test tolerates are the create the live site's own controller made.
func TestSweepReportsOrphansAndWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))
	eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

	// One healthy site: the engine creates it, stamps it, and records its id.
	makeSite(t, ns, "live", nil)
	eventually(t, "site live is ready", func() bool { return siteIsReady(ns, "live") })

	live := fetchSite(ns, "live")
	writesBefore := len(stub.recorded())
	tagID := stub.managedTagID()

	// Everything below is in NetBox with no CR anywhere.
	orphan := seedSite(stub, tagID, "orphan", "prod-eu", "netboxsite/"+ns+"/gone")
	reapplied := seedSite(stub, tagID, "reapplied", "prod-eu", "netboxsite/"+ns+"/live")
	foreign := seedSite(stub, tagID, "team-b", "prod-eu", "netboxsite/team-b/theirs")
	otherCluster := seedSite(stub, tagID, "us-east", "prod-us", "netboxsite/"+ns+"/theirs")
	unattributed := seedSite(stub, tagID, "no-owner", "prod-eu", "")
	handmade := seedSite(stub, 0, "handmade", "prod-eu", "netboxsite/"+ns+"/nope")

	makeSweep(t, ns, "nightly", nil)
	got := awaitSweep(t, ns, "nightly", metav1.ConditionTrue, netboxv1alpha1.ReasonSweepComplete)

	// prod-eu, tagged: the live site, the orphan, the re-applied one, team-b's and the
	// unattributed one. prod-us and the untagged one are filtered out server-side and are
	// never even fetched.
	want := netboxv1alpha1.SweepSummary{
		Scanned: 5, Claimed: 1, Orphans: 2, Suspected: 0, Unattributed: 1, Foreign: 1,
	}
	if got.Status.Summary != want {
		t.Errorf("summary = %+v, want %+v", got.Status.Summary, want)
	}

	if finding := findingFor(got, orphan); finding == nil {
		t.Error("the orphan is not in status.findings")
	} else if finding.Reason != netboxv1alpha1.SweepOrphaned {
		t.Errorf("the orphan's reason = %s, want Orphaned", finding.Reason)
	}

	// The most valuable single assertion here. `live` and `reapplied` carry the same
	// k8s_owner -- the same Kind, namespace and name -- and differ only in k8s_uid. A sweep
	// that matched on the owner string would call the live object claimed and stop; one that
	// matched on the uid finds that the CR behind `reapplied` is a different object that no
	// longer exists.
	if finding := findingFor(got, reapplied); finding == nil {
		t.Error("an object whose owner name matches a live CR but whose uid does not was " +
			"treated as claimed")
	}

	if finding := findingFor(got, unattributed); finding == nil {
		t.Error("the object with no owner stamp is not in status.findings")
	} else if finding.Reason != netboxv1alpha1.SweepUnattributed {
		t.Errorf("the unstamped object's reason = %s, want Unattributed", finding.Reason)
	}

	for what, id := range map[string]int64{
		"the live site":              int64(live.Status.ID),
		"another namespace's object": foreign,
		"another cluster's object":   otherCluster,
		"a hand-made object":         handmade,
	} {
		if finding := findingFor(got, id); finding != nil {
			t.Errorf("%s (netbox id %d) was reported as %s", what, id, finding.Reason)
		}
	}

	// Nothing was written, and nothing was removed. Both halves matter: the stub records
	// every POST, PATCH and DELETE it is sent, and every seeded object is still there.
	for _, write := range stub.recorded()[writesBefore:] {
		t.Errorf("the sweep sent %s %d to netbox", write.Method, write.ID)
	}

	for _, id := range []int64{orphan, reapplied, foreign, otherCluster, unattributed, handmade} {
		if stub.get(id) == nil {
			t.Errorf("netbox object %d is gone; a sweep must never delete", id)
		}
	}
}

// TestSweepGraceHoldsBackTheFirstSighting is the confidence gate end to end: with a grace
// period, the first run reports a suspicion rather than an accusation, and the firstSeen it
// wrote is what the next run measures against.
func TestSweepGraceHoldsBackTheFirstSighting(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))
	eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

	tagID := stub.managedTagID()
	orphan := seedSite(stub, tagID, "orphan", "prod-eu", "netboxsite/"+ns+"/gone")

	makeSweep(t, ns, "cautious", func(s *netboxv1alpha1.NetBoxSweep) {
		s.Spec.GracePeriod = metav1.Duration{Duration: time.Hour}
	})
	got := awaitSweep(t, ns, "cautious", metav1.ConditionTrue, netboxv1alpha1.ReasonSweepComplete)

	if got.Status.Summary.Orphans != 0 || got.Status.Summary.Suspected != 1 {
		t.Errorf("orphans/suspected = %d/%d, want 0/1 inside the grace period",
			got.Status.Summary.Orphans, got.Status.Summary.Suspected)
	}

	finding := findingFor(got, orphan)
	if finding == nil {
		t.Fatal("the suspected orphan is not in status.findings")
	}
	if finding.FirstSeen.IsZero() {
		t.Error("firstSeen is unset; the grace period would restart on every run")
	}
	if finding.Owner != "netboxsite/"+ns+"/gone" {
		t.Errorf("owner = %q; the report has to name the manifest to go and look at", finding.Owner)
	}
}

// TestSweepRefusalPreservesFindings is the asymmetry the whole design rests on. An empty
// findings list must only ever mean "the last complete scan found nothing", never "the last
// scan could not see anything" -- so a refused run writes a reason and leaves the report
// exactly as the last completed run left it.
func TestSweepRefusalPreservesFindings(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))
	eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

	tagID := stub.managedTagID()
	orphan := seedSite(stub, tagID, "orphan", "prod-eu", "netboxsite/"+ns+"/gone")

	sweep := makeSweep(t, ns, "nightly", nil)
	awaitSweep(t, ns, "nightly", metav1.ConditionTrue, netboxv1alpha1.ReasonSweepComplete)

	// Take the endpoint away. Its controller drops the cached client, so the next run has
	// nothing to list NetBox with.
	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: ns},
	}
	if err := k8sClient.Delete(context.Background(), endpoint); err != nil {
		t.Fatalf("deleting the endpoint: %v", err)
	}
	eventually(t, "the cached client is dropped", func() bool {
		_, _, ok := clients.Lookup(ns, "homelab")

		return !ok
	})

	poke(t, sweep)
	got := awaitSweep(t, ns, "nightly", metav1.ConditionFalse, netboxv1alpha1.ReasonSweepEndpointNotReady)

	if findingFor(got, orphan) == nil {
		t.Error("a refused run cleared status.findings; an empty report now means two " +
			"different things")
	}
	if got.Status.Summary.Orphans != 1 {
		t.Errorf("summary.orphans = %d after a refusal, want the preserved 1",
			got.Status.Summary.Orphans)
	}
	if got.Status.LastRunTime == nil {
		t.Error("lastRunTime was cleared; nothing says how stale the findings are")
	}
}

// TestSweepSuspendKeepsTheReport is the stop lever: scheduling stops, the findings stay, and
// Ready keeps whatever the last real run said -- a suspended sweep has not failed.
func TestSweepSuspendKeepsTheReport(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()
	stampingEndpoint(t, ns, target, managedBy(nil))
	eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

	tagID := stub.managedTagID()
	orphan := seedSite(stub, tagID, "orphan", "prod-eu", "netboxsite/"+ns+"/gone")

	makeSweep(t, ns, "nightly", nil)
	awaitSweep(t, ns, "nightly", metav1.ConditionTrue, netboxv1alpha1.ReasonSweepComplete)

	live := fetchSweep(ns, "nightly")
	live.Spec.Suspend = true
	if err := k8sClient.Update(context.Background(), live); err != nil {
		t.Fatalf("suspending the sweep: %v", err)
	}

	eventually(t, "the sweep reports Suspended", func() bool {
		sweep := fetchSweep(ns, "nightly")
		if sweep == nil {
			return false
		}
		for _, c := range sweep.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionSweepSuspended {
				return c.Status == metav1.ConditionTrue
			}
		}

		return false
	})

	got := fetchSweep(ns, "nightly")
	if findingFor(got, orphan) == nil {
		t.Error("suspending the sweep cleared its findings")
	}
	if got.Status.NextRunTime != nil {
		t.Error("a suspended sweep still advertises a nextRunTime")
	}
	for _, c := range got.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionSweepReady && c.Reason != netboxv1alpha1.ReasonSweepComplete {
			t.Errorf("Ready reason = %s after suspending; a suspended sweep has not failed", c.Reason)
		}
	}
}

// TestSweepRefusesADryRunEndpoint is the single most dangerous interaction in the feature. A
// client that cannot write means no CR ever got a status.id, so every object of every kind
// would look unclaimed -- an entire namespace reported as orphaned. It is an explicit guard
// clause and not an emergent property, so it gets an explicit test.
func TestSweepRefusesADryRunEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec func(*netboxv1alpha1.NetBoxEndpoint)
		want string
	}{{
		name: "endpoint mode DryRun",
		spec: func(e *netboxv1alpha1.NetBoxEndpoint) { e.Spec.Mode = netboxv1alpha1.EndpointModeDryRun },
		want: netboxv1alpha1.ReasonSweepEndpointDryRun,
	}, {
		// driftMode: Report hands out the same non-writing client, for a different reason
		// and from a different field. Both have to be caught, or the guard is one field wide.
		name: "driftMode Report",
		spec: func(e *netboxv1alpha1.NetBoxEndpoint) { e.Spec.DriftMode = netboxv1alpha1.DriftReport },
		want: netboxv1alpha1.ReasonSweepEndpointDryRun,
	}, {
		// driftMode: Off means the operator is not tracking NetBox state at all, so the
		// absence of a claim proves nothing.
		name: "driftMode Off",
		spec: func(e *netboxv1alpha1.NetBoxEndpoint) { e.Spec.DriftMode = netboxv1alpha1.DriftOff },
		want: netboxv1alpha1.ReasonSweepDriftOff,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			ns := newNamespace(t)
			stub, target := newNetBoxStub(t, siteKind)
			stub.withProvenance()
			// Seeded rather than bootstrapped: a DryRun or Report endpoint hands out a
			// client that cannot POST, so its bootstrap adopts definitions and never
			// creates them. Seeding the tag is what lets all three cases stage a real
			// orphan and still assert that the run is refused before anything is listed.
			stub.seedExtras("extras/tags", netbox.Object{
				"name": provenance.DefaultTag, "slug": provenance.DefaultTag,
			})
			makeSecret(t, k8sClient, ns, "nb-token", "valid-token")

			endpoint := &netboxv1alpha1.NetBoxEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: ns},
				Spec: netboxv1alpha1.NetBoxEndpointSpec{
					URL:            target,
					TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
					Mode:           netboxv1alpha1.EndpointModeApply,
					DriftMode:      netboxv1alpha1.DriftCorrect,
					ManagedBy:      managedBy(nil),
				},
			}
			tc.spec(endpoint)
			if err := k8sClient.Create(context.Background(), endpoint); err != nil {
				t.Fatalf("creating endpoint: %v", err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), endpoint) })

			eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

			tagID := stub.managedTagID()
			seedSite(stub, tagID, "orphan", "prod-eu", "netboxsite/"+ns+"/gone")

			makeSweep(t, ns, "nightly", nil)
			got := awaitSweep(t, ns, "nightly", metav1.ConditionFalse, tc.want)

			if len(got.Status.Findings) != 0 {
				t.Errorf("a refused run reported %d finding(s)", len(got.Status.Findings))
			}
		})
	}
}

// TestSweepTwoNamespacesOneNetBox is the property the namespaced design buys, and the spec
// asks for it by name: two sweeps, one NetBox, and neither sees the other's objects.
func TestSweepTwoNamespacesOneNetBox(t *testing.T) {
	first := newNamespaceSuffixed(t, "-a")
	second := newNamespaceSuffixed(t, "-b")

	stub, target := newNetBoxStub(t, siteKind)
	stub.withProvenance()

	for _, ns := range []string{first, second} {
		stampingEndpoint(t, ns, target, managedBy(nil))
		eventually(t, "endpoint ready in "+ns, func() bool { return endpointIsReady(ns, "homelab") })
	}

	tagID := stub.managedTagID()
	orphans := map[string]int64{
		first:  seedSite(stub, tagID, "orphan-a", "prod-eu", "netboxsite/"+first+"/gone"),
		second: seedSite(stub, tagID, "orphan-b", "prod-eu", "netboxsite/"+second+"/gone"),
	}

	for _, ns := range []string{first, second} {
		makeSweep(t, ns, "nightly", nil)
	}

	for _, ns := range []string{first, second} {
		got := awaitSweep(t, ns, "nightly", metav1.ConditionTrue, netboxv1alpha1.ReasonSweepComplete)

		if got.Status.Summary.Orphans != 1 || got.Status.Summary.Foreign != 1 {
			t.Errorf("%s: orphans/foreign = %d/%d, want 1/1", ns,
				got.Status.Summary.Orphans, got.Status.Summary.Foreign)
		}
		if findingFor(got, orphans[ns]) == nil {
			t.Errorf("%s: did not report its own orphan %d", ns, orphans[ns])
		}
		for other, id := range orphans {
			if other != ns && findingFor(got, id) != nil {
				t.Errorf("%s reported %s's object %d as its own", ns, other, id)
			}
		}
	}
}

// TestSweepRefusesAnUnknownKind covers the two kind-list refusals from the outside, because
// both are the same shape of mistake -- a spec.kinds entry the sweep cannot honestly scan --
// and both refuse the whole run rather than skipping the one kind.
func TestSweepRefusesAnUnknownKind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kinds []string
		want  string
	}{{
		name:  "a kind this build does not carry",
		kinds: []string{"NetBoxSite", "NetBoxDevice"},
		want:  netboxv1alpha1.ReasonSweepUnknownKind,
	}, {
		name:  "a kind that cannot be stamped",
		kinds: []string{"NetBoxTag"},
		want:  netboxv1alpha1.ReasonSweepKindNotStampable,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			ns := newNamespace(t)
			stub, target := newNetBoxStub(t, siteKind)
			stub.withProvenance()
			stampingEndpoint(t, ns, target, managedBy(nil))
			eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

			makeSweep(t, ns, "nightly", func(s *netboxv1alpha1.NetBoxSweep) { s.Spec.Kinds = tc.kinds })
			awaitSweep(t, ns, "nightly", metav1.ConditionFalse, tc.want)
		})
	}
}

// TestSweepRefusesAnUnstampedEndpoint is the honest limitation, asserted: with no
// spec.managedBy there is nothing that distinguishes this cluster's objects from anybody
// else's, so the sweep refuses rather than scanning without the filter.
func TestSweepRefusesAnUnstampedEndpoint(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, siteKind)
	makeSecret(t, k8sClient, ns, "nb-token", "valid-token")

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            target,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
			DriftMode:      netboxv1alpha1.DriftCorrect,
		},
	}
	if err := k8sClient.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), endpoint) })
	eventually(t, "endpoint ready", func() bool { return endpointIsReady(ns, "homelab") })

	makeSweep(t, ns, "nightly", nil)
	awaitSweep(t, ns, "nightly", metav1.ConditionFalse, netboxv1alpha1.ReasonSweepProvenanceDisabled)
}
