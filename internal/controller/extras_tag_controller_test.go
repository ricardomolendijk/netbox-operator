package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// tagWrite is one mutating request the stub received, in the order it received them.
// Recording the payload too, because "PATCHed exactly one field" is the assertion that
// separates an operator that co-exists with humans from one that overwrites their work.
type tagWrite struct {
	method  string
	id      int
	payload netbox.Object
}

// tagStub is a NetBox that serves extras/tags out of a map.
//
// Enough of the REST contract for the engine to look a tag up, create it, patch it and
// delete it -- plus the ability to change a tag behind the operator's back, which is how
// a drift test simulates somebody editing the NetBox UI.
type tagStub struct {
	*httptest.Server

	mu     sync.Mutex
	tags   map[int]netbox.Object
	nextID int
	writes []tagWrite
}

func newTagStub(t *testing.T) *tagStub {
	t.Helper()

	stub := &tagStub{tags: map[int]netbox.Object{}, nextID: 1}
	stub.Server = httptest.NewServer(http.HandlerFunc(stub.route))
	t.Cleanup(stub.Close)

	return stub
}

func (s *tagStub) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/status/" {
		writeJSON(w, http.StatusOK, netbox.Object{"netbox-version": "4.6.8", "plugins": map[string]any{}})

		return
	}

	rest, isTags := strings.CutPrefix(r.URL.Path, "/api/extras/tags/")
	if !isTags {
		http.NotFound(w, r)

		return
	}

	id, err := strconv.Atoi(strings.Trim(rest, "/"))
	hasID := err == nil

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && hasID:
		s.serveGet(w, id)
	case r.Method == http.MethodGet:
		s.serveList(w, r)
	case r.Method == http.MethodPost:
		s.servePost(w, r)
	case r.Method == http.MethodPatch && hasID:
		s.servePatch(w, r, id)
	case r.Method == http.MethodDelete && hasID:
		s.serveDelete(w, id)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *tagStub) serveGet(w http.ResponseWriter, id int) {
	tag, ok := s.tags[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	writeJSON(w, http.StatusOK, tag)
}

func (s *tagStub) serveList(w http.ResponseWriter, r *http.Request) {
	results := []netbox.Object{}

	// Sorted, so a query matching several tags returns them in a stable order and an
	// ambiguity test cannot pass by accident.
	for _, id := range slices.Sorted(maps.Keys(s.tags)) {
		if tagMatches(s.tags[id], r.URL.Query()) {
			results = append(results, s.tags[id])
		}
	}

	writeJSON(w, http.StatusOK, netbox.Object{"count": len(results), "next": nil, "results": results})
}

func (s *tagStub) servePost(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeTag(w, r)
	if !ok {
		return
	}

	id := s.nextID
	s.nextID++

	tag := maps.Clone(payload)
	tag["id"] = id
	tag["url"] = fmt.Sprintf("%s/api/extras/tags/%d/", s.URL, id)
	// The read-only columns every ChangeLoggedModel returns. Present so that a descriptor
	// which wrongly tried to manage one would show up as a permanent diff here.
	tag["display"] = tag["name"]
	tag["created"] = "2026-08-21T00:00:00Z"
	tag["last_updated"] = "2026-08-21T00:00:00Z"

	s.tags[id] = tag
	s.writes = append(s.writes, tagWrite{method: http.MethodPost, id: id, payload: payload})

	writeJSON(w, http.StatusCreated, tag)
}

func (s *tagStub) servePatch(w http.ResponseWriter, r *http.Request, id int) {
	tag, exists := s.tags[id]
	if !exists {
		writeJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	payload, ok := decodeTag(w, r)
	if !ok {
		return
	}

	// Merged, not replaced: NetBox's PATCH leaves a column the body omits alone, which is
	// the whole reason the operator can send only the diff.
	for name, value := range payload {
		tag[name] = value
	}

	s.tags[id] = tag
	s.writes = append(s.writes, tagWrite{method: http.MethodPatch, id: id, payload: payload})

	writeJSON(w, http.StatusOK, tag)
}

func (s *tagStub) serveDelete(w http.ResponseWriter, id int) {
	if _, exists := s.tags[id]; !exists {
		writeJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	delete(s.tags, id)
	s.writes = append(s.writes, tagWrite{method: http.MethodDelete, id: id})

	w.WriteHeader(http.StatusNoContent)
}

// tagMatches applies the query the engine sent. Only exact matches, which is all a
// natural key on extras.Tag can produce.
func tagMatches(tag netbox.Object, query url.Values) bool {
	for name, values := range query {
		if name == "limit" || name == "offset" {
			continue
		}

		if fmt.Sprint(tag[name]) != values[0] {
			return false
		}
	}

	return true
}

// seed puts a tag in NetBox that the operator did not create, which is the object every
// adoption and Conflict case turns on. It returns its id.
func (s *tagStub) seed(tag netbox.Object) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextID
	s.nextID++

	stored := maps.Clone(tag)
	stored["id"] = id
	stored["url"] = fmt.Sprintf("%s/api/extras/tags/%d/", s.URL, id)
	s.tags[id] = stored

	return id
}

// tag returns a copy of one stored tag, or nil when it is gone.
func (s *tagStub) tag(id int) netbox.Object {
	s.mu.Lock()
	defer s.mu.Unlock()

	tag, ok := s.tags[id]
	if !ok {
		return nil
	}

	return maps.Clone(tag)
}

// setField changes a tag behind the operator's back: a human editing the NetBox UI.
func (s *tagStub) setField(t *testing.T, id int, name string, value any) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	tag, ok := s.tags[id]
	if !ok {
		t.Fatalf("no tag %d to edit", id)
	}

	tag[name] = value
}

// countBySlug is how many tags carry a slug. NetBox enforces one; more than one would
// mean the operator duplicated rather than adopted.
func (s *tagStub) countBySlug(slug string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0

	for _, tag := range s.tags {
		if tag["slug"] == slug {
			count++
		}
	}

	return count
}

func (s *tagStub) recorded() []tagWrite {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.writes)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeTag(w http.ResponseWriter, r *http.Request) (netbox.Object, bool) {
	payload := netbox.Object{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, netbox.Object{"detail": "unparseable body"})

		return nil, false
	}

	return payload, true
}

// readyEndpoint points a namespace at stub and waits until it has a client.
//
// resyncPeriod is one second because that is what the engine uses as its drift re-check
// interval -- through endpointProvider, which is the wiring under test as much as
// anything else here. A test cannot wait for the ten-minute default.
func readyEndpoint(t *testing.T, ns, target string) {
	t.Helper()
	readyEndpointWith(t, ns, target, nil)
}

// readyEndpointWith is readyEndpoint for a test that needs a field it does not set:
// spec.driftMode, whose three values are three different operators
// (gitops_test.go, NBO-065).
func readyEndpointWith(t *testing.T, ns, target string, mutate func(*netboxv1alpha1.NetBoxEndpoint)) {
	t.Helper()
	makeSecret(t, k8sClient, ns, "nb-token", "valid-token")

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "homelab", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            target,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
			DriftMode:      netboxv1alpha1.DriftCorrect,
			ResyncPeriod:   metav1.Duration{Duration: time.Second},
		},
	}
	if mutate != nil {
		mutate(endpoint)
	}
	if err := k8sClient.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint in %s: %v", ns, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), endpoint) })

	eventually(t, "an endpoint client in "+ns, func() bool {
		_, ok := clients.Lookup(ns, "homelab")

		return ok
	})
}

// makeTag applies a NetBoxTag whose slug is its name, and removes it again afterwards so
// the finalizer does not outlive the stub it needs in order to come off.
func makeTag(t *testing.T, ns, slug string, mutate func(*netboxv1alpha1.NetBoxTag)) *netboxv1alpha1.NetBoxTag {
	t.Helper()

	tag := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Name: slug, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxTagSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Managed",
			Slug:             slug,
		},
	}
	if mutate != nil {
		mutate(tag)
	}

	if err := k8sClient.Create(context.Background(), tag); err != nil {
		t.Fatalf("creating tag %s/%s: %v", ns, slug, err)
	}

	t.Cleanup(func() { removeTag(ns, slug) })

	return tag
}

// removeTag deletes a tag and waits for the engine to release its finalizer. It reports
// nothing: a cleanup that fails a test hides the failure the test actually found, and a
// tag left terminating only costs some log noise once the stub has closed.
func removeTag(ns, slug string) {
	ctx := context.Background()
	key := client.ObjectKey{Namespace: ns, Name: slug}

	_ = k8sClient.Delete(ctx, &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Name: slug, Namespace: ns},
	})

	for range 100 {
		if err := k8sClient.Get(ctx, key, &netboxv1alpha1.NetBoxTag{}); apierrors.IsNotFound(err) {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func fetchTag(ns, slug string) *netboxv1alpha1.NetBoxTag {
	out := &netboxv1alpha1.NetBoxTag{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: slug}, out); err != nil {
		return nil
	}

	return out
}

func mustFetchTag(t *testing.T, ns, slug string) *netboxv1alpha1.NetBoxTag {
	t.Helper()

	out := fetchTag(ns, slug)
	if out == nil {
		t.Fatalf("tag %s/%s not found", ns, slug)
	}

	return out
}

// tagCondition returns one condition, or the zero value when it was never set.
func tagCondition(tag *netboxv1alpha1.NetBoxTag, condType string) metav1.Condition {
	for _, condition := range tag.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// tagIsReady is the poll predicate almost every case starts with.
func tagIsReady(ns, slug string) bool {
	tag := fetchTag(ns, slug)
	if tag == nil {
		return false
	}

	return tagCondition(tag, netboxv1alpha1.ConditionReady).Status == metav1.ConditionTrue
}

// TestTagIsCreatedInNetBoxAndReachesReady is the end-to-end claim NBO-008 exists to make:
// a `kubectl apply` of the first real kind produces a NetBox object, records its id, and
// says so.
func TestTagIsCreatedInNetBoxAndReachesReady(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)

	makeTag(t, ns, "managed", func(tag *netboxv1alpha1.NetBoxTag) {
		tag.Spec.Color = "2196f3"
		tag.Spec.Description = "managed by the operator"
		tag.Spec.ObjectTypes = []string{"dcim.device", "virtualization.virtualmachine"}
	})

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "managed") })

	tag := mustFetchTag(t, ns, "managed")
	if tag.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready tag; it is the operator's only claim on the object")
	}

	if tag.Status.URL == "" {
		t.Error("status.url is unset; it comes straight out of the create response")
	}

	if tag.Status.Adopted {
		t.Error("status.adopted is true for a tag the operator created itself")
	}

	if tag.Status.ObservedGeneration != tag.Generation {
		t.Errorf("observedGeneration = %d, generation = %d; `kubectl wait` lies without it",
			tag.Status.ObservedGeneration, tag.Generation)
	}

	if !slices.Contains(tag.Finalizers, netboxv1alpha1.Finalizer) {
		t.Error("no finalizer on a tag that exists in NetBox; deleting the CR would orphan it")
	}

	// AllResolved rather than NotImplemented: extras.Tag has no references at all, which
	// is exactly why it is reconcilable before internal/resolver lands (NBO-012).
	if refs := tagCondition(tag, netboxv1alpha1.ConditionRefsResolved); refs.Reason != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved reason = %q, want %q", refs.Reason, netboxv1alpha1.ReasonAllResolved)
	}

	if synced := tagCondition(tag, netboxv1alpha1.ConditionSynced); synced.Status != metav1.ConditionTrue {
		t.Errorf("Synced = %+v, want True", synced)
	}

	writes := stub.recorded()
	if len(writes) != 1 || writes[0].method != http.MethodPost {
		t.Fatalf("netbox saw %+v, want exactly one POST", writes)
	}

	want := netbox.Object{
		"name":         "Managed",
		"slug":         "managed",
		"color":        "2196f3",
		"description":  "managed by the operator",
		"weight":       float64(1000),
		"object_types": []any{"dcim.device", "virtualization.virtualmachine"},
	}
	if !reflect.DeepEqual(writes[0].payload, want) {
		t.Errorf("POST body = %v, want %v", writes[0].payload, want)
	}

	// The envelope stayed out of the payload. NetBox ignores a column it does not know
	// rather than rejecting it, so a leaked endpointRef would travel over the wire forever
	// and never fail.
	for _, envelope := range []string{"endpointRef", "onConflict", "deletionPolicy"} {
		if _, leaked := writes[0].payload[envelope]; leaked {
			t.Errorf("the POST body carried the envelope field %q", envelope)
		}
	}
}

// TestTagDriftIsCorrectedWithinOneResync is the difference between this operator and the
// one-shot CLI it replaces: an edit made in the NetBox UI is drift, and drift is corrected
// without anything changing in Git.
func TestTagDriftIsCorrectedWithinOneResync(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)
	makeTag(t, ns, "drifting", func(tag *netboxv1alpha1.NetBoxTag) { tag.Spec.Color = "2196f3" })

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "drifting") })
	id := int(mustFetchTag(t, ns, "drifting").Status.ID)

	stub.setField(t, id, "color", "ff0000")

	eventually(t, "the colour corrected", func() bool { return stub.tag(id)["color"] == "2196f3" })

	// Corrected in place. A new id would mean the engine created a second tag rather than
	// recognising the one it already owned.
	if got := int(mustFetchTag(t, ns, "drifting").Status.ID); got != id {
		t.Errorf("status.id = %d after drift correction, want the original %d", got, id)
	}

	writes := stub.recorded()
	last := writes[len(writes)-1]

	if last.method != http.MethodPatch {
		t.Fatalf("the last write was %s, want a PATCH: %+v", last.method, writes)
	}

	if !reflect.DeepEqual(last.payload, netbox.Object{"color": "2196f3"}) {
		t.Errorf("PATCH body = %v, want only color", last.payload)
	}
}

// TestTagSpecEditPatchesOnlyTheChangedField is the co-existence guarantee, checked at the
// wire. NetBox merges a partial PATCH, so sending unchanged columns is precisely how an
// operator overwrites a value somebody else owns.
func TestTagSpecEditPatchesOnlyTheChangedField(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)
	makeTag(t, ns, "recoloured", func(tag *netboxv1alpha1.NetBoxTag) { tag.Spec.Color = "2196f3" })

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "recoloured") })

	tag := mustFetchTag(t, ns, "recoloured")
	id := int(tag.Status.ID)
	tag.Spec.Color = "4caf50"

	if err := k8sClient.Update(context.Background(), tag); err != nil {
		t.Fatalf("editing spec.color: %v", err)
	}

	eventually(t, "the new colour reached netbox", func() bool { return stub.tag(id)["color"] == "4caf50" })

	writes := stub.recorded()
	if len(writes) != 2 {
		t.Fatalf("netbox saw %+v, want one POST and one PATCH", writes)
	}

	if !reflect.DeepEqual(writes[1].payload, netbox.Object{"color": "4caf50"}) {
		t.Errorf("PATCH body = %v, want only color", writes[1].payload)
	}
}

// TestTagAdoptsAPreExistingNetBoxTag covers the case a fresh cluster pointed at an
// existing NetBox is entirely made of.
func TestTagAdoptsAPreExistingNetBoxTag(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)

	id := stub.seed(netbox.Object{
		"name": "Legacy", "slug": "legacy", "color": "ff0000",
		"weight": float64(500), "description": "made by hand",
	})

	makeTag(t, ns, "legacy", func(tag *netboxv1alpha1.NetBoxTag) {
		tag.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
		tag.Spec.Color = "2196f3"
	})

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "legacy") })

	tag := mustFetchTag(t, ns, "legacy")
	if int(tag.Status.ID) != id {
		t.Errorf("status.id = %d, want the pre-existing tag %d", tag.Status.ID, id)
	}

	if !tag.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if want := map[string]string{"slug": "legacy"}; !reflect.DeepEqual(tag.Status.NaturalKey, want) {
		t.Errorf("status.naturalKey = %v, want %v", tag.Status.NaturalKey, want)
	}

	if n := stub.countBySlug("legacy"); n != 1 {
		t.Errorf("%d tags with slug legacy, want 1: it was duplicated rather than adopted", n)
	}

	writes := stub.recorded()
	for _, write := range writes {
		if write.method == http.MethodPost {
			t.Fatalf("netbox saw a POST during adoption: %+v", writes)
		}
	}

	// The adopting PATCH carried the fields the spec sets and differ, and nothing else.
	if len(writes) != 1 || writes[0].method != http.MethodPatch {
		t.Fatalf("netbox saw %+v, want exactly one PATCH", writes)
	}

	want := netbox.Object{"name": "Managed", "color": "2196f3", "weight": float64(1000)}
	if !reflect.DeepEqual(writes[0].payload, want) {
		t.Errorf("adopting PATCH = %v, want %v", writes[0].payload, want)
	}

	// The description the CR never mentions is left alone. "Spec omission means do not
	// manage" is what lets the operator share an object with a human.
	if got := stub.tag(id)["description"]; got != "made by hand" {
		t.Errorf("description = %v, want it untouched at %q", got, "made by hand")
	}
}

// TestTagConflictRefusesToAdoptByDefault is the other half of adoption: finding somebody
// else's object is not permission to take it over, because the very next step reconciles
// it towards this spec and there is no undo for that.
func TestTagConflictRefusesToAdoptByDefault(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)

	id := stub.seed(netbox.Object{"name": "Legacy", "slug": "guarded", "color": "ff0000"})
	makeTag(t, ns, "guarded", nil)

	eventually(t, "Reason=Conflict", func() bool {
		tag := fetchTag(ns, "guarded")
		if tag == nil {
			return false
		}

		return tagCondition(tag, netboxv1alpha1.ConditionReady).Reason == netboxv1alpha1.ReasonConflict
	})

	tag := mustFetchTag(t, ns, "guarded")
	ready := tagCondition(tag, netboxv1alpha1.ConditionReady)

	if ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %q, want False", ready.Status)
	}

	if !strings.Contains(ready.Message, strconv.Itoa(id)) {
		t.Errorf("Ready message = %q, want it to name netbox object %d", ready.Message, id)
	}

	if tag.Status.ID != 0 {
		t.Errorf("status.id = %d; a refused object must record no claim on it", tag.Status.ID)
	}

	if writes := stub.recorded(); len(writes) != 0 {
		t.Errorf("netbox saw %+v, want no writes at all", writes)
	}

	if got := stub.tag(id)["color"]; got != "ff0000" {
		t.Errorf("colour = %v, want the pre-existing ff0000 left alone", got)
	}
}

// TestDeletingATagRemovesItFromNetBox is the deletion contract at its simplest: no
// dependents, no PROTECT, and the finalizer comes off only once NetBox has confirmed.
func TestDeletingATagRemovesItFromNetBox(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)
	tag := makeTag(t, ns, "doomed", nil)

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "doomed") })
	id := int(mustFetchTag(t, ns, "doomed").Status.ID)

	if err := k8sClient.Delete(context.Background(), tag); err != nil {
		t.Fatalf("deleting the tag: %v", err)
	}

	eventually(t, "the tag gone from netbox", func() bool { return stub.tag(id) == nil })
	eventually(t, "the CR gone from the cluster", func() bool { return fetchTag(ns, "doomed") == nil })

	writes := stub.recorded()
	last := writes[len(writes)-1]

	if last.method != http.MethodDelete || last.id != id {
		t.Errorf("the last write was %s on %d, want DELETE on %d", last.method, last.id, id)
	}
}

// TestSameSlugInTwoNamespacesFirstWins is the cost of a namespaced CRD over a globally
// unique NetBox column, and it is a routine case rather than an edge one: tags are
// catalogue-shaped, so two teams claiming `shared` is Tuesday
// (docs/decisions/0002-crd-scoping.md).
func TestSameSlugInTwoNamespacesFirstWins(t *testing.T) {
	stub := newTagStub(t)
	first, second := newNamespaceSuffixed(t, "-a"), newNamespaceSuffixed(t, "-b")
	readyEndpoint(t, first, stub.URL)
	readyEndpoint(t, second, stub.URL)

	makeTag(t, first, "shared", nil)
	eventually(t, "the first namespace ready", func() bool { return tagIsReady(first, "shared") })

	winner := mustFetchTag(t, first, "shared")
	if winner.Status.ID == 0 {
		t.Fatal("the winner recorded no id")
	}

	makeTag(t, second, "shared", nil)
	eventually(t, "the second namespace in Conflict", func() bool {
		tag := fetchTag(second, "shared")
		if tag == nil {
			return false
		}

		return tagCondition(tag, netboxv1alpha1.ConditionReady).Reason == netboxv1alpha1.ReasonConflict
	})

	// The Conflict names the NetBox object that won, which is the only identifier the
	// engine has: it reconciles one object at a time and never sees the other namespace,
	// so it cannot name the winning CR.
	loser := tagCondition(mustFetchTag(t, second, "shared"), netboxv1alpha1.ConditionReady)
	if !strings.Contains(loser.Message, strconv.Itoa(int(winner.Status.ID))) {
		t.Errorf("Ready message = %q, want it to name the winning netbox object %d",
			loser.Message, winner.Status.ID)
	}

	// Several resyncs, to show the refusal is stable rather than a race the loser
	// eventually wins.
	time.Sleep(3 * time.Second)

	if got := mustFetchTag(t, second, "shared"); got.Status.ID != 0 {
		t.Errorf("the loser recorded status.id = %d for an object it does not own", got.Status.ID)
	}

	if got := mustFetchTag(t, first, "shared"); got.Status.ID != winner.Status.ID {
		t.Errorf("the winner's status.id moved from %d to %d", winner.Status.ID, got.Status.ID)
	}

	if !tagIsReady(first, "shared") {
		t.Error("the winner stopped being Ready once the second namespace claimed its slug")
	}

	if n := stub.countBySlug("shared"); n != 1 {
		t.Errorf("%d tags with slug shared, want 1", n)
	}

	if writes := stub.recorded(); len(writes) != 1 {
		t.Errorf("netbox saw %+v, want the winner's single POST", writes)
	}
}
