package v1alpha1

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// policyWithRules is the ticket's own example: a policy carrying three rewrites.
func policyWithRules() *NetBoxVLANTranslationPolicy {
	return &NetBoxVLANTranslationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "dc1-to-dc2", Namespace: "net"},
		Spec: NetBoxVLANTranslationPolicySpec{
			Name: "DC1 to DC2",
			Rules: []InlineVLANTranslationRule{
				{LocalVID: 100, RemoteVID: 2100, Description: "Management"},
				{LocalVID: 101, RemoteVID: 2101},
				{LocalVID: 102, RemoteVID: 2102},
			},
		},
	}
}

// TestPolicyInlineChildrenDeclaresOneSet is the shape: one set, no discriminator, no nesting.
//
// The discriminator matters and its absence is the assertion. `rules` is the only inline list
// on this parent, so it owns the policy's namespace of keys and a rule is `dc1-to-dc2-100`
// rather than `dc1-to-dc2-rule-100`. A discriminator added later would rename every
// materialised child, which is a delete and a create in NetBox.
func TestPolicyInlineChildrenDeclaresOneSet(t *testing.T) {
	sets := policyWithRules().InlineChildren()

	if len(sets) != 1 {
		t.Fatalf("InlineChildren() returned %d sets, want 1", len(sets))
	}

	if sets[0].Field != "rules" {
		t.Errorf("Field = %q, want rules", sets[0].Field)
	}

	if sets[0].Discriminator != "" {
		t.Errorf("Discriminator = %q, want none: `rules` is the only set on this parent",
			sets[0].Discriminator)
	}

	if len(sets[0].Entries) != 3 {
		t.Fatalf("%d entries, want 3", len(sets[0].Entries))
	}

	for _, entry := range sets[0].Entries {
		if len(entry.Children) != 0 {
			t.Errorf("entry %q declares nested children; a rule has none", entry.Key)
		}
	}
}

// TestPolicyInlineChildKeyIsTheLocalVID pins the key, the derived name and the owned-by path
// together -- they are all built from the same string, so they move together or not at all.
func TestPolicyInlineChildKeyIsTheLocalVID(t *testing.T) {
	policy := policyWithRules()
	entries := policy.InlineChildren()[0].Entries

	for i, want := range []string{"100", "101", "102"} {
		if entries[i].Key != want {
			t.Errorf("entry %d Key = %q, want %q", i, entries[i].Key, want)
		}

		path := []ChildSegment{{Field: "rules", Key: entries[i].Key}}

		if got := ChildName(policy.GetName(), path); got != "dc1-to-dc2-"+want {
			t.Errorf("ChildName = %q, want dc1-to-dc2-%s", got, want)
		}

		if got := ChildPath(path); got != "spec.rules["+want+"]" {
			t.Errorf("ChildPath = %q, want spec.rules[%s]", got, want)
		}
	}
}

// TestPolicyInlineChildCarriesTheParentByMetadataName is the reference the materialised rule
// resolves through, and the one detail that is easy to get wrong.
//
// `policyRef` names `metadata.name`, never `spec.name`. Here the two differ -- the CR is
// `dc1-to-dc2` and the NetBox policy is `DC1 to DC2` -- so a version reading the wrong one
// produces a reference no CR in the namespace answers to.
func TestPolicyInlineChildCarriesTheParentByMetadataName(t *testing.T) {
	policy := policyWithRules()
	entry := policy.InlineChildren()[0].Entries[0]

	rule, ok := entry.Desired.(*NetBoxVLANTranslationRule)
	if !ok {
		t.Fatalf("Desired is %T, want *NetBoxVLANTranslationRule", entry.Desired)
	}

	if rule.Spec.PolicyRef.Name != "dc1-to-dc2" {
		t.Errorf("policyRef.name = %q, want the CR's metadata.name dc1-to-dc2 (spec.name is "+
			"%q, which is not a CR name)", rule.Spec.PolicyRef.Name, policy.Spec.Name)
	}

	if rule.Spec.LocalVID != 100 || rule.Spec.RemoteVID != 2100 {
		t.Errorf("the child carries %d -> %d, want 100 -> 2100",
			rule.Spec.LocalVID, rule.Spec.RemoteVID)
	}

	if rule.Spec.Description != "Management" {
		t.Errorf("description = %q, want Management", rule.Spec.Description)
	}

	// The materialiser owns all four, and an entry that set one would be a child that
	// disagreed with its parent about which NetBox it is writing to.
	if rule.GetName() != "" || rule.GetNamespace() != "" || len(rule.GetOwnerReferences()) != 0 {
		t.Error("the child carries a name, a namespace or an owner reference; all three are " +
			"the materialiser's")
	}

	if rule.Spec.EndpointRef != "" || rule.Spec.DeletionPolicy != "" {
		t.Errorf("the child sets endpointRef %q or deletionPolicy %q; both are inherited "+
			"from the parent", rule.Spec.EndpointRef, rule.Spec.DeletionPolicy)
	}
}

// TestPolicyInlineChildrenSurvivesAReorder is why the path is keyed rather than indexed.
//
// The annotation is read on every reconcile to tell two entries apart, so an index-based path
// would change for every entry below an edit and prune-and-recreate the lot. Reordering the
// list must produce the same set of children.
func TestPolicyInlineChildrenSurvivesAReorder(t *testing.T) {
	forward := keysAndNames(policyWithRules())

	reversed := policyWithRules()
	reversed.Spec.Rules = []InlineVLANTranslationRule{
		{LocalVID: 102, RemoteVID: 2102},
		{LocalVID: 101, RemoteVID: 2101},
		{LocalVID: 100, RemoteVID: 2100, Description: "Management"},
	}

	if got := keysAndNames(reversed); !reflect.DeepEqual(got, forward) {
		t.Errorf("reordering the list produced %v, want the same set %v", got, forward)
	}
}

// keysAndNames is the set of (key -> derived name) pairs a policy declares, order removed.
func keysAndNames(p *NetBoxVLANTranslationPolicy) map[string]string {
	out := map[string]string{}

	for _, set := range p.InlineChildren() {
		for _, entry := range set.Entries {
			out[entry.Key] = ChildName(p.GetName(),
				[]ChildSegment{{Field: set.Field, Key: entry.Key}})
		}
	}

	return out
}

// TestPolicyInlineChildrenIsPure holds the contract the materialiser depends on: called twice
// on one object it returns the same tree, and it does not write into the spec it read.
func TestPolicyInlineChildrenIsPure(t *testing.T) {
	policy := policyWithRules()
	before := policy.DeepCopy()

	first := keysAndNames(policy)
	second := keysAndNames(policy)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls disagreed: %v then %v", first, second)
	}

	if !reflect.DeepEqual(policy.Spec, before.Spec) {
		t.Error("InlineChildren() modified the spec it read")
	}
}

// TestPolicyWithNoRulesStillDeclaresTheSet is the empty case, and it is not a no-op.
//
// A policy whose list has just been emptied has no desired child left to read a Kind off, so
// the pruner falls back to the Kinds in `status.children`. Returning the set anyway is what
// keeps "what this parent could declare" answerable without one.
func TestPolicyWithNoRulesStillDeclaresTheSet(t *testing.T) {
	policy := &NetBoxVLANTranslationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "empty"},
		Spec:       NetBoxVLANTranslationPolicySpec{Name: "empty"},
	}

	sets := policy.InlineChildren()
	if len(sets) != 1 {
		t.Fatalf("InlineChildren() returned %d sets, want 1 even when it is empty", len(sets))
	}

	if len(sets[0].Entries) != 0 {
		t.Errorf("%d entries on a policy declaring no rules", len(sets[0].Entries))
	}
}
