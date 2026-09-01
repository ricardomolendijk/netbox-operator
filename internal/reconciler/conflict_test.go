package reconciler

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// foreignTag is the live object of an adopted CR, stamped by another cluster: prod-us wrote it
// last, and its own CR over there is netboxfake/team-b/mgmt.
//
// The colour differs from the spec so the pass has something to PATCH, which is what makes the
// central assertion of this file possible: the conflict is reported and the write goes ahead.
func foreignTag(cluster, owner string) netbox.Object {
	live := driftedTag()
	live["tags"] = []any{map[string]any{"id": float64(7), "name": "k8s-managed"}}
	live["custom_fields"] = map[string]any{
		"k8s_uid": "other-uid", "k8s_cluster": cluster, "k8s_owner": owner,
	}

	return live
}

// conflictedEngine is stampedEngine with an Event recorder, since the Events are half of what
// this reports.
func conflictedEngine(t *testing.T, client NetBoxClient) (*Engine, *fakeRecorder) {
	t.Helper()

	engine := stampedEngine(t, stampableDescriptor(), client)
	events := &fakeRecorder{}
	engine.Events = events

	return engine, events
}

// TestConflictIsReportedAndTheWriteStillHappens is the whole position of NBO-047, and of issue
// #18 before it: the operator names the other writer and then writes anyway. If this ever
// asserts zero writes, somebody has built the lock that was decided against.
func TestConflictIsReportedAndTheWriteStillHappens(t *testing.T) {
	client := &fakeClient{get: foreignTag("prod-us", "netboxfake/team-b/mgmt"), patched: liveTag(adoptedID)}
	engine, events := conflictedEngine(t, client)

	obj := stampedObject()
	obj.Status.ID = adoptedID

	before := testutil.ToFloat64(
		metrics.Conflicts.WithLabelValues(fakeGVK.Kind, netboxv1alpha1.ReasonForeignCluster))

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := client.methods(); !slices.Equal(got, []string{"GET", "PATCH"}) {
		t.Fatalf("netbox calls = %v, want the read and the write: writes are not serialised "+
			"between clusters (docs/operations/provenance.md)", got)
	}

	condition := conditionOf(obj, netboxv1alpha1.ConditionConflict)
	if condition.Status != metav1.ConditionTrue ||
		condition.Reason != netboxv1alpha1.ReasonForeignCluster {
		t.Errorf("Conflict condition = %s/%s, want True/ForeignCluster",
			condition.Status, condition.Reason)
	}

	// The condition has to name the object, the other writer and the manifest to edit, or it is
	// a notification rather than a report.
	for _, want := range []string{"extras/tags/7", "netboxfake/team-b/mgmt", "prod-us", "prod-eu"} {
		if !strings.Contains(condition.Message, want) {
			t.Errorf("Conflict message does not name %q:\n  %s", want, condition.Message)
		}
	}

	// Ready is untouched: the object does match its spec, right now, for as long as that lasts.
	if ready := conditionOf(obj, netboxv1alpha1.ConditionReady); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True: a conflict is not this object's failure",
			ready.Status, ready.Reason)
	}

	want := &netboxv1alpha1.ConflictStatus{
		Reason:       netboxv1alpha1.ReasonForeignCluster,
		ClusterID:    "prod-us",
		Owner:        "netboxfake/team-b/mgmt",
		Observations: 1,
	}
	if got := obj.Status.Conflict; got == nil || got.Reason != want.Reason ||
		got.ClusterID != want.ClusterID || got.Owner != want.Owner ||
		got.Observations != want.Observations {
		t.Errorf("status.conflict = %+v, want %+v", got, want)
	}
	if obj.Status.Conflict.FirstObserved == nil {
		t.Error("status.conflict.firstObserved is unset")
	}

	if got := events.events; !slices.Equal(got, []string{"Warning/Conflict", "Normal/Updated"}) {
		t.Errorf("events = %v, want the conflict warning and the write", got)
	}

	after := testutil.ToFloat64(
		metrics.Conflicts.WithLabelValues(fakeGVK.Kind, netboxv1alpha1.ReasonForeignCluster))
	if after-before != 1 {
		t.Errorf("conflicts_total moved by %v, want 1", after-before)
	}
}

// TestConflictEscalatesOnlyWhenItPersists: one pass is a flap -- a migration, a rebuild,
// somebody restamping an object by hand -- and a claimant that is still there several passes
// later is two writers taking turns. Only the second is worth a second Event.
func TestConflictEscalatesOnlyWhenItPersists(t *testing.T) {
	client := &fakeClient{get: foreignTag("prod-us", "netboxfake/team-b/mgmt"), patched: liveTag(adoptedID)}
	engine, events := conflictedEngine(t, client)

	obj := stampedObject()
	obj.Status.ID = adoptedID

	// The live object never changes, which is what a fight looks like from one side: this
	// cluster writes, the other writes it back, and the next pass reads the other's stamp again.
	for pass := 1; pass <= conflictSustainedAfter; pass++ {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", pass, err)
		}

		if got := obj.Status.Conflict.Observations; got != int32(pass) {
			t.Errorf("after pass %d, observations = %d, want %d", pass, got, pass)
		}
	}

	warnings := make([]string, 0, 2)
	for _, event := range events.events {
		if strings.HasPrefix(event, "Warning/") {
			warnings = append(warnings, event)
		}
	}

	want := []string{"Warning/Conflict", "Warning/ConflictSustained"}
	if !slices.Equal(warnings, want) {
		t.Errorf("warnings over %d passes = %v, want %v", conflictSustainedAfter, warnings, want)
	}
}

// TestConflictClearsWhenTheClaimComesBack is the "returns to zero" half. An overlap somebody
// has just fixed -- by deleting the other CR, or by pointing it elsewhere -- has to stop being
// reported on the next pass, or the fix cannot be verified.
func TestConflictClearsWhenTheClaimComesBack(t *testing.T) {
	live := liveTag(adoptedID)
	live["tags"] = []any{map[string]any{"id": float64(7), "name": "k8s-managed"}}
	live["custom_fields"] = map[string]any{
		"k8s_uid": "6f1a-uid", "k8s_cluster": "prod-eu", "k8s_owner": "netboxfake/team-a/managed",
	}

	client := &fakeClient{get: live}
	engine, _ := conflictedEngine(t, client)

	obj := stampedObject()
	obj.Status.ID = adoptedID
	obj.Status.Conflict = &netboxv1alpha1.ConflictStatus{
		Reason: netboxv1alpha1.ReasonForeignCluster, ClusterID: "prod-us", Observations: 9,
	}
	obj.Status.Conditions = []metav1.Condition{{
		Type: netboxv1alpha1.ConditionConflict, Status: metav1.ConditionTrue,
		Reason: netboxv1alpha1.ReasonForeignCluster, LastTransitionTime: metav1.Now(),
	}}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.Conflict != nil {
		t.Errorf("status.conflict = %+v, want nil", obj.Status.Conflict)
	}

	if condition := conditionOf(obj, netboxv1alpha1.ConditionConflict); condition.Type != "" {
		t.Errorf("Conflict condition = %+v, want it removed rather than set to False", condition)
	}
}

// TestNoConflictAfterDeleteAndRecreate: `kubectl delete && kubectl apply` of one manifest gives
// the CR a new metadata.uid and changes nothing else, so its own object's stamp briefly names a
// uid that no longer exists. Reporting that would put a conflict on every re-applied object in
// the cluster, which is how a condition stops being read.
func TestNoConflictAfterDeleteAndRecreate(t *testing.T) {
	client := &fakeClient{
		get:     foreignTag("prod-eu", "netboxfake/team-a/managed"),
		patched: liveTag(adoptedID),
	}
	engine, events := conflictedEngine(t, client)

	obj := stampedObject()
	obj.Status.ID = adoptedID

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.Conflict != nil {
		t.Errorf("status.conflict = %+v, want nil: the owner stamp is this manifest's own",
			obj.Status.Conflict)
	}

	if slices.Contains(events.events, "Warning/Conflict") {
		t.Errorf("events = %v, want no conflict warning", events.events)
	}
}

// TestForeignNamespaceIsReported is the second real case: every kind is namespaced, so "which
// namespace owns this NetBox object" is the whole of the ownership question, and two namespaces
// claiming one natural key is the same fight at cluster scale.
func TestForeignNamespaceIsReported(t *testing.T) {
	client := &fakeClient{
		get:     foreignTag("prod-eu", "netboxfake/team-b/managed"),
		patched: liveTag(adoptedID),
	}
	engine, _ := conflictedEngine(t, client)

	obj := stampedObject()
	obj.Status.ID = adoptedID

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	condition := conditionOf(obj, netboxv1alpha1.ConditionConflict)
	if condition.Reason != netboxv1alpha1.ReasonForeignOwner {
		t.Fatalf("Conflict reason = %q, want ForeignOwner", condition.Reason)
	}

	if !strings.Contains(condition.Message, "netboxfake/team-b/managed") {
		t.Errorf("message does not name the other namespace's cr:\n  %s", condition.Message)
	}
}

// TestNoConflictWithoutProvenance: an endpoint with no spec.managedBy stamps nothing, so it can
// tell nobody's object from anybody's. It reports nothing rather than guessing -- the
// consequence is documented in docs/operations/provenance.md, "What breaks".
func TestNoConflictWithoutProvenance(t *testing.T) {
	client := &fakeClient{
		get:     foreignTag("prod-us", "netboxfake/team-b/mgmt"),
		patched: liveTag(adoptedID),
	}

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: stampableDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
		Events:      &fakeRecorder{},
	}

	obj := stampedObject()
	obj.Status.ID = adoptedID

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.Conflict != nil {
		t.Errorf("status.conflict = %+v, want nil", obj.Status.Conflict)
	}
}
