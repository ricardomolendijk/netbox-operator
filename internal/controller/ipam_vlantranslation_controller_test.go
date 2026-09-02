package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The controller half of NBO-068, against a real API server.
//
// The registry tests hold the two identities as data. What only an API server and a stub
// NetBox can answer is here: that a policy's inline rules materialise at names the API server
// accepts, that emptying the list prunes them, that the *second* natural-key candidate finds a
// row the first cannot see, and that editing one VID PATCHes one column.
//
// `spec.rules` producing child CRs with a controller owner reference is as far as an envtest
// can go on the ticket's cascade criterion: garbage collection is the kube-controller-manager's
// job and envtest runs no controller-manager, so what is asserted here is the owner reference
// that makes GC happen, plus the pruning the operator does itself.

// The two endpoints this feature writes to.
//
// The rule is the first kind in this package needing `altKeys`: its two candidates are
// `(policy_id, local_vid)` and `(policy_id, remote_vid)`, over *different* columns, so the
// stub has to narrow on both or the second candidate matches whatever the first left behind.
var (
	vlanTranslationPolicyKind = stubKind{endpoint: "ipam/vlan-translation-policies", key: "name"}
	vlanTranslationRuleKind   = stubKind{
		endpoint: "ipam/vlan-translation-rules",
		key:      "local_vid",
		refKeys:  []string{"policy_id"},
		altKeys:  []string{"remote_vid"},
	}
)

// translationTree is the two-endpoint NetBox a policy with rules needs, on one server.
//
// The vmTree shape, two stubs instead of four. Ids start in their own thousand so an assertion
// about a rule's id cannot pass against its policy's by coincidence.
type translationTree struct {
	policies *netboxStubServer
	rules    *netboxStubServer
}

func (t *translationTree) all() []*netboxStubServer {
	return []*netboxStubServer{t.policies, t.rules}
}

func newTranslationTreeStub(t *testing.T) (*translationTree, string) {
	t.Helper()

	tree := &translationTree{
		policies: newTreeStub(t, vlanTranslationPolicyKind, 1000),
		rules:    newTreeStub(t, vlanTranslationRuleKind, 2000),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, stub := range tree.all() {
			if strings.HasPrefix(r.URL.Path, "/api/"+stub.endpoint) {
				stub.route(w, r)

				return
			}
		}

		// /api/status/ and anything nobody claimed. The policy's stub answers the version
		// probe and 404s the rest, which is what makes an unexpected endpoint visible.
		tree.policies.route(w, r)
	}))
	t.Cleanup(srv.Close)

	for _, stub := range tree.all() {
		stub.url = srv.URL
	}

	return tree, srv.URL
}

// makeTranslationPolicy applies a NetBoxVLANTranslationPolicy and removes it afterwards so the
// finalizer does not outlive the stub it needs in order to come off.
func makeTranslationPolicy(
	t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxVLANTranslationPolicy),
) {
	t.Helper()

	policy := &netboxv1alpha1.NetBoxVLANTranslationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxVLANTranslationPolicySpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
		},
	}
	if mutate != nil {
		mutate(policy)
	}

	if err := k8sClient.Create(context.Background(), policy); err != nil {
		t.Fatalf("creating policy %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), policy) })
}

// seedPolicyRow puts a policy into the stub NetBox that no CR owns, and returns its id.
//
// The three standalone-rule tests below reference it in `id` mode, which the resolver verifies
// with a GET against the policy's own endpoint -- so the row has to be there or every one of
// them fails at RefsResolved rather than at the thing it is about. Id mode costs nothing here:
// what these tests assert is what reaches `ipam/vlan-translation-rules`, and an id-mode ref
// renders through the same code a name-mode one ends up in.
func seedPolicyRow(tree *translationTree) int64 {
	return tree.policies.seed(netbox.Object{"name": "dc1-to-dc2"})
}

// makeTranslationRule applies a standalone NetBoxVLANTranslationRule -- the longhand form, not
// a materialised child.
func makeTranslationRule(
	t *testing.T, ns, name string, policyID int64,
	mutate func(*netboxv1alpha1.NetBoxVLANTranslationRule),
) {
	t.Helper()

	rule := &netboxv1alpha1.NetBoxVLANTranslationRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxVLANTranslationRuleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			PolicyRef:        netboxv1alpha1.VLANTranslationPolicyRef{ID: idOf(policyID)},
			LocalVID:         100,
			RemoteVID:        2100,
		},
	}
	if mutate != nil {
		mutate(rule)
	}

	if err := k8sClient.Create(context.Background(), rule); err != nil {
		t.Fatalf("creating rule %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rule) })
}

func fetchTranslationPolicy(ns, name string) *netboxv1alpha1.NetBoxVLANTranslationPolicy {
	policy := &netboxv1alpha1.NetBoxVLANTranslationPolicy{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, policy); err != nil {
		return nil
	}

	return policy
}

func fetchTranslationRule(ns, name string) *netboxv1alpha1.NetBoxVLANTranslationRule {
	rule := &netboxv1alpha1.NetBoxVLANTranslationRule{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, rule); err != nil {
		return nil
	}

	return rule
}

func translationRuleCondition(ns, name, condition string) (metav1.Condition, bool) {
	rule := fetchTranslationRule(ns, name)
	if rule == nil {
		return metav1.Condition{}, false
	}

	for _, c := range rule.Status.Conditions {
		if c.Type == condition {
			return c, true
		}
	}

	return metav1.Condition{}, false
}

func translationPolicyIsReady(ns, name string) bool {
	policy := fetchTranslationPolicy(ns, name)
	if policy == nil {
		return false
	}

	for _, c := range policy.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// threeRules is the ticket's own example: a policy carrying three rewrites.
func threeRules(p *netboxv1alpha1.NetBoxVLANTranslationPolicy) {
	p.Spec.Rules = []netboxv1alpha1.InlineVLANTranslationRule{
		{LocalVID: 100, RemoteVID: 2100, Description: "Management"},
		{LocalVID: 101, RemoteVID: 2101, Description: "Storage"},
		{LocalVID: 102, RemoteVID: 2102, Description: "vMotion"},
	}
}

// TestTranslationPolicyMaterialisesItsRules is the ticket's first acceptance criterion: one
// manifest, three rule CRs at their derived names, three rows in NetBox.
//
// The names are `<policy>-<localVID>` with no discriminator, unlike a VM's disks: `rules` is
// the only inline set on this parent, so there is no sibling for a token to keep it apart from.
func TestTranslationPolicyMaterialisesItsRules(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	makeTranslationPolicy(t, ns, "dc1-to-dc2", threeRules)

	eventually(t, "the policy to materialise three rules", func() bool {
		policy := fetchTranslationPolicy(ns, "dc1-to-dc2")

		return policy != nil && len(policy.Status.Children) == 3
	})

	policy := fetchTranslationPolicy(ns, "dc1-to-dc2")

	byPath := map[string]netboxv1alpha1.ChildStatus{}
	for _, child := range policy.Status.Children {
		byPath[child.Path] = child
	}

	for path, name := range map[string]string{
		"spec.rules[100]": "dc1-to-dc2-100",
		"spec.rules[101]": "dc1-to-dc2-101",
		"spec.rules[102]": "dc1-to-dc2-102",
	} {
		child, declared := byPath[path]
		if !declared {
			t.Errorf("status.children carries no entry for %s", path)

			continue
		}

		if child.Kind != "NetBoxVLANTranslationRule" || child.Name != name {
			t.Errorf("%s materialised %s/%s, want NetBoxVLANTranslationRule/%s",
				path, child.Kind, child.Name, name)
		}
	}

	// The owner reference is the whole of the cascade half of the ticket: envtest runs no
	// garbage collector, so what can be asserted is the reference the collector acts on.
	child := fetchTranslationRule(ns, "dc1-to-dc2-100")
	if child == nil {
		t.Fatal("the first rule child was never materialised")
	}

	owner := metav1.GetControllerOf(child)
	switch {
	case owner == nil:
		t.Error("the rule child has no controller owner reference, so deleting the policy " +
			"would orphan it and the engine would recreate the row NetBox cascaded away")
	case owner.UID != policy.GetUID():
		t.Errorf("the rule child is controlled by uid %s, want the policy's %s",
			owner.UID, policy.GetUID())
	case owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion:
		t.Error("the rule child does not set blockOwnerDeletion")
	}

	eventually(t, "all three rules to reach NetBox", func() bool {
		return len(tree.rules.recorded()) >= 3
	})

	// Three rows, each with the policy written as `policy` and the VIDs under NetBox's own
	// names. `localVID` sent verbatim would be dropped in silence, which is why the field map
	// is a table rather than a convention.
	seen := map[float64]float64{}

	for id := int64(2000); id < 2010; id++ {
		row := tree.rules.get(id)
		if row == nil {
			continue
		}

		if row["policy"] != float64(child.Status.ID) && row["policy"] == nil {
			t.Errorf("rule %d carries no `policy` column: %v", id, row)
		}

		local, localOK := row["local_vid"].(float64)
		remote, remoteOK := row["remote_vid"].(float64)

		if !localOK || !remoteOK {
			t.Errorf("rule %d is missing local_vid or remote_vid: %v", id, row)

			continue
		}

		seen[local] = remote
	}

	for local, remote := range map[float64]float64{100: 2100, 101: 2101, 102: 2102} {
		if seen[local] != remote {
			t.Errorf("netbox has no rule translating %v to %v; it has %v", local, remote, seen)
		}
	}
}

// TestTranslationPolicyPrunesARemovedRule is the other half of the cascade the operator owns
// itself: emptying an entry out of `spec.rules` deletes that child and leaves the rest alone.
func TestTranslationPolicyPrunesARemovedRule(t *testing.T) {
	ns := newNamespace(t)
	_, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	makeTranslationPolicy(t, ns, "dc1-to-dc2", threeRules)

	eventually(t, "the third rule child to exist", func() bool {
		return fetchTranslationRule(ns, "dc1-to-dc2-102") != nil
	})

	policy := fetchTranslationPolicy(ns, "dc1-to-dc2")
	policy.Spec.Rules = policy.Spec.Rules[:2]

	if err := k8sClient.Update(context.Background(), policy); err != nil {
		t.Fatalf("removing the third rule: %v", err)
	}

	eventually(t, "the third rule child to be pruned", func() bool {
		return fetchTranslationRule(ns, "dc1-to-dc2-102") == nil
	})

	for _, name := range []string{"dc1-to-dc2-100", "dc1-to-dc2-101"} {
		if fetchTranslationRule(ns, name) == nil {
			t.Errorf("pruning the third rule took %s with it", name)
		}
	}
}

// TestDuplicateLocalVIDReportsConflict is the ticket's third acceptance criterion.
//
// Two *standalone* rule CRs, because the inline list is keyed on `localVID` and the API server
// rejects a duplicate there before any of this runs -- which is the point of keying it. The
// case that reaches NetBox is two manifests, and the answer is a Conflict rather than a second
// row: the first candidate finds the existing rule, and `onConflict: Fail` refuses to take it
// over.
func TestDuplicateLocalVIDReportsConflict(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	policy := seedPolicyRow(tree)
	tree.rules.seed(netbox.Object{
		"policy": float64(policy), "local_vid": float64(100), "remote_vid": float64(2100),
	})

	makeTranslationRule(t, ns, "voice", policy, nil)

	eventually(t, "the second rule to report a conflict", func() bool {
		c, found := translationRuleCondition(ns, "voice", netboxv1alpha1.ConditionReady)

		return found && c.Status == metav1.ConditionFalse &&
			c.Reason == netboxv1alpha1.ReasonConflict
	})

	if n := tree.rules.countByKey("100"); n != 1 {
		t.Errorf("%d rules with local_vid 100, want 1: the duplicate was written rather than "+
			"reported", n)
	}
}

// TestTranslationRuleIsFoundByTheRemoteVIDCandidate is why this kind has two natural-key
// candidates rather than one.
//
// A rule already occupies `remote_vid: 2100` inside the policy, under a *different*
// `local_vid`. Candidate one -- `(policy_id, local_vid)` -- finds nothing. Without a second
// candidate the engine would POST, and NetBox would answer 409 on
// `unique_policy_remote_vid`. With it the row is found, and under `onConflict: Adopt` one
// PATCH normalises it rather than a create failing.
func TestTranslationRuleIsFoundByTheRemoteVIDCandidate(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	policy := seedPolicyRow(tree)
	existing := tree.rules.seed(netbox.Object{
		"policy": float64(policy), "local_vid": float64(900), "remote_vid": float64(2100),
	})

	makeTranslationRule(t, ns, "voice", policy,
		func(r *netboxv1alpha1.NetBoxVLANTranslationRule) {
			r.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
		})

	eventually(t, "the rule to adopt the row its remote VID collides with", func() bool {
		rule := fetchTranslationRule(ns, "voice")

		return rule != nil && rule.Status.ID == existing
	})

	rule := fetchTranslationRule(ns, "voice")
	if !rule.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this row, so it adopted it")
	}

	if got := tree.rules.get(existing)["local_vid"]; got != float64(100) {
		t.Errorf("local_vid on the adopted row = %v, want 100: the adoption did not normalise "+
			"the numbering", got)
	}

	if n := len(tree.rules.recorded()); n == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for _, write := range tree.rules.recorded() {
		if write.Method == http.MethodPost {
			t.Errorf("the engine POSTed %v: the second candidate should have found the "+
				"existing row rather than creating a duplicate NetBox would refuse",
				write.Payload)
		}
	}
}

// TestEditingARemoteVIDPatchesOnlyThatField is the ticket's fourth acceptance criterion.
//
// A rule is not UpdateRecreate and has no RecreateOn, so an edit to either VID is an ordinary
// PATCH -- and the payload carries the one column that moved, not the whole object. Anything
// wider would rewrite `description` and `policy` on every edit, which is what makes a drift
// report about a hand-edited field lie.
func TestEditingARemoteVIDPatchesOnlyThatField(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	makeTranslationRule(t, ns, "voice", seedPolicyRow(tree),
		func(r *netboxv1alpha1.NetBoxVLANTranslationRule) {
			r.Spec.Description = "Voice"
		})

	eventually(t, "the rule to be created", func() bool {
		rule := fetchTranslationRule(ns, "voice")

		return rule != nil && rule.Status.ID != 0
	})

	afterCreate := len(tree.rules.recorded())

	rule := fetchTranslationRule(ns, "voice")
	rule.Spec.RemoteVID = 2200

	if err := k8sClient.Update(context.Background(), rule); err != nil {
		t.Fatalf("editing remoteVID: %v", err)
	}

	eventually(t, "the edit to reach NetBox", func() bool {
		return len(tree.rules.recorded()) > afterCreate
	})

	patches := tree.rules.recorded()[afterCreate:]
	if len(patches) != 1 {
		t.Fatalf("%d writes after the edit, want 1: %v", len(patches), patches)
	}

	patch := patches[0]
	if patch.Method != http.MethodPatch {
		t.Errorf("the edit was a %s, want PATCH", patch.Method)
	}

	if got := fmt.Sprint(patch.Payload["remote_vid"]); got != "2200" {
		t.Errorf("the PATCH carries remote_vid = %v, want 2200", got)
	}

	for column := range patch.Payload {
		if column != "remote_vid" {
			t.Errorf("the PATCH also carries %q; only the column that moved belongs in it: %v",
				column, patch.Payload)
		}
	}
}

// TestTranslationPolicyWritesNoTagsAndNoCustomFields is the consequence of the one surprising
// fact about this pair, asserted where it would actually bite.
//
// Both serializers list their fields longhand and neither lists `tags` or `custom_fields`
// (netbox/ipam/api/serializers_/vlans.py:116,123), so both Descriptors set Taggable and
// CustomFieldable false. A payload carrying either would be dropped by DRF in silence and
// re-sent on every reconcile -- and there would be no provenance stamp to show for it, because
// NetBox never stored one.
func TestTranslationPolicyWritesNoTagsAndNoCustomFields(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newTranslationTreeStub(t)
	readyEndpoint(t, ns, target)

	makeTranslationPolicy(t, ns, "dc1-to-dc2", threeRules)

	eventually(t, "the policy to be Ready", func() bool {
		return translationPolicyIsReady(ns, "dc1-to-dc2")
	})

	writes := append(tree.policies.recorded(), tree.rules.recorded()...) //nolint:gocritic // two stubs
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range []string{"tags", "custom_fields"} {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s %s) carries %q: neither serializer accepts it, and "+
					"DRF drops the key rather than rejecting it: %v",
					i, write.Method, write.Endpoint, column, write.Payload)
			}
		}
	}
}
