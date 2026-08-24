package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterStatus is one value of NetBox's ClusterStatusChoices.
//
// docs/netbox-schema.md -> virtualization.Cluster records the column as
// `status CharField len=50 def=UNRESOLVED:ClusterStatusChoices.STATUS_ACTIVE
// choices=ClusterStatusChoices` -- the choice *class*, not its members, because the AST walk
// cannot evaluate one (NBO-041 ingests NetBox's OpenAPI schema precisely to settle them).
// These five are read from `netbox/virtualization/choices.py`, `ClusterStatusChoices`, in the
// v4.6.8 tree the digest was taken from.
//
// Not the same set as SiteStatus or LocationStatus, and not the same Go type either: a cluster
// is `offline` where a site is `retired`, and there is no `staged`. Sharing an enum across
// two NetBox ChoiceSets would make a value added to one of them through `FIELD_CHOICES`
// silently legal on the other.
//
// +kubebuilder:validation:Enum=planned;staging;active;decommissioning;offline
type ClusterStatus string

const (
	// ClusterStatusPlanned is a cluster that does not exist yet.
	ClusterStatusPlanned ClusterStatus = "planned"

	// ClusterStatusStaging is a cluster being built out.
	ClusterStatusStaging ClusterStatus = "staging"

	// ClusterStatusActive is a cluster in service, and NetBox's own default.
	ClusterStatusActive ClusterStatus = "active"

	// ClusterStatusDecommissioning is a cluster being retired.
	ClusterStatusDecommissioning ClusterStatus = "decommissioning"

	// ClusterStatusOffline is a cluster that exists but is not running.
	ClusterStatusOffline ClusterStatus = "offline"
)

// NetBoxClusterSpec describes one virtualization.Cluster.
//
// Cluster is scoped via CachedScopeMixin since NetBox 4.2; `site` is a read-only cached
// column (_site) and writing it silently no-ops. netbox-populator writes it anyway --
// ../reconcile.go:270. See docs/netbox-schema.md.
//
// That is not a historical note, it is why this type has the shape it has. There is no
// `siteRef` field, not even as sugar expanding into `scope.siteRef`: NetBox's
// ClusterSerializer has no `site` member at all, so DRF drops the key, returns 201, and the
// operator would report an unscoped cluster as synced forever. `scope` is the only way to
// attach a cluster to anything, and the pair `(scope_type, scope_id)` is written and diffed
// as a unit (docs/concepts/generic-refs.md#the-scope-pair).
//
// `bases: ContactsMixin, CachedScopeMixin, PrimaryModel` (docs/netbox-schema.md ->
// virtualization.Cluster). `contacts` and `vlan_groups` are GenericRelations -- reverse
// relations rather than columns -- and get no fields here.
type NetBoxClusterSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the cluster's name -- `proxmox-ams`, `vsphere-prod`.
	//
	// **Not globally unique.** `virtualization.Cluster.meta.constraints` is
	// `(('group','name'), ('_site','name'))`: two separate constraints rather than one
	// composite, and both partial in practice. A region-scoped cluster has `_site = NULL`,
	// so the second constrains nothing and two region-scoped clusters named `proxmox` are a
	// legal NetBox state -- which is why a name-only lookup that matches twice is reported as
	// a Conflict with no write rather than resolved by taking the first row.
	//
	// The mirror image is the failure mode worth knowing about: two clusters with this name
	// in the same site collide even when their groups differ, because the group constraint is
	// a *separate* constraint. NetBox answers with a 400 and the operator surfaces it as
	// `Ready=False, Reason=Invalid` carrying NetBox's own field error, with a long backoff --
	// retrying an invalid payload quickly is pointless.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// TypeRef is the cluster's technology -- the NetBoxClusterType for vSphere, Proxmox,
	// Hyper-V. Required, because NetBox's column is (`type ForeignKey REQ ->
	// virtualization.ClusterType on_delete=PROTECT`) and there is no such thing as a cluster
	// without a type.
	//
	// Not a containment reference, and the `PROTECT` is the argument: deleting a cluster type
	// must not delete the clusters using it, and NetBox would refuse the delete anyway. So
	// this reference contributes no owner reference
	// (docs/decisions/0003-ownership-and-references.md rule 4) and a
	// `kubectl delete netboxclustertype` reports `Deleting/Protected` naming the cluster.
	//
	// Required by the API server, but a *reference*, so "required" means the field is set
	// rather than resolved: until it resolves the object reports RefsResolved=False naming it
	// and `type` is left out of the payload rather than sent as null. Pointing at a shared
	// catalogue namespace needs a NetBoxRefGrant there (NBO-014).
	TypeRef ClusterTypeRef `json:"typeRef"`

	// GroupRef puts the cluster in a NetBoxClusterGroup
	// (`group ForeignKey -> virtualization.ClusterGroup on_delete=PROTECT`).
	//
	// Part of this kind's identity: `(group, name)` is the first natural-key candidate, so
	// setting this field is what makes a cluster's lookup unambiguous. Leaving it unset is a
	// groupless cluster, which is a different identity rather than the same one with a field
	// missing -- the fallback candidate pins `group_id__isnull=true` rather than omitting the
	// filter (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
	//
	// `PROTECT` again, so no owner reference here either.
	// +optional
	GroupRef *ClusterGroupRef `json:"groupRef,omitempty"`

	// Scope attaches the cluster to a Region, SiteGroup, Site or Location.
	//
	// The field that replaces the `site` NetBox 4.2 removed. Omit it for an unscoped cluster,
	// which is a normal state rather than a missing value: neither `scope_type` nor `scope_id`
	// carries a real `REQ` (docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest).
	// An empty `scope: {}` clears the scope by writing both columns as null; an absent one
	// leaves whatever NetBox holds alone.
	//
	// Written as the `(scope_type, scope_id)` pair and diffed as a unit, so moving a cluster
	// from a Region to a Site is one change and one PATCH carrying both keys. The cached
	// `_site`, `_region`, `_site_group` and `_location` columns NetBox maintains from the pair
	// are never written, and never compared.
	//
	// This is also the kind's one containment reference
	// (docs/decisions/0003-ownership-and-references.md rule 4): a cluster scoped to a
	// NetBoxSite in the *same* namespace takes a non-controller owner reference on it, so
	// `kubectl delete netboxsite` cascades -- which matches NetBox's own
	// `_site on_delete=CASCADE`. A cross-namespace scope takes no owner reference and says so
	// with a CascadeUnavailable condition naming the namespace, because Kubernetes garbage
	// collection does not cross namespaces.
	// +optional
	Scope *ScopeRef `json:"scope,omitempty"`

	// TenantRef is the tenant the cluster belongs to
	// (`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// An attribute rather than a container: a cluster outliving its tenant is a normal state,
	// so this reference contributes no owner reference either. It is not part of the identity
	// -- neither NetBox constraint mentions `tenant` -- so it never appears in a lookup.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Status is the cluster's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status ClusterStatus `json:"status,omitempty"`

	// Description is free text shown next to the cluster. Declared on PrimaryModel rather
	// than on virtualization.Cluster (docs/netbox-schema.md -> virtualization.Cluster,
	// `description (PrimaryModel) CharField len=200`); an inherited column is as writable as
	// a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the cluster's long-form notes field. Also inherited from PrimaryModel, and
	// a TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxCluster is one virtualization.Cluster in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// No inline children, now or later. plan.md §7 lists Cluster among the kinds that get inline
// expansion but names no lists, and a cluster's plausible children -- Devices and
// VirtualMachines -- have independent lifecycles and are not components of it. Materialising
// them would make this the composite topology kind plan.md §2 principle 1 forbids.
//
// The SITE printer column reads the *intent* (`.spec.scope.siteRef.name`) rather than
// `status`, which is deliberately a narrower promise than NetBoxPrefix's SCOPE column: a
// cluster scoped to a Region or a Location shows nothing there, and the answer to "is this
// cluster scoped in NetBox at all" is `kubectl get -o yaml` or NetBox itself. A column that
// silently means "site, and only site" is better than one that means "the first member of the
// union that happens to be set".
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcluster
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.typeRef.name`
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.scope.siteRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxClusterSpec  `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCluster) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCluster) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxClusterList is a list of NetBoxCluster.
// +kubebuilder:object:root=true
type NetBoxClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCluster{}, &NetBoxClusterList{})
}
