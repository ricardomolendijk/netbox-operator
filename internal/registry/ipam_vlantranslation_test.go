package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestVLANTranslationDescriptorsAreRegisteredAndValid is the boot check for NBO-068's two
// kinds.
func TestVLANTranslationDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxVLANTranslationPolicy", "ipam/vlan-translation-policies", "ipam.vlantranslationpolicy"},
		{"NetBoxVLANTranslationRule", "ipam/vlan-translation-rules", "ipam.vlantranslationrule"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// Looked up in docs/netbox-schema.md's endpoint map, never derived:
			// `ipam/vlan-translation-policies` is not the pluralisation of
			// `ipam.VLANTranslationPolicy` by any rule the operator could apply.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// A translation policy is configuration a manifest recreates, not allocated
			// state -- the ipam.VLANGroup exception among the ipam kinds (#186). Deleting one
			// frees no address, no VLAN ID and no range, and the ticket's own criterion is
			// that deleting the policy CR leaves nothing behind in NetBox, which Retain would
			// make impossible.
			if d.RetainOnDelete {
				t.Errorf("RetainOnDelete = true; a VLAN translation table is configuration, " +
					"not an allocation (#176, #186, docs/concepts/deletion.md)")
			}
		})
	}
}

// TestVLANTranslationKindsCarryNoProvenanceStamp is the one genuinely surprising fact about
// this pair, pinned so a later "every PrimaryModel is taggable" tidy-up cannot undo it.
//
// Both models mix in TagsMixin and CustomFieldsMixin -- the policy is a PrimaryModel and the
// rule a NetBoxModel -- and neither *serializer* passes them through.
// `VLANTranslationPolicySerializer.Meta.fields` and `VLANTranslationRuleSerializer.Meta.fields`
// are both written out longhand and list neither column (NetBox 4.6.8,
// `netbox/ipam/api/serializers_/vlans.py:116,123`; the same fact is in the committed IR as
// `write_path`). So a payload carrying either would be dropped by DRF in silence and re-sent
// forever, and there is no stamp to adopt by -- the natural keys below are the whole of how the
// operator recognises its own objects here.
//
// coverage_test.go's TestDescriptorFlagsMatchTheSchema checks the same thing from the IR; this
// says it in the vocabulary of the ticket, so a reader of this file does not have to derive it.
func TestVLANTranslationKindsCarryNoProvenanceStamp(t *testing.T) {
	for _, kind := range []string{"NetBoxVLANTranslationPolicy", "NetBoxVLANTranslationRule"} {
		d := descriptorFor(t, kind)

		if d.Taggable {
			t.Errorf("%s: Taggable = true; the serializer's Meta.fields has no `tags`", kind)
		}

		if d.CustomFieldable {
			t.Errorf("%s: CustomFieldable = true; the serializer's Meta.fields has no "+
				"`custom_fields`", kind)
		}
	}
}

// TestVLANTranslationPolicyKeyIsTheUniqueNameColumn is the identity the IR does not carry, and
// the reason it does not.
//
// `natural_keys` is `[]` for ipam.VLANTranslationPolicy in the committed IR, because the
// extractor builds that list from `meta.constraints` alone and this model declares none. The
// uniqueness is one level down, on the column -- `name CharField REQ UNIQUE len=100`
// (docs/netbox-schema.md; `fields[name].sql.unique: true` in the IR) -- which is the same
// situation coverage_test.go's irSQL.Unique comment describes in general and dcim.Interface
// works around for its own reason.
//
// Asserted rather than trusted, because a wrong natural key is not a missing feature: it
// silently adopts another object, which is the class of defect behind #206 and #216.
func TestVLANTranslationPolicyKeyIsTheUniqueNameColumn(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANTranslationPolicy")

	want := []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Errorf("NaturalKeys = %+v, want %+v: the key is the column-level UNIQUE on `name`, "+
			"the only unique column this model has", d.NaturalKeys, want)
	}

	// There is no `slug` on this model at all, unlike every OrganizationalModel in the
	// catalogue, so a key built on one would filter on a column NetBox does not have -- which
	// it answers by ignoring the filter and returning everything.
	if _, mapped := d.FieldFor("slug"); mapped {
		t.Error("the field map declares slug; ipam.VLANTranslationPolicy has no slug column")
	}
}

// TestVLANTranslationRuleHasBothConstraintsAsCandidates is the ticket's design note as an
// assertion.
//
// Both constraints are real and both are enforced at once
// (docs/netbox-schema.md -> ipam.VLANTranslationRule, meta.constraints), so the second is a
// candidate of equal standing rather than a fallback: a rule whose `remote_vid` collides inside
// its policy is *found* by candidate two and reported as a Conflict naming it, instead of being
// POSTed and coming back as a 409 the operator can only relay.
//
// The order matters and follows NetBox's own `meta.ordering: ('policy', 'local_vid')`.
func TestVLANTranslationRuleHasBothConstraintsAsCandidates(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANTranslationRule")

	want := []NaturalKey{
		{Fields: []KeyField{
			{Filter: "policy_id", Spec: "policyRef"},
			{Filter: "local_vid", Spec: "localVID"},
		}},
		{Fields: []KeyField{
			{Filter: "policy_id", Spec: "policyRef"},
			{Filter: "remote_vid", Spec: "remoteVID"},
		}},
	}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// `policyRef` is matched on by both candidates, so it can never be deferred -- a deferred
	// reference is by construction unresolved when the lookup runs. validateDeferred enforces
	// that at boot; this says it is also *intended*, so nobody adds one to smooth over an
	// ordering problem that does not exist.
	if len(d.Deferred) != 0 {
		t.Errorf("Deferred = %+v, want none: both candidates match on policyRef", d.Deferred)
	}
}

// TestVLANTranslationRuleIsContainedByItsPolicy is ADR-0003 rule 4 on the one cascading foreign
// key in this pair.
//
// `policy` is `on_delete=CASCADE` (docs/netbox-schema.md -> ipam.VLANTranslationRule), so NetBox
// deletes a policy's rules with the policy. Without the containment ref the rule CR outlives
// its row and the engine's create-if-absent step recreates what NetBox deliberately deleted
// (#203) -- and the inline children of `spec.rules` would never be garbage-collected, because
// the owner reference is built from the containment ref.
func TestVLANTranslationRuleIsContainedByItsPolicy(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANTranslationRule")

	if d.ContainmentRef != "policyRef" {
		t.Errorf("ContainmentRef = %q, want policyRef (ipam.VLANTranslationRule.policy is "+
			"on_delete=CASCADE)", d.ContainmentRef)
	}

	field, mapped := d.FieldFor("policyRef")
	if !mapped {
		t.Fatal("the field map has no policyRef")
	}

	if !field.CascadeOnDelete {
		t.Error("policyRef does not declare CascadeOnDelete; validateContainment is only as " +
			"good as the flag it checks")
	}

	if field.API != "policy" {
		t.Errorf("policyRef writes %q, want `policy`: NetBox ignores a field name it does not "+
			"know rather than rejecting it", field.API)
	}
}

// TestVLANTranslationPolicyHasNoContainmentRef is the consequence, stated so it is not read as
// an omission: the cascade in this pair runs one way only.
func TestVLANTranslationPolicyHasNoContainmentRef(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANTranslationPolicy")

	if d.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want none: the only foreign key on "+
			"ipam.VLANTranslationPolicy is `owner`, which has no Kind", d.ContainmentRef)
	}
}

// TestVLANTranslationPolicyNeverWritesItsRulesColumn is the PATCH-loop guard.
//
// `rules` is in the serializer's `Meta.fields` and declared
// `VLANTranslationRuleSerializer(many=True, read_only=True)`
// (netbox/ipam/api/serializers_/vlans.py:123), so writing it does not fail -- it silently
// no-ops, and the next reconcile finds the same difference and writes again forever. The rules
// are written through their own endpoint, one CR each.
func TestVLANTranslationPolicyNeverWritesItsRulesColumn(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANTranslationPolicy")

	if !slices.Contains(d.ReadOnly, "rules") {
		t.Error("`rules` is not in ReadOnly; it is a read_only nested serializer, and NetBox " +
			"drops a write to it")
	}

	if slices.ContainsFunc(d.Fields, func(f Field) bool { return f.API == "rules" }) {
		t.Error("the field map writes `rules`; a policy's rules are child CRs against " +
			"ipam/vlan-translation-rules, not a column on the policy")
	}
}

// TestBothInterfaceKindsWriteTheTranslationPolicy is the half of NBO-068 that makes it worth
// doing now: two columns on two already-shipped Kinds turn from `blocked` to writable the
// moment the policy Kind exists.
//
// It is the assertion that replaced `vlan_translation_policy`'s entry in
// dcim_interface_test.go's TestInterfaceOmitsTheColumnsWhoseKindsDoNotExist. Both interfaces
// inherit the column from dcim.BaseInterface, so they point at one Kind and one policy can be
// shared by a physical interface and a VM interface at once.
func TestBothInterfaceKindsWriteTheTranslationPolicy(t *testing.T) {
	target := netboxv1alpha1.VLANTranslationPolicyRef{}.TargetGVK()

	for _, kind := range []string{"NetBoxInterface", "NetBoxVMInterface"} {
		d := descriptorFor(t, kind)

		field, mapped := d.FieldFor("vlanTranslationPolicyRef")
		if !mapped {
			t.Errorf("%s: no vlanTranslationPolicyRef; ipam.VLANTranslationPolicy has a Kind "+
				"now, so the column is no longer blocked", kind)

			continue
		}

		if field.API != "vlan_translation_policy" {
			t.Errorf("%s: vlanTranslationPolicyRef writes %q, want `vlan_translation_policy`",
				kind, field.API)
		}

		if field.Class != ClassRefOne || field.Target != target {
			t.Errorf("%s: vlanTranslationPolicyRef is %v -> %v, want a ClassRefOne at %v",
				kind, field.Class, field.Target, target)
		}

		// PROTECT, not CASCADE (docs/netbox-schema.md -> dcim.Interface,
		// virtualization.VMInterface). Declaring the flag truthfully is what makes
		// ContainmentRef enforceable rather than a convention, and a policy is emphatically
		// not an interface's containment parent.
		if field.CascadeOnDelete {
			t.Errorf("%s: vlanTranslationPolicyRef declares CascadeOnDelete; the column is "+
				"on_delete=PROTECT", kind)
		}

		// Not deferred either: unlike `qinq_svlan` a policy has no dependency on the
		// interface pointing at it, so there is no ordering problem and a create can carry it.
		if slices.ContainsFunc(d.Deferred, func(f DeferredField) bool {
			return f.APIField == "vlan_translation_policy"
		}) {
			t.Errorf("%s: vlan_translation_policy is deferred; nothing about a policy needs "+
				"the interface to exist first", kind)
		}
	}
}
