package v1alpha1

// A generic foreign key in NetBox is a polymorphic relation: a `*_type` / `*_id` column
// pair where the type half names a Django model and the id half a row in it. Over the REST
// API the type half is written as an `"app_label.model"` string
// (docs/netbox-schema.md, generic-FK note), and NetBox validates the two columns together.
//
// This file holds the CR shape for one. Every polymorphic FK gets a **named union struct**
// with one typed-ref member per legal target, rather than one ObjectRef plus a `kind`
// discriminator. The field name is what pins the target Kind, so CEL rejects an illegal
// target at admission, the resolver knows what to resolve against without a switch, and
// `kubectl explain netboxipaddress.spec.assignedObject` is a complete list of what the
// field accepts. See docs/concepts/generic-refs.md.
//
// Two CEL shapes, chosen by the nullability of the *column pair*:
//
//   - both columns nullable -> `... .size() <= 1`, so "unassigned" stays legal.
//   - both columns `REQ`    -> `... .size() == 1`.
//
// Read the nullability off the `*_type` / `*_id` columns and never off the
// `GenericForeignKey` row above them. That row is not a column, a GenericForeignKey takes
// no `null=` kwarg, and the digest's extractor defaults it to `REQ` -- so believing it
// would make an unassigned IP address illegal, which NetBox permits.

// IPAssignment selects what an IP address is attached to.
//
// At most one member may be set; none means the address is unassigned, which NetBox
// permits -- `assigned_object_type` and `assigned_object_id` are both nullable
// (docs/netbox-schema.md -> ipam.IPAddress).
//
// +kubebuilder:validation:XValidation:rule="[has(self.interfaceRef), has(self.vmInterfaceRef), has(self.fhrpGroupRef)].filter(x, x).size() <= 1",message="at most one of interfaceRef, vmInterfaceRef or fhrpGroupRef may be set"
type IPAssignment struct {
	// InterfaceRef attaches the address to a device interface -> `dcim.interface`.
	// +optional
	InterfaceRef *InterfaceRef `json:"interfaceRef,omitempty"`

	// VMInterfaceRef attaches the address to a virtual-machine interface ->
	// `virtualization.vminterface`.
	// +optional
	VMInterfaceRef *VMInterfaceRef `json:"vmInterfaceRef,omitempty"`

	// FHRPGroupRef attaches the address to a first-hop-redundancy group ->
	// `ipam.fhrpgroup`.
	// +optional
	FHRPGroupRef *FHRPGroupRef `json:"fhrpGroupRef,omitempty"`
}
