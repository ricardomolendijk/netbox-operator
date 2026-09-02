package registry

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// shippedIPAddressDescriptor is the registered one, as against registry_test.go's
// ipAddressDescriptor, which is a hand-built fixture for Validate.
func shippedIPAddressDescriptor(t *testing.T) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddress")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in ipam_ipaddress.go did not run", gvk)
	}

	return d
}

// TestIPAddressDescriptorIsRegisteredAndValid is the boot check for the first kind whose
// identity no database constraint backs.
func TestIPAddressDescriptorIsRegisteredAndValid(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	// Looked up, never pluralised: docs/netbox-schema.md's endpoint map is the source.
	if d.Endpoint != "ipam/ip-addresses" {
		t.Errorf("Endpoint = %q, want ipam/ip-addresses", d.Endpoint)
	}

	// Lowercased and unpunctuated, because this is the string other kinds' generic FKs
	// write into a `*_type` column.
	if d.ObjectType != "ipam.ipaddress" {
		t.Errorf("ObjectType = %q, want ipam.ipaddress", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// PrimaryModel, so both stamp columns exist -- and CustomFieldable is what makes
	// spec.allowDuplicate's identity possible at all.
	if !d.Taggable || !d.CustomFieldable {
		t.Errorf("Taggable/CustomFieldable = %v/%v, want both true for a PrimaryModel",
			d.Taggable, d.CustomFieldable)
	}
}

// TestIPAddressNaturalKeysPinTheGlobalTable is the whole identity of this kind, and the
// reason it has two candidates rather than one.
//
// ipam.IPAddress has no meta.constraints at all (docs/netbox-schema.md), so neither
// candidate is a uniqueness constraint read off the schema -- they are the convention NetBox
// enforces through ipam.VRF.enforce_unique. What the second one must not do is *omit*
// `vrf_id`: an omitted filter matches the same address in every VRF, so a global address
// would adopt a per-VRF one.
func TestIPAddressNaturalKeysPinTheGlobalTable(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	want := []NaturalKey{
		{Fields: []KeyField{
			{Filter: "address", Spec: "address"},
			{Filter: "vrf_id", Spec: "vrfRef"},
		}},
		{
			Fields:     []KeyField{{Filter: "address", Spec: "address"}},
			NullFields: []NullField{{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// A VRF-scoped address uses the first candidate only: the second asserts the VRF was
	// never declared, so it must not catch an address whose VRF is merely unresolved.
	inVRF := SpecState{Declared: []string{"address", "vrfRef"}, Resolved: []string{"address", "vrfRef"}}
	if got := d.Candidates(inVRF); len(got) != 1 || len(got[0].NullFields) != 0 {
		t.Errorf("Candidates(address+vrf) = %+v, want only the (address, vrf_id) candidate", got)
	}

	// Declared but unresolved: no candidate applies, so the engine waits instead of
	// adopting a global address of the same value.
	pending := SpecState{Declared: []string{"address", "vrfRef"}, Resolved: []string{"address"}}
	if got := d.Candidates(pending); len(got) != 0 {
		t.Errorf("Candidates(unresolved vrf) = %+v, want none: identity is not establishable yet", got)
	}

	// No VRF at all: the null-pinned candidate, and only it.
	global := SpecState{Declared: []string{"address"}, Resolved: []string{"address"}}
	got := d.Candidates(global)
	if len(got) != 1 || len(got[0].NullFields) != 1 {
		t.Fatalf("Candidates(global) = %+v, want the vrf-pinned candidate", got)
	}

	// NBO-206: a foreign key has no `__isnull` and no `__empty` parameter -- NetBox registers
	// only negation on an FK filter -- so the pin is the sentinel value. Asserted through the
	// renderer rather than a Param() helper, because one spelling and one place that chooses
	// it is the whole point of that fix.
	if pin := got[0].NullFields[0]; pin.Filter != "vrf_id" || pin.Column != NullColumnRef {
		t.Errorf("null pin = %+v, want vrf_id as a ref column", pin)
	}
}

// TestIPAddressRoleIsAValueAndNotAReference is NBO-025's second acceptance criterion on the
// descriptor side: `role` is a choice string here and a `ForeignKey -> ipam.Role` on
// ipam.Prefix and ipam.VLAN (docs/netbox-schema.md). The differ compares a choice on
// `.value` and a related field on `.id`, so getting the class wrong is a PATCH loop.
func TestIPAddressRoleIsAValueAndNotAReference(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	role, ok := d.FieldFor("role")
	if !ok {
		t.Fatal("no field map entry for role")
	}

	if role.Class != ClassValue || !role.Target.Empty() {
		t.Errorf("role = {Class: %q, Target: %s}, want a plain value with no target",
			role.Class, role.Target)
	}

	if _, exists := d.FieldFor("roleRef"); exists {
		t.Error("roleRef exists on this kind; ipam.IPAddress.role is a choice string, not an FK")
	}

	// The two references it does have, with the targets that make them resolvable.
	for spec, kind := range map[string]string{"vrfRef": "NetBoxVRF", "natInsideRef": "NetBoxIPAddress"} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Fatalf("no field map entry for %s", spec)
		}

		if field.Class != ClassRefOne || field.Target.Kind != kind {
			t.Errorf("%s = {Class: %q, Target: %s}, want one reference to %s",
				spec, field.Class, field.Target, kind)
		}
	}

	// `nat_inside` is a reference the cycle check must follow, so it is deliberately not
	// deferred: a deferral would stop resolver.blocking following the edge, and a mutual
	// pair of addresses each naming the other would be written rather than reported as a
	// RefCycle. Deferring it would also strip nothing -- the engine already omits an
	// unresolved reference.
	if len(d.Deferred) != 0 {
		t.Errorf("Deferred = %+v, want none on this kind", d.Deferred)
	}
}

// TestIPAddressAssignmentIsOneSpecFieldWritingTwoColumns covers the generic FK's descriptor
// half: the pair, its three legal targets, and the members that dispatch onto them.
func TestIPAddressAssignmentIsOneSpecFieldWritingTwoColumns(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	pair, ok := d.GenericFKFor("assignedObject")
	if !ok {
		t.Fatal("no generic FK declared for assignedObject")
	}

	if pair.TypeField != "assigned_object_type" || pair.IDField != "assigned_object_id" {
		t.Errorf("pair = (%s, %s), want (assigned_object_type, assigned_object_id)",
			pair.TypeField, pair.IDField)
	}

	// Not in Fields, and neither are its columns: a Field maps one spec name to one API
	// name, and this reference has two. Descriptor.Validate enforces it; this says why.
	if _, mapped := d.FieldFor("assignedObject"); mapped {
		t.Error("assignedObject is also an ordinary field, which would give it two renderings")
	}

	wantTypes := []string{"dcim.interface", "virtualization.vminterface", "ipam.fhrpgroup"}
	if !slices.Equal(pair.AllowedTypes, wantTypes) {
		t.Errorf("AllowedTypes = %v, want %v", pair.AllowedTypes, wantTypes)
	}

	// Lowercased and unpunctuated, because the class names in docs/netbox-schema.md are the
	// tempting wrong answer and NetBox answers a wrong content type with a write that
	// points at nothing.
	for _, objectType := range pair.AllowedTypes {
		if objectType != strings.ToLower(objectType) {
			t.Errorf("allowed type %q is not the lowercased Django model spelling", objectType)
		}
	}

	wantMembers := map[string]string{
		"interfaceRef":   "NetBoxInterface",
		"vmInterfaceRef": "NetBoxVMInterface",
		"fhrpGroupRef":   "NetBoxFHRPGroup",
	}
	for spec, kind := range wantMembers {
		member, ok := pair.MemberFor(spec)
		if !ok {
			t.Errorf("union has no member %s", spec)

			continue
		}

		if member.Target.Kind != kind {
			t.Errorf("%s targets %s, want %s", spec, member.Target.Kind, kind)
		}
	}

	// No caches. ipam.IPAddress maintains no denormalised columns from this pair, unlike
	// CachedScopeMixin's `_site` and friends -- and a cache declared here would have to be
	// in ReadOnly, which Validate checks.
	if len(pair.Cached) != 0 {
		t.Errorf("Cached = %v, want none: ipam.IPAddress has no `_`-prefixed columns", pair.Cached)
	}
}

// TestIPAddressAllowDuplicateIsNotANetBoxField guards the one spec field on this kind that
// configures the operator rather than describing a column.
//
// NetBox ignores a field name it does not know rather than rejecting it, so an
// `allowDuplicate` that reached a payload would be dropped in silence -- and the engine
// excludes it by name, which only works while the name here is the name on the CRD.
func TestIPAddressAllowDuplicateIsNotANetBoxField(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	if d.DuplicateSpec != "allowDuplicate" {
		t.Fatalf("DuplicateSpec = %q, want allowDuplicate", d.DuplicateSpec)
	}

	if _, mapped := d.FieldFor(d.DuplicateSpec); mapped {
		t.Error("allowDuplicate is in the field map, so it would be written to NetBox")
	}

	// The name has to match the CRD's own spelling or the field is unmapped and every
	// object that sets it reports Invalid. Read off the generated CRD rather than asserted
	// against a second copy of the string.
	crd, err := os.ReadFile("../../config/crd/bases/netbox.kubeforge.org_netboxipaddresses.yaml")
	if err != nil {
		t.Fatalf("reading the generated CRD: %v", err)
	}

	if !strings.Contains(string(crd), "\n              "+d.DuplicateSpec+":") {
		t.Errorf("the generated CRD has no spec.%s property", d.DuplicateSpec)
	}
}

// TestIPAddressHasNoAllocationFields is NBO-025's first acceptance criterion, as a property
// of the descriptor: allocation is NetBoxIPAddressClaim's
// (docs/decisions/0004-claims-first-allocation.md), so nothing here may look like it.
func TestIPAddressHasNoAllocationFields(t *testing.T) {
	d := shippedIPAddressDescriptor(t)

	for _, field := range d.Fields {
		for _, forbidden := range []string{"fromprefix", "prefixlength", "alloc"} {
			if strings.Contains(strings.ToLower(field.Spec), forbidden) {
				t.Errorf("spec field %q looks like allocation, which is NBO-036's kind", field.Spec)
			}
		}
	}

	if _, mapped := d.FieldFor("address"); !mapped {
		t.Error("no field map entry for address, which is this kind's required column")
	}
}
