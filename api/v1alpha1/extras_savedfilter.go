package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxSavedFilterSpec describes one extras.SavedFilter: a named set of query parameters
// NetBox offers as a one-click filter in its UI.
//
// Neither taggable nor custom-fieldable: the bases are `CloningMixin, ExportTemplatesMixin,
// OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md -> extras.SavedFilter).
//
// `user` is deliberately absent. It is a `ForeignKey -> settings.AUTH_USER_MODEL` and there
// is no Kind for a NetBox user, so a `userRef` would be a field the resolver could not
// resolve against anything -- and NetBox's own default for an unset `user` is "not owned by
// anybody", which combined with `shared: true` is exactly what a filter declared from Git
// should be.
type NetBoxSavedFilterSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the filter's name as shown in NetBox. Unique across NetBox
	// (docs/netbox-schema.md -> extras.SavedFilter, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the filter's URL-safe identifier, and this kind's first natural-key candidate.
	// Also unique across NetBox (`slug SlugField REQ UNIQUE len=100`).
	//
	// Two independently-unique columns, so this kind has two lookup candidates rather than
	// one, and the second is not dead weight: renaming `slug` in Git while leaving `name`
	// alone finds nothing under the new slug, falls through to `name`, adopts the existing
	// filter and PATCHes the slug -- which is a rename. Without the fallback it would try to
	// create a second filter and NetBox would refuse it on the unique `name`. See
	// internal/registry/extras_savedfilter.go for the order and why.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ObjectTypes are the NetBox models this filter applies to, as Django ContentType
	// strings.
	//
	// Required by NetBox: no `required=False` on the serializer
	// (`netbox/extras/api/serializers_/savedfilters.py:13-16`). Unlike the other
	// `object_types` in this app the queryset is unrestricted -- `ObjectType.objects.all()`
	// -- so any content type NetBox has is accepted here.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	ObjectTypes []string `json:"objectTypes"`

	// Parameters is the filter itself: the NetBox query parameters to apply, as a JSON
	// object.
	//
	//	parameters:
	//	  status: ["active"]
	//	  tenant: ["acme"]
	//
	// Required, and required to be an *object* rather than any JSON value: NetBox's `clean()`
	// rejects anything that is not a dictionary of keyword arguments
	// (`netbox/extras/models/models.py:588-594`). That is expressible in the schema, so it is
	// -- `type: object` here, rather than a 400 a user has to go and read.
	//
	// The values are lists because that is what a query string is: `?status=active&status=
	// reserved` is one parameter with two values, and NetBox's own UI writes them that way.
	// A bare string works too; NetBox normalises it.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Parameters JSONDocument `json:"parameters"`

	// Description is free text shown next to the filter.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Enabled offers the filter in NetBox's UI. Disabling it retires the filter without
	// losing it.
	//
	// A pointer with an explicit default rather than a plain bool: `omitempty` on a plain
	// bool drops a deliberate `false` out of the payload, so the operator could never turn it
	// off again.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Shared makes the filter visible to every NetBox user rather than only to its owner.
	//
	// Defaulted to true, which is NetBox's own default and the only useful value here: a
	// filter declared from Git has no `user`, and an unshared filter with no owner is one
	// nobody can see.
	// +kubebuilder:default=true
	// +optional
	Shared *bool `json:"shared,omitempty"`

	// Weight orders filters in NetBox's list, lowest first
	// (docs/netbox-schema.md -> extras.SavedFilter, `meta.ordering: ('weight', 'name')`).
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Weight *int32 `json:"weight,omitempty"`
}

// NetBoxSavedFilter is one extras.SavedFilter in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Presentation
// rather than data: nothing in NetBox depends on one, so deleting it destroys nothing and
// there is no data-loss guard.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsf
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxSavedFilter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxSavedFilterSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (f *NetBoxSavedFilter) NetBoxSpec() *NetBoxObjectSpec { return &f.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (f *NetBoxSavedFilter) NetBoxStatus() *NetBoxObjectStatus { return &f.Status }

// NetBoxSavedFilterList is a list of NetBoxSavedFilter.
// +kubebuilder:object:root=true
type NetBoxSavedFilterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxSavedFilter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxSavedFilter{}, &NetBoxSavedFilterList{})
}
