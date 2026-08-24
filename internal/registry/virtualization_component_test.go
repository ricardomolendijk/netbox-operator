package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestComponentKindsShareOneIdentity covers NetBoxVMInterface and NetBoxVirtualDisk together,
// because their identity is literally the same declaration: neither model carries
// meta.constraints of its own, and both inherit
// `UniqueConstraint(fields=('virtual_machine', 'name'))` from virtualization.ComponentModel
// (docs/netbox-schema.md -> virtualization.ComponentModel, meta.constraints).
//
// Two things are asserted per kind and both are the ones that cost data when wrong.
// `virtual_machine_id` is always in the filter, because the pair is unique per VM rather than
// globally and `eth0` is the most-reused component name there is -- a lookup without it adopts
// another VM's interface on the first reconcile. And the name filter is *exact*: unlike all
// four of virtualization.VirtualMachine's constraints, this one has no `Lower()`, so `Eth0`
// and `eth0` are two components on one VM and the operator must not merge them.
func TestComponentKindsShareOneIdentity(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		endpoint string
		typeName string
	}{
		{kind: "NetBoxVMInterface", endpoint: "virtualization/interfaces", typeName: "virtualization.vminterface"},
		{kind: "NetBoxVirtualDisk", endpoint: "virtualization/virtual-disks", typeName: "virtualization.virtualdisk"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; its init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// Looked up, never pluralised: `virtualization/interfaces` is the endpoint for a
			// model called VMInterface, which is exactly why Descriptor.Endpoint exists.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q", d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.typeName {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.typeName)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced", d.Scope)
			}

			if d.ContainmentRef != "virtualMachineRef" {
				t.Errorf("ContainmentRef = %q, want virtualMachineRef", d.ContainmentRef)
			}

			want := []NaturalKey{{Fields: []KeyField{
				{Filter: "virtual_machine_id", Spec: "virtualMachineRef"},
				{Filter: "name", Spec: "name"},
			}}}
			if !reflect.DeepEqual(d.NaturalKeys, want) {
				t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
			}

			if got := d.NaturalKeys[0].Fields[1].Param(); got != "name" {
				t.Errorf("the name filter renders as %q, want the exact `name`: "+
					"ComponentModel's constraint carries no Lower()", got)
			}

			// Both halves are required fields, so there is no state in which one is missing
			// and a second identity applies. A fallback here would be a lookup that finds
			// another VM's component.
			if len(d.NaturalKeys) != 1 {
				t.Errorf("NaturalKeys has %d candidates, want exactly one", len(d.NaturalKeys))
			}

			// An unresolved parent means the operator cannot tell whether the component
			// exists, so it must wait rather than fall through to a global name lookup.
			unresolved := SpecState{Declared: []string{"name", "virtualMachineRef"}, Resolved: []string{"name"}}
			if got := d.Candidates(unresolved); len(got) != 0 {
				t.Errorf("Candidates() with an unresolved virtualMachineRef = %+v, want none", got)
			}
		})
	}
}

// TestVMInterfaceRegistrationResolvesTheIPAssignmentUnion is the cross-ticket claim.
//
// `IPAssignment.vmInterfaceRef` (api/v1alpha1/genericref.go) has named NetBoxVMInterface since
// NBO-011 and nothing registered the Kind, so `ByObjectType("virtualization.vminterface")`
// had no answer and every use of the member reported RefKindUnavailable. This registration is
// what closes that, and the reverse index is the whole mechanism: it is built in Registry.Add
// from Descriptor.ObjectType, and internal/resolver reads the target Kind back out of it.
//
// Asserted here rather than on NetBoxIPAddress because that Kind does not exist yet (NBO-025):
// the property belongs to the *target* being registered, and it has to hold before the referrer
// ships or the referrer ships broken.
func TestVMInterfaceRegistrationResolvesTheIPAssignmentUnion(t *testing.T) {
	d, ok := ByObjectType("virtualization.vminterface")
	if !ok {
		t.Fatal("ByObjectType(\"virtualization.vminterface\") found nothing, so " +
			"IPAssignment.vmInterfaceRef still resolves to RefKindUnavailable")
	}

	// The member names a Kind; the reverse index has to hand back that same Kind, or the
	// union would resolve against a different one.
	if want := (netboxv1alpha1.VMInterfaceRef{}).TargetGVK(); d.GVK != want {
		t.Errorf("virtualization.vminterface resolves to %s, want %s", d.GVK, want)
	}

	// The far end has to be reachable too: an id-mode member is verified with a GET against
	// the target's endpoint, so a registered Kind with the wrong endpoint fails at resolve
	// time rather than here.
	if d.Endpoint != "virtualization/interfaces" {
		t.Errorf("Endpoint = %q, want virtualization/interfaces", d.Endpoint)
	}
}

// TestVMInterfaceDefersTheSelfReferencesAndQinQ is the deferral claim for this kind, and the
// contrast with NetBoxVirtualMachine's is the point: a VM's `primary_ip4` can never resolve on
// a first pass, so it is DeferAlways, while an interface's `parent` usually can -- and
// deferring it unconditionally would create every ordinary sub-interface top-level and then
// reparent it, two writes and a visible wrong intermediate state (NBO-015).
func TestVMInterfaceDefersTheSelfReferencesAndQinQ(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxVMInterface"))
	if !ok {
		t.Fatal("NetBoxVMInterface is not registered")
	}

	want := []DeferredField{
		{APIField: "parent", Mode: DeferIfUnresolved},
		{APIField: "bridge", Mode: DeferIfUnresolved},
		{APIField: "qinq_svlan", Mode: DeferIfUnresolved},
	}
	if !reflect.DeepEqual(d.Deferred, want) {
		t.Fatalf("Deferred = %+v, want %+v", d.Deferred, want)
	}

	// `tagged_vlans` is the only to-many reference, so it is the only column compared as an
	// order-independent id set. Getting this wrong in either direction is a loop: comparing
	// an M2M order-sensitively PATCHes forever, and comparing an array order-independently
	// misses a reordering the user asked for.
	if got := d.M2MFields(); !reflect.DeepEqual(got, []string{"tagged_vlans"}) {
		t.Errorf("M2MFields() = %v, want [tagged_vlans]", got)
	}

	// The reverse relations, and `_name`. All six are returned on every read and dropped on
	// every write, so an undeclared one is a PATCH the operator repeats forever.
	for _, column := range []string{
		"_name", "ip_addresses", "mac_addresses",
		"fhrp_group_assignments", "tunnel_terminations", "l2vpn_terminations",
	} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly", column)
		}
		if _, mapped := d.FieldFor(column); mapped {
			t.Errorf("%q is in the field map, but NetBox has no writable column of that name", column)
		}
	}
}
