package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// sweepNamespace is the namespace every table case below is scoped to. A literal rather than
// a real namespace, because none of these functions touches the API server.
const sweepNamespace = "team-a"

// testScope is the scope a sweep in sweepNamespace over NetBoxSite would build.
func testScope(ids []int64, uids []string) sweepScope {
	scope := sweepScope{
		kind:        "netboxsite",
		namespace:   sweepNamespace,
		uidField:    provenance.DefaultUIDField,
		ownerField:  provenance.DefaultOwnerField,
		claimedIDs:  map[int64]bool{},
		claimedUIDs: map[string]bool{},
	}
	for _, id := range ids {
		scope.claimedIDs[id] = true
	}
	for _, uid := range uids {
		scope.claimedUIDs[uid] = true
	}

	return scope
}

// stamped builds a listed NetBox object carrying the stamp values given. An empty value
// leaves the key out entirely, which is the difference between "no stamp" and "an empty
// stamp" and the whole input to the unattributed verdict.
func stamped(id int, uid, owner string) netbox.Object {
	fields := map[string]any{}
	if uid != "" {
		fields[provenance.DefaultUIDField] = uid
	}
	if owner != "" {
		fields[provenance.DefaultOwnerField] = owner
	}

	return netbox.Object{
		"id":                         float64(id),
		"display":                    "site-" + owner,
		provenance.CustomFieldsField: fields,
	}
}

// TestSweepClassify is the whole decision, case by case. It is table-driven because the
// order of the checks is the safety property: claims before attribution, attribution before
// any accusation.
func TestSweepClassify(t *testing.T) {
	cases := []struct {
		name   string
		object netbox.Object
		scope  sweepScope
		want   sweepVerdict
	}{{
		name:   "claimed by status.id",
		object: stamped(10, "uid-a", "netboxsite/"+sweepNamespace+"/a"),
		scope:  testScope([]int64{10}, nil),
		want:   verdictClaimed,
	}, {
		// A CR whose status was lost -- restored from a backup, or wiped by hand -- still
		// protects its object, because the uid stamp says which CR wrote it.
		name:   "claimed by k8s_uid with no status.id",
		object: stamped(11, "uid-b", "netboxsite/"+sweepNamespace+"/b"),
		scope:  testScope(nil, []string{"uid-b"}),
		want:   verdictClaimed,
	}, {
		// The case that makes the check order load-bearing: extras.Tag carries no
		// custom_fields, so a perfectly claimed NetBoxTag object has no stamp at all.
		name:   "claimed with no stamp of any kind",
		object: netbox.Object{"id": float64(12)},
		scope:  testScope([]int64{12}, nil),
		want:   verdictClaimed,
	}, {
		name:   "orphan: stamped for this namespace, no live CR",
		object: stamped(13, "uid-gone", "netboxsite/"+sweepNamespace+"/gone"),
		scope:  testScope([]int64{10}, []string{"uid-a"}),
		want:   verdictOrphan,
	}, {
		// The single worst failure mode available: a CR deleted and reapplied has the same
		// name and a new uid, so the *name* matching proves nothing and the old object is
		// genuinely orphaned.
		name:   "orphan: same owner name, different uid",
		object: stamped(14, "uid-old", "netboxsite/"+sweepNamespace+"/a"),
		scope:  testScope(nil, []string{"uid-new"}),
		want:   verdictOrphan,
	}, {
		name:   "foreign: stamped for another namespace",
		object: stamped(15, "uid-c", "netboxsite/team-b/c"),
		scope:  testScope(nil, nil),
		want:   verdictForeign,
	}, {
		name:   "foreign: stamped for another kind",
		object: stamped(16, "uid-d", "netboxprefix/"+sweepNamespace+"/d"),
		scope:  testScope(nil, nil),
		want:   verdictForeign,
	}, {
		name:   "unattributed: no owner stamp",
		object: stamped(17, "uid-e", ""),
		scope:  testScope(nil, nil),
		want:   verdictUnattributed,
	}, {
		name:   "unattributed: no custom fields at all",
		object: netbox.Object{"id": float64(18)},
		scope:  testScope(nil, nil),
		want:   verdictUnattributed,
	}, {
		// A half-read owner is how a sweep would attribute somebody else's object to
		// itself, so anything that is not exactly three segments is not parsed at all.
		name:   "unattributed: malformed owner stamp",
		object: stamped(19, "uid-f", sweepNamespace+"/f"),
		scope:  testScope(nil, nil),
		want:   verdictUnattributed,
	}, {
		name:   "unattributed: owner stamp with an empty segment",
		object: stamped(20, "uid-g", "netboxsite//g"),
		scope:  testScope(nil, nil),
		want:   verdictUnattributed,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.object, tc.scope); got != tc.want {
				t.Errorf("classify = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSweepGracePeriod is the confidence gate: an orphan is only ever *suspected* until it
// has been continuously unclaimed for the grace period, because "the CR is gone" and "the CR
// is between a delete and a re-apply" are told apart only by waiting.
func TestSweepGracePeriod(t *testing.T) {
	now := time.Now()
	object := stamped(30, "uid-gone", "netboxsite/"+sweepNamespace+"/gone")
	key := sweepKey{kind: "NetBoxSite", id: 30}

	cases := []struct {
		name    string
		grace   time.Duration
		prior   map[sweepKey]metav1.Time
		verdict sweepVerdict
		want    netboxv1alpha1.SweepFindingReason
	}{{
		name:    "first sighting inside the grace period is suspected",
		grace:   time.Hour,
		verdict: verdictOrphan,
		want:    netboxv1alpha1.SweepSuspected,
	}, {
		name:    "still inside the grace period on a later run",
		grace:   time.Hour,
		prior:   map[sweepKey]metav1.Time{key: metav1.NewTime(now.Add(-30 * time.Minute))},
		verdict: verdictOrphan,
		want:    netboxv1alpha1.SweepSuspected,
	}, {
		name:    "past the grace period is orphaned",
		grace:   time.Hour,
		prior:   map[sweepKey]metav1.Time{key: metav1.NewTime(now.Add(-2 * time.Hour))},
		verdict: verdictOrphan,
		want:    netboxv1alpha1.SweepOrphaned,
	}, {
		name:    "a zero grace period reports on first sight",
		grace:   0,
		verdict: verdictOrphan,
		want:    netboxv1alpha1.SweepOrphaned,
	}, {
		// The grace period is about confidence in an accusation. Unattributed is not an
		// accusation -- it is "I cannot tell" -- so waiting would not change it.
		name:    "unattributed ignores the grace period",
		grace:   time.Hour,
		prior:   map[sweepKey]metav1.Time{key: metav1.NewTime(now.Add(-2 * time.Hour))},
		verdict: verdictUnattributed,
		want:    netboxv1alpha1.SweepUnattributed,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass := &sweepPass{grace: tc.grace, prior: tc.prior, now: now,
				endpoint: sweepEndpoint{
					uidField:   provenance.DefaultUIDField,
					ownerField: provenance.DefaultOwnerField,
				}}

			finding := pass.finding(object, "NetBoxSite", tc.verdict)
			if finding.Reason != tc.want {
				t.Errorf("reason = %s, want %s", finding.Reason, tc.want)
			}
			if finding.NetBoxID != 30 {
				t.Errorf("netboxID = %d, want 30", finding.NetBoxID)
			}
		})
	}
}

// TestSweepFirstSeenSurvivesTheRun is the other half of the grace period: the clock has to be
// carried forward from status, or a sweep that runs more often than its grace period never
// confirms anything.
func TestSweepFirstSeenSurvivesTheRun(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-90 * time.Minute).Truncate(time.Second))
	pass := &sweepPass{
		grace: time.Hour,
		prior: priorFirstSeen([]netboxv1alpha1.SweepFinding{{
			Kind: "NetBoxSite", NetBoxID: 40, FirstSeen: started,
			Reason: netboxv1alpha1.SweepSuspected,
		}}),
		now: time.Now(),
	}

	finding := pass.finding(netbox.Object{"id": float64(40)}, "NetBoxSite", verdictOrphan)
	if !finding.FirstSeen.Equal(&started) {
		t.Errorf("firstSeen = %v, want the stored %v", finding.FirstSeen, started)
	}
	if finding.Reason != netboxv1alpha1.SweepOrphaned {
		t.Errorf("reason = %s, want Orphaned: 90 minutes is past a one-hour grace period",
			finding.Reason)
	}
}

// TestSweepReportOrdersAndCaps is the etcd guard. A status carrying fifty thousand findings
// does not get rejected on its own -- it takes the CR with it -- so the cap is not a nicety,
// and what it drops has to be the least actionable rather than whatever the loop reached
// last.
func TestSweepReportOrdersAndCaps(t *testing.T) {
	result := &sweepResult{kinds: []string{"NetBoxSite"}, findings: []netboxv1alpha1.SweepFinding{
		{Kind: "NetBoxSite", NetBoxID: 3, Reason: netboxv1alpha1.SweepUnattributed},
		{Kind: "NetBoxSite", NetBoxID: 2, Reason: netboxv1alpha1.SweepSuspected},
		{Kind: "NetBoxSite", NetBoxID: 9, Reason: netboxv1alpha1.SweepOrphaned},
		{Kind: "NetBoxPrefix", NetBoxID: 1, Reason: netboxv1alpha1.SweepOrphaned},
	}}

	findings, truncated := result.report(2)
	if !truncated {
		t.Error("findingsTruncated = false with 4 findings and a cap of 2")
	}

	want := []int64{1, 9}
	for i, finding := range findings {
		if finding.NetBoxID != want[i] {
			t.Errorf("findings[%d].netboxID = %d, want %d; orphans must survive the cap",
				i, finding.NetBoxID, want[i])
		}
	}

	// The summary always carries true counts, whatever the cap did.
	if result.summary.Orphans != 2 || result.summary.Suspected != 1 {
		t.Errorf("summary orphans/suspected = %d/%d, want 2/1",
			result.summary.Orphans, result.summary.Suspected)
	}

	if _, truncated := result.report(4); truncated {
		t.Error("findingsTruncated = true with 4 findings and a cap of 4: exactly reached is not exceeded")
	}
}

// TestSweepDescriptors is the kind list: no wildcard, and a kind the sweep cannot honestly
// scan refuses the whole run rather than being skipped.
func TestSweepDescriptors(t *testing.T) {
	cases := []struct {
		name       string
		kinds      []string
		wantReason string
	}{
		{name: "registered and stampable", kinds: []string{"NetBoxSite", "NetBoxPrefix"}},
		{
			// NetBoxNotAKind, not a real-but-unimplemented Kind name: this used to say
			// NetBoxDevice, which NBO-030 then shipped, and the test started asserting that a
			// registered Kind is unknown. A name no ticket will ever claim cannot go stale.
			name:       "a kind this build does not carry",
			kinds:      []string{"NetBoxSite", "NetBoxNotAKind"},
			wantReason: netboxv1alpha1.ReasonSweepUnknownKind,
		},
		{
			// extras.Tag has nowhere to put a stamp, so a NetBoxTag object can never be
			// attributed to this cluster -- exactly what docs/operations/provenance.md says
			// a sweep may never act on.
			name:       "a kind whose netbox model cannot be stamped",
			kinds:      []string{"NetBoxTag"},
			wantReason: netboxv1alpha1.ReasonSweepKindNotStampable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			descriptors, reason, err := sweepDescriptors(tc.kinds)
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q (err = %v)", reason, tc.wantReason, err)
			}
			if tc.wantReason != "" {
				if err == nil {
					t.Error("a refusal reason with no error")
				}

				return
			}
			if err != nil || len(descriptors) != len(tc.kinds) {
				t.Errorf("descriptors = %d, err = %v; want %d and no error",
					len(descriptors), err, len(tc.kinds))
			}
		})
	}
}

// TestStampUsable is the "I cannot tell whose objects these are" guard. Every one of the
// three fields is load-bearing, and a missing one is a refusal rather than a scan that
// guesses.
func TestStampUsable(t *testing.T) {
	full := provenance.Stamp{
		Config: provenance.Config{
			ClusterID:    "prod-eu",
			Tag:          provenance.DefaultTag,
			UIDField:     provenance.DefaultUIDField,
			ClusterField: provenance.DefaultClusterField,
			OwnerField:   provenance.DefaultOwnerField,
		},
		TagID: 7,
		Fields: []string{provenance.DefaultClusterField, provenance.DefaultOwnerField,
			provenance.DefaultUIDField},
	}

	if err := stampUsable(full); err != nil {
		t.Fatalf("a fully bootstrapped stamp was refused: %v", err)
	}

	noCluster := full
	noCluster.ClusterID = ""
	if stampUsable(noCluster) == nil {
		t.Error("a stamp with no clusterID was accepted; nothing would scope the scan")
	}

	noTag := full
	noTag.TagID = 0
	if stampUsable(noTag) == nil {
		t.Error("a stamp whose tag was never resolved was accepted")
	}

	// The definition is configured but the bootstrap never created it in NetBox, so the
	// filter cannot be applied and the scan would silently be cluster-wide.
	missing := full
	missing.Fields = []string{provenance.DefaultUIDField, provenance.DefaultOwnerField}
	if stampUsable(missing) == nil {
		t.Error("a stamp whose cluster custom field does not exist in netbox was accepted")
	}
}

// truncatingLister is a NetBox that always reports another page.
type truncatingLister struct{}

func (truncatingLister) List(context.Context, string, netbox.Params) ([]netbox.Object, error) {
	return nil, &netbox.TruncatedError{Endpoint: "dcim/sites", MaxPages: 1000, Collected: 250000}
}

// failingLister is a NetBox that is simply down.
type failingLister struct{}

func (failingLister) List(context.Context, string, netbox.Params) ([]netbox.Object, error) {
	return nil, &netbox.TransientError{Status: 503, Err: errors.New("netbox is down")}
}

// TestSweepRefusesATruncatedList is the failure that turns a report into a false accusation:
// a partial list makes live objects look absent. It has to be a refused run with a reason of
// its own, never a report over the pages that did arrive.
func TestSweepRefusesATruncatedList(t *testing.T) {
	ns := newNamespace(t)
	sweeper := &NetBoxSweepReconciler{Client: k8sClient, Clients: NewClientCache(), Scheme: scheme}

	descriptors, _, err := sweepDescriptors([]string{"NetBoxSite"})
	if err != nil {
		t.Fatalf("resolving descriptors: %v", err)
	}

	sweep := &netboxv1alpha1.NetBoxSweep{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: ns},
		Spec:       netboxv1alpha1.NetBoxSweepSpec{EndpointRef: "homelab", Kinds: []string{"NetBoxSite"}},
	}

	for _, tc := range []struct {
		name       string
		lister     sweepLister
		wantReason string
	}{
		{name: "truncated", lister: truncatingLister{}, wantReason: netboxv1alpha1.ReasonSweepTruncated},
		{name: "netbox down", lister: failingLister{}, wantReason: netboxv1alpha1.ReasonSweepAPIError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sweeper.scan(context.Background(), sweep,
				sweepEndpoint{lister: tc.lister, clusterFilter: "cf_k8s_cluster", clusterID: "prod-eu"},
				descriptors)
			if err == nil {
				t.Fatal("scan returned no error")
			}
			if got := scanReason(err); got != tc.wantReason {
				t.Errorf("scanReason = %q, want %q (err = %v)", got, tc.wantReason, err)
			}
		})
	}
}

// TestSweepReasonForTimeout keeps the timeout distinguishable from an unhappy NetBox: a
// timeout means "the answer you are looking at is incomplete", which is a different runbook
// page from "netbox returned 503".
func TestSweepReasonForTimeout(t *testing.T) {
	wrapped := errors.Join(errors.New("listing dcim/sites"), context.DeadlineExceeded)
	if got := scanReason(wrapped); got != netboxv1alpha1.ReasonSweepTimeout {
		t.Errorf("scanReason = %q, want %q", got, netboxv1alpha1.ReasonSweepTimeout)
	}
}
