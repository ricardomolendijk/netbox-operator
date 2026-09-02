// Child materialisation: the inline sugar of ADR-0003 rule 5, turned into real CRs.
//
// This is the first write path in the operator that *creates* a Kubernetes object rather
// than reading one, which is why almost every line below is a guard. The five properties it
// has to hold at once, each of which is a way this feature silently destroys data:
//
//   - child names are stable across list edits, so a reorder churns nothing;
//   - a pre-existing CR is never hijacked, so a hand-written sibling is never patched;
//   - removing one inline entry prunes exactly one child;
//   - a hand-edited child converges back on the fields the materialiser owns, and keeps the
//     ones it does not;
//   - `kubectl delete` on the parent leaves nothing behind in NetBox.
//
// There is no branch on Kind here. Which objects a parent declares arrives as data, from the
// parent's own InlineChildren() -- so the engine's whole per-kind knowledge of children is
// one type assertion, and a Kind that has none answers by not implementing the method.
//
// The child is a real CR and it reconciles itself: nothing below writes NetBox on a child's
// behalf. That is what makes the sugar droppable in v1beta1 -- the child is identified by its
// marker rather than by its parent's spec, so children already materialised survive their
// parent losing the field that declared them.
package reconciler

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
)

// ChildFieldManager is the field manager every materialised child is written under.
//
// A second name beside FieldManager, and the separation is load-bearing. Server-side apply
// records ownership per field per manager, so a name of its own is what lets the materialiser
// own the fields it sets on a child and leave every other field to whoever set it -- the same
// "spec omission means don't manage" rule the operator applies to NetBox. It also keeps
// ADR-0005 §1 checkable from outside: `f:spec` under netbox-operator/children in
// metadata.managedFields is the materialiser's own output, and `f:spec` under
// netbox-operator would be the invariant having been broken.
const ChildFieldManager = FieldManager + "/children"

// childRetry is how soon a pass comes back for children that have not settled.
//
// Short, because the states it covers are transient by construction: a child created this
// pass has no Ready condition yet, and a pruned one is mid-delete. A standing state -- a
// Conflict, a blocked prune -- also lands here, and the cost of retrying it is one cached
// list per interval.
const childRetry = 15 * time.Second

// pruneMargin is how many more children than the parent declares the prune will delete
// before refusing outright.
//
// The list call in stale() is the single most dangerous line in this file: a selector that
// came out empty would select every object of the kind in the namespace. prunable()'s
// three-way check is the first defence and this is the second. Eight, so that removing a
// handful of inline entries in one commit is ordinary and removing forty is not.
const pruneMargin = 8

// ChildWriter is the materialiser's route to the API server for child CRs.
//
// A fourth writer beside StatusWriter, FinalizerWriter and OwnerWriter, and unlike them it
// is not narrowed to one field -- materialising a child means creating and deleting whole
// objects, so there is no smaller shape that does the job. What it is narrowed to is its
// purpose: it is named for children, it is only ever handed objects the materialiser built,
// and it is the only interface in the engine that can bring a Kubernetes object into
// existence.
type ChildWriter interface {
	// Get reads the object at key into obj, so that a name already taken by somebody else
	// can be recognised before anything is written to it.
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error

	// Apply server-side-applies obj under ChildFieldManager. Called first without
	// client.ForceOwnership and again with it, which is how the fields a conflict is about
	// reach the Event -- see write().
	//
	// The options are client.ApplyOption rather than client.PatchOption because the
	// implementation is now client.Client.Apply rather than a patch of type apply
	// (objectcontroller.go, childWriter). client.ForceOwnership is both, so what this
	// interface is handed here is unchanged.
	Apply(ctx context.Context, obj client.Object, opts ...client.ApplyOption) error

	// List reads every object of list's kind matching opts.
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error

	// Delete removes obj. An ordinary DELETE on the CR: the child's own finalizer removes
	// the NetBox object behind it, so pruning inherits PROTECT handling for free (NBO-007).
	//
	// No propagation policy is passed, which leaves the API server's default -- background.
	// A materialised child is a leaf today; the one that will not be, a nested address under
	// a pruned interface, is deleted by the same pass that deletes its interface, because
	// both paths disappear from the desired set together.
	Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
}

// GitOps is the annotation set every materialised child carries so that a GitOps tool does
// not treat it as drift (ADR-0005 §2).
//
// Manager configuration, resolved once at startup and handed to the engine as a plain
// struct: not a per-object field, and not a lookup the materialiser performs per reconcile.
// The chart values behind it (gitops.argocd.enabled, gitops.flux.enabled,
// gitops.extraAnnotations) are NBO-061; this is the flags and the defaults.
//
// Every entry can be switched off, because annotating for a tool you do not run is noise.
// The two markers pruning reads -- the managed-by label and the generated-by annotation --
// are deliberately *not* in here: they are how the operator recognises its own output, and
// disabling them would break pruning rather than quieten a tool.
type GitOps struct {
	// ArgoCD adds argocd.argoproj.io/compare-options: IgnoreExtraneous. On by default, and
	// not optional-by-omission: without it an Argo Application containing a parent with
	// inline children reports OutOfSync forever, which breaks sync waves and every alert
	// built on sync status.
	ArgoCD bool

	// Flux adds kustomize.toolkit.fluxcd.io/reconcile: disabled. Off by default: Flux prunes
	// by its own inventory and simply does not see a resource it did not apply, so this
	// exists for symmetry rather than for a problem.
	Flux bool

	// Extra are annotations applied verbatim, for a tool this operator has never heard of.
	// Applied before everything else, so they cannot displace the two markers pruning reads.
	Extra map[string]string
}

// DefaultGitOps is the shipped configuration: Argo CD on, Flux off (ADR-0005 §5).
func DefaultGitOps() GitOps { return GitOps{ArgoCD: true} }

// materialise converges this parent's child CRs, and returns when to come back for whatever
// has not settled -- zero when there is nothing to come back for.
//
// It reports through conditions rather than returning an error, for the reason stop() gives:
// the failure *is* the object's state, and a returned error would add controller-runtime
// backoff on top of a requeue this already chose deliberately.
func (p *pass) materialise(ctx context.Context) time.Duration {
	parent, declares := p.obj.(netboxv1alpha1.InlineParent)
	if !declares {
		// The whole of the engine's per-kind knowledge of children. A Kind with no inline
		// lists reconciles exactly as it did before this file existed, and carries no
		// ChildrenReady condition rather than one saying "not applicable".
		return 0
	}

	if p.skipsChildren() {
		return 0
	}

	if p.engine.Children == nil {
		// A wiring bug rather than a mode, exactly like a nil OwnerWriter. Reported on the
		// object because there is nowhere else for it to go from here.
		logf.FromContext(ctx).Error(errNotConfigured, "no ChildWriter is wired",
			"action", "materialise")
		p.condition(netboxv1alpha1.ConditionChildrenReady, false,
			netboxv1alpha1.ReasonAPIError, "no ChildWriter is wired into the engine")

		return childRetry
	}

	m := &materialisation{p: p, paths: map[string]bool{}, kinds: map[schema.GroupVersionKind]bool{}}
	m.desire(parent.InlineChildren(), nil)

	if collision := m.collision(); collision != "" {
		p.condition(netboxv1alpha1.ConditionChildrenReady, false,
			netboxv1alpha1.ReasonConflict, collision)

		return childRetry
	}

	for _, child := range m.want {
		m.applyChild(ctx, child)
	}

	m.prune(ctx)

	return m.report(ctx)
}

// skipsChildren reports whether this pass must not touch the children at all, setting the
// condition that says why when there is anything to say.
//
// Guard clauses in the order a human would ask them: is this object on its way out, did the
// endpoint refuse to write anything, and does the parent exist in NetBox yet.
func (p *pass) skipsChildren() bool {
	if !p.obj.GetDeletionTimestamp().IsZero() {
		// No condition and no work. The garbage collector removes the children the moment
		// the parent is gone and the parent's own finalizer orders the NetBox deletes behind
		// them, so materialising into a terminating parent would recreate exactly what the
		// cascade is in the middle of removing.
		return true
	}

	if p.result == metrics.ResultDryRun || p.result == metrics.ResultReported {
		out := p.suppression("")
		p.condition(netboxv1alpha1.ConditionChildrenReady, false, out.ready,
			fmt.Sprintf("no child CR was created, updated or deleted: %s", out.why))

		return true
	}

	if p.obj.NetBoxStatus().ID == 0 {
		// The guard that also implements the DryRun case on a first apply: a DryRun endpoint
		// suppresses the create, so status.id stays zero and this object never gets as far
		// as materialising anything. Its own reason, because every child's reference back to
		// this object would resolve to nothing and the whole set would sit in WaitingForRef.
		p.condition(netboxv1alpha1.ConditionChildrenReady, false,
			netboxv1alpha1.ReasonPendingChildren,
			"no child CR was written: this object has no netbox id yet, so every child's "+
				"reference to it would sit unresolved")

		return true
	}

	return false
}

// materialisation is one pass's work on one parent's children: what it wants, and what it
// found out.
type materialisation struct {
	p *pass

	// want is the desired set, flattened out of the nested InlineChildSets by desire().
	want []desiredChild

	// paths is want's owned-by paths, which is the pruner's "is this still declared" test.
	paths map[string]bool

	// kinds is the GVKs want covers, which is half of what the pruner has to list.
	kinds map[schema.GroupVersionKind]bool

	// children is what reaches status.children.
	children []netboxv1alpha1.ChildStatus

	// conflicts, failures and pending are the three ways a child does not settle, kept apart
	// because they are fixed differently: a conflict needs somebody to rename or delete an
	// object, a failure needs the API server back, and a pending child needs only time.
	conflicts []string
	failures  []string
	pending   []string

	// blocked is why the prune refused, when it did.
	blocked string
}

// desiredChild is one child the parent declares, with everything derived from its path.
type desiredChild struct {
	path string
	name string
	gvk  schema.GroupVersionKind
	obj  client.Object
}

// desire flattens the declared tree into m.want, recursing so that depth is not baked in and
// the path grows by one segment per level.
func (m *materialisation) desire(sets []netboxv1alpha1.InlineChildSet, path []netboxv1alpha1.ChildSegment) {
	for _, set := range sets {
		for _, entry := range set.Entries {
			// Clip, not a plain append: two sibling entries appending to one backing array
			// would each overwrite the other's last segment, and the bug would show up as a
			// child named after its sibling.
			at := append(slices.Clip(path), netboxv1alpha1.ChildSegment{
				Field: set.Field, Discriminator: set.Discriminator, Key: entry.Key,
			})

			if entry.Desired != nil {
				m.add(at, entry.Desired)
			}

			m.desire(entry.Children, at)
		}
	}
}

// add records one declared child, deriving its name and path from the segments alone.
func (m *materialisation) add(path []netboxv1alpha1.ChildSegment, obj client.Object) {
	at := netboxv1alpha1.ChildPath(path)

	gvk, err := apiutil.GVKForObject(obj, m.p.engine.Scheme)
	if err != nil {
		m.failures = append(m.failures, fmt.Sprintf("%s: resolving the kind of %T: %v", at, obj, err))

		return
	}

	m.want = append(m.want, desiredChild{
		path: at,
		// metadata.name, never spec.name: a Kubernetes name is immutable, so this never
		// changes under a live object, while renaming the object in NetBox would otherwise
		// churn every child CR. See netboxv1alpha1.ChildName.
		name: netboxv1alpha1.ChildName(m.p.obj.GetName(), path),
		gvk:  gvk,
		obj:  obj,
	})
	m.paths[at] = true
	m.kinds[gvk] = true
}

// collision reports two declared children of one kind that derive the same name, and is
// empty when every name is distinct.
//
// Fail-closed for the whole parent rather than for the pair, because there is no safe partial
// answer: two entries deriving one name would each apply it in turn, so whichever reconciled
// last would win and the object would flap between two specs for as long as both were
// declared. The fix is in the parent's spec -- a different key, or a Discriminator on one of
// the two sets -- so the condition says which two entries and what they collided on.
//
// Keyed on kind and name together: two children of *different* kinds sharing a name are two
// different objects and perfectly legal, which is what Discriminator exists to avoid needing.
func (m *materialisation) collision() string {
	seen := make(map[string]string, len(m.want))

	for _, child := range m.want {
		at := child.gvk.Kind + "/" + child.name

		if first, taken := seen[at]; taken {
			return fmt.Sprintf(
				"%s and %s both derive the %s name %q, so nothing was written: give them "+
					"different keys, or a different discriminator on one of the two lists",
				first, child.path, child.gvk.Kind, child.name)
		}

		seen[at] = child.path
	}

	return ""
}

// applyChild converges one child, or reports why it did not.
func (m *materialisation) applyChild(ctx context.Context, child desiredChild) {
	owner, exists, err := m.occupant(ctx, child)

	switch {
	case err != nil:
		m.failures = append(m.failures, fmt.Sprintf("%s: %v", child.path, err))

		return

	case owner != "":
		// Never adopted, never overwritten, not even labelled. A CR at this name that we do
		// not control was written by a human or by Git, so its spec is not ours to touch
		// (ADR-0005 §1) and ADR-0003 rule 5 has the *parent* report it instead. That is what
		// keeps the inline sugar removable: it can never take over an object somebody else
		// declared.
		m.conflicts = append(m.conflicts, fmt.Sprintf(
			"%s would be %s %s/%s, which already exists and is %s: nothing was written to it",
			child.path, child.gvk.Kind, m.p.obj.GetNamespace(), child.name, owner))

		return
	}

	if err := m.decorate(child); err != nil {
		m.failures = append(m.failures, fmt.Sprintf("%s: %v", child.path, err))

		return
	}

	if err := m.write(ctx, child); err != nil {
		m.failures = append(m.failures, fmt.Sprintf("%s: %v", child.path, err))

		return
	}

	if !exists {
		logf.FromContext(ctx).Info("materialised a child",
			"action", "materialise", "child", child.gvk.Kind+"/"+child.name, "path", child.path)
		m.p.engine.eventAbout(m.p.obj, child.obj, netboxv1alpha1.EventChildMaterialised,
			"created %s %s from %s", child.gvk.Kind, child.name, child.path)
	}

	ready := childReady(child.obj)
	m.children = append(m.children, netboxv1alpha1.ChildStatus{
		Path: child.path, Kind: child.gvk.Kind, Name: child.name, Ready: ready,
	})

	if !ready {
		m.pending = append(m.pending,
			fmt.Sprintf("%s %s is not ready yet", child.gvk.Kind, child.name))
	}
}

// occupant describes whoever already holds the child's name and is not us, and reports
// whether anything holds it at all.
//
// A GET before every write, which is the guard-clause form of "a child CR that already exists
// and is not owned by us is never hijacked". Both markers are required: the owner-uid label,
// which is what the pruner selects on, and the controller owner reference, which is what
// specGuard reads to decide whose spec the operator may write. Either alone could be copied
// into a hand-written manifest; requiring both means a CR has to go out of its way to be
// mistaken for ours.
//
// A read that *failed* is not an absence and does not license a write -- only a proven
// NotFound does.
func (m *materialisation) occupant(ctx context.Context, child desiredChild) (owner string, exists bool, err error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(child.gvk)

	key := client.ObjectKey{Namespace: m.p.obj.GetNamespace(), Name: child.name}
	if err := m.p.engine.Children.Get(ctx, key, live); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}

		return "", false, fmt.Errorf("reading %s %s: %w", child.gvk.Kind, child.name, err)
	}

	if ours(live, m.p.obj.GetUID()) {
		return "", true, nil
	}

	return describeController(live), true, nil
}

// decorate puts everything the materialiser owns onto the child: its identity in the parent's
// namespace, the two markers, the GitOps annotations and the controller owner reference.
func (m *materialisation) decorate(child desiredChild) error {
	obj := child.obj

	// The parent's namespace, always, and there is no field to request otherwise: an owner
	// reference may not cross a namespace, so a child anywhere else could not be owned and
	// would not cascade. Cross-namespace materialisation is a non-goal, not a gap.
	obj.SetNamespace(m.p.obj.GetNamespace())
	obj.SetName(child.name)

	// TypeMeta explicitly, on an object whose Go type already implies it. An apply patch is
	// the *whole* object as a request body, so the API server refuses one that does not name
	// its own apiVersion and kind -- and a typed object's TypeMeta is empty unless somebody
	// fills it, because every other verb reads the kind from the URL instead.
	obj.GetObjectKind().SetGroupVersionKind(child.gvk)

	obj.SetLabels(overlay(obj.GetLabels(), map[string]string{
		netboxv1alpha1.ManagedByLabel: netboxv1alpha1.ManagedByValue,
		netboxv1alpha1.OwnerUIDLabel:  string(m.p.obj.GetUID()),
	}))
	obj.SetAnnotations(overlay(obj.GetAnnotations(), m.markers(child.path)))

	m.inherit(obj)

	// controllerutil rather than a hand-rolled owner reference. It already refuses a
	// cross-namespace owner and already refuses to steal a child another controller owns,
	// which is exactly what ADR-0003 rule 3 asks for, so wrapping it would add a name and no
	// behaviour. The containment owner reference of rule 4 does not join it: the child's own
	// pass appends only when no entry names that parent already, and addOwner is append-only
	// precisely so this `controller: true` survives contact with it (owners.go).
	if err := controllerutil.SetControllerReference(m.p.obj, obj, m.p.engine.Scheme); err != nil {
		return fmt.Errorf("owning %s %s: %w", child.gvk.Kind, child.name, err)
	}

	return nil
}

// markers is the annotation set for one child: the GitOps entries, then the two the operator
// reads back.
func (m *materialisation) markers(path string) map[string]string {
	gitops := DefaultGitOps()
	if m.p.engine.GitOps != nil {
		gitops = *m.p.engine.GitOps
	}

	out := make(map[string]string, len(gitops.Extra)+4)

	// Extra first, and the two markers last, so that a chart value can annotate for a tool
	// this operator has never heard of and cannot quietly disable the two the pruner reads.
	maps.Copy(out, gitops.Extra)

	if gitops.ArgoCD {
		out[netboxv1alpha1.ArgoCDCompareOptionsAnnotation] = netboxv1alpha1.ArgoCDIgnoreExtraneous
	}

	if gitops.Flux {
		out[netboxv1alpha1.FluxReconcileAnnotation] = netboxv1alpha1.FluxReconcileDisabled
	}

	// Which spec path produced this child, for the pruner, and which parent object produced
	// it, for a human reading `kubectl get -o yaml`. Two annotations because they carry two
	// different facts: every child of one parent has the identical generated-by, so it cannot
	// tell two inline entries apart, which is the whole of what pruning needs (ADR-0005 §2).
	out[netboxv1alpha1.OwnedByPathAnnotation] = path
	out[netboxv1alpha1.GeneratedByAnnotation] = fmt.Sprintf("%s/%s/%s",
		strings.ToLower(m.p.desc.GVK.Kind), m.p.obj.GetNamespace(), m.p.obj.GetName())

	return out
}

// inherit copies the two spec fields a child takes from its parent, unless the inline entry
// set them itself.
//
// endpointRef and deletionPolicy, and deliberately nothing else. Not tags, not customFields,
// not description: inheriting free text and tag sets makes a drift report lie about where a
// value came from, and the child's own entry is the only place a reader would think to look.
// A claim child gets the same two fields out of a different accessor, and it is not optional
// for it: NetBoxClaimSpec.endpointRef carries MinLength=1, so a materialised claim with no
// endpoint is refused by the API server rather than merely under-configured. That is the one
// thing the first real parent Kind found missing here (NBO-033) -- a claim is not an
// engine Object, it has no NetBoxObjectSpec, and the type assertion above returned early for
// it.
func (m *materialisation) inherit(obj client.Object) {
	parent := m.p.obj.NetBoxSpec()

	switch child := obj.(type) {
	case Object:
		spec := child.NetBoxSpec()

		if spec.EndpointRef == "" {
			spec.EndpointRef = parent.EndpointRef
		}

		if spec.DeletionPolicy == "" {
			spec.DeletionPolicy = parent.DeletionPolicy
		}

	case Claim:
		spec := child.ClaimSpec()

		if spec.EndpointRef == "" {
			spec.EndpointRef = parent.EndpointRef
		}

		// A claim's own default is Delete where an IPAM object's is Retain (#225), and
		// inheriting the parent's is what makes the chain honest either way: a VM deleted with
		// deletionPolicy: Delete frees the addresses its claims were handed, and one with
		// Retain leaves them -- as orphans, which docs/concepts/deletion.md says plainly
		// rather than sells.
		if spec.DeletionPolicy == "" {
			spec.DeletionPolicy = parent.DeletionPolicy
		}
	}
}

// write applies the child, and takes back any field somebody else had claimed.
//
// Unforced first, deliberately. A forced apply takes the field back silently; an unforced one
// is refused by the API server with a message *naming the fields it refused over*, which is
// the only way those names reach the Event. The forced retry then costs one extra request on
// the rare pass where a child was hand-edited, and nothing at all on every other pass.
//
// The asymmetry this produces is the one ADR-0032's design notes ask for: a field the
// materialiser sets is reverted, because the parent's spec is the declared source of truth
// for it; a field the materialiser never sets is left exactly as it is, because server-side
// apply only ever touches what it sends.
func (m *materialisation) write(ctx context.Context, child desiredChild) error {
	err := m.p.engine.Children.Apply(ctx, child.obj)
	if err == nil {
		return nil
	}

	if !apierrors.IsConflict(err) {
		return fmt.Errorf("applying %s %s: %w", child.gvk.Kind, child.name, err)
	}

	m.p.engine.warnAbout(m.p.obj, child.obj, netboxv1alpha1.EventChildFieldReverted,
		"%s %s: taking back fields another writer claimed: %v", child.gvk.Kind, child.name, err)

	if err := m.p.engine.Children.Apply(ctx, child.obj, client.ForceOwnership); err != nil {
		return fmt.Errorf("re-applying %s %s with force: %w", child.gvk.Kind, child.name, err)
	}

	return nil
}

// prune deletes the children whose inline entry is gone, or refuses to.
func (m *materialisation) prune(ctx context.Context) {
	stale := m.stale(ctx)

	// The second defence, prunable()'s three-way check being the first. A prune that wants
	// forty children of a parent declaring two is a bug in the operator rather than a user's
	// intent, and a blocked prune is recoverable where a wrong delete is not.
	if len(stale) > len(m.want)+pruneMargin {
		m.blocked = fmt.Sprintf(
			"refusing to delete %d children of an object that declares %d, which is past the "+
				"margin of %d: nothing was deleted. Remove the inline entries in smaller "+
				"batches, or delete this object and let the cascade take them",
			len(stale), len(m.want), pruneMargin)

		return
	}

	for i := range stale {
		m.delete(ctx, &stale[i])
	}
}

// stale lists our children whose inline entry is no longer declared.
func (m *materialisation) stale(ctx context.Context) []unstructured.Unstructured {
	var stale []unstructured.Unstructured

	for _, gvk := range m.candidateKinds() {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

		// Scoped to the parent's namespace and to its uid. The uid rather than its name,
		// because a parent deleted and recreated under the same name is a different object
		// and must not inherit the old one's children.
		if err := m.p.engine.Children.List(ctx, list,
			client.InNamespace(m.p.obj.GetNamespace()),
			client.MatchingLabels{netboxv1alpha1.OwnerUIDLabel: string(m.p.obj.GetUID())},
		); err != nil {
			m.failures = append(m.failures, fmt.Sprintf("listing %s: %v", gvk.Kind, err))

			continue
		}

		for i := range list.Items {
			if m.prunable(&list.Items[i]) {
				stale = append(stale, list.Items[i])
			}
		}
	}

	return stale
}

// candidateKinds are the child kinds to list: the ones this pass wants, plus the ones the
// last pass recorded.
//
// The second half is what makes "the user removed every inline entry" work at all. With an
// empty inline list there is no desired child left to read a GVK off, so status.children is
// the only remaining record of which kinds to go looking for -- and a pruner that cannot name
// a kind cannot prune it.
func (m *materialisation) candidateKinds() []schema.GroupVersionKind {
	kinds := maps.Clone(m.kinds)
	if kinds == nil {
		kinds = map[schema.GroupVersionKind]bool{}
	}

	for _, child := range m.p.before.Children {
		kinds[netboxv1alpha1.GroupVersion.WithKind(child.Kind)] = true
	}

	out := slices.Collect(maps.Keys(kinds))
	slices.SortFunc(out, func(a, b schema.GroupVersionKind) int { return cmp.Compare(a.Kind, b.Kind) })

	return out
}

// prunable reports whether a candidate is a child of ours whose inline entry is gone.
//
// All three checks, and two of them are redundant by construction: the label selector already
// narrowed the list to this parent's uid, and a child of ours always carries the path
// annotation. Requiring all three anyway is what makes a bug in any one of them
// non-destructive instead of a data-loss incident (ADR-0005 §2).
func (m *materialisation) prunable(candidate *unstructured.Unstructured) bool {
	path := candidate.GetAnnotations()[netboxv1alpha1.OwnedByPathAnnotation]
	if path == "" {
		// No marker, so a human wrote it -- and it is here despite the label selector, which
		// is what a manifest copied from `kubectl get -o yaml` looks like. Never touched, and
		// it survives every prune: the marker rather than the label is what makes a child
		// deletable (ADR-0003 rule 5).
		return false
	}

	if !ours(candidate, m.p.obj.GetUID()) {
		return false
	}

	return !m.paths[path]
}

// delete removes one pruned child, or reports that it is already on its way out.
func (m *materialisation) delete(ctx context.Context, candidate *unstructured.Unstructured) {
	kind, name := candidate.GetKind(), candidate.GetName()
	path := candidate.GetAnnotations()[netboxv1alpha1.OwnedByPathAnnotation]

	if !candidate.GetDeletionTimestamp().IsZero() {
		// Already deleting and not gone, which is the PROTECT case: the child's own finalizer
		// is waiting on a NetBox delete that something still blocks. Nothing is forced and no
		// finalizer is dropped -- a permanently blocked child leaving the parent permanently
		// pending is the correct outcome, and infinitely preferable to a force-delete that
		// orphans a NetBox object nobody is tracking any more (docs/concepts/deletion.md).
		m.pending = append(m.pending, fmt.Sprintf("%s %s is still terminating", kind, name))

		return
	}

	// An ordinary DELETE on the CR. The child's own finalizer removes the NetBox object, so
	// pruning inherits PROTECT handling, the deletion policy and the Events for free.
	if err := m.p.engine.Children.Delete(ctx, candidate); err != nil && !apierrors.IsNotFound(err) {
		m.failures = append(m.failures, fmt.Sprintf("deleting %s %s: %v", kind, name, err))

		return
	}

	logf.FromContext(ctx).Info("pruned a child whose inline entry is gone",
		"action", "prune", "child", kind+"/"+name, "path", path)
	m.p.engine.eventAbout(m.p.obj, candidate, netboxv1alpha1.EventChildPruned,
		"deleted %s %s: %s is no longer declared", kind, name, path)

	m.pending = append(m.pending, fmt.Sprintf("%s %s is being deleted", kind, name))
}

// report records what happened and returns when to come back for whatever has not settled.
func (m *materialisation) report(ctx context.Context) time.Duration {
	// Sorted by path, so status.children is stable across reconciles and finish()'s "nothing
	// changed, write nothing" comparison is not defeated by map iteration order.
	slices.SortFunc(m.children, func(a, b netboxv1alpha1.ChildStatus) int {
		return cmp.Compare(a.Path, b.Path)
	})
	m.p.obj.NetBoxStatus().Children = m.children

	reason, message := m.verdict()
	if reason == netboxv1alpha1.ReasonAllReady {
		m.p.condition(netboxv1alpha1.ConditionChildrenReady, true, reason, message)

		return 0
	}

	m.p.condition(netboxv1alpha1.ConditionChildrenReady, false, reason, message)

	// A True is downgraded; a False is left exactly as it is. `kubectl wait` on a parent has
	// to mean the parent *and* its children, so a VM whose interfaces do not exist yet must
	// not read as Ready (ADR-0003 rule 5) -- but a pass that already reported Ready=False has
	// a more specific answer than "a child is not ready", and overwriting it would hide the
	// cause.
	if meta.IsStatusConditionTrue(m.p.obj.NetBoxStatus().Conditions, netboxv1alpha1.ConditionReady) {
		m.p.condition(netboxv1alpha1.ConditionReady, false, reason, message)
	}

	logf.FromContext(ctx).V(1).Info("children have not settled",
		"action", "materialise", "reason", reason, "cause", message)

	return childRetry
}

// verdict is the one ChildrenReady reason this pass reports, in the order a reader needs it:
// what needs a decision, then what needs a look, then what needs only time.
func (m *materialisation) verdict() (reason, message string) {
	switch {
	case len(m.conflicts) > 0:
		return netboxv1alpha1.ReasonConflict, strings.Join(m.conflicts, "; ")

	case m.blocked != "":
		return netboxv1alpha1.ReasonPruneBlocked, m.blocked

	case len(m.failures) > 0:
		return netboxv1alpha1.ReasonAPIError, strings.Join(m.failures, "; ")

	case len(m.pending) > 0:
		return netboxv1alpha1.ReasonPendingChildren, strings.Join(m.pending, "; ")
	}

	return netboxv1alpha1.ReasonAllReady,
		fmt.Sprintf("%d child CRs are materialised and ready", len(m.children))
}

// ours reports whether obj carries both of the markers that name this parent.
func ours(obj client.Object, uid types.UID) bool {
	if obj.GetLabels()[netboxv1alpha1.OwnerUIDLabel] != string(uid) {
		return false
	}

	controller := metav1.GetControllerOf(obj)

	return controller != nil && controller.UID == uid
}

// describeController names whoever controls obj, for a Conflict message: the next step is
// always a human deciding between the two objects, and they need to know what the other one
// belongs to.
func describeController(obj client.Object) string {
	controller := metav1.GetControllerOf(obj)
	if controller == nil {
		return "unowned"
	}

	return fmt.Sprintf("controlled by %s %s", controller.Kind, controller.Name)
}

// childReady is the child's own Ready condition, read off the object the apply returned -- so
// status.children carries this pass's answer rather than the previous one's.
//
// Both status shapes, for the reason inherit() reads both spec shapes: a claim is a materialised
// child like any other and its conditions live on NetBoxClaimStatus, so reading only
// NetBoxObjectStatus would leave every claim child permanently not-Ready and every parent that
// declared one permanently PendingChildren (NBO-033).
func childReady(obj client.Object) bool {
	switch child := obj.(type) {
	case Object:
		return meta.IsStatusConditionTrue(
			child.NetBoxStatus().Conditions, netboxv1alpha1.ConditionReady)
	case Claim:
		return meta.IsStatusConditionTrue(
			child.ClaimStatus().Conditions, netboxv1alpha1.ConditionReady)
	}

	return false
}

// overlay returns base with add's entries on top, without mutating base.
func overlay(base, add map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(add))
	maps.Copy(out, base)
	maps.Copy(out, add)

	return out
}
