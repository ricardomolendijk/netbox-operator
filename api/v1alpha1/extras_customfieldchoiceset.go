package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CustomFieldChoiceSetBase is one of NetBox's predefined choice sets, used instead of -- or
// alongside -- a hand-written list.
//
// The digest records the column as `base_choices CharField len=50
// choices=CustomFieldChoiceSetBaseChoices` (docs/netbox-schema.md ->
// extras.CustomFieldChoiceSet) and, as ever, the choice *class* rather than its members. The
// three values are read from `netbox/extras/choices.py:85-95`,
// `CustomFieldChoiceSetBaseChoices`, in the same 4.6.8 tree the digest was taken from.
//
// The empty value is in the enum because the column is `null=True` and the serializer is
// `ChoiceField(..., required=False, allow_null=True)` with `allow_blank` left at false
// (`netbox/extras/models/customfields.py:935-941`,
// `netbox/extras/api/serializers_/customfields.py:20-24`). So `""` is how this API spells
// "no base set", and the descriptor marks the column EmptyIsNull so it is *sent* as JSON
// null -- the only value NetBox accepts to clear it, since `to_internal_value` rejects the
// empty string outright (`netbox/netbox/api/fields.py:64-68`).
//
// +kubebuilder:validation:Enum="";IATA;ISO_3166;UN_LOCODE
type CustomFieldChoiceSetBase string

const (
	// CustomFieldChoiceSetBaseIATA is the IATA airport-code set.
	CustomFieldChoiceSetBaseIATA CustomFieldChoiceSetBase = "IATA"

	// CustomFieldChoiceSetBaseISO3166 is the ISO 3166 country-code set.
	CustomFieldChoiceSetBaseISO3166 CustomFieldChoiceSetBase = "ISO_3166"

	// CustomFieldChoiceSetBaseUNLOCODE is the UN/LOCODE location-code set.
	CustomFieldChoiceSetBaseUNLOCODE CustomFieldChoiceSetBase = "UN_LOCODE"
)

// CustomFieldChoice is one `[value, label]` pair of a choice set.
//
// A two-element list rather than a struct with `value` and `label` fields, because that is
// what the column is and what the API takes: `extra_choices` is a `ChoiceSetField`, which is
// "an ArrayField of two-element [value, label] string pairs"
// (`netbox/extras/fields.py:17-23`), and the serializer declares it as
// `ListField(child=ListField(min_length=2, max_length=2))`
// (`netbox/extras/api/serializers_/customfields.py:25-29`). A friendlier struct would have to
// be flattened on the way out and unflattened on the way back, and NetBox ignores a shape it
// does not recognise rather than rejecting it -- so the friendly version would write nothing
// and report success.
//
// This is the field the spec warned might be named something else over REST. It is not:
// `extra_choices` is the AST name *and* the serializer's name, and the bound is on the inner
// list rather than the outer one.
//
// +kubebuilder:validation:MinItems=2
// +kubebuilder:validation:MaxItems=2
// +kubebuilder:validation:items:MaxLength=100
type CustomFieldChoice []string

// NetBoxCustomFieldChoiceSetSpec describes one extras.CustomFieldChoiceSet.
//
// Neither taggable nor custom-fieldable: its bases are `CloningMixin,
// ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md ->
// extras.CustomFieldChoiceSet), with no TagsMixin and no CustomFieldsMixin. So a
// NetBoxCustomFieldChoiceSet is a managed object that carries no provenance stamp at all,
// which is the case docs/operations/provenance.md calls out and NetBoxSweep (NBO-046) must
// never delete.
//
// One thing NetBox enforces that this schema deliberately does not: `clean()` refuses a
// choice set with neither a base set nor extra choices
// (`netbox/extras/models/customfields.py:1018-1020`). It is expressible as a CEL rule and it
// is left to NetBox anyway, because NetBox also refuses a duplicate value inside
// `extraChoices` and a colour keyed on a value no choice declares, and a schema that
// enforces one of three related rules invites the reader to believe it enforces all of them.
// The 400 arrives as `Ready=False, Reason=Invalid` carrying NetBox's own sentence.
type NetBoxCustomFieldChoiceSetSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the choice set's name, and this kind's natural key. Unique across NetBox
	// (docs/netbox-schema.md -> extras.CustomFieldChoiceSet, `name CharField REQ UNIQUE
	// len=100`).
	//
	// NetBox enforces uniqueness on it globally while this CRD is namespaced
	// (docs/decisions/0002-crd-scoping.md), so two NetBoxCustomFieldChoiceSets in different
	// namespaces claiming one name is one choice set and a Conflict -- not two.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Description is free text shown next to the choice set.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// BaseChoices selects one of NetBox's predefined sets, whose members are prepended to
	// ExtraChoices.
	//
	// `""` clears it, and is sent as JSON null -- see CustomFieldChoiceSetBase.
	// +optional
	BaseChoices CustomFieldChoiceSetBase `json:"baseChoices,omitempty"`

	// ExtraChoices are the set's own `[value, label]` pairs.
	//
	// The order is data rather than incidental, so the descriptor compares this as an
	// ordered array and not as a set: NetBox concatenates the base set and these in the
	// order given, and only re-sorts at read time when OrderAlphabetically is set
	// (`netbox/extras/models/customfields.py:974-987`, the `choices` property). Comparing it
	// order-independently would silently ignore a reordering the user asked for.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md) -- though NetBox refuses `[]` unless BaseChoices is
	// set, since a choice set with no choices is not a choice set.
	// +kubebuilder:validation:MaxItems=1024
	// +optional
	ExtraChoices []CustomFieldChoice `json:"extraChoices,omitempty"`

	// ChoiceColors maps a choice value to the colour NetBox renders it in, as a JSON
	// object: `{"active": "green", "retired": "red"}`.
	//
	// A JSON document rather than a `map[string]string`, because the column is a JSONField
	// and the operator has to be able to write whatever NetBox will accept there
	// (docs/netbox-schema.md -> extras.CustomFieldChoiceSet, `choice_colors JSONField
	// def=UNRESOLVED:dict`). The legal values are NetBox's own colour names
	// (`netbox/extras/choices.py:98-131`, `CustomFieldChoiceColorChoices`) and NetBox
	// validates them, along with the rule that every key must be a value some choice
	// declares (`netbox/extras/models/customfields.py:1042-1060`).
	//
	// Omit it to leave NetBox's own value alone; set it to `{}` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	ChoiceColors *JSONDocument `json:"choiceColors,omitempty"`

	// OrderAlphabetically sorts the combined choice list by value when NetBox reads it back
	// (`netbox/extras/models/customfields.py:985-986`).
	//
	// A pointer with an explicit default rather than a plain bool, for the same reason
	// NetBoxTag.weight is one: `omitempty` on a plain bool drops a deliberate `false` out of
	// the payload, so the operator could never turn the flag back off.
	// +kubebuilder:default=false
	// +optional
	OrderAlphabetically *bool `json:"orderAlphabetically,omitempty"`
}

// NetBoxCustomFieldChoiceSet is one extras.CustomFieldChoiceSet in NetBox: the list of
// values a `select` or `multiselect` custom field may hold.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Like
// NetBoxCustomField it is schema rather than data, so the convention is that it lives in the
// same shared namespace the NetBoxCustomFields that point at it do.
//
// Deleting one is safe in a way deleting a NetBoxCustomField is not, and the difference is
// worth naming: `CustomField.choice_set` is `on_delete=PROTECT`
// (`netbox/extras/models/customfields.py:236-243`), so NetBox refuses to delete a choice set
// any custom field still uses. That refusal arrives as `Deleting=False, Reason=Protected`
// and clears itself when the last custom field using it goes -- which is why this kind
// carries no data-loss guard and NetBoxCustomField does.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcfcs
// +kubebuilder:printcolumn:name="Base",type=string,JSONPath=`.spec.baseChoices`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCustomFieldChoiceSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCustomFieldChoiceSetSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus             `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCustomFieldChoiceSet) NetBoxSpec() *NetBoxObjectSpec {
	return &c.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCustomFieldChoiceSet) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxCustomFieldChoiceSetList is a list of NetBoxCustomFieldChoiceSet.
// +kubebuilder:object:root=true
type NetBoxCustomFieldChoiceSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCustomFieldChoiceSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCustomFieldChoiceSet{}, &NetBoxCustomFieldChoiceSetList{})
}
