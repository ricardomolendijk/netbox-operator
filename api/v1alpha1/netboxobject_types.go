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

	// ConditionDeleting reports what the engine is doing about the NetBox object behind a
	// CR that carries a deletion timestamp.
	//
	// It is only ever False. The finalizer comes off the moment the NetBox side is
	// settled, so a True would describe a CR that no longer exists to carry it; the
	// Reason is therefore always what is holding the deletion up.
	ConditionDeleting = "Deleting"
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
	// cannot resolve at all -- a generic foreign key, whose target is a union of Kinds
	// rather than one and whose dispatch is NBO-019. It is accepted, left out of the
	// payload, and reported rather than silent.
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

	// ReasonRefKindUnavailable is on RefsResolved: the target Kind has no descriptor, or
	// its CRD is not installed. Distinct from RefNotFound because the manifest is correct
	// and the fix is an operator upgrade rather than an edit.
	ReasonRefKindUnavailable = "RefKindUnavailable"

	// ReasonProtected is on Deleting: NetBox refused the delete because something still
	// references the object. Nothing about this object can clear it -- the referring
	// object has to go first -- so it is a backed-off requeue rather than a fast retry,
	// and the message names what NetBox said is in the way.
	ReasonProtected = "Protected"
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
	// +optional
	CustomFields map[string]string `json:"customFields,omitempty"`
}

// NetBoxObjectSpec is the part of every object CR's spec that the engine owns. Kinds embed
// it inline, so its fields are spec fields like any other -- and the engine excludes
// exactly these from the NetBox payload, since they configure the operator rather than
// describe a NetBox object.
type NetBoxObjectSpec struct {
	// EndpointRef names the NetBoxEndpoint to write through, in this object's own
	// namespace. Required: there is no cluster-wide default endpoint, so an omitted
	// reference cannot be resolved into one.
	// +kubebuilder:validation:MinLength=1
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
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
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

	// DeletionAttempts counts the deletes NetBox refused because something still
	// references the object.
	//
	// It is a status field rather than in-memory state because a controller has no memory
	// between passes: the exponential backoff on a protected delete has to be computed
	// from a count that survives a requeue, a leader election and a restart. Non-zero
	// only while a CR is terminating.
	// +optional
	DeletionAttempts int32 `json:"deletionAttempts,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
