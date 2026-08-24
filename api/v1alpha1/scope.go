package v1alpha1

// ScopeRef selects the object a scoped NetBox model is attached to.
//
// "Scope" here is NetBox's, and it has nothing to do with a CRD's Kubernetes scope: every
// kind in v1alpha1 is namespaced (docs/decisions/0002-crd-scoping.md). NetBox 4.2 replaced
// the per-model `site` foreign key on several models with `CachedScopeMixin`'s polymorphic
// `scope_type` / `scope_id` pair plus four read-only denormalised caches -- `_region`,
// `_site_group`, `_site`, `_location` (docs/netbox-schema.md -> dcim.CachedScopeMixin).
// Writing `site` to such a model is not rejected, it is *ignored*, so the object reports
// itself synced while carrying no scope at all. See docs/concepts/scopes.md.
//
// At most one member may be set. None means globally scoped: both columns are written as
// null, because an omitted pair would leave whatever NetBox holds in place and there would
// be no way to clear a scope.
//
// There is deliberately no `siteRef` shortcut on a scoped kind, not even as sugar that
// expands into `scope.siteRef`. A field called `siteRef` on a NetBoxPrefix would read as
// the foreign key NetBox no longer has, and the point of this type is that the operator's
// API cannot express that mistake.
//
// The exclusivity rule is CEL on the type rather than a check in the controller, so
// `kubectl apply` refuses two members instead of the operator discovering it a reconcile
// later. It is `<= 1` rather than `== 1` because neither column carries `REQ`: the `REQ` on
// `CachedScopeMixin`'s `scope` row is an extractor artefact -- a GenericForeignKey is not a
// column and takes no `null=` kwarg -- and a globally-scoped prefix is legal and common.
//
// +kubebuilder:validation:XValidation:rule="[has(self.regionRef), has(self.siteGroupRef), has(self.siteRef), has(self.locationRef)].filter(x, x).size() <= 1",message="at most one of regionRef, siteGroupRef, siteRef or locationRef may be set"
type ScopeRef struct {
	// RegionRef scopes the object to a region, written as `scope_type: dcim.region`.
	// +optional
	RegionRef *RegionRef `json:"regionRef,omitempty"`

	// SiteGroupRef scopes the object to a site group, written as
	// `scope_type: dcim.sitegroup`.
	// +optional
	SiteGroupRef *SiteGroupRef `json:"siteGroupRef,omitempty"`

	// SiteRef scopes the object to a site, written as `scope_type: dcim.site`.
	// +optional
	SiteRef *SiteRef `json:"siteRef,omitempty"`

	// LocationRef scopes the object to a location, written as
	// `scope_type: dcim.location`.
	// +optional
	LocationRef *LocationRef `json:"locationRef,omitempty"`
}

// ScopeMemberFields are the CR spec fields of the scope union, in the order the engine
// considers them.
//
// Exported so that the registry's declaration of the union and this struct cannot drift
// apart silently: a member added here and forgotten there would be a field the API server
// accepts and the resolver never reads, which is the same silent no-op the whole type
// exists to prevent. internal/registry asserts the two agree, and asserts each entry is a
// JSON field of ScopeRef.
//
// The object type each maps to is deliberately *not* here. It is the target Kind's own
// Descriptor.ObjectType, so `dcim.sitegroup` is spelled once in the codebase rather than
// once per place that needs it.
var ScopeMemberFields = []string{"regionRef", "siteGroupRef", "siteRef", "locationRef"}
