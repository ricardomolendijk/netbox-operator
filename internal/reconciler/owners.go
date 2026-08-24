// Owner references: the containment owner reference of ADR-0003 rule 4.
//
// A NetBox foreign key and a Kubernetes owner reference model different things -- one is a
// column, the other a lifecycle dependency -- and the difference that matters here is
// legality. A foreign key may point anywhere; an owner reference may only point inside its
// own namespace, and since ADR-0002 makes every kind namespaced that is the whole of the
// rule. So the engine either sets a legal owner reference or says out loud that deleting the
// parent will not clean this object up. It never sets an illegal one and it never quietly
// does nothing.
//
// The second thing legality depends on is the *member* a polymorphic reference resolved
// through, because a generic FK's cascade is declared per target model and members of one
// union can disagree (#214). So the decision is made per pass rather than once at boot -- and
// a decision that can change has to be reversible: an object that moves to a member NetBox
// does not cascade from, or out of the reference altogether, has the owner reference *removed*
// (disown). A stale containment owner reference is worse than none, because garbage collection
// reads it as a live owner of an object that no longer references it.
//
// There is no switch on Kind here. Which spec field is the containment parent arrives as
// data on the Descriptor (registry.Descriptor.ContainmentRef), which is why a kind added
// tomorrow gets its owner reference from its descriptor alone and this file does not change.
//
// Rule 3 -- the *controller* owner reference on a child the operator materialises -- has no
// implementation here because it has no caller yet: nothing in this build creates a
// Kubernetes object, and the materialiser that will is NBO-032. Its body is
// controllerutil.SetControllerReference, which already refuses a cross-namespace owner and
// already refuses to steal a child another controller owns, so wrapping it would add a name
// and no behaviour. What this file does owe rule 3 is not to fight it: addOwner is
// append-only precisely so a controller reference already naming the same parent survives
// contact with this step. See docs/concepts/ownership.md.
package reconciler

import (
	"context"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// OwnerWriter persists an object's metadata.ownerReferences.
//
// A third writer beside StatusWriter and FinalizerWriter rather than a method on either,
// for the reason the other two are separate: each is named for the one field it may write,
// so "the engine never writes a spec" (ADR-0005 §1) stays a property of the interfaces the
// engine holds rather than a property of how carefully their implementations are written.
type OwnerWriter interface {
	UpdateOwnerReferences(ctx context.Context, obj client.Object) error
}

// ownParent puts the containment parent's non-controller owner reference on this object, or
// reports why no owner reference is possible.
//
// Called after the references are resolved, because the parent's namespace, Kind and uid are
// facts the resolution produced -- and produced through the one path that enforces
// NetBoxRefGrant, so this step cannot learn anything about another namespace that the
// reference itself was not authorised to learn (resolver/grants.go).
func (p *pass) ownParent(ctx context.Context) error {
	parent, applicable := p.containmentParent()
	if !applicable {
		return p.disown(ctx)
	}

	if reason, message, refused := p.refusesOwnership(parent); refused {
		// A condition and no Event. This state does not change until somebody moves an
		// object between namespaces or rewrites a ref, so it is standing rather than
		// eventful -- and an Event ages out of the namespace long before the deletion that
		// would reveal it, which is the discovery this condition exists to pre-empt.
		p.condition(netboxv1alpha1.ConditionParentOwned, false, reason, message)

		logf.FromContext(ctx).V(1).Info("no containment owner reference",
			"action", "own", "reason", reason, "cause", message)

		return p.disown(ctx)
	}

	return p.own(ctx, ownerReferenceTo(parent))
}

// disown removes the containment owner reference an earlier pass set, when this pass sets
// none. It is the other half of own, and the reason both exist is that the answer changes
// under the object.
//
// A containment reference is not decided once. Its member can change -- `regionRef` to
// `siteRef` on the scope union -- and with it whether NetBox cascades at all (#214); the
// parent can move namespace; the ref can be cleared; the annotation can be set to `false`.
// Every one of those turns a reference this step wrote into one it would now refuse, and an
// owner reference nobody removes is not merely untidy:
//
//   - Left beside a new one, the object has two containment owners. Garbage collection ANDs
//     them, so "delete the site *or* the region" becomes "delete both" -- the exact reading
//     ADR-0003 rule 4 refuses a list of parents to avoid.
//   - Left alone, it is a promise about an object this one no longer references. Deleting
//     that former parent garbage-collects this object, whose finalizer then deletes a NetBox
//     row that was never in its scope.
//
// Removing is always the safe direction: an object with no owner reference is not collected,
// so the failure mode of over-removing is a missing cascade that the next pass restores,
// while the failure mode of under-removing is a deletion nobody asked for.
func (p *pass) disown(ctx context.Context) error {
	return p.writeOwners(ctx, dropContainmentOwners(p.obj, p.desc.ContainmentTargets(),
		metav1.OwnerReference{}), "disowned by its containment parent", "")
}

// containmentParent is the resolved containment reference, and whether this object has one
// to be owned by at all.
//
// Two ways not to: the kind names no containment ref -- no foreign key on it cascades, which
// is a narrower set than "every catalogue kind" (#203) -- or it names one the spec did not set
// or that did not resolve.
// Neither is this step's news to break. An unset optional ref means there is no parent, and
// an unresolved one is already RefsResolved=False naming itself; a second condition about it
// would report one fact twice and be free to disagree with the first.
//
// One Result, never a list: registry.ErrContainmentToMany refuses a descriptor whose
// containment ref is to-many at boot, so the first element is the only element. Read off the
// resolution by spec field name, which is why a containment parent reached through the
// generic-FK union of #179 needs nothing special here -- ResolveAll files a union under the
// union's own spec field, exactly as it files an ordinary ref.
func (p *pass) containmentParent() (resolver.Result, bool) {
	if p.desc.ContainmentRef == "" || len(p.containment) == 0 {
		return resolver.Result{}, false
	}

	return p.containment[0], true
}

// refusesOwnership reports whether no owner reference may be set for this parent, and the
// condition reason and message that say so.
//
// Guard clauses in the order a human would ask them: did somebody ask us not to, is the
// parent a Kubernetes object at all, and is it somewhere an owner reference may reach.
func (p *pass) refusesOwnership(parent resolver.Result) (reason, message string, refused bool) {
	if p.obj.GetAnnotations()[netboxv1alpha1.ParentOwnershipAnnotation] == "false" {
		return netboxv1alpha1.ReasonParentOwnershipDisabled, fmt.Sprintf(
			"%s=false, so no owner reference was added for %s: deleting its target will not "+
				"delete this object",
			netboxv1alpha1.ParentOwnershipAnnotation, p.desc.ContainmentRef), true
	}

	// The three NetBox-side modes resolve against a row rather than a CR, so there is no
	// Kubernetes object for an owner reference to name -- including `id`, the escape hatch
	// for a pre-existing NetBox object the operator does not manage.
	if parent.TargetUID == "" {
		return netboxv1alpha1.ReasonCascadeUnavailable, fmt.Sprintf(
			"%s resolved by %s, which names a netbox object and not a CR, so there is nothing "+
				"an owner reference can point at: deleting that netbox object will not delete "+
				"this object",
			p.desc.ContainmentRef, parent.Mode), true
	}

	// The cascade the owner reference mirrors is per union member, not per reference: a
	// generic FK's cascade is declared on each target model, so one member of a union can
	// cascade while its sibling does not (#214). Asked here rather than at boot because the
	// member is not known until the reference resolves -- validateContainment already refused
	// the descriptor if *no* member cascades, so reaching this means this object picked one of
	// the members that does not.
	//
	// Refused rather than set, because an owner reference without a server-side cascade is
	// the failure this whole rule exists to prevent, pointing the other way: garbage
	// collection would delete the CR while the NetBox row it describes stays behind.
	if !p.desc.CascadesFrom(p.desc.ContainmentRef, parent.TargetGVK) {
		return netboxv1alpha1.ReasonCascadeUnavailable, fmt.Sprintf(
			"%s resolved to %s %s, and netbox does not delete this object when that is deleted, "+
				"so no owner reference was added: deleting it will not delete this object. Another "+
				"member of %s may cascade -- the cascade of a polymorphic reference is declared per "+
				"target model",
			p.desc.ContainmentRef, parent.TargetGVK.Kind, parent.Target, p.desc.ContainmentRef), true
	}

	// The sharp edge of this whole mechanism, and the reason it is a condition rather than a
	// line in the docs: the same manifest cascades or does not depending on where the two
	// objects live. A grant authorises the *reference*; nothing can authorise the owner
	// reference, because the garbage collector does not resolve across namespaces -- it
	// reads a cross-namespace owner as an owner that does not exist, and deletes the
	// dependent at once. So refusing is not conservatism, it is the only safe answer.
	if parent.Target.Namespace != p.obj.GetNamespace() {
		return netboxv1alpha1.ReasonCascadeUnavailable, fmt.Sprintf(
			"%s points at %s %s and an owner reference may not cross a namespace, so deleting "+
				"it will not delete this object in namespace %s. Put the two in one namespace "+
				"to get the cascade, or delete this object explicitly",
			p.desc.ContainmentRef, parent.TargetGVK.Kind, parent.Target, p.obj.GetNamespace()), true
	}

	return "", "", false
}

// own records the owner reference and persists it if it was not already there.
//
// The condition is set whether or not anything is written: it is the standing answer to "will
// deleting the parent remove this", and an object that already carries the reference has to
// keep saying so rather than only saying it once on the pass that added it.
func (p *pass) own(ctx context.Context, owner metav1.OwnerReference) error {
	p.condition(netboxv1alpha1.ConditionParentOwned, true, netboxv1alpha1.ReasonParentOwned,
		fmt.Sprintf("owned by %s %s/%s, so deleting it garbage-collects this object",
			owner.Kind, p.obj.GetNamespace(), owner.Name))

	// Both, and in this order, because a move is one event and not two: an object that went
	// from `regionRef` to `siteRef` needs the NetBoxRegion entry gone and the NetBoxSite entry
	// present after one pass. Dropping first also keeps the object from ever being observed
	// with two containment owners, which garbage collection would AND.
	changed := dropContainmentOwners(p.obj, p.desc.ContainmentTargets(), owner)

	if addOwner(p.obj, owner) {
		changed = true
	}

	return p.writeOwners(ctx, changed, "owned by its containment parent",
		owner.Kind+"/"+owner.Name)
}

// writeOwners persists metadata.ownerReferences when this pass changed them, and says so.
//
// One writer for own and disown, so the "no OwnerWriter is wired" case and the error wrapping
// cannot drift apart between the two.
func (p *pass) writeOwners(ctx context.Context, changed bool, message, owner string) error {
	if !changed {
		return nil
	}

	if p.engine.Owners == nil {
		return fmt.Errorf("%w: no OwnerWriter is wired", errNotConfigured)
	}

	if err := p.engine.Owners.UpdateOwnerReferences(ctx, p.obj); err != nil {
		// No rollback of the in-memory list, unlike the finalizer in claim(). Nothing later
		// in this pass reads metadata.ownerReferences, and this error ends the pass -- the
		// controller re-reads the object from the cache next time round -- so there is no
		// version of the object that could outlive the failed write and be acted on.
		return fmt.Errorf("writing the owner references of %s/%s: %w",
			p.obj.GetNamespace(), p.obj.GetName(), err)
	}

	logf.FromContext(ctx).Info(message,
		"action", "own", "ref", p.desc.ContainmentRef, "owner", owner)

	return nil
}

// ownerReferenceTo renders a resolved containment parent as the owner reference to set.
func ownerReferenceTo(parent resolver.Result) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: parent.TargetGVK.GroupVersion().String(),
		Kind:       parent.TargetGVK.Kind,
		Name:       parent.Target.Name,
		UID:        parent.TargetUID,

		// Controller is left unset, which is the decision in ADR-0003 rule 4 and not a
		// detail: an object has at most one controller reference, and it belongs to
		// whatever *created* the object (rule 3). Garbage collection counts a
		// non-controller owner exactly the same, so the cascade works either way and the
		// two references never compete.
		//
		// BlockOwnerDeletion is left unset too, and that is a narrower call. It only bites
		// under foreground deletion, where it would let this object -- which somebody else
		// wrote -- hold up `kubectl delete --cascade=foreground` on a shared parent. It
		// also brings RBAC with it: with the OwnerReferencesPermissionEnforcement admission
		// plugin enabled, setting it requires `update` on the owner's finalizers
		// subresource, so an operator that set it would need that permission on every kind
		// that can be a parent. A controller reference earns the flag by having created the
		// child; a containment reference has not.
	}
}

// addOwner appends owner to obj's owner references, and reports whether anything changed.
//
// Additive and idempotent, and deliberately not controllerutil.SetOwnerReference, which
// *upserts*: it replaces the entry naming the same object wholesale. That is the one
// behaviour this must not have. ADR-0003 says a controller owner reference and a containment
// owner reference naming the same parent dedupe to one -- and the one they dedupe to is the
// controller reference, because it is the marker child materialisation reads to know which
// children it may prune (NBO-032) and specGuard reads to know whose spec it may write
// (ADR-0005 §2). An upsert here would silently strip `controller: true` off it. Appending
// cannot.
//
// An owner reference somebody else set is untouched for the same structural reason: nothing
// below removes or rewrites an entry, so a foreign owner survives by construction rather
// than by a check that could be forgotten.
func addOwner(obj client.Object, owner metav1.OwnerReference) bool {
	refs := obj.GetOwnerReferences()

	for i, existing := range refs {
		if !sameOwner(existing, owner) {
			continue
		}

		if existing.UID == owner.UID {
			return false
		}

		// Same parent by name, different uid: it was deleted and recreated, and a stale uid
		// is worse than no owner reference at all -- the garbage collector reads an owner it
		// cannot find as an owner that is gone and deletes the dependent. Only the uid is
		// refreshed; Controller and BlockOwnerDeletion are left exactly as they are, so a
		// controller reference is not quietly downgraded to the non-controller one this step
		// would otherwise have added.
		refs[i].UID = owner.UID
		obj.SetOwnerReferences(refs)

		return true
	}

	obj.SetOwnerReferences(append(refs, owner))

	return true
}

// dropContainmentOwners removes the containment owner references this step is responsible for
// and that this pass would not set, and reports whether anything changed.
//
// `targets` are the Kinds the containment ref may resolve to (registry.ContainmentTargets), and
// they are what makes this narrow enough to be safe. An entry is removed only when all three
// hold:
//
//   - It names one of those Kinds, so it is in the containment slot rather than somebody
//     else's owner reference. An owner reference to a Kind this ref cannot point at is not
//     this step's to reason about, and is left alone -- which is what keeps "it never removes
//     an owner reference it did not add" true of everything outside the slot.
//   - It is not `keep`, the reference this pass is setting. Passing the zero value removes
//     every candidate, which is what disown wants.
//   - It is not a controller reference. Rule 3 owns that one: it belongs to whatever created
//     the object, it is the marker child materialisation reads to know which children it may
//     prune, and ADR-0003's dedupe rule says a controller reference naming the same parent
//     wins over the containment one. Removing it would take that away, and removing it *and*
//     appending a non-controller entry for the same parent would be the downgrade addOwner
//     exists to avoid.
//
// So the one thing it can take away that a human put there is a hand-written *non-controller*
// owner reference naming a Kind the containment ref points at -- which is the operator's own
// slot, and which this step would have written itself the moment the reference resolved.
func dropContainmentOwners(obj client.Object, targets []schema.GroupVersionKind,
	keep metav1.OwnerReference,
) bool {
	refs := obj.GetOwnerReferences()
	kept := make([]metav1.OwnerReference, 0, len(refs))

	for _, existing := range refs {
		if containmentSlot(existing, targets) && !sameOwner(existing, keep) {
			continue
		}

		kept = append(kept, existing)
	}

	if len(kept) == len(refs) {
		return false
	}

	obj.SetOwnerReferences(kept)

	return true
}

// containmentSlot reports whether an owner reference occupies the containment slot: a
// non-controller reference to one of the Kinds the containment ref resolves to.
//
// Group and Kind, not the version, for the reason sameOwner ignores it: one object referenced
// as v1alpha1 and as v1beta1 is one object, and a stale entry written under an older version
// is exactly the kind this has to be able to remove.
func containmentSlot(owner metav1.OwnerReference, targets []schema.GroupVersionKind) bool {
	if owner.Controller != nil && *owner.Controller {
		return false
	}

	group, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(targets, func(target schema.GroupVersionKind) bool {
		return target.Group == group.Group && target.Kind == owner.Kind
	})
}

// sameOwner reports whether two owner references name the same object.
//
// Group, Kind and name. Not the version: one object referenced as v1alpha1 and as v1beta1 is
// one object, and counting them as two would put two owner references on the dependent and
// give it the AND semantics ADR-0003 spends a paragraph avoiding -- it would survive until
// both were deleted. Not the uid either, because recognising a recreated parent as the same
// object is exactly what lets addOwner refresh the stale uid instead of appending beside it.
//
// The same comparison controller-runtime's referSameObject makes, for the same reasons; it is
// unexported there.
func sameOwner(a, b metav1.OwnerReference) bool {
	aGroup, err := schema.ParseGroupVersion(a.APIVersion)
	if err != nil {
		return false
	}

	bGroup, err := schema.ParseGroupVersion(b.APIVersion)
	if err != nil {
		return false
	}

	return aGroup.Group == bGroup.Group && a.Kind == b.Kind && a.Name == b.Name
}
