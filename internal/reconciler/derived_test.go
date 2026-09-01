package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// The derived-reference fold: the one thing NBO-033 found the child materialiser could not
// express. A VM's `primary_ip4` is a column on the *parent* whose value is the id of a child
// it materialised, so it flows up rather than down -- and the engine's answer is to fold a
// name-mode reference into the spec map before the payload is built, where everything
// downstream treats it as an ordinary declared reference.
//
// The fake Kind here contributes one, so the assertions are about the engine's half and not
// about a VM's.

var derivingGVK = schema.GroupVersionKind{
	Group: netboxv1alpha1.GroupName, Version: "v1alpha1", Kind: "NetBoxDerivingFake",
}

// derivingKind is a fakeKind that contributes a derived reference, which is the whole of what
// a Kind has to do to get the back-patch.
type derivingKind struct {
	fakeKind

	derived []netboxv1alpha1.DerivedRef
	refused error
}

func (d *derivingKind) DerivedRefs() ([]netboxv1alpha1.DerivedRef, error) {
	return d.derived, d.refused
}

func (d *derivingKind) DeepCopyObject() runtime.Object {
	out := &derivingKind{derived: slices.Clone(d.derived), refused: d.refused}

	copied, ok := d.fakeKind.DeepCopyObject().(*fakeKind)
	if !ok {
		return nil
	}
	out.fakeKind = *copied

	return out
}

// derivingObject is a fake object of that Kind, with nothing written in `primaryIP4Ref`.
func derivingObject(refs ...netboxv1alpha1.DerivedRef) *derivingKind {
	obj := &derivingKind{fakeKind: *fakeObject(), derived: refs}
	obj.SetGroupVersionKind(derivingGVK)

	return obj
}

func derivingScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := fakeScheme(t)
	scheme.AddKnownTypeWithName(derivingGVK, &derivingKind{})

	return scheme
}

var _ netboxv1alpha1.InlineRefParent = (*derivingKind)(nil)

// TestDeriveFoldsTheReferenceIntoTheSpec is the mechanism in one assertion: after the fold the
// spec map holds `primaryIP4Ref` in exactly the shape a written one has -- map[string]any as
// encoding/json produces it -- so nothing downstream can tell the two apart.
func TestDeriveFoldsTheReferenceIntoTheSpec(t *testing.T) {
	obj := derivingObject(netboxv1alpha1.DerivedRef{
		Field: "primaryIP4Ref", Ref: netboxv1alpha1.ObjectRef{Name: "dns-eth0-ip-10-20-0-10-24"},
	})

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	if _, written := spec["primaryIP4Ref"]; written {
		t.Fatal("the fixture already declares primaryIP4Ref, so this proves nothing")
	}

	if err := spec.derive(obj); err != nil {
		t.Fatalf("derive() = %v", err)
	}

	folded, ok := spec["primaryIP4Ref"].(map[string]any)
	if !ok {
		t.Fatalf("spec[primaryIP4Ref] = %#v, want the map[string]any shape a decoded ref has",
			spec["primaryIP4Ref"])
	}

	if folded["name"] != "dns-eth0-ip-10-20-0-10-24" {
		t.Errorf("the folded reference is %v, want name dns-eth0-ip-10-20-0-10-24", folded)
	}
}

// TestDeriveIsDeclaredAndDeferred is the property that makes the ring converge without a
// second write path: a derived reference reaches `state.Declared`, so the deferral machinery
// picks it up and `status.deferredPending` names it -- which is what strips it from the create
// and applies it in a follow-up PATCH (NBO-015).
func TestDeriveIsDeclaredAndDeferred(t *testing.T) {
	obj := derivingObject(netboxv1alpha1.DerivedRef{
		Field: "primaryIP4Ref", Ref: netboxv1alpha1.ObjectRef{Name: "dns-eth0-ip-10-20-0-10-24"},
	})

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	if err := spec.derive(obj); err != nil {
		t.Fatalf("derive() = %v", err)
	}

	desc := deferringDescriptor()

	desired, state, refs, err := spec.desired(desc)
	if err != nil {
		t.Fatalf("desired() = %v", err)
	}

	if !slices.Contains(state.Declared, "primaryIP4Ref") {
		t.Errorf("state.Declared = %v, want primaryIP4Ref: an undeclared deferral is never "+
			"pending, so the follow-up patch would never be reported", state.Declared)
	}

	if !slices.Contains(refs, "primaryIP4Ref") {
		t.Errorf("the reference set is %v, want primaryIP4Ref: a derived reference has to be "+
			"resolved like any other, since the id lives in the child's own status", refs)
	}

	// Resolved, then deferred: the create must not carry it even once the id is known, because
	// the address it names cannot exist before the VM does.
	desired["primary_ip4"] = int64(42)

	deferral := newDeferral(desc, state, desired)
	if got := deferral.strip(); !slices.Equal(got, []string{"primary_ip4"}) {
		t.Errorf("the create strips %v, want [primary_ip4]", got)
	}
}

// TestDeriveRefusesToDisplaceAWrittenReference is the engine's backstop for the Conflict a
// Kind's own DerivedRefs() reports with better words. Two declarations for one column is a
// refusal: overwriting the written one would make a manifest field silently ineffective, and
// preferring it would make the inline `primary` silently ineffective instead.
func TestDeriveRefusesToDisplaceAWrittenReference(t *testing.T) {
	obj := derivingObject(netboxv1alpha1.DerivedRef{
		Field: "primaryIP4Ref", Ref: netboxv1alpha1.ObjectRef{Name: "derived"},
	})
	obj.Spec.PrimaryIP4Ref = &fakeRef{Name: "written-by-hand"}

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	err = spec.derive(obj)
	if !errors.Is(err, netboxv1alpha1.ErrDerivedRefConflict) {
		t.Fatalf("derive() = %v, want ErrDerivedRefConflict", err)
	}

	ref, ok := spec["primaryIP4Ref"].(map[string]any)
	if !ok || ref["name"] != "written-by-hand" {
		t.Errorf("spec[primaryIP4Ref] = %v after the refusal; the written reference must be "+
			"left exactly as it was", spec["primaryIP4Ref"])
	}
}

// TestDeriveIsANoOpForAKindThatContributesNone holds the extensibility claim from the other
// side: a Kind that does not implement InlineRefParent reconciles exactly as it did before
// this function existed, with no branch on Kind anywhere.
func TestDeriveIsANoOpForAKindThatContributesNone(t *testing.T) {
	obj := fakeObject()

	before, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	after, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	if err := after.derive(obj); err != nil {
		t.Fatalf("derive() = %v", err)
	}

	if len(before) != len(after) {
		t.Errorf("derive() changed a spec of %d fields into one of %d", len(before), len(after))
	}
}

// TestDerivedRefConflictWritesNothing is the "zero writes" half of the acceptance criterion,
// asserted against the NetBox client rather than against a condition: a spec holding two
// answers for one column stops the pass before it can locate, create or patch anything.
func TestDerivedRefConflictWritesNothing(t *testing.T) {
	obj := derivingObject(netboxv1alpha1.DerivedRef{
		Field: "primaryIP4Ref", Ref: netboxv1alpha1.ObjectRef{Name: "derived"},
	})
	// Wrapping the sentinel is what a Kind's own DerivedRefs() does, and it is what makes the
	// engine's error table need one arm rather than one per cause.
	obj.refused = fmt.Errorf(
		"spec.interfaces[eth0].addresses[10.20.0.10/24] and spec.primaryIP4Ref both set "+
			"primaryIP4Ref: %w", netboxv1alpha1.ErrDerivedRefConflict)

	nb := &netboxState{}
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: deferringDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        &fakeRefs{resolution: resolver.Resolution{ByField: map[string]resolver.FieldRefs{}}},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      derivingScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("the pass made %d netbox calls, want none: a spec with two sources for one "+
			"column must not be written at all", len(nb.calls))
	}

	ready := conditionOf(&obj.fakeKind, netboxv1alpha1.ConditionReady)
	switch {
	case ready.Type == "":
		t.Fatal("no Ready condition was set")
	case ready.Status != metav1.ConditionFalse:
		t.Errorf("Ready = %s, want False", ready.Status)
	case ready.Reason != netboxv1alpha1.ReasonConflict:
		t.Errorf("Ready reason = %s, want %s: the spec holds two answers to one question, "+
			"which is the same category as two netbox rows matching one natural key",
			ready.Reason, netboxv1alpha1.ReasonConflict)
	case !strings.Contains(ready.Message, "spec.primaryIP4Ref"):
		t.Errorf("the Ready message does not name both declarations: %q", ready.Message)
	}
}

// TestMaterialiseInheritsOntoAClaimChild is the gap the first real parent Kind found in the
// materialiser: a claim is not an engine Object -- it has NetBoxClaimSpec, not
// NetBoxObjectSpec -- so the inheritance step used to return early for it, and
// `endpointRef` carries MinLength=1, which makes that an object the API server refuses rather
// than one that is merely under-configured.
//
// The deletion chain is the other half, and it is the reason this matters beyond admission:
// VM deleted -> the claim child is deleted -> the claim's own `Delete` policy frees the NetBox
// address. A claim that inherited nothing would fall back to its CRD default, which is the
// right answer by luck rather than because the parent said so -- and would be the *wrong*
// answer for a parent that said `Retain`.
func TestMaterialiseInheritsOntoAClaimChild(t *testing.T) {
	for _, policy := range []netboxv1alpha1.DeletionPolicy{
		netboxv1alpha1.DeletionDelete, netboxv1alpha1.DeletionRetain,
	} {
		t.Run(string(policy), func(t *testing.T) {
			parent := inlineParent()
			parent.Spec.DeletionPolicy = policy

			claim := &netboxv1alpha1.NetBoxIPAddressClaim{}
			(&materialisation{p: &pass{obj: parent}}).inherit(claim)

			if claim.Spec.EndpointRef != "homelab" {
				t.Errorf("the claim child's endpointRef = %q, want the parent's homelab; a "+
					"materialised claim with none is refused by the api server",
					claim.Spec.EndpointRef)
			}

			if claim.Spec.DeletionPolicy != policy {
				t.Errorf("the claim child's deletionPolicy = %q, want the parent's %q",
					claim.Spec.DeletionPolicy, policy)
			}
		})
	}
}

// TestMaterialiseLeavesAClaimChildsOwnFields holds the other direction: inheritance fills what
// the entry left empty and never overrides what it set.
func TestMaterialiseLeavesAClaimChildsOwnFields(t *testing.T) {
	claim := &netboxv1alpha1.NetBoxIPAddressClaim{
		Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{
				EndpointRef:    "elsewhere",
				DeletionPolicy: netboxv1alpha1.DeletionRetain,
			},
		},
	}

	(&materialisation{p: &pass{obj: inlineParent()}}).inherit(claim)

	if claim.Spec.EndpointRef != "elsewhere" || claim.Spec.DeletionPolicy != netboxv1alpha1.DeletionRetain {
		t.Errorf("inherit() overwrote the claim's own endpointRef=%q deletionPolicy=%q",
			claim.Spec.EndpointRef, claim.Spec.DeletionPolicy)
	}
}

// TestChildReadyReadsAClaimsConditions is the second half of the same gap. status.children
// carries a Ready flag per child and the parent is not Ready while any child is not, so a
// claim whose conditions could not be read would leave every VM that declared one permanently
// PendingChildren.
func TestChildReadyReadsAClaimsConditions(t *testing.T) {
	claim := &netboxv1alpha1.NetBoxIPAddressClaim{}

	if childReady(claim) {
		t.Error("a claim with no conditions reads as Ready")
	}

	claim.Status.Conditions = []metav1.Condition{{
		Type: netboxv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		Reason: netboxv1alpha1.ReasonAddressAllocated, LastTransitionTime: metav1.Now(),
	}}

	if !childReady(claim) {
		t.Error("a Ready claim reads as not Ready, so its parent would never leave PendingChildren")
	}
}

// registryHoldsTheVMAsAnInlineParent is not a test of this package, and it is here because
// this is where the type assertion lives: the engine's whole per-kind knowledge of inline
// children is `p.obj.(InlineParent)`, and a Kind whose method set drifted out of shape would
// silently materialise nothing at all.
func TestTheVirtualMachineIsRecognisedByTheEngine(t *testing.T) {
	vm := &netboxv1alpha1.NetBoxVirtualMachine{}

	if _, ok := any(vm).(netboxv1alpha1.InlineParent); !ok {
		t.Error("NetBoxVirtualMachine is not an InlineParent, so it materialises no children")
	}

	if _, ok := any(vm).(netboxv1alpha1.InlineRefParent); !ok {
		t.Error("NetBoxVirtualMachine is not an InlineRefParent, so `primary` writes nothing")
	}

	if _, ok := any(vm).(Object); !ok {
		t.Error("NetBoxVirtualMachine is not an engine Object")
	}

	if _, registered := registry.Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxVirtualMachine")); !registered {
		t.Error("no descriptor is registered for NetBoxVirtualMachine")
	}
}
