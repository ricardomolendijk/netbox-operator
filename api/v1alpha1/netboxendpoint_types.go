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
// the netbox.populator.io/endpoint-credential label, because the manager's Secret cache
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
	// labelled netbox.populator.io/endpoint-credential: "true" or the operator cannot
	// read it; see docs/operations/rbac.md.
	// +optional
	CABundleSecretRef *SecretKeyRef `json:"caBundleSecretRef,omitempty"`
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
	// URL of the NetBox instance, with or without a trailing /api.
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`

	// TokenSecretRef names the Secret holding the API token. That Secret must be
	// labelled netbox.populator.io/endpoint-credential: "true" or the operator cannot
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

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
