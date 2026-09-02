package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set on a NetBoxEndpoint.
const (
	// ConditionReady is true when the endpoint has a usable client. Object controllers
	// wait on this one.
	ConditionReady = "Ready"
	// ConditionAuthenticated is true when the token was accepted.
	ConditionAuthenticated = "Authenticated"
	// ConditionVersionSupported is true when the server's version is in the supported
	// range.
	ConditionVersionSupported = "VersionSupported"

	// ConditionProvenanceReady is true when the tag and custom-field definitions that
	// spec.managedBy asks for exist in NetBox, so an object write may carry them.
	//
	// Absent, rather than False, when spec.managedBy is unset: nothing was asked for, so
	// there is nothing to report. It is the one endpoint condition that describes the
	// state of somebody else's *schema* rather than of the connection, which is why it is
	// separate from Ready -- and why Ready is nonetheless False while it is False on a
	// writing endpoint, since the alternative is one identical 400 per object.
	ConditionProvenanceReady = "ProvenanceReady"
)

// Condition reasons. Kept as constants because tooling and the docs both key on them.
const (
	ReasonReady         = "Ready"
	ReasonSecretMissing = "SecretMissing"
	// ReasonCABundleMissing is distinct from ReasonSecretMissing so a reader is sent to
	// the right Secret: a missing CA bundle is not an authentication failure.
	ReasonCABundleMissing    = "CABundleMissing"
	ReasonTokenMissing       = "TokenMissing"
	ReasonAuthError          = "AuthError"
	ReasonProbeFailed        = "ProbeFailed"
	ReasonVersionUnsupported = "VersionUnsupported"
	ReasonVersionUnparseable = "VersionUnparseable"
	ReasonInvalidConfig      = "InvalidConfig"

	// ReasonProvisioned is on ProvenanceReady: every definition spec.managedBy asks for
	// is present, whether this pass created it or found it.
	ReasonProvisioned = "Provisioned"

	// ReasonBootstrapDisabled is on ProvenanceReady and on Ready: spec.managedBy.bootstrap
	// is false and a definition it needs does not exist. Creating a CustomField is a
	// schema change to somebody else's NetBox, so the operator says what is missing
	// instead of making it.
	ReasonBootstrapDisabled = "BootstrapDisabled"

	// ReasonBootstrapFailed is on ProvenanceReady and on Ready: NetBox refused the
	// bootstrap. Usually a token without extras.add_customfield.
	ReasonBootstrapFailed = "BootstrapFailed"

	// ReasonBootstrapSuppressed is on ProvenanceReady: a definition is missing and this
	// endpoint cannot write, so nothing could be created. It does not fail the endpoint --
	// an endpoint that sends nothing cannot produce the 400 the gate exists to prevent.
	ReasonBootstrapSuppressed = "BootstrapSuppressed"
)

// EndpointMode selects whether the operator may change anything through this endpoint.
// +kubebuilder:validation:Enum=Apply;DryRun
type EndpointMode string

const (
	// EndpointModeApply permits writes.
	EndpointModeApply EndpointMode = "Apply"
	// EndpointModeDryRun suppresses every write. Reads still happen, so drift is
	// reported against real state.
	EndpointModeDryRun EndpointMode = "DryRun"
)

// DriftMode selects what the operator does about a NetBox object that no longer matches
// the spec of the CR that owns it.
//
// There is deliberately no value in which NetBox wins and the difference is promoted back
// into a CR's spec. That would make the operator a second writer to desired state, which
// is the fight docs/decisions/0005-gitops-coexistence.md exists to prevent. Turning
// NetBox's current contents into manifests is `nbctl export`'s job (NBO-040): it writes
// files for a human to commit, rather than a controller writing specs.
//
// +kubebuilder:validation:Enum=Correct;Report;Off
type DriftMode string

const (
	// DriftCorrect detects drift and fixes it. The default and the intended steady
	// state: Git is authoritative, so a NetBox-side edit is simply wrong.
	DriftCorrect DriftMode = "Correct"

	// DriftReport detects drift, reports it on conditions, Events and metrics, and sends
	// nothing at all.
	//
	// For the first week of an adoption, and for running alongside a team that still
	// edits NetBox by hand. It is migration time rather than a supported operating mode,
	// which is why it is not the default and why there is nothing it can write.
	DriftReport DriftMode = "Report"

	// DriftOff disables the periodic resync, so the operator acts only when a CR
	// changes. Writes are still permitted: a spec edit is applied, a UI edit is not
	// noticed until something touches that object again.
	//
	// For a very large NetBox where the resync cost is real, at the price of a UI edit
	// persisting until the next CR change.
	DriftOff DriftMode = "Off"
)

// SecretKeyRef points at one key of a Secret in this namespace. Every use site requires
// the netbox.kubeforge.org/endpoint-credential label, because the manager's Secret cache
// selects on it rather than holding every Secret in the cluster.
//
// Deliberately not namespace-qualified: reading a Secret from another namespace is a
// privilege escalation dressed up as convenience, and it would make the operator's RBAC
// cluster-wide. A namespace that needs its own endpoint creates its own Secret.
type SecretKeyRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key within the Secret. Left unset, the controller uses a default appropriate to
	// the field: "token" for tokenSecretRef, "ca.crt" for caBundleSecretRef.
	//
	// Deliberately not a +kubebuilder:default: this type is used for both, a marker
	// applies at every use site, and defaulting a CA bundle's key to "token" makes the
	// controller's own fallback unreachable and the endpoint fail with InvalidConfig.
	// +optional
	Key string `json:"key,omitempty"`
}

// TLSConfig tunes the TLS handshake with NetBox.
type TLSConfig struct {
	// InsecureSkipVerify disables certificate verification. Logged at info on every
	// successful reconcile, because it is not a thing to forget about.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CABundleSecretRef holds additional trusted roots, PEM-encoded. That Secret must be
	// labelled netbox.kubeforge.org/endpoint-credential: "true" or the operator cannot
	// read it; see docs/operations/rbac.md.
	// +optional
	CABundleSecretRef *SecretKeyRef `json:"caBundleSecretRef,omitempty"`
}

// ManagedBy configures the provenance stamp the operator writes onto every NetBox object
// it manages through this endpoint: one tag, and a small set of custom fields.
//
// It is one struct rather than a handful of top-level fields because the whole feature is
// on or off together. Leave it unset and the operator stamps nothing, which is the
// pre-NBO-075 behaviour and is still supported; set it and every object written through
// this endpoint is attributable to a cluster, a namespace and a CR. See
// docs/operations/provenance.md, which also lists what stops working when it is unset.
type ManagedBy struct {
	// ClusterID names the cluster this operator runs in, and is the only required field.
	//
	// Deliberately not derived from the kube-system namespace UID or the API server URL.
	// Both are stable enough to be tempting and neither is predictable: an operator that
	// invents its own identifier produces a value nobody can guess when they need to
	// search NetBox for "everything cluster X owns", and one that changes on a cluster
	// rebuild breaks the reclaim in docs/decisions/0005-gitops-coexistence.md section 3 --
	// which is the one thing the identifier exists to make survive a rebuild.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]+$`
	ClusterID string `json:"clusterID"`

	// Tag is the NetBox tag applied to every managed object, used as both the tag's name
	// and its slug -- which is why it is constrained to the slug alphabet rather than to
	// extras.Tag's freer `name` column.
	//
	// It is the load-bearing half of the stamp: NetBoxSweep (NBO-046) decides what it may
	// delete by this tag alone, so renaming it on a live endpoint leaves every object
	// stamped with the old name unsweepable until the next resync re-stamps them.
	// +kubebuilder:default="k8s-managed"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	// +optional
	Tag string `json:"tag,omitempty"`

	// UIDField is the custom field holding the owning CR's metadata.uid.
	//
	// Every custom-field name here is constrained to NetBox's own alphabet for the column
	// (`^[a-z0-9_]+$`, at most 50 characters): NetBox rejects anything else on the
	// CustomField, and a name rejected at bootstrap would fail the endpoint rather than
	// the object. The empty string switches one field off, which is why the pattern
	// permits it.
	// +kubebuilder:default="k8s_uid"
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^[a-z0-9_]*$`
	// +optional
	UIDField string `json:"uidField,omitempty"`

	// ClusterField is the custom field holding ClusterID. It is what makes a multi-writer
	// conflict visible (NBO-047): two clusters managing one object differ here and nowhere
	// else, because the tag is the same on both.
	// +kubebuilder:default="k8s_cluster"
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^[a-z0-9_]*$`
	// +optional
	ClusterField string `json:"clusterField,omitempty"`

	// OwnerField is the custom field holding the owning CR as
	// `<lowercased kind>/<namespace>/<name>`.
	//
	// The same spelling as the netbox.kubeforge.org/generated-by annotation in
	// docs/decisions/0005-gitops-coexistence.md section 2, so one string identifies a CR on
	// both sides. It is the human-readable half of the pair: a UID answers "is this the
	// same object", this answers "which manifest do I go and edit".
	// +kubebuilder:default="k8s_owner"
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^[a-z0-9_]*$`
	// +optional
	OwnerField string `json:"ownerField,omitempty"`

	// AllocationIdentityField is the custom field that holds a claim's deterministic
	// allocation identity (docs/decisions/0005-gitops-coexistence.md section 3).
	//
	// Bootstrapped here and written by nothing yet: the identity is computed by the
	// allocation engine (NBO-036), and the definition has to exist before it can write
	// one, because NetBox answers a `custom_fields` key it has no CustomField for with a
	// 400. Set it to "" to leave the definition uncreated on an endpoint that will never
	// serve a claim.
	// +kubebuilder:default="k8s_allocation_identity"
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^[a-z0-9_]*$`
	// +optional
	AllocationIdentityField string `json:"allocationIdentityField,omitempty"`

	// Bootstrap permits the operator to create the tag and the custom-field definitions
	// above when they are absent. Defaults to true, and is the field to set to false.
	//
	// Creating a CustomField is a schema change to somebody else's NetBox, so it is
	// opt-out rather than unconditional: with it false the operator only ever looks the
	// definitions up, and reports exactly which ones a human has to create through
	// ProvenanceReady=False/BootstrapDisabled.
	//
	// A pointer so that `bootstrap: false` survives a round trip: a plain bool with
	// omitempty marshals a deliberate false to nothing, which the CRD default then reads
	// back as true.
	// +kubebuilder:default=true
	// +optional
	Bootstrap *bool `json:"bootstrap,omitempty"`
}

// RateLimit caps how hard the operator hits one NetBox.
type RateLimit struct {
	// QPS is sustained requests per second. Zero means unlimited.
	// +kubebuilder:validation:Minimum=0
	// +optional
	QPS int32 `json:"qps,omitempty"`

	// Burst is the bucket size. Defaults to QPS.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Burst int32 `json:"burst,omitempty"`
}

// NetBoxEndpointSpec describes one NetBox instance.
type NetBoxEndpointSpec struct {
	// URL of the NetBox instance, with or without a trailing /api. A base URL and nothing
	// else: scheme, host, optional port, optional path prefix. A query string, a fragment
	// and userinfo are each rejected -- the operator appends the REST path to this value,
	// so anything after it is not part of a NetBox base URL and changes what gets
	// requested.
	//
	// These are layer 1 -- CEL on the CRD, enforced by the API server unconditionally --
	// because each is a property of `self` alone, which is the line
	// docs/operations/admission-webhooks.md draws. netbox.New's checkBaseURL is the
	// reconcile-time backstop and carries the full reasoning.
	//
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:XValidation:rule="self.matches('^https?://[^/]')",message="url must name a host"
	// +kubebuilder:validation:XValidation:rule="!self.contains('?')",message="url must not carry a query string: the operator appends /api and the rest of the REST path to it, so a query would absorb that suffix and choose the request path itself"
	// +kubebuilder:validation:XValidation:rule="!self.contains('#')",message="url must not carry a fragment: a fragment is never sent to a server, so it can only mislead whoever reads it"
	// +kubebuilder:validation:XValidation:rule="!self.contains('@')",message="url must not carry userinfo: the credential belongs in the Secret named by tokenSecretRef, not in a spec field that everyone who can read the CR can read"
	URL string `json:"url"`

	// TokenSecretRef names the Secret holding the API token. That Secret must be
	// labelled netbox.kubeforge.org/endpoint-credential: "true" or the operator cannot
	// read it; see docs/operations/rbac.md.
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`

	// TLSConfig tunes the TLS handshake.
	// +optional
	TLSConfig *TLSConfig `json:"tlsConfig,omitempty"`

	// Timeout for a single request. Defaults to 30s.
	// +kubebuilder:default="30s"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// Mode is Apply or DryRun.
	// +kubebuilder:default=Apply
	// +optional
	Mode EndpointMode `json:"mode,omitempty"`

	// DriftMode is what to do about a NetBox object that no longer matches its CR:
	// Correct, Report or Off.
	// +kubebuilder:default=Correct
	// +optional
	DriftMode DriftMode `json:"driftMode,omitempty"`

	// ResyncPeriod re-checks NetBox for drift even when no CR changed. Ignored when
	// driftMode is Off, which is what "no periodic resync" means.
	// +kubebuilder:default="10m"
	// +optional
	ResyncPeriod metav1.Duration `json:"resyncPeriod,omitempty"`

	// RateLimit caps client-side request rate.
	// +optional
	RateLimit *RateLimit `json:"rateLimit,omitempty"`

	// ManagedBy configures the provenance stamp written onto every object managed through
	// this endpoint. Unset means the operator stamps nothing, because the cluster
	// identifier it would have to write is not something it may invent.
	// +optional
	ManagedBy *ManagedBy `json:"managedBy,omitempty"`
}

// NetBoxEndpointStatus reports what the operator found.
type NetBoxEndpointStatus struct {
	// NetBoxVersion is the version string reported by GET /api/status/.
	// +optional
	NetBoxVersion string `json:"netboxVersion,omitempty"`

	// Plugins are the NetBox plugins the server reports. Recorded because a plugin that
	// adds a required custom field is an otherwise baffling source of 400s.
	// +optional
	Plugins []string `json:"plugins,omitempty"`

	// ObservedGeneration is the spec generation this status refers to.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ManagedBy is the provenance bootstrap as it stands in NetBox: the tag's resolved id
	// and the custom-field definitions that exist. Unset while spec.managedBy is.
	// +optional
	ManagedBy *ManagedByStatus `json:"managedBy,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ManagedByStatus is what the provenance bootstrap found or created.
//
// The tag id is why this is a status field and not only a condition message: an object is
// tagged by id, so the id is the one part of the stamp a reader cannot derive from the
// spec, and NetBoxSweep (NBO-046) needs it to build a filter.
type ManagedByStatus struct {
	// ClusterID is spec.managedBy.clusterID, echoed so the whole stamp reads in one place.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`

	// Tag is the tag's slug in NetBox.
	// +optional
	Tag string `json:"tag,omitempty"`

	// TagID is the tag's NetBox id, which is what an object write actually carries.
	// +optional
	TagID int64 `json:"tagID,omitempty"`

	// CustomFields are the custom-field definitions that exist in NetBox, sorted. A name
	// configured but absent from this list is one the operator could not create.
	// +optional
	CustomFields []string `json:"customFields,omitempty"`
}

// NetBoxEndpoint is a connection to one NetBox instance.
//
// Namespaced, so endpointRef resolves in the referring object's own namespace and each
// namespace can point at a different NetBox with no extra machinery. See
// docs/decisions/0002-crd-scoping.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbep;nbendpoint
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Drift",type=string,JSONPath=`.spec.driftMode`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.netboxVersion`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxEndpointSpec   `json:"spec,omitempty"`
	Status NetBoxEndpointStatus `json:"status,omitempty"`
}

// NetBoxEndpointList is a list of NetBoxEndpoint.
// +kubebuilder:object:root=true
type NetBoxEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxEndpoint{}, &NetBoxEndpointList{})
}
