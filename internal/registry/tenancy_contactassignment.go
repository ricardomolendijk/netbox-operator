package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyContactAssignmentDescriptor()) }

// The two column names of tenancy.ContactAssignment's polymorphic target, spelled once.
//
// Not `ScopeTypeField` / `ScopeIDField`: those are NetBox's *scope* pair and belong to
// CachedScopeMixin. This is a different pair on a different model, and the natural key below
// filters on these names, so a typo would be a lookup asking a question NetBox does not
// answer -- which returns the unfiltered set rather than an error (#206).
const (
	// ContactAssignmentTypeField holds the target's `app_label.model` string.
	ContactAssignmentTypeField = "object_type"

	// ContactAssignmentIDField holds the target's primary key.
	ContactAssignmentIDField = "object_id"
)

// contactsMixinObjectTypes is every object type NetBox 4.6.8 will accept in
// `tenancy.ContactAssignment.object_type`: the 25 models that mix in
// `netbox.models.features.ContactsMixin`.
//
// Derived from NetBox rather than from the Members list below, deliberately, and that is the
// whole reason it is written out. Registry.Validate cross-checks every member whose Kind this
// build carries against this list (ErrMemberTypeNotAllowed), and the check only means
// something while the two are stated independently: a list computed from the members would
// make the boot check tautological, and a member pointing at `ipam.vlan` -- which is not
// contactable -- would ship.
//
// The gate is `ContactAssignment.clean()`, which refuses an object type without the feature:
// `if not has_feature(self.object_type, 'contacts')`
// (netbox/tenancy/models/contacts.py:173-179). The serializer restricts nothing on its own --
// `object_type = ContentTypeField(queryset=ContentType.objects.all())`
// (netbox/tenancy/api/serializers_/contacts.py:68-70) -- which is the difference from
// ipam.Prefix's `scope_type`, where the queryset filter is what narrows the pair to four.
//
// One line per model, with the file and line the mixin appears on, because "which models are
// contactable" is the fact a NetBox version bump changes and the citation is what makes the
// next diff reviewable. The count is 25 in 4.6.8; `dcim.manufacturer` and `vpn.tunnelgroup`
// are easy to miss, being the two that carry the mixin without carrying anything else about
// contacts.
func contactsMixinObjectTypes() []string {
	return []string{
		"circuits.circuit",              // circuits/models/circuits.py:44
		"circuits.provider",             // circuits/models/providers.py:15
		"circuits.provideraccount",      // circuits/models/providers.py:49
		"circuits.virtualcircuit",       // circuits/models/virtual_circuits.py:33
		"dcim.device",                   // dcim/models/devices.py:499
		"dcim.location",                 // dcim/models/sites.py:300
		"dcim.manufacturer",             // dcim/models/devices.py:54
		"dcim.powerpanel",               // dcim/models/power.py:24
		"dcim.rack",                     // dcim/models/racks.py:264
		"dcim.region",                   // dcim/models/sites.py:27
		"dcim.site",                     // dcim/models/sites.py:169
		"dcim.sitegroup",                // dcim/models/sites.py:98
		"ipam.aggregate",                // ipam/models/ip.py:76
		"ipam.asn",                      // ipam/models/asns.py:123
		"ipam.ipaddress",                // ipam/models/ip.py:940
		"ipam.iprange",                  // ipam/models/ip.py:648
		"ipam.prefix",                   // ipam/models/ip.py:216
		"ipam.service",                  // ipam/models/services.py:74
		"tenancy.tenant",                // tenancy/models/tenants.py:57
		"virtualization.cluster",        // virtualization/models/clusters.py:47
		"virtualization.clustergroup",   // virtualization/models/clusters.py:30
		"virtualization.virtualmachine", // virtualization/models/virtualmachines.py:102
		"vpn.l2vpn",                     // vpn/models/l2vpn.py:18
		"vpn.tunnel",                    // vpn/models/tunnels.py:30
		"vpn.tunnelgroup",               // vpn/models/tunnels.py:19
	}
}

// contactAssignmentTargetFK is the union behind `(object_type, object_id)`.
//
// Not a shared constructor like ScopeFK: exactly one kind carries this pair, so it lives next
// to that kind. If a second one ever does, this is what moves.
//
// Every member declares `CascadeOnDelete: true`, by one mechanism rather than the two
// CachedScopeMixin needs: `ContactsMixin` declares
// `contacts = GenericRelation('tenancy.ContactAssignment', content_type_field='object_type',
// object_id_field='object_id')` (netbox/netbox/models/features.py:392-396), and Django deletes
// the rows behind a GenericRelation when the object owning it is deleted. There are no
// denormalised `_`-prefixed columns on this model, so the GenericRelation is the whole of the
// cascade -- and it is a property of `ContactsMixin`, which every allowed type has by
// definition, so the answer is the same for all of them. Stated per member anyway, because
// GenericFKMember.CascadeOnDelete is all-or-none at boot (ErrMemberCascadePartial) and
// "unstated" is a third answer that would silently cost this Kind its containment parent.
func contactAssignmentTargetFK() GenericFKSpec {
	cascade := true
	members := []GenericFKMember{
		{Spec: "regionRef", Target: netboxv1alpha1.RegionRef{}.TargetGVK()},
		{Spec: "siteGroupRef", Target: netboxv1alpha1.SiteGroupRef{}.TargetGVK()},
		{Spec: "siteRef", Target: netboxv1alpha1.SiteRef{}.TargetGVK()},
		{Spec: "locationRef", Target: netboxv1alpha1.LocationRef{}.TargetGVK()},
		{Spec: "deviceRef", Target: netboxv1alpha1.DeviceRef{}.TargetGVK()},
		{Spec: "prefixRef", Target: netboxv1alpha1.PrefixRef{}.TargetGVK()},
		{Spec: "ipAddressRef", Target: netboxv1alpha1.IPAddressRef{}.TargetGVK()},
		{Spec: "tenantRef", Target: netboxv1alpha1.TenantRef{}.TargetGVK()},
		{Spec: "clusterRef", Target: netboxv1alpha1.ClusterRef{}.TargetGVK()},
		{Spec: "clusterGroupRef", Target: netboxv1alpha1.ClusterGroupRef{}.TargetGVK()},
		{Spec: "virtualMachineRef", Target: netboxv1alpha1.VirtualMachineRef{}.TargetGVK()},
	}

	for i := range members {
		members[i].CascadeOnDelete = &cascade
	}

	return GenericFKSpec{
		TypeField: ContactAssignmentTypeField,
		IDField:   ContactAssignmentIDField,
		Spec:      "objectRef",
		// The full 25, not the 11 the union offers. Members is what this CRD offers a user
		// -- bounded by which Kinds have a typed alias to point at -- and AllowedTypes is
		// what NetBox will accept in the column. Keeping them independent is what gives the
		// boot cross-check something to say.
		AllowedTypes: contactsMixinObjectTypes(),
		Members:      members,
		// No cached columns. tenancy.ContactAssignment declares the pair on the model itself
		// and carries no `_`-prefixed denormalisation of it -- unlike ipam.Prefix, which gets
		// `_region`, `_site_group`, `_site` and `_location` from CachedScopeMixin.
		Cached: nil,
	}
}

// tenancyContactAssignmentDescriptor is tenancy.ContactAssignment as data.
//
// A **join object**: no name, no slug, nothing but the pair it joins. Two things follow, and
// both are firsts in the catalogue.
//
// **Its identity is a generic-FK pair plus two ordinary references.** `meta.constraints` is
// `UniqueConstraint(fields=('object_type', 'object_id', 'contact', 'role'),
// name='..._unique_object_contact_role')` (docs/netbox-schema.md ->
// tenancy.ContactAssignment; netbox/tenancy/models/contacts.py:159-164). ipam.VLANGroup showed
// a pair can be matched on by column name; this is the first key that mixes that with
// `?contact_id=` and `?role_id=`. Because `role` is in the constraint, the same contact may be
// attached to the same object twice under different roles, and neither is drift on the other
// -- two CRs differing only in `roleRef` are two rows.
//
// **Its pair is `REQ`, which no shipped union has been before.** `object_type ForeignKey REQ`
// and `object_id PositiveBigIntegerField REQ` (netbox/tenancy/models/contacts.py:124-132,
// neither with `null=True`), so the CEL rule on ContactAssignmentTarget is `== 1` and the spec
// field is required. The `REQ` on the `object` row above them is the extractor artefact
// described in docs/concepts/generic-refs.md -- a GenericForeignKey takes no `null=` kwarg --
// but here the two real columns carry it too, so the artefact happens to agree.
//
// docs/concepts/generic-refs.md notes that a `REQ` pair *blocks* creation and would have to be
// followed by the cycle check. It is not followed here, and does not need to be: nothing in
// NetBox points at a ContactAssignment, so there is no `contactAssignmentRef` anywhere in this
// API and the object is a leaf in the reference graph. A ring through it is unconstructible
// rather than unchecked.
func tenancyContactAssignmentDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxContactAssignment"),
		Endpoint:   "tenancy/contact-assignments",
		ObjectType: "tenancy.contactassignment",
		Scope:      apiextensionsv1.NamespaceScoped,

		// bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
		// (docs/netbox-schema.md -> tenancy.ContactAssignment). Not a PrimaryModel, which is
		// why this kind has no `description` and no `comments` -- but it does mix in both
		// TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `objectRef` is absent from this table on purpose: one spec field writing two
		// columns is a GenericFKSpec, not a Field.
		Fields: []Field{
			// Written as `contact`, filtered as `contact_id`. No CascadeOnDelete:
			// `on_delete=PROTECT` (docs/netbox-schema.md -> tenancy.ContactAssignment), so
			// deleting the contact is *refused* while an assignment names it rather than
			// cascading -- there is no server-side deletion for an owner reference to mirror.
			{
				Spec: "contactRef", API: "contact", Class: ClassRefOne,
				Target: netboxv1alpha1.ContactRef{}.TargetGVK(),
			},
			// Same, for the role: `role ForeignKey REQ -> tenancy.ContactRole
			// on_delete=PROTECT`.
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.ContactRoleRef{}.TargetGVK(),
			},
			{Spec: "priority", API: "priority"},
		},

		// One candidate, and every filter on it is registered on ContactAssignmentFilterSet in
		// NetBox 4.6.8 -- which is the check worth doing rather than assuming, because
		// django-filter drops a parameter it does not recognise and answers with the
		// *unfiltered* set, so a guessed filter name is a lookup that matches everything
		// (#206):
		//
		//   object_type  MultiValueContentTypeFilter()                 netbox/tenancy/filtersets.py:119
		//   object_id    Meta.fields = (..., 'object_id', ...)         netbox/tenancy/filtersets.py:153
		//   contact_id   ModelMultipleChoiceFilter(Contact.objects)    netbox/tenancy/filtersets.py:120-124
		//   role_id      ModelMultipleChoiceFilter(ContactRole.objects) netbox/tenancy/filtersets.py:138-142
		//
		// `object_type` takes the `app_label.model` string and not a ContentType id:
		// MultiValueContentTypeFilter splits on `.` and resolves through
		// `ContentType.objects.get_by_natural_key` (netbox/utilities/filters.py:186-207) --
		// the same filter class ipam.VLANGroup's `scope_type` uses.
		//
		// The two halves of the pair are matched by *column* name, which is what
		// reconciler.applyGenericFK writes into the decoded spec once the union resolves
		// (docs/concepts/generic-refs.md, "Natural keys"). Both halves or neither: an id is
		// only unique within its type, so `?object_id=7` alone matches the site with id 7 and
		// the tenant with id 7 alike.
		//
		// No second candidate and no null pin. All four columns are `REQ`, so there is no
		// state in which one of them is absent -- the conditional-constraint shape every
		// nested-group kind needs simply has nothing to express here. A member whose Kind is
		// not registered leaves the pair unresolved, no candidate is applicable, and the engine
		// waits rather than creating a second row.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: ContactAssignmentTypeField, Spec: ContactAssignmentTypeField},
				{Filter: ContactAssignmentIDField, Spec: ContactAssignmentIDField},
				{Filter: "contact_id", Spec: "contactRef"},
				{Filter: "role_id", Spec: "roleRef"},
			},
		}},

		GenericFKs: []GenericFKSpec{contactAssignmentTargetFK()},

		// No deferral, and none possible: all four identity columns are matched on by the one
		// candidate, so DeferAlways is ErrDeferredNaturalKey at boot, and there is nothing
		// left over to defer conditionally.
		UpdateStrategy: UpdatePatch,

		// The polymorphic target is the containment parent, and it is the *only* candidate:
		// `contact` and `role` are both PROTECT, so neither cascades, while every member of
		// the union does (contactAssignmentTargetFK). Deleting the NetBoxSite a contact is
		// assigned to therefore takes the assignment CR with it -- when that is legal, meaning
		// same namespace; an assignment onto a shared catalogue object gets no owner reference
		// and reports CascadeUnavailable naming `objectRef`.
		//
		// Exactly one, because Kubernetes garbage collection waits for *every* owner. Adding
		// `contactRef` as a second -- which is what NBO-056's own acceptance list asks for --
		// would turn "delete the site and the assignment goes" into "delete both", and would
		// be an owner reference on a PROTECT foreign key: the CR would be garbage-collected
		// while NetBox still held the row, and the row could not have been deleted anyway
		// (#217, docs/concepts/ownership.md).
		ContainmentRef: "objectRef",

		// The four columns every ChangeLoggedModel carries. `object`, the GenericForeignKey
		// the serializer returns as a nested read-only view of the pair
		// (netbox/tenancy/api/serializers_/contacts.py:71), is not a column and no spec field
		// maps onto it, so it cannot reach a payload.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
