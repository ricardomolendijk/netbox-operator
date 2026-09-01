// The one place inline child sugar flows *upward*: a reference a parent's inline entries
// contribute to the parent's own NetBox payload.
//
// A file of its own beside inline_children.go rather than an addition to it, because it is a
// second capability and not a second shape of the first one. Everything in that file describes
// objects the materialiser writes; everything here describes a value on the object that
// declared them.
package v1alpha1

import (
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DerivedRef is one reference a Kind's inline sugar contributes to that Kind's *own* NetBox
// payload.
//
// The direction is what makes it a separate capability, and what follows from it is different
// too. A child is an object the materialiser brings into existence; this is a **column on the
// parent** whose value happens to be a child's id -- `virtualization.VirtualMachine.primary_ip4`
// is written on the VM, and the address it names is one the VM materialised.
//
// It is a reference and not a value, and that is what keeps the mechanism to a handful of
// lines. What a parent can know about a child that does not exist yet is the child's *name*,
// because the name is derived (ChildName); the id lives in the child's own status and reading
// it is the resolver's job. So a derived reference is folded into the spec map the payload is
// built from, and from there it is indistinguishable from a written one: it resolves, it
// defers, it drifts and it is reported exactly as a hand-written `spec.primaryIP4Ref` is
// (NBO-015). No second write path, and nothing downstream had to learn that inline children
// exist.
//
// +kubebuilder:object:generate=false
type DerivedRef struct {
	// Field is the spec field the reference is written under, spelled as the CRD spells it and
	// as registry.Field.Spec names it: `primaryIP4Ref`.
	Field string

	// Ref is the reference itself, always in `name` mode -- the child's derived CR name. No
	// other mode could be derived: an id is the thing this is waiting to learn, and a slug or a
	// lookup would resolve against NetBox rather than against the sibling object.
	Ref ObjectRef
}

// InlineRefParent is implemented by the Kinds whose inline sugar contributes a reference to
// their own payload. NetBoxVirtualMachine's `primary` is the only one in v1alpha1.
//
// A capability for the reason InlineParent is one: the engine's whole per-kind knowledge of it
// is a single type assertion, a Kind that contributes none answers by not implementing the
// method, and there is no branch on Kind anywhere (CONTRIBUTING.md, "Extensibility").
//
// +kubebuilder:object:generate=false
type InlineRefParent interface {
	// DerivedRefs returns the references this object's inline sugar contributes to its own
	// payload, or an error naming the declarations that disagree.
	//
	// Pure, like InlineChildren(): it is called on every pass, it reads the spec and nothing
	// else, and it caches nothing.
	//
	// The error arm is the "two sources of truth for one column" case, and it is a refusal
	// rather than a precedence rule. The engine reports it as a Conflict and writes nothing:
	// choosing one of the two silently would make the other a lie that no condition mentions.
	DerivedRefs() ([]DerivedRef, error)
}

// DerivedSpecRefs returns the references obj's inline sugar contributes to its own spec, and
// nothing at all for an object that contributes none.
//
// **The one entry point every reader of a spec goes through**, and the reason it is a function
// here rather than a type assertion at each call site. Two places read an object's spec and
// have to agree about what is in it: internal/reconciler's payload builder, which decides
// which fields are declared and therefore deferred, and internal/resolver's SpecMap, which
// decides which references are resolved. A derived reference one of them could see and the
// other could not is a field that is declared and never resolves -- an object permanently
// `DeferredFieldPending` over a column nothing will ever write. That failure is silent, and it
// is exactly what this function exists to make impossible.
func DerivedSpecRefs(obj client.Object) ([]DerivedRef, error) {
	parent, contributes := obj.(InlineRefParent)
	if !contributes {
		return nil, nil
	}

	refs, err := parent.DerivedRefs()
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return refs, nil
}

// ErrDerivedRefConflict is what every derived-reference clash unwraps to, so the engine's error
// table needs one arm rather than one per cause.
var ErrDerivedRefConflict = errors.New("two declarations would write one netbox column")

// derivedRefClash is two declarations that would write one NetBox column.
//
// A typed error rather than a formatted string, because the engine has to recognise the
// *category* in order to report it as a Conflict, and classifying an error by matching on its
// message is the thing CONTRIBUTING.md forbids. The message still carries both locations,
// because the next step is always a human deleting one of the two.
type derivedRefClash struct {
	// field is the spec field both declarations would write.
	field string

	// path and other are the inline entries, in the owned-by-path spelling. `other` is empty
	// when the second declaration is the explicit spec field `field` names.
	path, other string

	// why is the sentence naming the rule that was broken, so the message is a reason and not
	// only a pair of locations.
	why string
}

func (e *derivedRefClash) Unwrap() error { return ErrDerivedRefConflict }

func (e *derivedRefClash) Error() string {
	second := "spec." + e.field
	if e.other != "" {
		second = e.other
	}

	return fmt.Sprintf("%s and %s both set %s: %s; nothing was written",
		e.path, second, e.field, e.why)
}
