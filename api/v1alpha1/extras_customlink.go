package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CustomLinkButtonClass is the colour NetBox renders a custom link's button in.
//
// The digest records `button_class CharField len=30
// def=UNRESOLVED:CustomLinkButtonClassChoices.DEFAULT choices=CustomLinkButtonClassChoices`
// (docs/netbox-schema.md -> extras.CustomLink). The class inherits from
// `ButtonColorChoices` and adds one member of its own, so the values come from two files:
// thirteen from `netbox/netbox/choices.py:85-117`, `ButtonColorChoices`, plus `ghost-dark`
// from `netbox/extras/choices.py:135-142`, `CustomLinkButtonClassChoices.LINK`. `grey` is
// deliberately absent -- it is a Python alias for `gray` on the same value
// (`netbox/netbox/choices.py:98`) and not a distinct choice.
//
// +kubebuilder:validation:Enum=default;blue;indigo;purple;pink;red;orange;yellow;green;teal;cyan;gray;black;white;ghost-dark
type CustomLinkButtonClass string

// NetBoxCustomLinkSpec describes one extras.CustomLink: a button NetBox renders on an
// object's page, whose text and URL are Jinja2 templates rendered with that object as
// context.
//
// Neither taggable nor custom-fieldable: the bases are `CloningMixin, ExportTemplatesMixin,
// OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md -> extras.CustomLink).
type NetBoxCustomLinkSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the link's name, and this kind's natural key. Unique across NetBox
	// (docs/netbox-schema.md -> extras.CustomLink, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// ObjectTypes are the NetBox models this link appears on, as Django ContentType
	// strings.
	//
	// Required by NetBox: `object_types ManyToManyField -> contenttypes.ContentType` with no
	// `required=False` on the serializer
	// (`netbox/extras/api/serializers_/customlinks.py:13-16`). Not references -- the values
	// are `app_label.model` strings, so the descriptor declares this an ObjectTypeList and
	// the resolver never sees it.
	//
	// Not checked against this operator's kind registry, deliberately: NetBox's own
	// `ContentTypeField` is scoped to `ObjectType.objects.with_feature('custom_links')`, so a
	// type that does not exist and a type that cannot carry links both come back as
	// `Invalid content type` and arrive as `Reason=Invalid`. A registry check would reject a
	// link on `dcim.device` in a cluster whose operator cannot manage devices, which is a
	// perfectly reasonable thing to want.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	ObjectTypes []string `json:"objectTypes"`

	// LinkText is Jinja2 template code for the button's label, rendered with the object as
	// context: `Open {{ obj.name }} in Grafana`.
	//
	// Required, and required by NetBox: `link_text TextField REQ` with no `blank=True`
	// (docs/netbox-schema.md -> extras.CustomLink). A template that renders to the empty
	// string hides the button, which is NetBox's documented way to make a link conditional.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=10000
	LinkText string `json:"linkText"`

	// LinkURL is Jinja2 template code for the button's target.
	//
	// Required for the same reason LinkText is. Not validated as a URL here: it is a
	// template, so before rendering it is frequently not one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=10000
	LinkURL string `json:"linkUrl"`

	// GroupName collapses links sharing it into one dropdown menu.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	GroupName string `json:"groupName,omitempty"`

	// ButtonClass is the button's colour. The first link in a group decides the dropdown's.
	// +kubebuilder:default=default
	// +optional
	ButtonClass CustomLinkButtonClass `json:"buttonClass,omitempty"`

	// Enabled shows the link. Disabling it is how a link is retired without losing it.
	//
	// A pointer with an explicit default rather than a plain bool: `omitempty` on a plain
	// bool drops a deliberate `false` out of the payload, so the operator could never turn
	// the link off again.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// NewWindow opens the link in a new browser window.
	// +kubebuilder:default=false
	// +optional
	NewWindow *bool `json:"newWindow,omitempty"`

	// Weight orders links within a group, lowest first
	// (docs/netbox-schema.md -> extras.CustomLink, `meta.ordering: ['group_name', 'weight',
	// 'name']`).
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Weight *int32 `json:"weight,omitempty"`
}

// NetBoxCustomLink is one extras.CustomLink in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Presentation
// rather than data, so nothing else in NetBox depends on one and deleting it destroys
// nothing: no data-loss guard, and `deletionPolicy` stays at its `Delete` default.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcl
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCustomLink struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCustomLinkSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCustomLink) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCustomLink) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxCustomLinkList is a list of NetBoxCustomLink.
// +kubebuilder:object:root=true
type NetBoxCustomLinkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCustomLink `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCustomLink{}, &NetBoxCustomLinkList{})
}
