package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamVLANTranslationRuleDescriptor()) }

// ipamVLANTranslationRuleDescriptor is ipam.VLANTranslationRule as data.
//
// **Two natural-key candidates, and neither is a fallback.** Unlike the policy above, this
// model's identity comes straight out of the committed IR -- `natural_keys` has two entries,
// both unconditional and both with `unusable: null`:
//
//	models.UniqueConstraint(fields=('policy', 'local_vid'),
//	    name='%(app_label)s_%(class)s_unique_policy_local_vid')
//	models.UniqueConstraint(fields=('policy', 'remote_vid'),
//	    name='%(app_label)s_%(class)s_unique_policy_remote_vid')
//
// (hack/testdata/ir-4.6.8.json.gz -> ipam.VLANTranslationRule.natural_keys;
// docs/netbox-schema.md -> ipam.VLANTranslationRule, meta.constraints.) The IR names the
// filters as well: `policy_id`, `local_vid` and `remote_vid` are all on
// VLANTranslationRuleFilterSet (NetBox 4.6.8 `netbox/ipam/filtersets.py:1184`).
//
// The precedent for an ordered pair with two candidates is wireless.WirelessLink, and the
// shape is borrowed from it -- but the reason is different and the difference is the whole of
// this kind. There, the two candidates are one constraint and its reverse, because Postgres
// would store `(a,b)` and `(b,a)` as two rows and the second candidate exists to stop the
// operator creating a duplicate of a link somebody declared the other way round. Here they are
// **two separate constraints**, both enforced by the database at once, and the second candidate
// exists to find a row the first cannot see:
//
//   - Ordinary case: the rule is looked up by `(policy_id, local_vid)` and found or created.
//   - **A rule already occupies this policy's `remote_vid`.** Candidate one finds nothing --
//     no rule has this `local_vid` -- and candidate two finds the offender. Under the default
//     `onConflict: Fail` that is a Conflict naming the other rule, with nothing written; under
//     `Adopt` it is one PATCH that moves the existing rule's `local_vid`.
//
// Without candidate two, that second case is a POST, and NetBox answers it with a 409 on
// `unique_policy_remote_vid`. Both endings are correct and neither is silent, which is what
// makes this a genuine choice rather than a bug fix: the lookup turns the collision into a
// Conflict *the operator can name and, on request, resolve*, instead of a server error it can
// only report. The ticket's design note asks for exactly that -- let the constraint surface,
// do not pre-validate it -- and two candidates is how a lookup surfaces one.
//
// The ordering of the two matters and follows NetBox's own: `meta.ordering` is
// `('policy', 'local_vid')`, so `local_vid` is the side NetBox treats as primary and it is
// tried first.
//
// **No `Deferred` entries, and none possible.** validateDeferred refuses to defer a field a
// natural key matches on, and `policy` is matched on by both candidates. That is correct
// rather than a limitation: a rule whose policy has not resolved has no identity at all, so
// the engine waits at RefsResolved=False rather than creating a rule in whichever policy it
// finds first (docs/concepts/references.md).
//
// **No provenance stamp**, for the same serializer reason as the policy:
// `VLANTranslationRuleSerializer.Meta.fields` is
// `('id', 'url', 'display', 'policy', 'local_vid', 'remote_vid', 'description')` (NetBox 4.6.8,
// `netbox/ipam/api/serializers_/vlans.py:116`), with no `tags` and no `custom_fields` despite
// the NetBoxModel base.
func ipamVLANTranslationRuleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVLANTranslationRule"),
		Endpoint:   "ipam/vlan-translation-rules",
		ObjectType: "ipam.vlantranslationrule",
		Scope:      apiextensionsv1.NamespaceScoped,

		// False because the serializer drops both columns, not because the model lacks the
		// mixins. See the doc comment.
		Taggable:        false,
		CustomFieldable: false,

		// Every writable column this model has. Nothing is inherited and nothing is missing:
		// unlike almost every other kind here there is no `owner`, no `comments` and no
		// `tags` to leave out.
		Fields: []Field{
			{Spec: "localVID", API: "local_vid"},
			{Spec: "remoteVID", API: "remote_vid"},
			{Spec: "description", API: "description"},
			// Written as `policy`, filtered as `policy_id`. CASCADE, which is what makes it
			// this kind's containment parent (docs/netbox-schema.md ->
			// ipam.VLANTranslationRule, `policy ForeignKey REQ ->
			// ipam.VLANTranslationPolicy on_delete=CASCADE`).
			{
				Spec: "policyRef", API: "policy", Class: ClassRefOne,
				Target:          netboxv1alpha1.VLANTranslationPolicyRef{}.TargetGVK(),
				CascadeOnDelete: true,
			},
		},

		// Both constraints, `local_vid` first because NetBox's own ordering puts it first. See
		// the doc comment for why the second is not a fallback.
		//
		// No null pins and no third candidate: all three columns in the two keys are required,
		// so there is no state in which one is missing and a narrower identity applies.
		//
		// No DuplicateSpec. Two rules translating the same VID inside one policy is not a case
		// where the operator picks its own by the provenance stamp -- there is no stamp on this
		// kind at all -- and it is not a case NetBox's data model requires, the way
		// ipam.IPAddress's duplicates are. It means somebody declared the same rewrite twice,
		// and the honest answer is Conflict.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "policy_id", Spec: "policyRef"},
					{Filter: "local_vid", Spec: "localVID"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "policy_id", Spec: "policyRef"},
					{Filter: "remote_vid", Spec: "remoteVID"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The policy, because `policy` is the FK the *server* cascades: NetBox deletes a
		// policy's rules with the policy, so the CR has to go with the row or the engine's
		// create-if-absent step resurrects what NetBox deliberately deleted
		// (docs/decisions/0003-ownership-and-references.md rule 4, #203). It is also the single
		// non-controller owner reference, which is what garbage-collects a `spec.rules` child
		// when its policy is deleted.
		ContainmentRef: "policyRef",

		// No RetainOnDelete, matching the policy: a rewrite is configuration a manifest
		// recreates, and it would be incoherent for the child to outlive a parent NetBox
		// cascades away anyway (#176, #186, docs/concepts/deletion.md).

		// The two hyperlinks, and nothing else. `created` and `last_updated` are absent for the
		// reason they are absent on the policy: this serializer's `Meta.fields` does not list
		// them, so there is no column to guard.
		ReadOnly: []string{"url", "display"},
	}
}
