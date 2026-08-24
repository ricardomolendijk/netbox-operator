package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// applyTag server-side applies a NetBoxTag from raw YAML-shaped JSON, the way Flux does.
//
// Raw rather than the typed struct on purpose: `omitempty` means a typed client cannot send
// `description: ""` at all, so a test that used one could not express the state it is about.
// It is also the honest reproduction -- a GitOps tool applies the bytes in the repository,
// not a Go struct.
func applyTag(t *testing.T, ns, slug, specFields string) {
	t.Helper()

	body := []byte(`{"apiVersion":"netbox.kubeforge.org/v1alpha1","kind":"NetBoxTag",` +
		`"metadata":{"name":"` + slug + `","namespace":"` + ns + `"},` +
		`"spec":{"endpointRef":"homelab","onConflict":"Adopt","name":"Managed","slug":"` + slug + `"` +
		specFields + `}}`)

	tag := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: slug},
	}
	if err := apiClient.Patch(context.Background(), tag, client.RawPatch(types.ApplyPatchType, body),
		client.FieldOwner("kustomize-controller"), client.ForceOwnership); err != nil {
		t.Fatalf("applying tag %s/%s: %v", ns, slug, err)
	}

	t.Cleanup(func() { removeTag(ns, slug) })
}

// TestServerSideApplyCanClearADescription is NBO-079's acceptance criterion against a real
// API server: a user who deletes the text in Git gets the text deleted in NetBox.
//
// Only reproducible here. The signal the engine reads -- which spec fields the applier
// claimed -- is produced by the API server's field management and by nothing the operator can
// fake, so a unit test can assert the engine acts on the metadata but not that the metadata
// says what this test needs it to say.
func TestServerSideApplyCanClearADescription(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, tagKind)
	readyEndpoint(t, ns, target)

	applyTag(t, ns, "clearable", `,"description":"written in git"`)
	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "clearable") })

	id := mustFetchTag(t, ns, "clearable").Status.ID
	if got := stub.get(id)["description"]; got != "written in git" {
		t.Fatalf("description = %v, want the value the apply carried", got)
	}

	// The change a user makes in Git: the line is deleted from the text but the field is
	// still theirs, so it is applied as empty rather than dropped from the manifest.
	applyTag(t, ns, "clearable", `,"description":""`)

	eventually(t, "the description cleared", func() bool { return stub.get(id)["description"] == "" })
}

// TestServerSideApplyLeavesAnUnclaimedDescriptionAlone is the other half, and the reason the
// fix is not simply dropping `omitempty`: a field the manifest never mentions stays as NetBox
// has it, even though its Go value is the same empty string the case above sends.
//
// The two tests apply the same object with the same stored spec. The only difference is the
// managed-fields entry, which is exactly the claim.
func TestServerSideApplyLeavesAnUnclaimedDescriptionAlone(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, tagKind)
	readyEndpoint(t, ns, target)

	id := stub.seed(netbox.Object{
		"name": "Managed", "slug": "kept", "color": "9e9e9e",
		"weight": float64(1000), "description": "written by a human",
	})

	applyTag(t, ns, "kept", "")
	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "kept") })

	if got := mustFetchTag(t, ns, "kept").Status.ID; got != id {
		t.Fatalf("status.id = %d, want the seeded tag %d; nothing was adopted", got, id)
	}

	if got := stub.get(id)["description"]; got != "written by a human" {
		t.Errorf("description = %v, want it untouched: the manifest never mentioned it", got)
	}
}

// applySite is applyTag for a NetBoxSite, which is the kind that carries the two decimal
// columns.
func applySite(t *testing.T, ns, slug, specFields string) {
	t.Helper()

	body := []byte(`{"apiVersion":"netbox.kubeforge.org/v1alpha1","kind":"NetBoxSite",` +
		`"metadata":{"name":"` + slug + `","namespace":"` + ns + `"},` +
		`"spec":{"endpointRef":"homelab","onConflict":"Adopt","name":"Home","slug":"` + slug + `"` +
		specFields + `}}`)

	site := &netboxv1alpha1.NetBoxSite{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: slug},
	}
	if err := apiClient.Patch(context.Background(), site, client.RawPatch(types.ApplyPatchType, body),
		client.FieldOwner("kustomize-controller"), client.ForceOwnership); err != nil {
		t.Fatalf("applying site %s/%s: %v", ns, slug, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), site) })
}

// TestServerSideApplyCanClearALatitude is #170: a coordinate could be set and never unset,
// because the pattern admitted no value that meant "cleared".
//
// It reaches NetBox as `null` rather than as `""`, and that is the half worth asserting on
// the wire: NetBox's latitude is a nullable DecimalField, which parses `""` as a number and
// rejects it -- so a pattern that admitted the empty string without the payload turning it
// into null would trade an admission failure for a failure on every write, which is worse
// (registry.Field.EmptyIsNull).
func TestServerSideApplyCanClearALatitude(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	applySite(t, ns, "coords", `,"status":"active","latitude":"51.9244","longitude":"4.4777"`)
	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "coords") })

	id := fetchSite(ns, "coords").Status.ID
	if got := stub.get(id)["latitude"]; got != "51.924400" {
		t.Fatalf("latitude = %v, want the padded 51.924400 the apply carried", got)
	}

	// The line deleted from the manifest's value but not from the manifest: still claimed,
	// so still managed, and now empty.
	applySite(t, ns, "coords", `,"status":"active","latitude":"","longitude":"4.4777"`)

	eventually(t, "the latitude cleared", func() bool {
		value, present := stub.get(id)["latitude"]

		return present && value == nil
	})

	// The other coordinate is untouched, so the clear is one field's intent rather than the
	// whole payload going empty.
	if got := stub.get(id)["longitude"]; got != "4.477700" {
		t.Errorf("longitude = %v, want 4.477700 left alone", got)
	}
}

// TestOperatorFieldManagerNeverOwnsSpec is ADR-0005 §1 as an assertion about the API server's
// own bookkeeping, which is a stronger statement than counting the operator's calls.
//
// Field management records, per field, which manager last set it. So if the operator's field
// manager appears under `f:spec`, the operator wrote a spec -- whatever the code looks like,
// whatever the test doubles said. Reading managedFields does not weaken the invariant, it
// makes it externally checkable: `kubectl get -o yaml` shows it, and so does this.
func TestOperatorFieldManagerNeverOwnsSpec(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, tagKind)
	readyEndpoint(t, ns, target)

	applyTag(t, ns, "owned", `,"description":"written in git"`)
	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "owned") })

	// A drift correction on top, because that is the busiest writing path there is: it
	// writes two conditions, lastSyncTime and lastAppliedHash.
	id := mustFetchTag(t, ns, "owned").Status.ID
	stub.setField(id, "color", "ff0000")
	eventually(t, "the colour corrected", func() bool { return stub.get(id)["color"] == "9e9e9e" })

	entries := mustFetchTag(t, ns, "owned").ManagedFields

	var ours int

	for _, entry := range entries {
		if entry.Manager != reconciler.FieldManager {
			continue
		}
		ours++

		if entry.FieldsV1 == nil {
			continue
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			t.Fatalf("decoding the operator's managed fields: %v", err)
		}

		if _, spec := fields["f:spec"]; spec {
			t.Errorf("%s owns %s under subresource %q; the operator wrote a spec",
				reconciler.FieldManager, entry.FieldsV1.Raw, entry.Subresource)
		}
	}

	if ours == 0 {
		t.Fatalf("no managed-fields entry for %q at all, so this test proved nothing: %v",
			reconciler.FieldManager, managerNames(entries))
	}
}

// managerNames is the field managers on an object, for a failure message that says who did
// write it.
func managerNames(entries []metav1.ManagedFieldsEntry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Manager+"/"+string(entry.Operation)+"/"+entry.Subresource)
	}

	return strings.Join(names, ", ")
}
