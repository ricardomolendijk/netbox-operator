package registry

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestByObjectTypeIsTheReverseIndex covers the lookup a generic FK needs and nothing else
// had: an `app_label.model` string back to the Kind that answers for it.
//
// Without it GenericFKSpec.AllowedTypes named no Kind at all, so a polymorphic reference had
// nothing to watch and nothing to index, and converged only on the referrer's resync (#25).
func TestByObjectTypeIsTheReverseIndex(t *testing.T) {
	r := New()
	for _, d := range []Descriptor{tagDescriptor(), clusterDescriptor()} {
		if err := r.Add(d); err != nil {
			t.Fatalf("Add %s: %v", d.GVK.Kind, err)
		}
	}

	got, ok := r.ByObjectType("virtualization.cluster")
	if !ok || got.GVK != clusterDescriptor().GVK {
		t.Errorf("ByObjectType(virtualization.cluster) = (%s, %v), want NetBoxCluster", got.GVK, ok)
	}

	// Not a fallback to anything. A type nothing is registered for has no Kind, and
	// answering with one would watch the wrong informer.
	if _, ok := r.ByObjectType("dcim.interface"); ok {
		t.Error("ByObjectType(dcim.interface) found a descriptor, want none")
	}
}

// TestAddRefusesADuplicateObjectType keeps the reverse index one-to-one.
//
// Two Kinds claiming one `app_label.model` string makes the lookup ambiguous, and an
// ambiguous answer there is a polymorphic reference resolved against the wrong Kind -- which
// NetBox accepts, because the id exists on the other model too.
func TestAddRefusesADuplicateObjectType(t *testing.T) {
	r := New()
	if err := r.Add(tagDescriptor()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	impostor := tagDescriptor()
	impostor.GVK = testGVK("NetBoxImpostor")

	if err := r.Add(impostor); !errors.Is(err, ErrDuplicateObjectType) {
		t.Fatalf("Add = %v, want ErrDuplicateObjectType", err)
	}

	if err := r.Validate(); !errors.Is(err, ErrDuplicateObjectType) {
		t.Fatalf("Validate = %v, want ErrDuplicateObjectType", err)
	}
}

// TestValidateUnionMembers covers the dispatch table itself. Each row is a way a union member
// would be unreachable, unresolvable or ambiguous, and every one of them is silent at runtime
// if it is not caught here: NetBox ignores a column it cannot make sense of rather than
// rejecting it.
func TestValidateUnionMembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []GenericFKMember
		wantErr error
	}{
		{
			name:    "no members at all",
			members: nil,
			wantErr: ErrInvalidGenericFK,
		},
		{
			name:    "a member with no spec field",
			members: []GenericFKMember{{Target: testGVK("NetBoxSite")}},
			wantErr: ErrInvalidGenericFKMember,
		},
		{
			name:    "a member with no target kind",
			members: []GenericFKMember{{Spec: "siteRef"}},
			wantErr: ErrInvalidGenericFKMember,
		},
		{
			name: "two members claiming one spec field",
			members: []GenericFKMember{
				{Spec: "siteRef", Target: testGVK("NetBoxSite")},
				{Spec: "siteRef", Target: testGVK("NetBoxRegion")},
			},
			wantErr: ErrInvalidGenericFKMember,
		},
		{
			name:    "a well-formed member",
			members: []GenericFKMember{{Spec: "siteRef", Target: testGVK("NetBoxSite")}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tagDescriptor()
			d.GenericFKs = []GenericFKSpec{{
				TypeField: "scope_type", IDField: "scope_id", Spec: "scopeRef",
				AllowedTypes: []string{"dcim.site"}, Members: tc.members,
			}}

			err := d.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestRegistryValidateCrossChecksUnionMembers is the boot-time half of the allowed-types
// restriction: a union offering a target NetBox would reject in that column fails the boot.
//
// A registry check and not a Descriptor one, because it needs the *target's* descriptor to
// learn its object type -- which is what keeps the `app_label.model` spelling written down in
// exactly one place.
func TestRegistryValidateCrossChecksUnionMembers(t *testing.T) {
	referrer := tagDescriptor()
	referrer.GVK = testGVK("NetBoxReferrer")
	referrer.ObjectType = "extras.referrer"
	referrer.GenericFKs = []GenericFKSpec{{
		TypeField: "scope_type", IDField: "scope_id", Spec: "scopeRef",
		// The member's Kind is registered below and answers for `virtualization.cluster`,
		// which is not what this column takes.
		AllowedTypes: []string{"dcim.site"},
		Members:      []GenericFKMember{{Spec: "clusterRef", Target: clusterDescriptor().GVK}},
	}}

	r := New()
	for _, d := range []Descriptor{referrer, clusterDescriptor()} {
		if err := r.Add(d); err != nil {
			t.Fatalf("Add %s: %v", d.GVK.Kind, err)
		}
	}

	err := r.Validate()
	if !errors.Is(err, ErrMemberTypeNotAllowed) {
		t.Fatalf("Validate() = %v, want ErrMemberTypeNotAllowed", err)
	}

	for _, want := range []string{"clusterRef", "virtualization.cluster", "dcim.site"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestRegistryValidateAllowsAnUnregisteredMemberKind is the other side of that check: a member
// naming a Kind this build does not carry yet is legal.
//
// It has to be. v1alpha1.IPAssignment names dcim.Interface and virtualization.VMInterface,
// neither of which exists until M4, and failing the boot over one would take the whole
// operator down for every other kind. The resolver reports such a member as
// RefKindUnavailable when a manifest actually uses it.
func TestRegistryValidateAllowsAnUnregisteredMemberKind(t *testing.T) {
	r := New()
	if err := r.Add(ipAddressDescriptor()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestObjectTypeSpelling pins Django's ContentType spelling, which is the one thing about a
// generic FK that cannot be inferred from anything else.
//
// `model` is lowercased and unpunctuated, so it is `virtualization.vminterface` and
// `ipam.fhrpgroup`. The Go-cased spellings are the mistake a reader makes from the schema
// digest, which lists the *class* names -- and NetBox answers a wrong content type with a
// 400 at best and a silently ignored column at worst.
func TestObjectTypeSpelling(t *testing.T) {
	for _, tc := range []struct {
		objectType string
		valid      bool
	}{
		{"virtualization.vminterface", true},
		{"ipam.fhrpgroup", true},
		{"dcim.interface", true},
		{"virtualization.VMInterface", false},
		{"ipam.FHRPGroup", false},
		{"dcim.Interface", false},
		{"interface", false},
	} {
		t.Run(tc.objectType, func(t *testing.T) {
			d := tagDescriptor()
			d.GenericFKs = []GenericFKSpec{{
				TypeField: "scope_type", IDField: "scope_id", Spec: "scopeRef",
				AllowedTypes: []string{tc.objectType},
				Members:      []GenericFKMember{{Spec: "siteRef", Target: testGVK("NetBoxSite")}},
			}}

			err := d.Validate()
			if got := !errors.Is(err, ErrInvalidObjectType); got != tc.valid {
				t.Errorf("Validate() = %v, want valid: %v", err, tc.valid)
			}
		})
	}
}

// TestIPAssignmentMembersMatchTheDescriptor is the check that keeps the two declarations of a
// union in step: the CRD struct, which decides what a user may write, and the Descriptor,
// which decides what the resolver will dispatch on.
//
// A member added to one and not the other is silent in the worst direction: a field the API
// server accepts and the resolver refuses as "not a member of this union". Reflecting over the
// JSON tags rather than listing them is what makes a rename fail here.
func TestIPAssignmentMembersMatchTheDescriptor(t *testing.T) {
	want := jsonFieldNames(reflect.TypeFor[netboxv1alpha1.IPAssignment]())

	pairs := ipAddressDescriptor().GenericFKs
	if len(pairs) != 1 {
		t.Fatalf("the fixture declares %d polymorphic pairs, want one", len(pairs))
	}

	got := pairs[0].MemberSpecs()
	if !slices.Equal(got, want) {
		t.Errorf("descriptor members = %v, IPAssignment members = %v", got, want)
	}

	// And every one of them resolves to a type the column takes. The registry cannot check
	// this yet -- none of the three Kinds exists before M4 -- so the object types the union's
	// doc comments name are asserted against AllowedTypes here instead.
	for _, objectType := range []string{"dcim.interface", "virtualization.vminterface", "ipam.fhrpgroup"} {
		if !slices.Contains(pairs[0].AllowedTypes, objectType) {
			t.Errorf("allowedTypes = %v, want it to contain %q", pairs[0].AllowedTypes, objectType)
		}
	}
}

// jsonFieldNames are a struct's JSON field names, in declaration order.
func jsonFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())

	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}

	return names
}
