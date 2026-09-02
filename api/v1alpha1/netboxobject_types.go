package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set on every object CR by the reconcile engine.
const (
	// ConditionSynced is true when the last write succeeded and the last check found no
	// drift. Ready says the object exists and matches; Synced says what the engine did
	// about it, which is the difference between "correct" and "correct because we just
	// fixed it".
	ConditionSynced = "Synced"

	// ConditionRefsResolved is true when every reference in the spec resolved to a NetBox
	// id. False names the field that has not resolved and why, and the object does not
	// reach Ready while it is False.
	ConditionRefsResolved = "RefsResolved"

	// ConditionDriftDetected is true when NetBox differs from the spec and the operator
	// has not corrected it -- driftMode: Report, or a DryRun endpoint.
	//
	// Separate from Synced=False rather than folded into it because it answers a
	// different question: Synced is about what the engine did, DriftDetected is about
	// what NetBox currently holds. It is the condition to alert on while an endpoint is
	// in Report mode, where Ready=False is expected and permanent, and it carries the
	// field list so the answer to "what would the operator change" is in the object
	// rather than only in a log line.
	//
	// False after a correction and False when there was nothing to correct, so it is a
	// stable value rather than one that flaps on every write.
	ConditionDriftDetected = "DriftDetected"

	// ConditionParentOwned reports whether deleting this object's containment parent will
	// take this object with it: True when the non-controller owner reference of
	// ADR-0003 rule 4 is set, False when it cannot be or was declined.
	//
	// It exists because the answer is not a property of the manifest. An owner reference is
	// legal only inside one namespace and a NetBox foreign key is not, so the *same* spec
	// cascades in a namespace that holds its parent and does not in one that points at a
	// shared catalogue -- and the difference is otherwise invisible until somebody deletes
	// the parent and finds this object still here. That is not a discovery to leave to
	// deletion day, so it is a standing condition rather than an Event: an Event ages out of
	// the namespace within the hour, and this state is permanent for as long as the two
	// objects live where they live.
	//
	// Set only on a kind whose Descriptor names a ContainmentRef *and* whose spec sets it.
	// A kind with no containment parent -- every catalogue kind -- has no cascade to
	// report and carries no such condition, rather than a page of objects all saying
	// "not applicable".
	//
	// It never influences Ready. A missing cascade is a statement about deletion, and an
	// object whose NetBox counterpart matches its spec is Ready whatever happens later.
	ConditionParentOwned = "ParentOwned"

	// ConditionDeleting reports what the engine is doing about the NetBox object behind a
	// CR that carries a deletion timestamp.
	//
	// It is only ever False. The finalizer comes off the moment the NetBox side is
	// settled, so a True would describe a CR that no longer exists to carry it; the
	// Reason is therefore always what is holding the deletion up.
	ConditionDeleting = "Deleting"

	// ConditionConflict is true when the NetBox object this CR manages carries somebody
	// else's provenance stamp: another cluster's, or another CR's in this one (NBO-047).
	//
	// It is a report and not a gate. The operator does not serialise writes between
	// clusters and will not start -- decided in issue #18, with the reasoning in
	// docs/operations/provenance.md -- so this condition being True means the write went
	// ahead anyway and the other writer's next reconcile will undo it. Ready is
	// deliberately unaffected: the object does match its spec, for as long as it takes the
	// other side to write again, and failing it here would turn every `kubectl wait` in a
	// deliberately overlapping setup into a timeout while changing nothing about the
	// overlap.
	//
	// A standing condition rather than only an Event for the reason ParentOwned is one: an
	// Event ages out of the namespace within the hour and this state lasts until somebody
	// changes one of the two manifests. `status.conflict` carries who, since when, and how
	// many reconciles running -- see docs/operations/multi-writer.md.
	//
	// Removed, not set to False, once the stamp is this cluster's again. Every object of
	// every kind carrying a `Conflict: False` would be a page of conditions saying nothing;
	// its absence is the normal state.
	ConditionConflict = "Conflict"

	// ConditionChildrenReady reports what happened to the child CRs this object's inline
	// lists declare (ADR-0003 rule 5, NBO-032).
	//
	// Set only on a Kind that implements InlineParent, and only once the parent has a
	// NetBox id: a Kind with no inline lists has no children to report on, and a page of
	// objects all saying "not applicable" is worse than silence.
	//
	// It does influence Ready, unlike ParentOwned. `kubectl wait` on a VM has to mean the
	// VM *and* its interfaces and addresses, because a VM whose interfaces do not exist yet
	// is not the object the manifest asked for.
	ConditionChildrenReady = "ChildrenReady"
)

// Finalizer is what keeps a CR alive until its NetBox object has been dealt with.
//
// It is added before the engine writes anything to NetBox, and removed only once the
// NetBox side is settled -- see docs/concepts/deletion.md for why that order and not the
// other one.
const Finalizer = "netbox.kubeforge.org/finalizer"

// SkipFinalizerAnnotation is the break-glass. Set to "true" and the engine drops the
// finalizer without calling NetBox at all.
//
// It exists because a finalizer that is added and never removed makes a namespace
// undeletable forever, and no operator should be able to do that to a cluster. It
// guarantees an object left behind in NetBox, which is sometimes the right trade and is
// never the default.
const SkipFinalizerAnnotation = "netbox.kubeforge.org/skip-finalizer"

// ParentOwnershipAnnotation opts one object out of the containment owner reference of
// ADR-0003 rule 4. Set it to "false" and no owner reference is added, so deleting the
// containment parent leaves this object alone.
//
// The default is on, because "delete the site and its prefixes go too" is what people
// expect and the alternative is silent orphans in NetBox. This is the escape for the case
// where it is wrong, and it is per-object rather than per-endpoint deliberately: the
// objects that want to outlive their parent are individual ones, and an endpoint-wide
// switch would be a third deletion knob next to deletionPolicy and onConflict.
//
// Only "false" opts out. Any other value, the annotation being absent included, is the
// default -- so a typo leaves the documented behaviour in place rather than silently
// disabling a cascade somebody is relying on.
const ParentOwnershipAnnotation = "netbox.kubeforge.org/parent-ownership"

// The markers every CR the operator *materialises* carries, so that the operator can
// recognise its own output and a GitOps tool can be told to ignore it
// (ADR-0005 §2). None of them is optional: they are load-bearing rather than decorative.
const (
	// OwnedByPathAnnotation records **which spec path** of the parent produced this child,
	// key-based: `spec.interfaces[eth0].addresses[10.20.0.10/24]`.
	//
	// This is the one the pruner reads, and it is why GeneratedByAnnotation is not enough on
	// its own. Pruning has to delete the child of an inline entry the user *removed*, which
	// means matching live children against the entries that are still declared -- and every
	// child of one parent carries the identical generated-by, so that annotation identifies
	// the parent and cannot tell two of its entries apart.
	OwnedByPathAnnotation = GroupName + "/owned-by-path"

	// GeneratedByAnnotation records **which parent object** produced this child, as
	// `<lowercase kind>/<namespace>/<name>`.
	//
	// Human-readable provenance for `kubectl get -o yaml`, where the owner reference's uid
	// says nothing. The same spelling as the k8s_owner custom field in
	// docs/operations/provenance.md, so one string identifies a CR on both sides.
	//
	// Not configurable, together with ManagedByLabel: they are how the operator recognises
	// its own output, and disabling them would break pruning.
	GeneratedByAnnotation = GroupName + "/generated-by"

	// OwnerUIDLabel is the parent's metadata.uid, and it is a *label* rather than an
	// annotation for one reason: pruning has to **list** our children, and label selectors
	// are indexed server-side while annotations are not. A uid is 36 characters and a valid
	// label value.
	OwnerUIDLabel = GroupName + "/owner-uid"

	// ManagedByLabel and ManagedByValue are the standard recommended label, so that
	// `kubectl get -l app.kubernetes.io/managed-by=netbox-operator` answers "what did the
	// operator make" across every kind at once.
	ManagedByLabel = "app.kubernetes.io/managed-by"

	// ManagedByValue is what the operator writes into ManagedByLabel.
	ManagedByValue = "netbox-operator"

	// ArgoCDCompareOptionsAnnotation and ArgoCDIgnoreExtraneous are Argo CD's own mechanism
	// for a resource generated by a controller rather than by the manifest set.
	//
	// On by default, and not optional-by-omission: an Argo Application containing a parent
	// with inline children reports OutOfSync **forever** without it, because the children
	// are live resources with no counterpart in Git. That is not cosmetic -- a permanently
	// OutOfSync Application breaks sync waves and every alert built on sync status, and the
	// usual response is to delete the operator.
	ArgoCDCompareOptionsAnnotation = "argocd.argoproj.io/compare-options"

	// ArgoCDIgnoreExtraneous is the value written into ArgoCDCompareOptionsAnnotation.
	ArgoCDIgnoreExtraneous = "IgnoreExtraneous"

	// FluxReconcileAnnotation and FluxReconcileDisabled exclude the child from Flux's view.
	//
	// Off by default: Flux prunes by its own inventory and simply does not see a resource it
	// did not apply, so the annotation buys nothing unless you want the child hidden from
	// `flux diff` too. It exists for symmetry with the Argo case rather than for a problem.
	FluxReconcileAnnotation = "kustomize.toolkit.fluxcd.io/reconcile"

	// FluxReconcileDisabled is the value written into FluxReconcileAnnotation.
	FluxReconcileDisabled = "disabled"
)

// AllowDataLossAnnotation permits a deletion that destroys data NetBox will not warn about.
//
// It applies to the kinds whose descriptor declares DataLossOnDelete, which today is
// NetBoxCustomField: deleting an extras.CustomField drops that field's stored value from
// every object in NetBox that has one, irreversibly, and NetBox performs the delete without
// complaint because the values live in each object's own JSON rather than in rows that could
// be `PROTECT`-ed. Without this annotation the finalizer stays on and reports
// `Deleting=False, Reason=DataLossBlocked`.
//
// Only "true" permits it. Anything else -- the annotation being absent included -- blocks,
// so a typo is safe in the direction that keeps the data. `spec.deletionPolicy: Retain` is
// the other way out and needs no annotation: it deletes nothing in NetBox at all
// (docs/concepts/deletion.md).
const AllowDataLossAnnotation = "netbox.kubeforge.org/allow-data-loss"

// Condition reasons for an object CR. The vocabulary is deliberately small: a reason is
// keyed on by tooling and by the docs, so a new one is a documented addition rather than
// a phrase invented at the call site.
const (
	// ReasonSynced is on Ready: the object exists in NetBox and matches the spec.
	ReasonSynced = "Synced"

	// ReasonWaitingForEndpoint is on Ready: the NetBoxEndpoint has no usable client.
	ReasonWaitingForEndpoint = "WaitingForEndpoint"

	// ReasonWaitingForKey is on Ready: no natural-key candidate is usable yet, so the
	// engine cannot tell whether the object exists. Writing anything here would adopt or
	// duplicate the wrong object, so it waits.
	ReasonWaitingForKey = "WaitingForKey"

	// ReasonWaitingForRef is on Ready: a reference in the spec has not resolved.
	ReasonWaitingForRef = "WaitingForRef"

	// ReasonDeferredFieldPending is on Ready: the object exists in NetBox and a deferred
	// field has not been written to it yet.
	//
	// Distinct from ReasonWaitingForRef, which is the same omission for a different
	// cause: WaitingForRef means the engine has nothing to write, this means it has the
	// value and has not sent it. The two are fixed differently -- one waits on another
	// object, the other on the next pass of this one -- and status.deferredPending names
	// the fields either way (docs/concepts/object-lifecycle.md).
	ReasonDeferredFieldPending = "DeferredFieldPending"

	// ReasonConflict is on Ready: NetBox holds an object this CR cannot safely claim --
	// several match its natural key, or one matches and adoption was not asked for.
	ReasonConflict = "Conflict"

	// ReasonAdoptOnly is on Ready: onConflict is AdoptOnly and nothing exists to adopt.
	ReasonAdoptOnly = "AdoptOnly"

	// ReasonInvalid is on Ready: NetBox rejected the payload, or the spec cannot be
	// turned into one. Retrying an unchanged payload cannot succeed.
	ReasonInvalid = "Invalid"

	// ReasonAPIError is on Ready: NetBox was unreachable, rate limiting, or failing.
	ReasonAPIError = "APIError"

	// ReasonTruncated is on Ready: a lookup paginated past the client's page cap, so the
	// engine cannot tell whether the object exists and writes nothing.
	//
	// Distinct from ReasonAPIError, which is the reason a truncated list would otherwise
	// fall into: "the query was wrong, or the endpoint is enormous" and "NetBox is down"
	// look nothing alike from the outside and are fixed differently -- one narrows a filter
	// or raises MaxPages, the other waits (docs/concepts/errors-and-retries.md).
	ReasonTruncated = "Truncated"

	// ReasonDryRunPending is on Ready: the endpoint is in DryRun, so the write that would
	// make this object correct was reported and not sent.
	ReasonDryRunPending = "DryRunPending"

	// ReasonReportPending is on Ready: the endpoint's driftMode is Report, so the write
	// that would make this object correct was reported and not sent.
	//
	// Distinct from ReasonDryRunPending because the two are configured in different
	// fields and fixed in different ways, and a reason that named DryRun on an endpoint
	// whose mode is Apply would send the reader to the wrong one.
	ReasonReportPending = "ReportPending"

	// ReasonNoDrift is on Synced and on DriftDetected: the live object already matches,
	// and nothing was sent.
	ReasonNoDrift = "NoDrift"

	// ReasonDriftCorrected is on Synced: fields differed and were PATCHed.
	ReasonDriftCorrected = "DriftCorrected"

	// ReasonDriftDetectedDryRun is on Synced: fields differ and the endpoint is in
	// DryRun, so they were reported rather than corrected.
	ReasonDriftDetectedDryRun = "DriftDetectedDryRun"

	// ReasonDriftReported is on Synced: fields differ and the endpoint's driftMode is
	// Report, so they were reported rather than corrected.
	ReasonDriftReported = "DriftReported"

	// ReasonDriftDetected is on DriftDetected: NetBox differs from the spec and nothing
	// was sent to change it. The message is the change set, `field: old → new`.
	ReasonDriftDetected = "DriftDetected"

	// ReasonAllResolved is on RefsResolved: every reference resolved.
	ReasonAllResolved = "AllResolved"

	// ReasonNotImplemented is on RefsResolved: the spec declares a reference this build
	// cannot resolve at all. It is accepted, left out of the payload, and reported rather
	// than silent.
	//
	// Nothing is in that set any more. To-many references landed with NBO-088 and generic
	// foreign keys with NBO-019, which were the two members. It stays as the guard rather
	// than as a state: a declared reference the resolver neither resolved nor refused is a
	// gap between the field map and the resolver, and it has to be reported on the object
	// instead of dropped from the payload silently.
	ReasonNotImplemented = "NotImplemented"

	// The RefsResolved reasons for a reference that did not resolve. One per cause, each
	// with its own requeue policy in internal/resolver -- see docs/concepts/references.md
	// for the table. Ready reports WaitingForRef for every one of them: one reason for
	// "a reference is missing", and these for which.

	// ReasonRefNotFound is on RefsResolved: nothing to point at. No CR of that name, no
	// NetBox object matching that slug or lookup, or a raw id NetBox does not hold.
	ReasonRefNotFound = "RefNotFound"

	// ReasonRefNotReady is on RefsResolved: the target CR exists and has no NetBox id yet.
	//
	// A state rather than a failure, and the common case on a first apply. The message
	// quotes the target's own Ready reason when it has one, so a target that is *failing*
	// does not read as a referrer that is broken.
	ReasonRefNotReady = "RefNotReady"

	// ReasonRefTargetFailed is on RefsResolved: the target CR holds a NetBox id and its own
	// Ready reason says that id is for an object the target no longer describes -- a
	// Conflict, an AdoptOnly that matched nothing, or a spec NetBox rejected.
	//
	// Distinct from ReasonRefNotReady, which is a wait an event ends. This one needs somebody
	// to fix the *target*, so it carries no retry interval, and it exists because the
	// alternative -- treating every Ready=False target as a wait -- made `driftMode: Report`
	// block every object in its namespace indefinitely (NBO-089).
	ReasonRefTargetFailed = "RefTargetFailed"

	// ReasonRefAmbiguous is on RefsResolved: a slug or lookup matched several NetBox
	// objects. The message names every id, because the next step is a human choosing
	// between them.
	ReasonRefAmbiguous = "RefAmbiguous"

	// ReasonRefDenied is on RefsResolved: a cross-namespace reference with no
	// NetBoxRefGrant permitting it (NBO-014).
	ReasonRefDenied = "RefDenied"

	// ReasonRefCycle is on RefsResolved: the references depend on each other, so no order of
	// reconciles resolves them and only a spec change can. The message names the ring in
	// order, starting and ending at the object reporting it, and every member of the ring
	// reports it -- a user who saw it on one object and not on the other would conclude the
	// other was fine.
	ReasonRefCycle = "RefCycle"

	// ReasonRefDepthExceeded is on RefsResolved: the reference graph around the object was
	// too deep, or too wide, for the cycle check to walk to the end (NBO-016).
	//
	// Its own reason rather than RefCycle. A 40-deep Region tree told "you have a cycle"
	// sends its author hunting for one that does not exist, and the fix here is to flatten
	// the hierarchy rather than to break a ring.
	ReasonRefDepthExceeded = "RefDepthExceeded"

	// ReasonRefTypeNotAllowed is on RefsResolved: a polymorphic reference names a target
	// its NetBox column will not take -- a union member the Descriptor does not declare, or
	// one whose `app_label.model` type is outside the pair's allowed types.
	//
	// Terminal, like RefCycle and unlike RefNotFound: no object appearing anywhere makes an
	// illegal target legal, so there is no timer. The message names what was given and what
	// the column accepts, because those two together are the whole fix.
	ReasonRefTypeNotAllowed = "RefTypeNotAllowed"

	// ReasonRefKindUnavailable is on RefsResolved: the target Kind has no descriptor, or
	// its CRD is not installed. Distinct from RefNotFound because the manifest is correct
	// and the fix is an operator upgrade rather than an edit.
	ReasonRefKindUnavailable = "RefKindUnavailable"

	// ReasonParentOwned is on ParentOwned: the containment parent's owner reference is set,
	// so deleting the parent garbage-collects this object.
	ReasonParentOwned = "ParentOwned"

	// ReasonCascadeUnavailable is on ParentOwned: the containment parent resolved, and no
	// owner reference to it is legal, so deleting the parent will *not* remove this object.
	//
	// One reason for both causes, because the consequence and the fix are the same shape --
	// the parent is not a Kubernetes object this one may depend on -- and the message names
	// which it was. The two causes:
	//
	//   - The parent is in another namespace. An owner reference may not cross one, ever
	//     (ADR-0002 makes every kind namespaced, so this is the whole of the legality rule).
	//     A NetBoxRefGrant authorises the *reference*; nothing can authorise the owner
	//     reference, because the garbage collector is not namespace-aware and Kubernetes
	//     resolves a cross-namespace owner as absent -- which would delete this object
	//     immediately rather than never.
	//   - The parent was written as a `slug`, a `lookup` or a raw `id`, so it names a NetBox
	//     row and there is no CR for an owner reference to point at.
	//   - The parent is a member of a polymorphic union that NetBox does not cascade from,
	//     while a sibling member does: the cascade of a generic FK is declared per target
	//     model, so a Kind can be deleted with one of its legal scopes and not with another
	//     (#214). The owner reference is decided from the member the object actually
	//     resolved through, so the same manifest with a different member of the same union
	//     cascades.
	ReasonCascadeUnavailable = "CascadeUnavailable"

	// ReasonParentOwnershipDisabled is on ParentOwned: the object carries
	// ParentOwnershipAnnotation set to "false", so the owner reference was not added.
	//
	// Distinct from CascadeUnavailable, which is the operator reporting that it *cannot*.
	// This one is somebody having decided that it should not, and conflating the two would
	// send whoever set the annotation looking for a namespace problem that does not exist.
	ReasonParentOwnershipDisabled = "ParentOwnershipDisabled"

	// ReasonPendingDependents is on Deleting: the child CRs this object materialised are
	// still in the cluster, so its own NetBox object is not deleted yet.
	//
	// This is what orders a cascade, and it is not the same thing as blockOwnerDeletion.
	// That flag only takes effect under *foreground* propagation and `kubectl delete`
	// defaults to background, so under the default the garbage collector removes a parent
	// and its children concurrently. Without this wait the parent's NetBox object would
	// often go first, which NetBox refuses with PROTECT while its interfaces still exist --
	// the right end state, reached through a queue of 409s and a condition pointing at the
	// wrong cause.
	//
	// Distinct from ReasonProtected for that reason: this is the operator waiting on
	// Kubernetes objects it knows about, and that is NetBox refusing over a row the operator
	// may know nothing about. The messages send a reader to different places.
	ReasonPendingDependents = "PendingDependents"

	// ReasonProtected is on Deleting: NetBox refused the delete because something still
	// references the object. Nothing about this object can clear it -- the referring
	// object has to go first -- so it is a backed-off requeue rather than a fast retry,
	// and the message names what NetBox said is in the way.
	ReasonProtected = "Protected"

	// ReasonForeignCluster is on Conflict: the live NetBox object's cluster stamp names a
	// cluster that is not this one, so two operators are managing one object and each undoes
	// the other (NBO-047, case 1).
	//
	// The loudest of the two, and checked first: two clusters cannot see each other's CRs, so
	// nothing inside either cluster can detect this except the stamp.
	ReasonForeignCluster = "ForeignCluster"

	// ReasonForeignOwner is on Conflict: the cluster stamp is this cluster's and the owner
	// stamp names a different CR -- another namespace claiming the same natural key
	// (NBO-047, case 2), or two CRs in one namespace that both resolved to one NetBox object.
	//
	// One reason for both, because the fix is the same shape -- one of the two manifests has
	// to stop describing this object -- and the message names which CR it is. A same-namespace
	// collision on the natural key is refused at admission (NBO-044); this is what catches the
	// cross-namespace case, which cannot be refused there without leaking what exists in a
	// namespace the applier may not read (ADR-0002).
	ReasonForeignOwner = "ForeignOwner"

	// ReasonAllReady is on ChildrenReady: every child this object's inline lists declare
	// exists and is itself Ready.
	ReasonAllReady = "AllReady"

	// ReasonPendingChildren is on ChildrenReady: every child was written and at least one is
	// not Ready yet, or is still terminating after a prune. The normal state on a first
	// apply, and the state a child whose own NetBox delete is PROTECT-refused leaves the
	// parent in. The message names the children, because the fix is always on one of them.
	ReasonPendingChildren = "PendingChildren"

	// ReasonPruneBlocked is on ChildrenReady: the prune wanted to delete more children than
	// the parent declares plus a small margin, so it deleted nothing.
	//
	// A prune that wants to remove forty children of a parent that declares two is a bug in
	// the operator rather than a user's intent, and the list call that computes it is the
	// single most dangerous line in the materialiser. Refusing is not conservatism: the
	// blocked state is recoverable and a wrong delete is not.
	ReasonPruneBlocked = "PruneBlocked"
	// ReasonDataLossBlocked is on Deleting: the delete would destroy data on other objects
	// and nobody has said that is acceptable.
	//
	// Its own reason rather than Protected, because Protected is NetBox refusing and clears
	// itself when the referring object goes -- this one is the *operator* refusing, and only
	// a human clears it, by setting AllowDataLossAnnotation or switching
	// spec.deletionPolicy to Retain. Reporting it as Protected would send whoever is paged
	// looking through NetBox for a dependency that does not exist.
	ReasonDataLossBlocked = "DataLossBlocked"

	// ReasonReservedByOperator is on Ready: this CR names a NetBox object the operator is
	// already the writer of, so nothing was written.
	//
	// The provenance bootstrap creates the `k8s-managed` tag and the `k8s_uid`,
	// `k8s_cluster`, `k8s_owner` and `k8s_allocation_identity` custom fields before an
	// endpoint reports Ready, and keeps their `object_types` in step with the kinds this
	// build carries (docs/operations/provenance.md). A CR for one of those is a second writer
	// of an object every stamped object in the cluster depends on, and the engine has no way
	// to make that safe -- so it refuses rather than merging, and the message names the
	// endpoint's own `spec.managedBy` field that reserved the name.
	//
	// Not Conflict and not Invalid. Conflict means NetBox holds an object this CR could take
	// over with `onConflict: Adopt`, which is exactly what must not happen here; Invalid
	// means the spec is malformed, and this spec is fine -- it is the name that is taken, by
	// this operator, for this endpoint.
	ReasonReservedByOperator = "ReservedByOperator"
)

// Event reasons emitted by the engine. Events are the audit trail of what changed in
// NetBox, so they name the action and never the internal state.
// Every deletion outcome gets an Event because the CR is about to stop existing: once the
// finalizer is off there is no status left to read, so the Event is the only record that
// survives the object.
const (
	EventCreated   = "Created"
	EventAdopted   = "Adopted"
	EventUpdated   = "Updated"
	EventRecreated = "Recreated"
	EventConflict  = "Conflict"
	EventInvalid   = "Invalid"

	// EventDriftDetected is drift found and deliberately left alone under
	// driftMode: Report. Normal rather than Warning: nothing has malfunctioned, the
	// endpoint is doing what it was configured to do, and a Warning per object per resync
	// would make the mode unusable in the adoption week it exists for.
	//
	// It replaces the write Event rather than joining it, because "updated" and "would
	// have updated" must not read alike in `kubectl describe`.
	EventDriftDetected = "DriftDetected"

	// EventDeleted is the NetBox object gone, whether this operator removed it or found
	// it already absent.
	EventDeleted = "Deleted"

	// EventRetained is spec.deletionPolicy: Retain -- the finalizer came off and NetBox
	// was not touched.
	EventRetained = "Retained"

	// EventNothingToDelete is a CR deleted with no status.id, so the operator has no
	// object it can prove it owns.
	EventNothingToDelete = "NothingToDelete"

	// EventDeleteBlocked is a delete NetBox has now refused several times. Emitted once,
	// so a permanently stuck deletion is visible rather than silent.
	EventDeleteBlocked = "DeleteBlocked"

	// EventFinalizerSkipped is the break-glass annotation being honoured, which leaves an
	// object behind in NetBox.
	EventFinalizerSkipped = "FinalizerSkipped"

	// EventConflictSustained is a conflict that has survived several consecutive reconciles
	// with the same claimant, which is what tells a two-writer fight apart from a flap
	// (NBO-047).
	//
	// A second Event reason rather than a repeat of Conflict, and emitted exactly once, at
	// the threshold: a migration, a `TakeOver` by hand in the NetBox UI or a cluster being
	// rebuilt all produce one or two conflicting reconciles and then stop, and waking somebody
	// for those is how a signal becomes noise. `status.conflict.observations` carries the
	// count either way.
	EventConflictSustained = "ConflictSustained"

	// EventChildMaterialised is a child CR created or updated from an inline entry, and
	// EventChildPruned one deleted because its entry is gone. On the transition only: an
	// Event per resync would put one line per child per interval into the namespace.
	EventChildMaterialised = "ChildMaterialised"

	// EventChildPruned is a child CR deleted because the inline entry that declared it is no
	// longer in the parent's spec.
	EventChildPruned = "ChildPruned"

	// EventChildFieldReverted is a field on a materialised child that somebody else had
	// taken ownership of, taken back.
	//
	// A Warning, and it names the fields, because it is the one place the operator
	// deliberately overwrites a human's edit: the parent's inline entry is the declared
	// source of truth for the fields the materialiser sets, so a hand edit to one of them is
	// reverted -- while a field the materialiser never sets is left exactly as it is.
	EventChildFieldReverted = "ChildFieldReverted"
)

// ConflictPolicy is what the engine does when a NetBox object already matches this CR's
// natural key but was not created by this CR.
//
// +kubebuilder:validation:Enum=Fail;Adopt;AdoptOnly
type ConflictPolicy string

const (
	// ConflictFail reports a Conflict condition naming what matched, and writes nothing.
	// It is the zero value and the default: adoption takes over an object somebody else
	// created and immediately reconciles it towards this spec, so an accidental adoption
	// overwrites live data with no undo. Opting in is one field; recovering from a wrong
	// adoption is a restore.
	ConflictFail ConflictPolicy = "Fail"

	// ConflictAdopt takes the matching object over and reconciles it, creating one when
	// nothing matches.
	ConflictAdopt ConflictPolicy = "Adopt"

	// ConflictAdoptOnly takes a matching object over but never creates one. For objects a
	// human owns, where the operator should correct drift and never bring one into
	// existence.
	ConflictAdoptOnly ConflictPolicy = "AdoptOnly"
)

// DeletionPolicy is what happens to the NetBox object when its CR is deleted.
//
// Not spelled `reclaimPolicy`: that is PersistentVolume vocabulary, where it decides what
// happens to *storage* after a claim is released and carries a `Recycle` value with no
// analogue here. `deletionPolicy` matches
// docs/decisions/0003-ownership-and-references.md and Crossplane.
//
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	// DeletionDelete removes the NetBox object when the CR goes away. The default,
	// because a CR that created an object and then leaves it behind is a leak nobody
	// asked for.
	DeletionDelete DeletionPolicy = "Delete"

	// DeletionRetain drops the finalizer and leaves NetBox alone. For migrating off the
	// operator, and for an object that is shared with something else -- in both cases the
	// NetBox object outliving the CR is the point rather than an accident.
	DeletionRetain DeletionPolicy = "Retain"
)

// ProvenanceStatus is the provenance stamp the engine last wrote onto one NetBox object.
//
// It records what was written rather than what was configured, so an object stamped
// before spec.managedBy was edited reports the old stamp until it is next reconciled --
// which is the honest answer to "what is on the object in NetBox right now".
type ProvenanceStatus struct {
	// ClusterID is the cluster identifier written into the cluster custom field.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

	// Tag is the tag's slug, written by id. Empty for a kind whose NetBox model has no
	// `tags` column.
	// +optional
	Tag string `json:"tag,omitempty"`

	// CustomFields are the custom fields written, as NetBox names to the values sent.
	// Empty for a kind whose NetBox model has no `custom_fields` column.
	//
	// These keys are also the only ones compared: NetBox merges a partial `custom_fields`
	// PATCH and returns every custom field defined for the object type, including ones the
	// operator knows nothing about, so it compares the keys it sets and leaves the rest
	// alone. An absent or empty map therefore still means "manage nothing" rather than
	// "clear everything"; removing one key's value is said with a `null` under
	// spec.customFields (docs/concepts/field-ownership.md, #196).
	// +optional
	CustomFields map[string]string `json:"customFields,omitempty"`
}

// ConflictStatus is another writer's claim on the NetBox object behind one CR: what its
// provenance stamp said, and for how long it has been saying it (NBO-047).
//
// It is on the CR's status rather than only in a condition message because the two questions
// an operator asks are "who else is writing this" and "is this still happening", and neither
// is answerable from a sentence: `kubectl get nbprefix -A -o
// jsonpath='{..status.conflict.owner}'` lists every disputed object in the cluster, a
// condition message does not. It is on the *CR* rather than in a report object of its own
// because every conflict already has a CR, and that CR is one of the two manifests somebody
// has to edit -- so there is nothing for a separate object to name that this one does not.
//
// Unset when the object's stamp is this cluster's own, which includes the ordinary case of no
// stamp at all.
type ConflictStatus struct {
	// Reason is ForeignCluster or ForeignOwner -- the same value as the Conflict condition's
	// reason, repeated here so a single `-o jsonpath` over `status.conflict` is a complete
	// answer.
	Reason string `json:"reason"`

	// ClusterID is the other writer's cluster stamp, verbatim. Empty when the live object
	// carries none, which is what an endpoint whose clusterField is switched off writes.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

	// Owner is the other writer's CR, as `<lowercased kind>/<namespace>/<name>` -- the
	// manifest to go and edit, in the same spelling `status.provenance` and the
	// netbox.kubeforge.org/generated-by annotation use. Empty when the live object carries no
	// owner stamp.
	//
	// It names a CR in *another* cluster when Reason is ForeignCluster, so it is a lead rather
	// than a resolvable reference: nothing in this cluster can look it up, and that is the
	// point -- otherwise the only record of the other writer would be in a cluster you may not
	// have.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Observations is how many consecutive reconciles have found this same claimant. One is a
	// flap -- a migration, a hand edit, a cluster mid-rebuild; a number that keeps climbing is
	// two writers taking turns, and only the second is worth waking somebody for.
	//
	// Consecutive reconciles rather than a duration, because a reconcile is when the operator
	// can observe anything at all: multiply by the endpoint's resyncPeriod for the wall-clock
	// answer, or read firstObserved. It resets to one when the claimant changes, since a
	// different writer is a different fight.
	// +optional
	Observations int32 `json:"observations,omitempty"`

	// FirstObserved is when this claimant was first seen on this object. Preserved across
	// reconciles for as long as the claimant does not change.
	// +optional
	FirstObserved *metav1.Time `json:"firstObserved,omitempty"`
}

// ChildStatus is one child CR a parent's inline list materialised.
type ChildStatus struct {
	// Path is the key-based spec path that declared this child, the same string as the
	// OwnedByPathAnnotation on the child itself: `spec.interfaces[eth0]`. The map key,
	// because it is the child's identity within the parent -- the name is derived from it.
	Path string `json:"path"`

	// Kind is the child's Kind, in this API group.
	Kind string `json:"kind"`

	// Name is the child CR's name, derived from the parent's metadata.name and Path.
	Name string `json:"name"`

	// Ready is the child's own Ready condition, as of this pass. False includes "the child
	// was only just created", which is why the parent requeues rather than settling.
	// +optional
	Ready bool `json:"ready,omitempty"`
}

// NetBoxObjectSpec is the part of every object CR's spec that the engine owns. Kinds embed
// it inline, so its fields are spec fields like any other -- and the engine excludes them
// from the NetBox payload rather than mapping them through a descriptor, since they
// configure the operator rather than describe one NetBox column.
//
// CustomFields is the one that does reach NetBox. It is here rather than in every kind's
// field map for the same reason `tags` is not in one either: it is not a per-kind column
// but the same container on every CustomFieldsMixin model, under the same name, so a field
// map entry per kind would be 120 copies of one fact (NBO-075).
type NetBoxObjectSpec struct {
	// EndpointRef names the NetBoxEndpoint to write through, in this object's own
	// namespace. Required: there is no cluster-wide default endpoint, so an omitted
	// reference cannot be resolved into one.
	//
	// **Immutable.** Pointing a CR at a different NetBox is not a mutation of the object, it
	// is a different object: the natural key is looked up in whichever NetBox this names, so
	// an edit here would leave the old NetBox holding an object nothing manages and would
	// adopt or create a second one in the new one, with the CR's status.id switching between
	// the two. Edit it by deleting the CR -- which lets spec.deletionPolicy do what it says
	// about the object being left behind -- and re-applying it.
	//
	// A CEL transition rule and not the admission webhook, which is the whole layer-1 test:
	// it needs `self` and `oldSelf` and no second object, so the API server enforces it
	// unconditionally and it survives every webhook in the cluster being down (NBO-044).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="endpointRef is immutable; pointing a CR at a different NetBox is a different object, so delete this one and re-apply it"
	EndpointRef string `json:"endpointRef"`

	// OnConflict is what to do when NetBox already holds a matching object.
	// +kubebuilder:default=Fail
	// +optional
	OnConflict ConflictPolicy `json:"onConflict,omitempty"`

	// DeletionPolicy is what happens to the NetBox object when this CR is deleted.
	//
	// Read fresh on every pass rather than latched when deletion starts, so switching it
	// to Retain on an object whose delete NetBox keeps refusing is a way out of that
	// state (docs/concepts/deletion.md).
	//
	// Left unset it is Delete for most kinds and Retain for the IPAM ones, where deleting
	// the NetBox object destroys state rather than configuration -- an address freed for
	// reallocation, a range whose ownership record is gone (decision #176). The default is
	// therefore *not* a CRD marker: this field is declared once for every kind, so a marker
	// here could only give them all the same answer. Each kind declares its own on its
	// Descriptor and docs/concepts/deletion.md lists them.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// CustomFields are NetBox custom-field values to write, keyed by NetBox custom-field
	// name. Every key must already exist as an `extras.CustomField` covering this object
	// type -- NetBox rejects the whole payload otherwise.
	//
	// The map is deliberately **not** exhaustive: only the keys named here are written and
	// compared, and every other custom field on the NetBox object is left exactly as it is.
	// NetBox merges a partial `custom_fields` PATCH and returns every custom field defined
	// for the object type, including ones this operator knows nothing about, so treating
	// the map as the whole container would null out every custom field another writer on
	// that NetBox owns, on every reconcile. Omitting the map, or setting it to `{}`,
	// therefore means "manage nothing" rather than "clear everything".
	//
	// A value is written as the JSON type it is given, because that is the only thing
	// NetBox accepts. A custom field carries a `type` (extras/choices.py,
	// CustomFieldTypeChoices) and its serializer validates against it, so `chef_managed:
	// "true"` on a `boolean` field is rejected outright -- *"Invalid value for custom field
	// 'chef_managed': Value must be true or false"* -- and no amount of retrying makes a
	// string into a boolean. Every non-text type is in the same position: `integer`,
	// `decimal`, `boolean`, `json`, `multiselect` and `multiobject` each need their own JSON
	// shape.
	//
	//   customFields:
	//     chef_managed: true             # boolean
	//     extra_disk_1: 500              # integer
	//     rack_position: "12"            # text that happens to look like a number
	//     tiers: [gold, silver]          # multiselect
	//
	// So the type is the user's to state and the operator's to carry through unchanged
	// (#303). Quoting is therefore load-bearing rather than noise: `"12"` on a text field
	// and `12` on an integer field are both right, and each is wrong on the other -- which
	// is a distinction a `map[string]string` could not make at all, and why this is not one.
	//
	// `null` and `""` are different intents and both are expressible:
	//
	//   customFields:
	//     rack_position: ""      # set this custom field to the empty string
	//     audit_ticket: null     # remove this custom field's value
	//
	// `null` is sent to NetBox as JSON null, which is the value NetBox stores for a custom
	// field that has no value and the value it returns on read -- indistinguishable from a
	// field never set, which is what "removed" means here. `""` stores and returns the
	// empty string. Removing the key from the manifest entirely is the third state: it
	// hands the field back and NetBox keeps whatever it holds
	// (docs/concepts/field-ownership.md).
	//
	// Set on a kind whose NetBox model carries no `custom_fields` column -- extras.Tag is
	// one, so a NetBoxTag is the case -- and the object reports Ready=False,
	// Reason=Invalid. Refused rather than dropped: a discarded value would leave the
	// object claiming to be synced while NetBox never received it.
	// +optional
	CustomFields map[string]JSONDocument `json:"customFields,omitempty"`
}

// NetBoxObjectStatus is the part of every object CR's status that the engine owns. It is
// the only thing the operator writes: the spec belongs to Git
// (docs/decisions/0005-gitops-coexistence.md).
type NetBoxObjectStatus struct {
	// ID is the NetBox primary key. Zero until the object provably exists server-side --
	// a DryRun write is reported and never sent, so it leaves this empty rather than
	// inventing an id that nothing would ever match.
	// +optional
	ID int64 `json:"id,omitempty"`

	// URL is the object's absolute NetBox URL, as returned by the API.
	// +optional
	URL string `json:"url,omitempty"`

	// NaturalKey is the lookup that actually located the object, filter by filter. The
	// first thing anyone needs when asking why an object was adopted, or was not.
	// +optional
	NaturalKey map[string]string `json:"naturalKey,omitempty"`

	// Adopted reports that the engine took over an object it did not create.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// Provenance is the stamp this object carries in NetBox: the tag and the custom fields
	// the engine wrote, as it wrote them.
	//
	// Unset when the endpoint's spec.managedBy is unset, and unset for a kind whose NetBox
	// model carries neither `tags` nor `custom_fields` -- extras.Tag is one, so a
	// NetBoxTag is managed and unstamped by construction. That is the state NetBoxSweep
	// (NBO-046) reports and never deletes; see docs/operations/provenance.md.
	// +optional
	Provenance *ProvenanceStatus `json:"provenance,omitempty"`

	// Conflict is the other writer this object's NetBox counterpart says it belongs to, when
	// its stamp names one (NBO-047). The write went ahead regardless -- see
	// ConditionConflict and docs/operations/multi-writer.md.
	// +optional
	Conflict *ConflictStatus `json:"conflict,omitempty"`

	// LastAppliedHash is a digest of the last payload NetBox accepted. NetBox
	// canonicalises some values on write, so the request and the response legitimately
	// differ; this is the record of what was actually sent.
	// +optional
	LastAppliedHash string `json:"lastAppliedHash,omitempty"`

	// DeferredPending are the CR spec fields the engine has declared deferred and has not
	// yet written to NetBox: a `primary_ip4` whose address does not exist yet, or one that
	// was stripped from the create and is waiting for its follow-up PATCH.
	//
	// A status field rather than only a condition message. The intermediate state is
	// legitimate and can be long-lived -- a reference that never resolves stays here
	// forever, on purpose -- so "what is this object still waiting to write" has to be
	// answerable from `kubectl get -o yaml` and greppable across a namespace, which a
	// sentence inside a condition is not (docs/concepts/object-lifecycle.md).
	//
	// Spec field names rather than NetBox column names, because it is the spelling the
	// user wrote and the one the RefsResolved message already uses.
	// +listType=atomic
	// +optional
	DeferredPending []string `json:"deferredPending,omitempty"`

	// LastSyncTime is when the engine last wrote to NetBox. Unset until it does, and
	// untouched by a reconcile that found nothing to do -- otherwise every resync would
	// bump the resourceVersion of every object in the cluster.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ObservedGeneration is the spec generation this status refers to. Always set,
	// because `kubectl wait` lies without it.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Children are the child CRs this object's inline lists materialised, one entry per
	// declared child (NBO-032).
	//
	// A status field rather than only a label query, so that `kubectl describe` answers
	// "what did this VM create" without one. It is also what lets the pruner know which
	// *kinds* to list after every inline entry has been removed: with an empty inline list
	// there is no desired child left to read a GVK off, and the children recorded here are
	// the only remaining record of what to go looking for.
	// +listType=map
	// +listMapKey=path
	// +optional
	Children []ChildStatus `json:"children,omitempty"`

	// DeletionAttempts counts the deletes NetBox refused because something still
	// references the object.
	//
	// It is a status field rather than in-memory state because a controller has no memory
	// between passes: the exponential backoff on a protected delete has to be computed
	// from a count that survives a requeue, a leader election and a restart. Non-zero
	// only while a CR is terminating.
	// +optional
	DeletionAttempts int32 `json:"deletionAttempts,omitempty"`

	// LastDeletionAttempt is when the last of those refused deletes was sent, and it is what
	// makes the count above mean anything: the backoff is read off the clock rather than off
	// whatever woke the controller, so a pass that arrives early re-queues for the remainder
	// and calls nothing (#289, reconciler.deletionHold). In status for the same reason the
	// count is -- it has to survive a requeue, a leader election and a restart.
	// +optional
	LastDeletionAttempt *metav1.Time `json:"lastDeletionAttempt,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
