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
	// id. References are accepted and ignored until NBO-012, so in v1alpha1's first
	// milestone this condition reports NotImplemented rather than lying.
	ConditionRefsResolved = "RefsResolved"
)

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

	// ReasonDryRunPending is on Ready: the endpoint is in DryRun, so the write that would
	// make this object correct was reported and not sent.
	ReasonDryRunPending = "DryRunPending"

	// ReasonNoDrift is on Synced: the live object already matches, and nothing was sent.
	ReasonNoDrift = "NoDrift"

	// ReasonDriftCorrected is on Synced: fields differed and were PATCHed.
	ReasonDriftCorrected = "DriftCorrected"

	// ReasonDriftDetectedDryRun is on Synced: fields differ and the endpoint is in
	// DryRun, so they were reported rather than corrected.
	ReasonDriftDetectedDryRun = "DriftDetectedDryRun"

	// ReasonAllResolved is on RefsResolved: every reference resolved.
	ReasonAllResolved = "AllResolved"

	// ReasonNotImplemented is on RefsResolved: the spec declares references and this
	// build does not resolve any (NBO-012). They are accepted and left out of the
	// payload, which is reported rather than silent.
	ReasonNotImplemented = "NotImplemented"
)

// Event reasons emitted by the engine. Events are the audit trail of what changed in
// NetBox, so they name the action and never the internal state.
const (
	EventCreated   = "Created"
	EventAdopted   = "Adopted"
	EventUpdated   = "Updated"
	EventRecreated = "Recreated"
	EventConflict  = "Conflict"
	EventInvalid   = "Invalid"
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

	// LastAppliedHash is a digest of the last payload NetBox accepted. NetBox
	// canonicalises some values on write, so the request and the response legitimately
	// differ; this is the record of what was actually sent.
	// +optional
	LastAppliedHash string `json:"lastAppliedHash,omitempty"`

	// LastSyncTime is when the engine last wrote to NetBox. Unset until it does, and
	// untouched by a reconcile that found nothing to do -- otherwise every resync would
	// bump the resourceVersion of every object in the cluster.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// ObservedGeneration is the spec generation this status refers to. Always set,
	// because `kubectl wait` lies without it.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
