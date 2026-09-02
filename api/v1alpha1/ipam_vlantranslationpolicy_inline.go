// The inline child sugar of ADR-0003 rule 5, for NetBoxVLANTranslationPolicy: the one entry
// struct its `rules` list carries, and the InlineParent implementation the engine reads it
// through.
//
// A file of its own rather than an addition to ipam_vlantranslationpolicy.go, so that "adding
// a kind is adding files" stays true of adding *sugar* to one as well (CONTRIBUTING.md,
// "Extensibility"). The spec field itself has to live in the spec struct, because Go has
// nowhere else to put a struct field; everything that reads it is here.
//
// **The simplest InlineParent in the project, and worth reading for that.** One set, no
// nesting, no discriminator and no DerivedRefs: a policy's rules are not references to
// anything the policy also names, unlike a VM's primary address. So this is the whole of it --
// read the list, build the CRs, return.
//
// **Every field below is optional and the child kind is fully usable standalone**, on the
// terms sugar is allowed into v1alpha1 at all: an optional field nobody set can be removed at
// a version boundary without breaking anyone, and a materialised child is identified by its
// marker rather than by its parent's spec, so rules already materialised survive their policy
// losing the field that declared them (docs/decisions/0003-ownership-and-references.md rule
// 5).
//
// The inline form deliberately does not mirror the longhand spec. `policyRef` is absent
// because the parent *is* the policy, and `endpointRef`, `deletionPolicy`, `onConflict` and
// the provenance controls are absent because the materialiser inherits them from the parent.
// A rule that needs a different endpoint from its policy's is not an entry, it is a separate
// CR.
package v1alpha1

import (
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rulesField is the spec field the derived name and the owned-by path are built from.
//
// No discriminator: `rules` is the only inline set on this parent, so it owns the policy's
// namespace of keys outright and a rule materialises as `<policy>-<localVID>` rather than
// `<policy>-rule-<localVID>`. A discriminator exists to keep two *sibling* sets apart, and
// there is no sibling here (api/v1alpha1/inline_children.go, InlineChildSet.Discriminator).
const rulesField = "rules"

// InlineVLANTranslationRule is one entry of NetBoxVLANTranslationPolicy.spec.rules.
//
// The three columns of ipam.VLANTranslationRule a policy can state on its rule's behalf.
// `policy` is the fourth and is not here: it is the parent, and a materialised child gets it
// from the object that declared it.
type InlineVLANTranslationRule struct {
	// LocalVID is the VLAN ID on this side of the translation, and this entry's key within
	// the list.
	//
	// The list is `+listType=map` on this field, so two entries with the same `localVID` are
	// rejected by the API server. That is the identity of a list entry rather than a copy of
	// NetBox's `(policy, local_vid)` constraint -- see the field comment on
	// NetBoxVLANTranslationPolicySpec.Rules for why the distinction matters, and why
	// `remoteVID` gets no equivalent.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	LocalVID int32 `json:"localVID"`

	// RemoteVID is the VLAN ID this rule translates to.
	//
	// Required, unkeyed and unvalidated against its siblings. A policy declaring two rules
	// that translate onto one remote VID is admitted here and refused by NetBox with a 409
	// naming `ipam_vlantranslationrule_unique_policy_remote_vid`, which the second rule's CR
	// reports as `Ready=False, Reason=Conflict` while the first stays Ready.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	RemoteVID int32 `json:"remoteVID"`

	// Description is free text shown next to the rule.
	//
	// Two states rather than three, which is the one place the inline form is weaker than the
	// standalone CR: an entry that omits it cannot be told from one that set `""`, because
	// there is no way to write "leave NetBox's own description alone" inside a list entry the
	// parent rewrites wholesale. Write the rule as its own CR if that distinction matters
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// InlineChildren returns the child CRs this policy's spec declares.
//
// Pure -- it is called on every reconcile, it reads no API server and caches nothing -- and it
// names no sibling, so nothing here needs ChildName.
//
// The set is returned even when it is empty rather than skipped, for the reason
// NetBoxVirtualMachine's does: a policy whose inline list has just been emptied has no desired
// child left to read a Kind off, so the pruner falls back to the Kinds recorded in
// `status.children`, and being explicit about what this parent could declare costs nothing.
func (p *NetBoxVLANTranslationPolicy) InlineChildren() []InlineChildSet {
	rules := InlineChildSet{Field: rulesField}

	for i := range p.Spec.Rules {
		rule := p.Spec.Rules[i]

		rules.Entries = append(rules.Entries, InlineChildEntry{
			Key:     rule.key(),
			Desired: rule.child(p),
		})
	}

	return []InlineChildSet{rules}
}

// key is this entry's identity within the set: its local VID, in base 10.
//
// Base 10 and not zero-padded, because the key is what ChildName slugifies and what
// ChildPath prints -- `dc1-to-dc2-100` and `spec.rules[100]` are what a human reads in
// `kubectl get` and in the owned-by annotation, and a padded `0100` would be neither the
// number the spec wrote nor the one NetBox holds.
func (r InlineVLANTranslationRule) key() string {
	return strconv.FormatInt(int64(r.LocalVID), 10)
}

// child is the NetBoxVLANTranslationRule this entry declares, minus everything the
// materialiser owns.
//
// `endpointRef` and `deletionPolicy` are left empty on purpose: the materialiser inherits both
// from the parent unless the entry sets them, and there is no inline field that does.
//
// `policyRef` names the parent by `metadata.name`, never by `spec.name`. The two differ
// whenever the NetBox policy is called something a CR name cannot spell, and a reference by
// NetBox name would resolve to whichever CR in the namespace happened to claim it.
func (r InlineVLANTranslationRule) child(p *NetBoxVLANTranslationPolicy) *NetBoxVLANTranslationRule {
	return &NetBoxVLANTranslationRule{
		Spec: NetBoxVLANTranslationRuleSpec{
			PolicyRef:   VLANTranslationPolicyRef{Name: p.GetName()},
			LocalVID:    r.LocalVID,
			RemoteVID:   r.RemoteVID,
			Description: r.Description,
		},
	}
}

// Compile-time proof that the policy implements the sugar. A capability the engine reaches by
// type assertion is a contract nothing else checks, so a signature drifting out of shape would
// otherwise show up as a policy that quietly materialises nothing.
//
// No InlineRefParent: that second capability exists for a parent whose own spec has to name
// one of its children -- a VM's primary address -- and a policy names none of its rules.
var (
	_ InlineParent  = (*NetBoxVLANTranslationPolicy)(nil)
	_ client.Object = (*NetBoxVLANTranslationRule)(nil)
)
