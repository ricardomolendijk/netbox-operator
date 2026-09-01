package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// JSONDocument is the value of a NetBox `JSONField`: whatever JSON the column will hold,
// carried through to the API and back unchanged.
//
// NetBox has a dozen of them and they are not one shape. `extras.SavedFilter.parameters` is
// a query-parameter object, `extras.CustomFieldChoiceSet.choice_colors` is a flat
// string-to-string map, `extras.CustomField.default` is *any* JSON scalar or container --
// NetBox's own help text is "must be a JSON value ... encapsulate strings with double quotes"
// (`netbox/extras/models/customfields.py:183-190`) -- and
// `extras.CustomField.validation_schema` is a JSON Schema document. So there is no Go struct
// to write: the operator's job here is to be a faithful pipe, and NetBox is the validator.
//
// An alias rather than a defined type, so that controller-gen and the generated deepcopy see
// `apiextensions.JSON` itself and emit `x-kubernetes-preserve-unknown-fields` without any
// help. The name exists for the reader: a spec field typed `*JSONDocument` says "this column
// is a JSONField and its contents are NetBox's business", which `*apiextensionsv1.JSON`
// at fourteen use sites does not.
//
// Fields of this type are declared ClassJSON on the descriptor, which is load-bearing rather
// than cosmetic: the scalar comparison unwraps any JSON object carrying an `id` or a `value`
// key, because that is how NetBox renders a foreign key and a choice on read -- so a
// `parameters: {"id": ["3"]}` compared as a scalar would never settle and the operator would
// PATCH it forever (registry.ClassJSON, netbox.FieldRules.JSON).
//
// Always a pointer in a spec, and always with `omitempty`. That is what keeps the three
// states of docs/concepts/field-ownership.md distinguishable: absent means "do not manage
// this column", `{}` means "an empty document", and `null` means the column's own null.
type JSONDocument = apiextensionsv1.JSON
