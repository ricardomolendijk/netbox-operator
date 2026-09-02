package reconciler

import (
	"context"
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// stampedEndpoint is an endpoint whose provenance bootstrap resolved everything, which is
// what the endpoint controller caches once spec.managedBy is set.
func stampedEndpoint(client NetBoxClient) Endpoint {
	cfg := provenance.FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})

	return Endpoint{
		Client: client, Resync: testResync,
		Provenance: provenance.Stamp{Config: cfg, TagID: 7, Fields: cfg.CustomFieldNames()},
	}
}

// stampableDescriptor is fakeDescriptor's kind on a NetBox model that carries both stamp
// columns, as dcim.Site and dcim.Region do.
func stampableDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.Taggable, d.CustomFieldable = true, true

	return d
}

// stampedEngine wires an engine whose endpoint stamps, and returns the pieces a test
// asserts on.
func stampedEngine(t *testing.T, d registry.Descriptor, client NetBoxClient) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: d, registered: true},
		Endpoints:   fakeEndpoints{endpoint: stampedEndpoint(client), ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}
}

// stampedObject is fakeObject with the UID the API server would have given it.
func stampedObject() *fakeKind {
	obj := fakeObject()
	obj.UID = "6f1a-uid"

	return obj
}

// wantCustomFields is the stamp fakeObject's owner produces.
var wantCustomFields = map[string]any{
	"k8s_uid":     "6f1a-uid",
	"k8s_cluster": "prod-eu",
	"k8s_owner":   "netboxfake/team-a/managed",
}

func TestStampOnCreate(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}
	obj := stampedObject()

	if _, err := stampedEngine(t, stampableDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Object{
		"name": "Managed", "slug": "managed", "color": "9e9e9e",
		"tags":          []any{7},
		"custom_fields": wantCustomFields,
	}
	if got := client.lastPayload(); !reflect.DeepEqual(got, want) {
		t.Errorf("create payload =\n  %#v\nwant\n  %#v", got, want)
	}

	wantStatus := &netboxv1alpha1.ProvenanceStatus{
		ClusterID: "prod-eu", Tag: "k8s-managed",
		CustomFields: map[string]string{
			"k8s_uid": "6f1a-uid", "k8s_cluster": "prod-eu", "k8s_owner": "netboxfake/team-a/managed",
		},
	}
	if !reflect.DeepEqual(obj.Status.Provenance, wantStatus) {
		t.Errorf("status.provenance = %+v, want %+v", obj.Status.Provenance, wantStatus)
	}
}

// TestStampOnAdopt is the NBO-006 case: an object somebody else made gains the stamp when it
// is taken over, and the tags a human put on it survive.
func TestStampOnAdopt(t *testing.T) {
	live := liveTag(9)
	live["tags"] = []any{map[string]any{"id": float64(3), "name": "by-hand"}}

	client := &fakeClient{list: []netbox.Object{live}, patched: liveTag(9)}

	obj := stampedObject()
	obj.Spec.OnConflict = netboxv1alpha1.ConflictAdopt

	if _, err := stampedEngine(t, stampableDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if !obj.Status.Adopted {
		t.Fatal("the object was not adopted")
	}

	// A PATCH, not a POST: the stamp is the only difference, and it is sent as one.
	// GETONE, not LIST: NBO-074 made the engine look up through Client.GetOne so the
	// ambiguity error can name every match, and GetOne is itself built on List.
	if got := client.methods(); !slices.Equal(got, []string{"GETONE", "PATCH"}) {
		t.Fatalf("netbox calls = %v, want GETONE then PATCH", got)
	}

	want := netbox.Object{"tags": []any{3, 7}, "custom_fields": wantCustomFields}
	if got := client.lastPayload(); !reflect.DeepEqual(got, want) {
		t.Errorf("patch payload =\n  %#v\nwant\n  %#v", got, want)
	}
}

// TestStampCausesNoDriftOnceApplied is the loop guard. `tags` is read back as nested objects
// and written as bare ids, and `custom_fields` comes back carrying definitions the operator
// does not own; either compared wrongly is a PATCH every resync for the life of the object.
func TestStampCausesNoDriftOnceApplied(t *testing.T) {
	live := liveTag(7)
	live["tags"] = []any{map[string]any{"id": float64(7), "name": "k8s-managed"}}
	live["custom_fields"] = map[string]any{
		"k8s_uid": "6f1a-uid", "k8s_cluster": "prod-eu", "k8s_owner": "netboxfake/team-a/managed",
		"k8s_allocation_identity": nil, "somebody_elses_field": "keep me",
	}

	client := &fakeClient{get: live}

	obj := stampedObject()
	obj.Status.ID = 7

	if _, err := stampedEngine(t, stampableDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := client.methods(); !slices.Equal(got, []string{"GET"}) {
		t.Errorf("netbox calls = %v, want a read and no write", got)
	}

	synced := conditionOf(obj, netboxv1alpha1.ConditionSynced)
	if synced.Reason != netboxv1alpha1.ReasonNoDrift {
		t.Errorf("Synced reason = %q, want NoDrift", synced.Reason)
	}
}

// TestStampRestoresARemovedTag: somebody deleted the tag in the NetBox UI. The stamp is
// re-applied on the update path, which is why stamp() runs there and not only on create and
// adopt.
func TestStampRestoresARemovedTag(t *testing.T) {
	live := liveTag(7)
	live["tags"] = []any{}
	live["custom_fields"] = map[string]any{
		"k8s_uid": "6f1a-uid", "k8s_cluster": "prod-eu", "k8s_owner": "netboxfake/team-a/managed",
	}

	client := &fakeClient{get: live, patched: liveTag(7)}

	obj := stampedObject()
	obj.Status.ID = 7

	if _, err := stampedEngine(t, stampableDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Object{"tags": []any{7}}
	if got := client.lastPayload(); !reflect.DeepEqual(got, want) {
		t.Errorf("patch payload = %#v, want only the tag restored: %#v", got, want)
	}
}

// TestNoStampWithoutManagedBy is the pre-NBO-075 behaviour, and it has to stay reachable: an
// endpoint with no spec.managedBy writes exactly what the spec says and nothing else.
func TestNoStampWithoutManagedBy(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: stampableDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	obj := stampedObject()

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Object{"name": "Managed", "slug": "managed", "color": "9e9e9e"}
	if got := client.lastPayload(); !reflect.DeepEqual(got, want) {
		t.Errorf("payload = %#v, want the spec alone: %#v", got, want)
	}
	if obj.Status.Provenance != nil {
		t.Errorf("status.provenance = %+v, want nil", obj.Status.Provenance)
	}
}

// TestNoStampOnAnUnstampableKind is extras.Tag: its NetBox model carries neither column, so
// writing either would be a value NetBox silently drops and the engine re-sends forever.
// fakeDescriptor leaves both flags false, exactly as internal/registry/extras_tag.go does.
func TestNoStampOnAnUnstampableKind(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}
	obj := stampedObject()

	if _, err := stampedEngine(t, fakeDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Object{"name": "Managed", "slug": "managed", "color": "9e9e9e"}
	if got := client.lastPayload(); !reflect.DeepEqual(got, want) {
		t.Errorf("payload = %#v, want the spec alone: %#v", got, want)
	}
	if obj.Status.Provenance != nil {
		t.Errorf("status.provenance = %+v, want nil on a kind that cannot be stamped", obj.Status.Provenance)
	}
}

// TestStampIsClearedFromStatus: spec.managedBy was removed from the endpoint, so the object
// no longer carries a stamp the operator wrote. Leaving the old record would tell
// NetBoxSweep (NBO-046) it may delete something nothing is stamping any more.
func TestStampIsClearedFromStatus(t *testing.T) {
	client := &fakeClient{get: liveTag(7)}

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: stampableDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	obj := stampedObject()
	obj.Status.ID = 7
	obj.Status.Provenance = &netboxv1alpha1.ProvenanceStatus{ClusterID: "prod-eu", Tag: "k8s-managed"}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.Provenance != nil {
		t.Errorf("status.provenance = %+v, want nil", obj.Status.Provenance)
	}
}

func TestFieldRulesTagsAreM2M(t *testing.T) {
	if !fieldRules(stampableDescriptor()).M2M[provenance.TagsField] {
		t.Error("tags is not compared as an M2M set on a taggable kind, which is a patch loop")
	}
	if fieldRules(fakeDescriptor()).M2M[provenance.TagsField] {
		t.Error("tags is compared as an M2M set on a kind that has no tags column")
	}
}

// TestStampSurvivesADryRun: nothing is sent, so nothing is stamped in NetBox -- but the
// payload the endpoint reports it *would* have written has to include the stamp, or the
// rehearsal is not a rehearsal of the real write.
func TestStampSurvivesADryRun(t *testing.T) {
	client := &fakeClient{dryRun: dryRunClient(t)}
	obj := stampedObject()

	if _, err := stampedEngine(t, stampableDescriptor(), client).Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := client.lastPayload()
	if payload["tags"] == nil || payload["custom_fields"] == nil {
		t.Errorf("the rehearsed payload %#v omits the stamp", payload)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %q, want False on a suppressed write", ready.Status)
	}
}

// TestOwnStampRecoversALostStatusID is the root cause of the orphan finalizer.go can now only
// clean up: a create that reached NetBox whose status write never landed. status.id is 0, the
// natural key matches the operator's own object, and before this the engine called that
// somebody else's and advised adoption -- on the object it had made itself, forever, because
// nothing about the next pass is any different.
//
// The uid stamp is what settles it. It is on the object because the POST carried it, it holds
// this CR's metadata.uid, and no other CR can be carrying that value.
func TestOwnStampRecoversALostStatusID(t *testing.T) {
	live := liveTag(9)
	live["custom_fields"] = map[string]any{"k8s_uid": "6f1a-uid"}

	client := &fakeClient{list: []netbox.Object{live}, patched: liveTag(9)}
	events := &fakeRecorder{}

	engine := stampedEngine(t, stampableDescriptor(), client)
	engine.Events = events

	// No spec.onConflict: recognising your own object must not need a policy about
	// somebody else's.
	obj := stampedObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.ID != 9 {
		t.Fatalf("status.id = %d, want 9 recovered from the stamp", obj.Status.ID)
	}

	// A PATCH, not a POST. Creating here is the double-write this recovery exists to stop.
	if got := client.methods(); !slices.Equal(got, []string{"GETONE", "PATCH"}) {
		t.Fatalf("netbox calls = %v, want GETONE then PATCH", got)
	}

	// Recognised, not adopted: this pass took over nothing, and an Adopted Event on an
	// object the operator created itself is a false report of a takeover.
	if obj.Status.Adopted {
		t.Error("status.adopted = true, want false: the object was the operator's own")
	}

	if slices.Contains(events.events, "Normal/Adopted") {
		t.Errorf("events = %v, want no Adopted event", events.events)
	}
}

// TestAForeignStampIsStillAnAdoption is the boundary of the recovery above, and the case that
// keeps it from being a way to take over other people's objects: a stamp naming a different
// uid is a different CR's object -- including this manifest's own previous incarnation, since
// `kubectl delete && kubectl apply` issues a new uid -- so the adoption question is asked
// exactly as it was.
func TestAForeignStampIsStillAnAdoption(t *testing.T) {
	live := liveTag(9)
	live["custom_fields"] = map[string]any{"k8s_uid": "somebody-else"}

	client := &fakeClient{list: []netbox.Object{live}, patched: liveTag(9)}
	events := &fakeRecorder{}

	engine := stampedEngine(t, stampableDescriptor(), client)
	engine.Events = events

	obj := stampedObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if obj.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: nothing was adopted", obj.Status.ID)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady).Reason; got != netboxv1alpha1.ReasonConflict {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonConflict)
	}
}
