package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// contactKinds is the four kinds NBO-056 adds, with the two facts that are looked up rather
// than derived: the endpoint (`tenancy/contact-groups`, never pluralised from the model name)
// and the `app_label.model` spelling other kinds point at through a generic FK.
var contactKinds = []struct {
	kind       string
	endpoint   string
	objectType string
}{
	{"NetBoxContactGroup", "tenancy/contact-groups", "tenancy.contactgroup"},
	{"NetBoxContactRole", "tenancy/contact-roles", "tenancy.contactrole"},
	{"NetBoxContact", "tenancy/contacts", "tenancy.contact"},
	{"NetBoxContactAssignment", "tenancy/contact-assignments", "tenancy.contactassignment"},
}

// TestContactDescriptorsAreRegisteredAndValid is the boot check for all four kinds.
func TestContactDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range contactKinds {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

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

			// Patch, not Recreate, on all four. `ContactAssignment.contact` and `.role` are
			// both on_delete=PROTECT, so delete-then-create to change a priority would be
			// refused by NetBox -- and on the three catalogue kinds a recreate would take the
			// assignments pointing at them with it.
			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// All four mix in TagsMixin and CustomFieldsMixin, so all four carry the whole
			// provenance stamp. The assignment is the one worth asserting: it is not a
			// PrimaryModel (bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin,
			// ChangeLoggedModel), so "join object" would be an easy reason to assume it
			// carries neither.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want true/true "+
					"(docs/netbox-schema.md -> %s, bases)", d.Taggable, d.CustomFieldable, tc.objectType)
			}
		})
	}
}

// TestContactGroupIsKeyedOnNameWithTheParentPinnedNotOmitted is the constraint this kind was
// checked against rather than assumed from its base class.
//
// plan.md §8.1 claims every MPTT kind is `(parent, name)` plus a `parent IS NULL` variant.
// dcim.Region and dcim.SiteGroup declare both (netbox/dcim/models/sites.py:62-67, :133-143);
// tenancy.ContactGroup declares **only** `(parent, name)`
// (netbox/tenancy/models/contacts.py:53-58). The second candidate therefore exists because the
// engine needs a way to find a top-level group, not because a constraint guarantees it is
// unique -- and the pin is what keeps it from adopting a nested group of the same name.
//
// It is `name` and not `slug` because NestedGroupModel's `slug` carries no UNIQUE
// (netbox/netbox/models/__init__.py:183-186), unlike OrganizationalModel's.
func TestContactGroupIsKeyedOnNameWithTheParentPinnedNotOmitted(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxContactGroup"))

	want := []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			},
		},
		{
			Fields:     []KeyField{{Filter: "name", Spec: "name"}},
			NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
		},
	}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// No candidate matches on `slug`. It is required and non-unique, so a slug candidate would
	// adopt whichever group came back first.
	for i, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			if field.Spec == "slug" {
				t.Errorf("candidate %d matches on slug, which carries no UNIQUE on "+
					"tenancy.ContactGroup", i)
			}
		}
	}

	// The pin's column class decides its spelling on the wire, and there is no zero value:
	// `parent` is a foreign key, so the sentinel `?parent_id=null` is right and the numeric
	// `?parent_id__empty=true` would be a filter NetBox does not register (#206).
	if got := d.NaturalKeys[1].NullFields[0].Column; got != NullColumnRef {
		t.Errorf("null pin declares Column %q, want %q", got, NullColumnRef)
	}
}

// TestContactGroupCandidatesFollowTheParent walks the three states `parentRef` can be in,
// because which candidate applies is the whole of this kind's identity logic.
func TestContactGroupCandidatesFollowTheParent(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxContactGroup"))

	for _, tc := range []struct {
		name      string
		state     SpecState
		want      int
		wantFirst string
	}{
		{
			name:      "nested and resolved uses (parent, name)",
			state:     SpecState{Declared: []string{"name", "parentRef"}, Resolved: []string{"name", "parentRef"}},
			want:      1,
			wantFirst: "parent_id",
		},
		{
			name:      "top-level uses name with the parent pinned null",
			state:     SpecState{Declared: []string{"name"}, Resolved: []string{"name"}},
			want:      1,
			wantFirst: "name",
		},
		{
			// A parent declared but not created yet must not fall through to the top-level
			// candidate: that would find an unrelated top-level group, adopt it, and the
			// follow-up PATCH would reparent somebody else's data (NBO-015).
			name:  "parent declared but unresolved matches nothing and waits",
			state: SpecState{Declared: []string{"name", "parentRef"}, Resolved: []string{"name"}},
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != tc.want {
				t.Fatalf("Candidates() returned %d candidates (%+v), want %d", len(got), got, tc.want)
			}

			if tc.want > 0 && got[0].Fields[0].Filter != tc.wantFirst {
				t.Errorf("leading candidate filters on %q, want %q", got[0].Fields[0].Filter, tc.wantFirst)
			}
		})
	}
}

// TestContactCatalogueContainment pins which of the four kinds has a containment parent, and
// which does not, against the `on_delete` that decides it. Every one of these was a judgement
// somebody could have made the other way, so the reason is asserted rather than remembered.
func TestContactCatalogueContainment(t *testing.T) {
	for _, tc := range []struct {
		kind, containment, why string
	}{
		{
			kind: "NetBoxContactGroup", containment: "parentRef",
			why: "parent TreeForeignKey -> tenancy.ContactGroup on_delete=CASCADE",
		},
		{
			kind: "NetBoxContactRole", containment: "",
			why: "tenancy.ContactRole has no foreign keys at all",
		},
		{
			kind: "NetBoxContact", containment: "",
			why: "groups is a ManyToManyField; an M2M has no on_delete and cascades nothing",
		},
		{
			kind: "NetBoxContactAssignment", containment: "objectRef",
			why: "ContactsMixin.contacts is a GenericRelation, which Django cascade-deletes; " +
				"contact and role are both on_delete=PROTECT",
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if d.ContainmentRef != tc.containment {
				t.Errorf("ContainmentRef = %q, want %q (%s)", d.ContainmentRef, tc.containment, tc.why)
			}
		})
	}
}

// TestContactAssignmentIdentityIsThePairPlusContactAndRole is the natural key this kind exists
// to get right, and the filter names are the load-bearing half.
//
// Every one of the four is registered on ContactAssignmentFilterSet in NetBox 4.6.8, which is
// checked rather than assumed because django-filter *drops* a parameter it does not recognise
// and answers with the unfiltered set -- so a guessed filter name is a lookup that matches
// every assignment in NetBox and adopts the first (#206):
//
//	object_type  MultiValueContentTypeFilter()                   netbox/tenancy/filtersets.py:119
//	object_id    Meta.fields = ('id', 'object_type_id', 'object_id', 'priority')  :153
//	contact_id   ModelMultipleChoiceFilter(Contact.objects.all())                 :120-124
//	role_id      ModelMultipleChoiceFilter(ContactRole.objects.all())             :138-142
func TestContactAssignmentIdentityIsThePairPlusContactAndRole(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxContactAssignment"))

	want := []NaturalKey{{
		Fields: []KeyField{
			{Filter: "object_type", Spec: "object_type"},
			{Filter: "object_id", Spec: "object_id"},
			{Filter: "contact_id", Spec: "contactRef"},
			{Filter: "role_id", Spec: "roleRef"},
		},
	}}
	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// The two halves are matched by column name, which is the shape #180 built for
	// ipam.VLANGroup: applyGenericFK writes the resolved pair into the decoded spec under
	// exactly these names.
	if d.NaturalKeys[0].Fields[0].Spec != ContactAssignmentTypeField ||
		d.NaturalKeys[0].Fields[1].Spec != ContactAssignmentIDField {
		t.Errorf("the pair is not matched by column name: %+v", d.NaturalKeys[0].Fields[:2])
	}

	// No null pin anywhere. All four columns are REQ, so there is no state in which one is
	// absent and nothing conditional to express -- and a pin on a REQ column would be a
	// candidate that can never match.
	if len(d.NaturalKeys[0].NullFields) != 0 {
		t.Errorf("candidate pins %+v; every column in this key is REQ", d.NaturalKeys[0].NullFields)
	}

	// The identity includes the role, which is what makes two assignments of one contact to
	// one object legal. Dropping `role_id` from the key would make the second one look like
	// drift on the first and PATCH them over each other forever.
	if !slices.ContainsFunc(d.NaturalKeys[0].Fields, func(f KeyField) bool { return f.Filter == "role_id" }) {
		t.Error("role_id is not in the natural key; the same contact could then not hold two roles")
	}
}

// TestContactAssignmentUnionAgreesWithNetBox checks the two independent statements behind the
// polymorphic pair: what this CRD offers a user, and what NetBox will accept in the column.
//
// They are deliberately not derived from each other -- Registry.Validate cross-checks them and
// the check is only worth running while both are stated. AllowedTypes is the 25 models that mix
// in netbox.models.features.ContactsMixin in 4.6.8; Members is bounded by which of those Kinds
// has a typed alias to write the target down on.
func TestContactAssignmentUnionAgreesWithNetBox(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxContactAssignment"))

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly one pair", d.GenericFKs)
	}

	pair := d.GenericFKs[0]

	if pair.TypeField != "object_type" || pair.IDField != "object_id" || pair.Spec != "objectRef" {
		t.Errorf("pair = (%q, %q) behind %q, want (object_type, object_id) behind objectRef",
			pair.TypeField, pair.IDField, pair.Spec)
	}

	// The count is pinned so that a NetBox version bump adding a ContactsMixin model fails
	// loudly here instead of silently narrowing what a user may annotate. 25 in 4.6.8 --
	// `dcim.manufacturer` and `vpn.tunnelgroup` are the two easiest to miss.
	if len(pair.AllowedTypes) != 25 {
		t.Errorf("AllowedTypes has %d entries, want 25 (the ContactsMixin models in NetBox 4.6.8)",
			len(pair.AllowedTypes))
	}

	if !slices.IsSorted(pair.AllowedTypes) {
		t.Errorf("AllowedTypes is not sorted: %v -- a list nobody can scan is a list nobody reviews",
			pair.AllowedTypes)
	}

	for _, required := range []string{"dcim.manufacturer", "vpn.tunnelgroup", "ipam.service"} {
		if !slices.Contains(pair.AllowedTypes, required) {
			t.Errorf("AllowedTypes omits %q, which carries ContactsMixin in NetBox 4.6.8", required)
		}
	}

	// No cached columns: this model declares the pair itself and has no `_`-prefixed
	// denormalisation of it, unlike ipam.Prefix's four from CachedScopeMixin.
	if len(pair.Cached) != 0 {
		t.Errorf("Cached = %v, want none", pair.Cached)
	}

	// Every member cascades, by one mechanism: ContactsMixin's `contacts` GenericRelation
	// (netbox/netbox/models/features.py:392-396). Unstated is a third answer and is refused at
	// boot as ErrMemberCascadePartial, so the assertion is that none of them is nil.
	for _, member := range pair.Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("member %s does not state a cascade; every ContactsMixin target does", member.Spec)
		}
	}

	// Members are only the Kinds a typed alias exists for, and the list is asserted whole:
	// adding one is a deliberate act, and losing one silently would take a Kind's contacts away.
	wantMembers := []string{
		"regionRef", "siteGroupRef", "siteRef", "locationRef", "deviceRef", "prefixRef",
		"ipAddressRef", "tenantRef", "clusterRef", "clusterGroupRef", "virtualMachineRef",
	}
	if got := pair.MemberSpecs(); !reflect.DeepEqual(got, wantMembers) {
		t.Errorf("MemberSpecs() = %v, want %v", got, wantMembers)
	}
}

// TestContactAssignmentMemberWithNoKindIsReportedRatherThanDropped is the RefKindUnavailable
// case, asserted rather than worked around.
//
// `deviceRef` is a legal member of this union -- `dcim.device` carries ContactsMixin -- and
// NetBoxDevice is not registered in this build. That combination has one correct outcome
// (docs/concepts/generic-refs.md, "Kinds that do not exist yet"): the member resolves to
// RefsResolved=False, Reason=RefKindUnavailable in all four ref modes, because all four need
// the target's Descriptor for its endpoint. What must *not* happen is the member being dropped
// from the union, which would report success while writing nothing.
//
// The registry half of that is what is checkable here: the member is declared, its target Kind
// has no Descriptor, and Registry.Validate passes anyway -- an unregistered member is passed
// over at boot rather than failing the boot of every other kind.
func TestContactAssignmentMemberWithNoKindIsReportedRatherThanDropped(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxContactAssignment"))
	pair := d.GenericFKs[0]

	member, declared := pair.MemberFor("deviceRef")
	if !declared {
		t.Fatal("deviceRef is not a member; dcim.device carries ContactsMixin and must be offerable")
	}

	if _, registered := Get(member.Target); registered {
		t.Skip("NetBoxDevice is registered now, so this Kind is no longer the unavailable one")
	}

	if !slices.Contains(pair.AllowedTypes, "dcim.device") {
		t.Error("deviceRef's object type is outside AllowedTypes, so it would be " +
			"RefTypeNotAllowed rather than RefKindUnavailable")
	}

	// A private registry rather than the package-level one, whose Validate() also reports the
	// deliberate duplicate MustRegister fixture in this package. What is asserted is the rule in
	// validateUnionTypes: a member whose Kind is not registered is passed over at boot, because
	// the manifest is correct and the fix is an operator upgrade -- so it must not fail the boot
	// of every other kind.
	reg := New()
	for _, d := range []Descriptor{d, tenancyContactDescriptor(), tenancyContactRoleDescriptor()} {
		if err := reg.Add(d); err != nil {
			t.Fatalf("registering %s: %v", d.GVK, err)
		}
	}

	if err := reg.Validate(); err != nil {
		t.Errorf("Validate() = %v; a member naming an unregistered Kind must not fail the boot", err)
	}
}

// TestContactFieldClassesAreWhatTheyClaim keeps the four descriptors honest about the one
// place they differ from each other structurally: tenancy.Contact's `groups` is the only
// many-to-many, and only the assignment has a polymorphic pair.
func TestContactFieldClassesAreWhatTheyClaim(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		m2m     []string
		pairs   int
		refOnes []string
	}{
		{kind: "NetBoxContactGroup", pairs: 0, refOnes: []string{"parent"}},
		{kind: "NetBoxContactRole", pairs: 0},
		{kind: "NetBoxContact", m2m: []string{"groups"}, pairs: 0},
		{kind: "NetBoxContactAssignment", pairs: 1, refOnes: []string{"contact", "role"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if got := d.M2MFields(); !slices.Equal(got, tc.m2m) {
				t.Errorf("M2MFields() = %v, want %v", got, tc.m2m)
			}

			if got := d.ArrayFields(); len(got) != 0 {
				t.Errorf("ArrayFields() = %v, want none", got)
			}

			if got := d.ObjectTypeListFields(); len(got) != 0 {
				t.Errorf("ObjectTypeListFields() = %v, want none", got)
			}

			if len(d.GenericFKs) != tc.pairs {
				t.Errorf("GenericFKs has %d pairs, want %d", len(d.GenericFKs), tc.pairs)
			}

			refs := make([]string, 0, 2)
			for _, f := range d.Fields {
				if f.Class == ClassRefOne {
					refs = append(refs, f.API)
				}
			}

			if !slices.Equal(refs, tc.refOnes) {
				t.Errorf("to-one references = %v, want %v", refs, tc.refOnes)
			}
		})
	}
}

// TestContactAssignmentDriftsCleanly pairs the payload the operator sends with the shape NetBox
// returns, and asserts the second reconcile finds nothing to do.
//
// Three things earn the test on this kind. `contact` and `role` are written as ids and come
// back as nested objects. `priority` is a choice column, so it comes back as
// `{value, label}` rather than a string. And the polymorphic pair comes back with `object`
// beside it -- a read-only nested view of the two columns, which is not in the payload and must
// not look like a difference.
func TestContactAssignmentDriftsCleanly(t *testing.T) {
	sent := netbox.Object{
		"object_type": "dcim.site", "object_id": int64(5),
		"contact": 11, "role": 12, "priority": "primary",
	}
	live := netbox.Object{
		"object_type": "dcim.site", "object_id": float64(5),
		"contact":  map[string]any{"id": float64(11), "name": "NOC"},
		"role":     map[string]any{"id": float64(12), "name": "Technical", "slug": "technical"},
		"priority": map[string]any{"value": "primary", "label": "Primary"},
		"object": map[string]any{
			"id": float64(5), "name": "RTM1", "slug": "rtm1",
			"url": "https://netbox.home.arpa/api/dcim/sites/5/",
		},
		"created":      "2026-08-24T10:00:00Z",
		"last_updated": "2026-08-24T10:00:00Z",
		"display":      "NOC (Primary) -> RTM1",
	}

	if drift := netbox.Drift(live, sent, netbox.FieldRules{}); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}
