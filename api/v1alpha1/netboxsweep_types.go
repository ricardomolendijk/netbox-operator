package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set on a NetBoxSweep.
const (
	// ConditionSweepReady is true when the last run completed over every listed kind and
	// the findings in `status` are the whole answer. False means the run was refused, and
	// `status.findings` is then the **previous** run's answer rather than an empty one; the
	// `Reason` says which refusal it was.
	ConditionSweepReady = "Ready"

	// ConditionSweepSuspended is true when spec.suspend stopped the schedule. Its own
	// condition rather than a Ready=False reason, because a suspended sweep has not failed
	// and must not read like one on a dashboard.
	ConditionSweepSuspended = "Suspended"
)

// Condition reasons for a NetBoxSweep. Every refusal has its own reason, because "the
// sweep did not run" has as many different fixes as there are causes and a single
// `Refused` would send every one of them to the same runbook page.
const (
	// ReasonSweepComplete is on Ready: every listed kind was scanned in full.
	ReasonSweepComplete = "Complete"

	// ReasonSweepSuspended is on Suspended: spec.suspend is true, so nothing ran. `Ready`
	// is deliberately left exactly as the last run left it, because a suspended sweep has
	// not failed -- and its findings are still the last true answer, just an older one.
	ReasonSweepSuspended = "Suspended"

	// ReasonSweepScheduled is on Suspended=False: the sweep is scheduled normally.
	ReasonSweepScheduled = "Scheduled"

	// ReasonSweepEndpointNotReady is on Ready: the NetBoxEndpoint has no usable client, so
	// there is nothing to list NetBox with.
	ReasonSweepEndpointNotReady = "EndpointNotReady"

	// ReasonSweepEndpointDryRun is on Ready: the endpoint is in `mode: DryRun`. A DryRun
	// CR never gets a `status.id`, so every object of every kind would look unclaimed --
	// the single most dangerous interaction this feature has, and an explicit guard rather
	// than an emergent property.
	ReasonSweepEndpointDryRun = "EndpointDryRun"

	// ReasonSweepDriftOff is on Ready: the endpoint's `driftMode: Off`. The operator is
	// not tracking NetBox state at all, so the absence of a claim proves nothing.
	ReasonSweepDriftOff = "DriftOff"

	// ReasonSweepProvenanceDisabled is on Ready: the endpoint's `spec.managedBy` writes no
	// stamp, or the cluster/uid/owner custom fields it needs do not exist in NetBox. With
	// no stamp there is nothing that distinguishes this cluster's objects from anybody
	// else's, and refusing is the only safe reading of "I cannot tell whose objects these
	// are".
	ReasonSweepProvenanceDisabled = "ProvenanceDisabled"

	// ReasonSweepUnknownKind is on Ready: a kind in spec.kinds has no registered
	// descriptor in this build, so its NetBox endpoint is unknown and its CRs cannot be
	// listed. The whole run is refused rather than the one kind skipped: a partial scan
	// that reports on the kinds it managed is indistinguishable from a complete one.
	ReasonSweepUnknownKind = "UnknownKind"

	// ReasonSweepKindNotStampable is on Ready: a kind in spec.kinds has a NetBox model with
	// no `custom_fields` column -- extras.Tag is one, so NetBoxTag is the case. Such an
	// object cannot carry the cluster stamp, so it can never be attributed to this cluster
	// and a scan of it could only ever guess. Refused rather than skipped, and
	// docs/operations/provenance.md already states the consequence: an unstampable object
	// is never reclaimed by a sweep.
	ReasonSweepKindNotStampable = "KindNotStampable"

	// ReasonSweepTruncated is on Ready: a list paginated past the client's page cap, so
	// the set of live objects is incomplete. An incomplete list makes live objects look
	// absent, which is exactly the input that would turn a report into a false accusation.
	ReasonSweepTruncated = "Truncated"

	// ReasonSweepTimeout is on Ready: the run exceeded spec.timeout. A timeout is a failed
	// run, never a partial one.
	ReasonSweepTimeout = "Timeout"

	// ReasonSweepAPIError is on Ready: NetBox was unreachable, rate limiting or failing.
	ReasonSweepAPIError = "APIError"
)

// Event reasons emitted by the sweep controller.
const (
	// EventOrphansFound is a run that confirmed at least one orphan. Normal rather than
	// Warning: an orphan is a fact about NetBox, not a malfunction of the operator, and it
	// is reported so somebody can decide what to do about it.
	EventOrphansFound = "OrphansFound"

	// EventSweepRefused is a run that did not happen. Warning, because the findings in
	// `status` are now older than they look.
	EventSweepRefused = "SweepRefused"
)

// SweepFindingReason is why one NetBox object appears in `status.findings`.
//
// +kubebuilder:validation:Enum=Orphaned;Suspected;Unattributed
type SweepFindingReason string

const (
	// SweepOrphaned is a stamped object whose owning CR is gone and whose grace period has
	// expired. This is the finding to act on.
	SweepOrphaned SweepFindingReason = "Orphaned"

	// SweepSuspected is the same thing inside its grace period. A CR mid-recreation, a
	// namespace being reapplied, or an operator that was down while Git changed all look
	// like an orphan for a few seconds, so a finding is reported as suspected until it has
	// been continuously unclaimed for spec.gracePeriod.
	SweepSuspected SweepFindingReason = "Suspected"

	// SweepUnattributed is a stamped object whose owner stamp is missing or unparseable.
	// It cannot be attributed to any namespace -- it may be another namespace's object
	// written by an older operator, a leftover from `netbox-populator`, or a genuine
	// orphan from before the stamp existed. The sweep cannot tell, so it reports the count
	// and never claims it is an orphan.
	SweepUnattributed SweepFindingReason = "Unattributed"
)

// SweepFinding is one NetBox object this sweep could not match to a live CR.
type SweepFinding struct {
	// Kind is the Kubernetes Kind whose descriptor found it, which is also the kind of CR
	// that would have to exist for it not to be a finding.
	Kind string `json:"kind"`

	// NetBoxID is the object's NetBox primary key. With Kind, this is the pair that
	// identifies a finding across runs and carries FirstSeen forward.
	NetBoxID int64 `json:"netboxID"`

	// Display is NetBox's own `display` string for the object, which is what the NetBox UI
	// shows and therefore the only field a human can search on.
	// +optional
	Display string `json:"display,omitempty"`

	// URL is the object's absolute NetBox API URL, copied verbatim from the list response.
	// Paste it into a browser and NetBox redirects to the object.
	// +optional
	URL string `json:"url,omitempty"`

	// Owner is the `k8s_owner` stamp as NetBox holds it: `<kind>/<namespace>/<name>` of
	// the CR that last wrote this object. Empty on an Unattributed finding, which is what
	// makes it unattributable.
	// +optional
	Owner string `json:"owner,omitempty"`

	// UID is the `k8s_uid` stamp: the `metadata.uid` of the CR that wrote the object. It
	// is what separates "the CR was deleted" from "the CR was deleted and reapplied" --
	// the second has a new UID, so the old object is genuinely orphaned even though a CR
	// of that name exists.
	// +optional
	UID string `json:"uid,omitempty"`

	// FirstSeen is when this sweep first found the object unclaimed, carried forward from
	// the previous run's status. It is the clock the grace period is measured against, so
	// it uses one clock -- the operator's -- and never NetBox's `last_updated`.
	//
	// It lives in `status`, so a controller restart does not reset it. If `status` is lost
	// the clock restarts, which fails in the safe direction: a finding goes back to
	// Suspected rather than forward to Orphaned.
	FirstSeen metav1.Time `json:"firstSeen"`

	// Reason is why this object is a finding.
	Reason SweepFindingReason `json:"reason"`
}

// SweepSummary is the true count of everything the last run saw, whatever the cap did to
// `status.findings`.
type SweepSummary struct {
	// Scanned is the stamped NetBox objects examined, across every listed kind.
	Scanned int32 `json:"scanned"`

	// Claimed is the objects matched to a live CR in this namespace, by NetBox id or by
	// `k8s_uid`. These are the healthy ones.
	Claimed int32 `json:"claimed"`

	// Orphans is confirmed orphans: unclaimed for longer than spec.gracePeriod.
	Orphans int32 `json:"orphans"`

	// Suspected is unclaimed objects still inside their grace period.
	Suspected int32 `json:"suspected"`

	// Unattributed is stamped objects with no usable owner stamp.
	Unattributed int32 `json:"unattributed"`

	// Foreign is objects stamped for another namespace of this same cluster. Counted and
	// never listed: they are not this sweep's business, and the count exists only so that
	// `scanned` adds up and nobody reads a gap as a lost object.
	Foreign int32 `json:"foreign"`
}

// NetBoxSweepSpec is one scheduled, read-only scan for NetBox objects this namespace has
// left behind.
type NetBoxSweepSpec struct {
	// EndpointRef names the NetBoxEndpoint to scan through, in this sweep's own namespace.
	// The endpoint is where both halves of the scope come from: its `spec.managedBy.tag`
	// and its `clusterID`.
	// +kubebuilder:validation:MinLength=1
	EndpointRef string `json:"endpointRef"`

	// Kinds is the Kubernetes Kinds to scan, and there is no wildcard. Scanning a kind is
	// an explicit act: a wildcard would silently start listing every one of the ~120 kinds
	// in the catalogue the moment a new one shipped, which is a load change nobody asked
	// for on somebody else's NetBox.
	//
	// A kind this build does not carry refuses the whole run rather than being skipped --
	// see ReasonSweepUnknownKind.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^NetBox[A-Za-z0-9]+$`
	// +listType=atomic
	Kinds []string `json:"kinds"`

	// Interval is how often to scan. The default is deliberately a day rather than the
	// endpoint's resync period: one run lists every stamped object of every listed kind,
	// so a sweep on a resync cadence is a standing load on somebody's NetBox in exchange
	// for a report nobody reads that often. An orphan is not urgent -- it is a leak, and
	// leaks are counted daily.
	// +kubebuilder:default="24h"
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// GracePeriod is how long an object must have been continuously unclaimed before it is
	// reported as Orphaned rather than Suspected.
	//
	// It exists because the two states that look identical from NetBox -- "the CR is gone"
	// and "the CR is between a delete and a re-apply" -- are told apart only by waiting.
	// Zero disables it, and every finding is then reported as Orphaned on its first
	// sighting.
	// +kubebuilder:default="1h"
	// +optional
	GracePeriod metav1.Duration `json:"gracePeriod,omitempty"`

	// MaxFindings caps `status.findings`. Beyond it `status.findingsTruncated` is set and
	// `status.summary` still carries true counts.
	//
	// The cap is not a nicety: an etcd object has a size limit, and a status carrying fifty
	// thousand findings does not get rejected on its own -- it takes the CR with it. The
	// full list always reaches the debug log.
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +optional
	MaxFindings int32 `json:"maxFindings,omitempty"`

	// Timeout bounds one whole run, every kind together. Exceeding it is a refused run
	// with no findings written, never a partial report.
	// +kubebuilder:default="10m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// Suspend stops scheduling without deleting the object or its findings. The lever to
	// pull when a sweep is generating load at the wrong moment.
	// +kubebuilder:default=false
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// NetBoxSweepStatus is the whole point of the kind: the record of what this cluster has
// left behind in NetBox, in a form that is still there hours later.
type NetBoxSweepStatus struct {
	// LastRunTime is when the last **completed** run finished. A refused run does not move
	// it, so the gap between it and now is how stale the findings are.
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// NextRunTime is when the controller intends to run again.
	// +optional
	NextRunTime *metav1.Time `json:"nextRunTime,omitempty"`

	// LastRunDuration is how long the last completed run took, as a duration string.
	// +optional
	LastRunDuration string `json:"lastRunDuration,omitempty"`

	// ObservedGeneration is the spec generation this status refers to.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Summary is the true counts from the last completed run.
	// +optional
	Summary SweepSummary `json:"summary,omitempty"`

	// Findings are the objects the last completed run could not match to a live CR,
	// capped at spec.maxFindings and ordered so that the cap drops the least actionable:
	// Orphaned first, then Suspected, then Unattributed, and within each by kind and
	// NetBox id.
	//
	// **A refused run leaves this alone.** An empty findings list has to mean "the last
	// complete scan found nothing", never "the last scan could not see anything" -- read
	// `Ready` and `lastRunTime` to tell which you are looking at.
	// +listType=atomic
	// +optional
	Findings []SweepFinding `json:"findings,omitempty"`

	// FindingsTruncated reports that the last run had more findings than the cap.
	// +optional
	FindingsTruncated bool `json:"findingsTruncated,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NetBoxSweep reports NetBox objects that this namespace's CRs left behind, and **never
// deletes one**.
//
// The situation it exists for is real and has two causes: a CR removed from Git while the
// operator or the cluster was down, so no finalizer ever ran; and `deletionPolicy: Retain`,
// where leaving the object behind is the point. Either way the object still carries this
// cluster's provenance stamp and there is no CR left to reconcile it, so nothing in the
// operator will ever look at it again.
//
// **Reporting, not reclaiming.** A leaked NetBox object is visible, costs nothing but a row,
// and is reclaimable by hand at any time. An object freed by mistake may already have been
// handed to somebody else, and no undo exists for that. So this kind lists what it found and
// stops; `nbctl adopt` and a human are the remedy. See docs/operations/sweeps.md.
//
// Namespaced, like every kind in v1alpha1 (ADR-0002), and here that is a safety property
// rather than a consequence: the blast radius of a sweep is one namespace's findings,
// authorising one is ordinary namespaced RBAC, and the NetBoxEndpoint it scans through is
// itself namespaced -- so a sweep in `team-a` cannot reach a NetBox that `team-a` cannot
// already write to.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsweep
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpointRef`
// +kubebuilder:printcolumn:name="Orphans",type=integer,JSONPath=`.status.summary.orphans`
// +kubebuilder:printcolumn:name="Suspected",type=integer,JSONPath=`.status.summary.suspected`
// +kubebuilder:printcolumn:name="Unattributed",type=integer,JSONPath=`.status.summary.unattributed`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Last Run",type=date,JSONPath=`.status.lastRunTime`
type NetBoxSweep struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxSweepSpec   `json:"spec,omitempty"`
	Status NetBoxSweepStatus `json:"status,omitempty"`
}

// NetBoxSweepList is a list of NetBoxSweep.
// +kubebuilder:object:root=true
type NetBoxSweepList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxSweep `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxSweep{}, &NetBoxSweepList{})
}
