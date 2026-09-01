package admission

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// generatedDuplicate denies `spec.allowDuplicate` on an object the operator materialised.
//
// **The double-allocation shape of
// [#167](https://github.com/ricardomolendijk/netbox-operator/issues/167), refused at
// admission.** The flag makes the provenance stamp part of an object's identity: without it
// the natural key is the address and its VRF, with it the key is the address, its VRF and this
// CR's own `metadata.uid` (internal/reconciler/duplicate.go). A CR that loses `status.id` --
// a status write lost to a restart, a restore, a re-created cluster -- then matches nothing it
// can claim, and the engine's create-if-absent step **creates a second address**.
//
// A materialised child is the object most exposed to that: it is re-created from an unchanged
// manifest by construction, which is the whole point of deriving its name deterministically.
// So the combination is refused rather than documented as a sharp edge.
//
// **Why here and not CEL.** Two of the three facts are out of a CRD schema's reach. Which spec
// field declares duplicates is `Descriptor.DuplicateSpec`, which no schema can see; and whether
// the operator generated this object is a *controller* owner reference in this API group, while
// a CRD validation rule at the root sees only `metadata.name` and `metadata.generateName`. The
// rule is nonetheless entirely generic -- it names no Kind, and a Kind that declares no
// duplicate field cannot reach it -- so a new Kind is covered here with no edit, as everything
// else in this package is.
//
// The inline sugar closes the same hole from the other side: `InlineIPAddress` has no
// `allowDuplicate` field at all, so a parent cannot ask for one (NBO-033). This is what stops a
// human adding it to a child afterwards, which server-side apply would otherwise *keep* --
// the materialiser reverts the fields it sets and leaves the fields it does not.
func (r *objectReview) generatedDuplicate(context.Context) (string, error) {
	if !allowsDuplicates(r.desc, r.spec) {
		return "", nil
	}

	owner := metav1.GetControllerOf(r.obj)
	if owner == nil {
		return "", nil
	}

	group, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		// A malformed apiVersion is admitted rather than reported as a failed check: this rule
		// is about the duplicate flag, and refusing a write over an owner reference nothing
		// here can parse would deny an object for a reason that has nothing to do with it. The
		// API server has already validated the reference's shape anyway.
		return "", nil //nolint:nilerr // an unparsable owner is not this rule's business
	}

	if group.Group != netboxv1alpha1.GroupName {
		// Controlled by something outside this API group, so it is not the operator's own
		// output and its spec is somebody else's business.
		return "", nil
	}

	return fmt.Sprintf(
		"spec.%s may not be set on a %s the operator materialised: %s %s created it, and a "+
			"stamped child that loses status.id would create a second netbox object rather than "+
			"find its own (issue #167). Write a %s of your own for an address that legitimately "+
			"exists twice",
		r.desc.DuplicateSpec, r.desc.GVK.Kind, owner.Kind, owner.Name, r.desc.GVK.Kind), nil
}
