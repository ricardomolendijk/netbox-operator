package main

import "strconv"

// The IR as this generator reads it. Only the facts an emitter uses are declared: a struct
// field per IR key would make every unused one look load-bearing, and `hack/build-netbox-ir.py`
// is the schema of record either way (docs/regenerating.md).
type ir struct {
	NetBoxVersion string            `json:"netbox_version"`
	Inputs        map[string]any    `json:"inputs"`
	Enums         map[string]irEnum `json:"enums"`
	Kinds         map[string]irKind `json:"kinds"`
}

// irEnum is one NetBox ChoiceSet.
type irEnum struct {
	// Extendable reports that the set declares a `key`, so a deployment's FIELD_CHOICES
	// setting can replace or extend its members (utilities/choices.py, ChoiceSetMeta).
	// Its members are a default rather than a closed set, which is what decides whether a
	// CRD may pin them as an enum.
	Extendable bool `json:"extendable"`

	// Key is the FIELD_CHOICES key, present exactly when Extendable.
	Key string `json:"key"`

	Source string     `json:"source"`
	Values []irChoice `json:"values"`
}

// irChoice is one member of a ChoiceSet. Value is `any` because NetBox declares a few sets
// with integer members -- `dcim.Interface.poe_mode`'s siblings and the QinQ roles among them --
// and a `string` here rejects the whole IR rather than the one set.
type irChoice struct {
	Value any `json:"value"`

	// Label is `any` for the same reason: a label built by a `.format()` call arrives as the
	// unresolved expression the AST walk saw, which is an object rather than a string.
	Label any `json:"label"`

	// Numeric reports that the member's wire value is a JSON number, which makes the whole
	// set's Go type an integer rather than a string.
	Numeric bool `json:"-"`
}

// LabelText is the human label, or the unresolved expression when the AST walk could not
// evaluate one.
func (c irChoice) LabelText() string {
	if text, ok := c.Label.(string); ok {
		return text
	}

	if raw, ok := c.Label.(map[string]any); ok {
		if unresolved, ok := raw["unresolved"].(string); ok {
			return "unresolved: " + unresolved
		}
	}

	return ""
}

// String is the member's wire value. A JSON number arrives as float64, and an integral one is
// printed without the `.0` a %v would add, since `1.0` is not a value NetBox accepts.
func (c irChoice) String() string {
	if number, ok := c.Value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}

	text, _ := c.Value.(string)

	return text
}

// irKind is one NetBox model with an API endpoint.
type irKind struct {
	App         string              `json:"app"`
	Model       string              `json:"model"`
	Endpoint    string              `json:"endpoint"`
	ObjectType  string              `json:"object_type"`
	SourceFile  string              `json:"source_file"`
	Bases       []string            `json:"bases"`
	Fields      []irField           `json:"fields"`
	NaturalKeys []irNaturalKey      `json:"natural_keys"`
	Filters     map[string]irFilter `json:"filters"`
}

// irFilter is one query parameter a kind's filterset registers, with the lookup suffixes it
// accepts. Consulted rather than assumed: django-filter drops an unregistered parameter and
// returns the unfiltered result set (#206).
type irFilter struct {
	Column      string            `json:"column"`
	FilterClass string            `json:"filter_class"`
	Lookups     map[string]string `json:"lookups"`
}

// irNaturalKey is one unique constraint, already expanded into query parameters.
type irNaturalKey struct {
	Constraint string        `json:"constraint"`
	Source     string        `json:"source"`
	Condition  string        `json:"condition"`
	Fields     []irKeyField  `json:"fields"`
	NullFields []irNullField `json:"null_fields"`
	Unusable   string        `json:"unusable"`
}

// irKeyField is one column of a constraint, with the filter that matches it.
type irKeyField struct {
	Column         string `json:"column"`
	Filter         string `json:"filter"`
	Lookup         string `json:"lookup"`
	ReadOnlyColumn bool   `json:"read_only_column"`
}

// irNullField is one column a partial constraint requires to be null. Filter is empty when
// the IR could not find a parameter for it -- see nullPin, which reopens the FK case #216
// settled.
type irNullField struct {
	Column string `json:"column"`
	Filter string `json:"filter"`
	Reason string `json:"reason"`
}

// irField is one column.
type irField struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Class       string   `json:"class"`
	Enum        string   `json:"enum"`
	DeclaredBy  string   `json:"declared_by"`
	Nullable    bool     `json:"nullable"`
	Required    bool     `json:"required"`
	ReadOnly    bool     `json:"read_only"`
	InWritePath bool     `json:"in_write_path"`
	ObjectTypes []string `json:"object_types"`
	Ref         *irRef   `json:"ref"`
	SQL         irSQL    `json:"sql"`
}

// irRef is an FK's target and deletion behaviour.
type irRef struct {
	Target   string `json:"target"`
	OnDelete string `json:"on_delete"`
	Self     bool   `json:"self"`
}

// irSQL is the Django field's own arguments.
type irSQL struct {
	MaxLength     int    `json:"max_length"`
	MaxDigits     int    `json:"max_digits"`
	DecimalPlaces int    `json:"decimal_places"`
	Unique        bool   `json:"unique"`
	Null          bool   `json:"null"`
	Blank         bool   `json:"blank"`
	Default       any    `json:"default"`
	To            string `json:"to"`
	OnDelete      string `json:"on_delete"`
	Choices       string `json:"choices"`
}
