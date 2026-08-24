package provenance

import (
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// ours is the endpoint under test: cluster prod-eu, every field on and provisioned.
func ours() Stamp {
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})

	return Stamp{Config: cfg, TagID: 7, Fields: cfg.CustomFieldNames()}
}

// mine is the CR doing the reconciling: netboxfake/team-a/managed in prod-eu.
var mine = Owner{Kind: "NetBoxFake", Namespace: "team-a", Name: "managed", UID: "6f1a-uid"}

// stamped renders a live NetBox object carrying the three stamp fields. An empty value is
// left out of `custom_fields` entirely, which is what NetBox returns for a definition nothing
// ever wrote.
func stamped(uid, cluster, owner string) netbox.Object {
	fields := map[string]any{}
	for name, value := range map[string]string{
		DefaultUIDField: uid, DefaultClusterField: cluster, DefaultOwnerField: owner,
	} {
		if value != "" {
			fields[name] = value
		}
	}

	return netbox.Object{"id": float64(9), "custom_fields": fields}
}

// TestConflict is the classification table: every combination of stamp values that can reach
// a write, and what each one is.
//
// Exhaustive on purpose. This is the whole of what NBO-047 ships -- the operator does not
// serialise writes between clusters (#18), so a wrong verdict here is not a wrong action, it
// is a wrong report, and a report nobody trusts is worth less than none.
func TestConflict(t *testing.T) {
	cases := []struct {
		name       string
		stamp      Stamp
		live       netbox.Object
		wantReason string
		wantWriter string
	}{
		{
			name:  "our own object",
			stamp: ours(),
			live:  stamped("6f1a-uid", "prod-eu", "netboxfake/team-a/managed"),
		},
		{
			// The whole reason the uid is not grounds for a verdict on its own: `kubectl
			// delete && kubectl apply` of one manifest produces exactly this, and a conflict
			// here would train everybody to ignore the condition.
			name:  "the same manifest deleted and re-created has a new uid and is not a conflict",
			stamp: ours(),
			live:  stamped("older-uid", "prod-eu", "netboxfake/team-a/managed"),
		},
		{
			// Unmanaged. Adoption is what spec.onConflict is for, and every object that
			// predates the operator is in this set.
			name:  "an unstamped object is not a conflict",
			stamp: ours(),
			live:  netbox.Object{"id": float64(9)},
		},
		{
			name:  "custom_fields present but every stamp field empty",
			stamp: ours(),
			live:  stamped("", "", ""),
		},
		{
			name:  "a kind that carries no custom fields at all",
			stamp: ours(),
			live:  netbox.Object{"id": float64(9), "custom_fields": nil},
		},
		{
			name:       "another cluster",
			stamp:      ours(),
			live:       stamped("other-uid", "prod-us", "netboxfake/team-b/mgmt"),
			wantReason: netboxv1alpha1.ReasonForeignCluster,
			wantWriter: "netboxfake/team-b/mgmt in cluster prod-us",
		},
		{
			// Same manifest name in two clusters: the owner ref matches and the cluster does
			// not, which is why the cluster is asked about first.
			name:       "another cluster running the same manifest",
			stamp:      ours(),
			live:       stamped("other-uid", "prod-us", "netboxfake/team-a/managed"),
			wantReason: netboxv1alpha1.ReasonForeignCluster,
			wantWriter: "netboxfake/team-a/managed in cluster prod-us",
		},
		{
			name:       "another cluster that stamps no owner",
			stamp:      ours(),
			live:       stamped("other-uid", "prod-us", ""),
			wantReason: netboxv1alpha1.ReasonForeignCluster,
			wantWriter: "cluster prod-us",
		},
		{
			name:       "another namespace in this cluster",
			stamp:      ours(),
			live:       stamped("other-uid", "prod-eu", "netboxfake/team-b/managed"),
			wantReason: netboxv1alpha1.ReasonForeignOwner,
			wantWriter: "netboxfake/team-b/managed in cluster prod-eu",
		},
		{
			// Two CRs in one namespace that both resolved to one NetBox object. NBO-044's
			// webhook refuses the natural-key collision at admission; this is the runtime
			// backstop for everything it cannot see.
			name:       "a second cr in this namespace",
			stamp:      ours(),
			live:       stamped("other-uid", "prod-eu", "netboxfake/team-a/other"),
			wantReason: netboxv1alpha1.ReasonForeignOwner,
			wantWriter: "netboxfake/team-a/other in cluster prod-eu",
		},
		{
			// The endpoint's clusterField switched off: attribution falls back to the owner
			// ref, which still names a manifest.
			name: "no cluster stamp, foreign owner",
			stamp: func() Stamp {
				s := ours()
				s.ClusterField = ""

				return s
			}(),
			live:       stamped("other-uid", "prod-us", "netboxfake/team-b/mgmt"),
			wantReason: netboxv1alpha1.ReasonForeignOwner,
			wantWriter: "netboxfake/team-b/mgmt",
		},
		{
			// An older operator, or netbox-populator: a tag and nothing else. Unattributable,
			// and reported as adoption rather than as a conflict -- there is no name to give,
			// and "somebody, somewhere" is not something anybody can act on.
			name:  "a stamp with no uid, cluster or owner",
			stamp: ours(),
			live:  netbox.Object{"id": float64(9), "tags": []any{map[string]any{"id": float64(7)}}},
		},
		{
			// Fail quiet, not loud: nothing was ever stamped by this endpoint, so a foreign
			// value cannot be told from one of our own.
			name:  "an endpoint that stamps nothing has no verdict",
			stamp: Stamp{},
			live:  stamped("other-uid", "prod-us", "netboxfake/team-b/mgmt"),
		},
		{
			// A stamp whose bootstrap never resolved the tag id: same answer, same reason.
			name:  "an unresolved stamp has no verdict",
			stamp: Stamp{Config: FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})},
			live:  stamped("other-uid", "prod-us", "netboxfake/team-b/mgmt"),
		},
		{
			name:  "a non-string custom field value reads as unstamped",
			stamp: ours(),
			live:  netbox.Object{"id": float64(9), "custom_fields": map[string]any{"k8s_cluster": float64(3)}},
		},
		{
			// NetBox stores what it is given, and a value somebody pasted with a trailing
			// newline is the same cluster.
			name:  "whitespace around a value is not a different writer",
			stamp: ours(),
			live:  stamped(" 6f1a-uid ", "  prod-eu\n", "netboxfake/team-a/managed "),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.stamp.Conflict(tc.live, mine)

			if ok != (tc.wantReason != "") {
				t.Fatalf("Conflict() reported %v with reason %q, want a conflict: %v",
					ok, got.Reason, tc.wantReason != "")
			}

			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}

			if ok && got.Writer() != tc.wantWriter {
				t.Errorf("Writer() = %q, want %q", got.Writer(), tc.wantWriter)
			}
		})
	}
}

// TestWriterNamesAUIDWhenItIsAllThereIs is the last resort: an endpoint that stamps only the
// uid still has to produce something a human can search NetBox for.
func TestWriterNamesAUIDWhenItIsAllThereIs(t *testing.T) {
	claim := Claim{UID: "other-uid"}

	if got, want := claim.Writer(), "the cr with uid other-uid"; got != want {
		t.Errorf("Writer() = %q, want %q", got, want)
	}
}

// TestReadUsesTheConfiguredNames is the reuse assertion: internal/reconciler's duplicate
// handling reads the uid through this, so a renamed field has to be honoured here and not just
// when writing.
func TestReadUsesTheConfiguredNames(t *testing.T) {
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "lab", UIDField: "cr_uid"})
	live := netbox.Object{"custom_fields": map[string]any{
		"cr_uid": "6f1a-uid", "k8s_uid": "not-this-one",
	}}

	if got := cfg.Read(live); got.UID != "6f1a-uid" {
		t.Errorf("Read().UID = %q, want the value under cr_uid", got.UID)
	}
}
