package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CustomFieldType is the kind of data a custom field holds.
//
// The digest records `type CharField len=50
// def=UNRESOLVED:CustomFieldTypeChoices.TYPE_TEXT choices=CustomFieldTypeChoices`
// (docs/netbox-schema.md -> extras.CustomField) -- the choice *class* and a `def=` the AST
// walk could not evaluate. The thirteen values and the default are read from
// `netbox/extras/choices.py:13-43`, `CustomFieldTypeChoices`, in the same 4.6.8 tree the
// digest was taken from.
//
// +kubebuilder:validation:Enum=text;longtext;integer;decimal;boolean;date;datetime;url;json;select;multiselect;object;multiobject
type CustomFieldType string

const (
	// CustomFieldTypeText is a single-line string, and NetBox's own default.
	CustomFieldTypeText CustomFieldType = "text"

	// CustomFieldTypeLongText is a multi-line string.
	CustomFieldTypeLongText CustomFieldType = "longtext"

	// CustomFieldTypeInteger is a whole number.
	CustomFieldTypeInteger CustomFieldType = "integer"

	// CustomFieldTypeDecimal is a fractional number.
	CustomFieldTypeDecimal CustomFieldType = "decimal"

	// CustomFieldTypeBoolean is true or false.
	CustomFieldTypeBoolean CustomFieldType = "boolean"

	// CustomFieldTypeDate is a calendar date.
	CustomFieldTypeDate CustomFieldType = "date"

	// CustomFieldTypeDateTime is a date and a time.
	CustomFieldTypeDateTime CustomFieldType = "datetime"

	// CustomFieldTypeURL is a URL, which NetBox renders as a link.
	CustomFieldTypeURL CustomFieldType = "url"

	// CustomFieldTypeJSON is an arbitrary JSON document.
	CustomFieldTypeJSON CustomFieldType = "json"

	// CustomFieldTypeSelect is one value from a choice set, which ChoiceSetRef must name.
	CustomFieldTypeSelect CustomFieldType = "select"

	// CustomFieldTypeMultiSelect is several values from a choice set.
	CustomFieldTypeMultiSelect CustomFieldType = "multiselect"

	// CustomFieldTypeObject is a reference to one NetBox object of RelatedObjectType.
	CustomFieldTypeObject CustomFieldType = "object"

	// CustomFieldTypeMultiObject is a reference to several NetBox objects of
	// RelatedObjectType.
	CustomFieldTypeMultiObject CustomFieldType = "multiobject"
)

// CustomFieldFilterLogic is how `?cf_<name>=<value>` matches in NetBox.
//
// Values from `netbox/extras/choices.py:46-56`, `CustomFieldFilterLogicChoices`; the default
// is `loose` (`netbox/extras/models/customfields.py:178-183`).
//
// Worth knowing which one you want: the operator's own provenance definitions are created
// `exact`, deliberately, because every one of them is an identity and a substring answer to
// "which object *is* this one" is a different object (internal/provenance/bootstrap.go). This
// field defaults to NetBox's `loose` rather than to that, because a user's custom field is
// not necessarily an identity and the CRD's job is to default the way NetBox does.
//
// +kubebuilder:validation:Enum=disabled;loose;exact
type CustomFieldFilterLogic string

const (
	// CustomFieldFilterLogicDisabled excludes the field from filtering entirely.
	CustomFieldFilterLogicDisabled CustomFieldFilterLogic = "disabled"

	// CustomFieldFilterLogicLoose matches any instance of the given string, and is
	// NetBox's own default.
	CustomFieldFilterLogicLoose CustomFieldFilterLogic = "loose"

	// CustomFieldFilterLogicExact matches the whole field.
	CustomFieldFilterLogicExact CustomFieldFilterLogic = "exact"
)

// CustomFieldUIVisible is whether NetBox shows the field in its UI.
//
// Values from `netbox/extras/choices.py:59-69`, `CustomFieldUIVisibleChoices`; the default is
// `always` (`netbox/extras/models/customfields.py:243-249`).
//
// +kubebuilder:validation:Enum=always;if-set;hidden
type CustomFieldUIVisible string

const (
	// CustomFieldUIVisibleAlways always shows the field, and is NetBox's own default.
	CustomFieldUIVisibleAlways CustomFieldUIVisible = "always"

	// CustomFieldUIVisibleIfSet shows the field only on objects that have a value for it.
	CustomFieldUIVisibleIfSet CustomFieldUIVisible = "if-set"

	// CustomFieldUIVisibleHidden never shows the field. Its value is still readable over
	// the API, so this is presentation rather than access control.
	CustomFieldUIVisibleHidden CustomFieldUIVisible = "hidden"
)

// CustomFieldUIEditable is whether NetBox lets the field be edited in its UI.
//
// Values from `netbox/extras/choices.py:72-82`, `CustomFieldUIEditableChoices`; the default
// is `yes` (`netbox/extras/models/customfields.py:250-256`).
//
// `no` is the setting for a field a program owns, and it is the one to reach for on a field
// this operator writes: it stops somebody correcting a value in the UI that the next
// reconcile puts straight back.
//
// +kubebuilder:validation:Enum=yes;no;hidden
type CustomFieldUIEditable string

const (
	// CustomFieldUIEditableYes lets the field be edited, and is NetBox's own default.
	CustomFieldUIEditableYes CustomFieldUIEditable = "yes"

	// CustomFieldUIEditableNo shows the field read-only.
	CustomFieldUIEditableNo CustomFieldUIEditable = "no"

	// CustomFieldUIEditableHidden hides the field from edit forms altogether.
	CustomFieldUIEditableHidden CustomFieldUIEditable = "hidden"
)

// NetBoxCustomFieldSpec describes one extras.CustomField: a column added to NetBox's own
// schema, not a row of data.
//
// **The one kind where the operator is already a writer.** The provenance bootstrap creates
// `k8s_uid`, `k8s_cluster`, `k8s_owner` and `k8s_allocation_identity` -- or whatever
// `spec.managedBy` on the endpoint renamed them to -- before that endpoint reports Ready, and
// keeps their `object_types` in step with the kinds this build carries
// (internal/provenance/bootstrap.go, docs/operations/provenance.md). A CR naming one of those
// is therefore **refused**: `Ready=False, Reason=ReservedByOperator`, and nothing at all is
// written. See docs/custom-fields.md for the whole argument; the short version is that
// `object_types` on `k8s_uid` is derived from the descriptor registry and widens on every
// upgrade, a CR declares it statically, and the CR would win -- and narrowing `object_types`
// deletes that field's stored value from every object of the types removed
// (`netbox/extras/signals.py:23-49`, `handle_cf_object_types_changed` on `post_remove`).
//
// Which names are reserved is per endpoint rather than a fixed list, because the names are
// configurable: an endpoint with no `spec.managedBy` reserves nothing, and one that set
// `uidField: ""` frees `k8s_uid`.
//
// Neither taggable nor custom-fieldable: the bases are `CloningMixin, ExportTemplatesMixin,
// OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md -> extras.CustomField), with no
// TagsMixin and no CustomFieldsMixin -- a custom field cannot carry custom fields. So a
// NetBoxCustomField is a managed object with no provenance stamp, which is the case
// docs/operations/provenance.md calls out and NetBoxSweep (NBO-046) must never delete.
type NetBoxCustomFieldSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the field's internal name -- the key `spec.customFields` on every other kind
	// writes -- and this kind's natural key. Unique across NetBox.
	//
	// The pattern and the length are NetBox's own: `CharField len=50 unique=True` with a
	// `^[a-z0-9_]+$` validator applied case-insensitively and a second validator forbidding
	// a double underscore (`netbox/extras/models/customfields.py:120-138`). The double
	// underscore is not decoration: NetBox's own filters are spelled `?cf_<name>__ic=`, so a
	// name containing `__` would be an unparseable filter.
	//
	// Uppercase is legal in NetBox and rejected here, which is the one place this schema is
	// stricter than the API. A custom field's name is a JSON key written by hand in every
	// manifest that populates it, and `k8s_Uid` versus `k8s_uid` is a 400 on every object
	// rather than a lookup that finds the wrong one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=50
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+(_[a-z0-9]+)*$`
	Name string `json:"name"`

	// ObjectTypes are the NetBox models this field applies to, as Django ContentType
	// strings: `dcim.device`, `ipam.prefix`.
	//
	// Required, and required by NetBox rather than by preference:
	// `object_types ManyToManyField -> contenttypes.ContentType` with no `required=False` on
	// the serializer (docs/netbox-schema.md -> extras.CustomField,
	// `netbox/extras/api/serializers_/customfields.py:45-48`). A field declared for the wrong
	// set makes every write to a type outside it a 400.
	//
	// Not references of any cardinality, so the descriptor declares this an ObjectTypeList
	// rather than an M2M: the values are `app_label.model` strings, and a resolver told to
	// resolve one would go looking for a CR named `dcim.device`, which cannot exist. The
	// item pattern is the REST spelling -- lowercased and unpunctuated, so `dcim.device` and
	// never `dcim.Device`.
	//
	// **Not validated against this operator's kind registry, on purpose.** A NetBox holds
	// content types for models this build has no Kind for, and a custom field on
	// `dcim.device` is perfectly reasonable in a cluster whose operator cannot yet manage
	// devices -- so a registry check would reject the useful case in order to catch a typo.
	// NetBox catches the typo instead, and catches more of them: its `ContentTypeField` is
	// scoped to `ObjectType.objects.with_feature('custom_fields')`, so a type that does not
	// exist *and* a type that exists without supporting custom fields both come back as
	// `Invalid content type: <what you wrote>`
	// (`netbox/netbox/api/fields.py:102-122`). That arrives as `Ready=False,
	// Reason=Invalid` carrying NetBox's own sentence.
	//
	// Narrowing this list on an existing field **destroys data**: NetBox strips the field's
	// stored value from every object of every type removed
	// (`netbox/extras/signals.py:23-49`). Nothing guards that -- the guard is on deletion
	// only -- so treat an edit here the way you would treat a `DROP COLUMN`.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	ObjectTypes []string `json:"objectTypes"`

	// Type is the kind of data the field holds.
	//
	// **Immutable, because NetBox refuses to change it**: the serializer rejects a PATCH
	// whose `type` differs from the stored one with "Changing the type of custom fields is
	// not supported" (`netbox/extras/api/serializers_/customfields.py:75-79`). Without the
	// CEL rule below, editing it in Git would be a 400 on every reconcile forever, reported
	// as `Reason=Invalid` and fixable only by putting the old value back -- so the API server
	// rejects the edit at `kubectl apply`, where the message can say what to do instead.
	//
	// Recreating the CR is not a way round it either: the natural key is the name, so a fresh
	// CR adopts the same NetBox object. Changing a field's type means deleting the NetBox
	// custom field, which destroys its values -- see AllowDataLossAnnotation.
	//
	// Defaulted to NetBox's own default so the operator manages the column from the first
	// reconcile, and because NetBox's serializer requires it on create
	// (`netbox/extras/api/serializers_/customfields.py:49`).
	// +kubebuilder:default=text
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable: NetBox refuses to change the type of an existing custom field"
	// +optional
	Type CustomFieldType `json:"type,omitempty"`

	// RelatedObjectType is the NetBox model an `object` or `multiobject` field points at, as
	// one `app_label.model` string.
	//
	// Singular and a plain value, not an ObjectTypeList: the column is
	// `related_object_type ForeignKey -> contenttypes.ContentType on_delete=PROTECT`
	// (docs/netbox-schema.md -> extras.CustomField), so it holds one content type rather
	// than a set of them. It still travels as a string rather than an id, because
	// `ContentTypeField` renders a ContentType as `app_label.model` in both directions
	// (`netbox/netbox/api/fields.py:102-122`) -- which is why this is not a reference either.
	//
	// The pattern admits `""` so the column can be cleared, and the descriptor marks it
	// EmptyIsNull because that is the only value NetBox accepts to clear it: the serializer
	// is `required=False, allow_null=True` with no `allow_blank`
	// (`netbox/extras/api/serializers_/customfields.py:50-54`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^([a-z_]+\.[a-z0-9_]+)?$`
	// +optional
	RelatedObjectType string `json:"relatedObjectType,omitempty"`

	// RelatedObjectFilter narrows the objects an `object` field may point at, as a NetBox
	// query-parameter document: `{"status": "active"}`.
	//
	// Omit it to leave NetBox's own value alone; set it to `{}` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	RelatedObjectFilter *JSONDocument `json:"relatedObjectFilter,omitempty"`

	// ChoiceSetRef names the NetBoxCustomFieldChoiceSet a `select` or `multiselect` field
	// draws its values from.
	//
	// `on_delete=PROTECT` (`netbox/extras/models/customfields.py:236-243`), so this reference
	// is not a containment parent and deleting the choice set while a field still uses it is
	// refused by NetBox -- `Deleting=False, Reason=Protected` on the choice set, which clears
	// itself once the last field using it is gone.
	//
	// A strict ObjectRef, like every other reference in this API version. The column is
	// `blank=True, null=True`, so *clearing* it is a state NetBox has and this field cannot
	// express: omitting the reference means "do not manage the column"
	// (docs/concepts/field-ownership.md), and there is no `choiceSetRef: {}` yet. OptionalRef
	// exists for exactly that and no kind uses it (#185); the first kind that needs a
	// clearable reference should be one whose clearable state somebody actually wants, not
	// this one -- a `select` field with its choice set removed is a field NetBox refuses to
	// validate anyway.
	// +optional
	ChoiceSetRef *CustomFieldChoiceSetRef `json:"choiceSetRef,omitempty"`

	// Label is the name NetBox shows users. Empty means NetBox derives one from Name.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Label string `json:"label,omitempty"`

	// GroupName groups this field with others under one heading in NetBox's UI. The
	// operator's own definitions use `Kubernetes` (internal/provenance/bootstrap.go).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	GroupName string `json:"groupName,omitempty"`

	// Description is free text shown next to the field.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is long-form free text on the field itself.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=10000
	// +optional
	Comments string `json:"comments,omitempty"`

	// Required makes the field mandatory when creating or editing an object of any type it
	// applies to.
	//
	// Turning it on is a decision with a blast radius: every writer of every type in
	// ObjectTypes then has to supply the field, this operator included -- and this operator
	// only writes the keys a manifest names, so a required custom field nobody sets is a 400
	// on every object of that type. NetBox does not backfill.
	//
	// A pointer with an explicit default rather than a plain bool, for the same reason
	// NetBoxTag.weight is one: `omitempty` on a plain bool drops a deliberate `false` out of
	// the payload, so the operator could never turn the flag back off.
	// +kubebuilder:default=false
	// +optional
	Required *bool `json:"required,omitempty"`

	// Unique makes the field's value unique across objects of one type.
	// +kubebuilder:default=false
	// +optional
	Unique *bool `json:"unique,omitempty"`

	// IsCloneable copies this field's value when a NetBox object is cloned in the UI.
	// +kubebuilder:default=false
	// +optional
	IsCloneable *bool `json:"isCloneable,omitempty"`

	// FilterLogic is how NetBox's `?cf_<name>=` filter matches this field.
	// +kubebuilder:default=loose
	// +optional
	FilterLogic CustomFieldFilterLogic `json:"filterLogic,omitempty"`

	// UIVisible is whether NetBox shows the field.
	// +kubebuilder:default=always
	// +optional
	UIVisible CustomFieldUIVisible `json:"uiVisible,omitempty"`

	// UIEditable is whether NetBox lets the field be edited in its UI. Set it to `no` on a
	// field a program owns, so that a hand edit does not fight the next reconcile.
	// +kubebuilder:default=yes
	// +optional
	UIEditable CustomFieldUIEditable `json:"uiEditable,omitempty"`

	// SearchWeight is how heavily this field counts in NetBox's global search. Lower is
	// more important; zero excludes it.
	//
	// A pointer with an explicit default for the reason NetBoxTag.weight is one: `omitempty`
	// on a plain int32 would drop a deliberate `0` -- which here is the value that switches
	// the field out of search entirely (`netbox/extras/models/customfields.py:168-176`).
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	SearchWeight *int32 `json:"searchWeight,omitempty"`

	// Weight orders the field within its group in NetBox's forms; higher appears lower.
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Weight *int32 `json:"weight,omitempty"`

	// Default is the value NetBox fills in for objects that have none, as JSON. A string
	// default has to be written as a JSON string -- `default: "\"eu-west\""` in YAML -- which
	// is NetBox's own rule, not this operator's
	// (`netbox/extras/models/customfields.py:183-190`).
	//
	// Setting it on an existing field back-fills: NetBox writes the default onto every
	// object of every type *added* to ObjectTypes afterwards
	// (`netbox/extras/signals.py:44-46`, `populate_initial_data`).
	//
	// Omit it to leave NetBox's own value alone; set it to `null` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Default *JSONDocument `json:"default,omitempty"`

	// ValidationMinimum is the smallest value an `integer` or `decimal` field may hold.
	//
	// A string, and not a number. NetBox's column is `DecimalField decimal(16,4)`
	// (docs/netbox-schema.md -> extras.CustomField) and DRF renders a decimal as a JSON
	// string, so `"1.0000"` is what comes back and a float in the spec would compare unequal
	// to it forever. The descriptor marks the column EmptyIsNull, because DRF parses `""` as
	// a number and rejects it -- so without that, `""` would be admissible and unwritable
	// (#170).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^(-?[0-9]{1,12}(\.[0-9]{1,4})?)?$`
	// +optional
	ValidationMinimum string `json:"validationMinimum,omitempty"`

	// ValidationMaximum is the largest value an `integer` or `decimal` field may hold. A
	// string for the same reason ValidationMinimum is.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^(-?[0-9]{1,12}(\.[0-9]{1,4})?)?$`
	// +optional
	ValidationMaximum string `json:"validationMaximum,omitempty"`

	// ValidationRegex is a regular expression every value of a text field must match. Use
	// `^` and `$` to anchor it; NetBox does not.
	//
	// Not compiled here. It is a Python regular expression, validated by NetBox with
	// `validate_regex` (`netbox/extras/models/customfields.py:222-231`), and a Go
	// `regexp.Compile` in an admission rule would reject expressions Python accepts and
	// accept ones it does not.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=500
	// +optional
	ValidationRegex string `json:"validationRegex,omitempty"`

	// ValidationSchema is a JSON Schema document every value of a `json` field must
	// satisfy.
	//
	// Omit it to leave NetBox's own value alone; set it to `null` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	ValidationSchema *JSONDocument `json:"validationSchema,omitempty"`
}

// NetBoxCustomField is one extras.CustomField in NetBox: a column on NetBox's own schema
// that every other kind's `spec.customFields` can then write into.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It is schema
// rather than data, so the convention is that it lives in one shared namespace: a custom
// field is global in NetBox whatever namespace declares it, and two teams declaring the same
// name in two namespaces is one NetBox object and a Conflict.
//
// **Deleting one destroys data, and this kind refuses by default.** NetBox strips the field's
// stored value from every object that has one, on a `pre_delete` signal, with no `PROTECT`
// anywhere (`netbox/extras/signals.py:59-68` calling
// `netbox/extras/models/customfields.py:387-401`). So the finalizer stays on and reports
// `Deleting=False, Reason=DataLossBlocked` until either the
// `netbox.kubeforge.org/allow-data-loss: "true"` annotation says otherwise, or
// `spec.deletionPolicy: Retain` says the NetBox object should be left alone. See
// docs/concepts/deletion.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcf
// +kubebuilder:printcolumn:name="Field",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxCustomField struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxCustomFieldSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxCustomField) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxCustomField) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxCustomFieldList is a list of NetBoxCustomField.
// +kubebuilder:object:root=true
type NetBoxCustomFieldList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxCustomField `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxCustomField{}, &NetBoxCustomFieldList{})
}
