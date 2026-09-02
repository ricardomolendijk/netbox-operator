package export

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// manifest renders one NetBox object as one YAML document, plus whatever a human needs to
// know about how it was rendered.
func manifest(obj object, names map[string]string, opts Options) (string, []string) {
	spec, notes := specOf(obj, names, opts)
	spec["endpointRef"] = opts.EndpointRef

	doc := map[string]any{
		"apiVersion": obj.desc.GVK.GroupVersion().String(),
		"kind":       obj.desc.GVK.Kind,
		"metadata": map[string]any{
			"name":      obj.name,
			"namespace": opts.Namespace,
		},
		"spec": spec,
	}

	// sigs.k8s.io/yaml goes through encoding/json, which sorts map keys, so the byte
	// output is a function of the content alone -- which is what makes two exports of an
	// unchanged NetBox diff to nothing.
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		// Every value in doc came out of encoding/json in the first place, so this is
		// unreachable rather than merely unlikely; reporting it as a note keeps the
		// signature honest without inventing an error path nothing can take.
		return "", append(notes, fmt.Sprintf("%s %q could not be rendered: %v",
			obj.desc.GVK.Kind, obj.name, err))
	}

	return string(rendered), notes
}

// specOf builds one CR spec by reading the descriptor's field map right to left.
//
// Only fields the map names are ever emitted, which is what makes the read-only rule
// structural rather than a filter to keep in step: registry.Validate already refuses a
// spec field mapped onto a read-only column (ErrFieldReadOnly), so `id`, `url`, `display`,
// `created`, `last_updated`, every `_`-prefixed cache and every counter cache have no spec
// field to be emitted into. There is nothing to exclude, because there is nothing to
// include them from.
func specOf(obj object, names map[string]string, opts Options) (map[string]any, []string) {
	spec := map[string]any{}
	var notes []string

	for _, field := range obj.desc.Fields {
		value, fieldNotes := fieldValue(obj, field, names, opts)
		notes = append(notes, fieldNotes...)
		if keep(value, opts.Full) {
			spec[field.Spec] = value
		}
	}

	for _, generic := range obj.desc.GenericFKs {
		value, genericNotes := genericValue(obj, generic, names, opts)
		notes = append(notes, genericNotes...)
		if keep(value, opts.Full) {
			spec[generic.Spec] = value
		}
	}

	if fields := customFields(obj.raw); len(fields) > 0 {
		spec["customFields"] = fields
	}

	return spec, notes
}

// fieldValue is one spec field's value, by class. Guard clauses in cardinality order; the
// scalar case is the fallback, and it is most fields.
func fieldValue(
	obj object, field registry.Field, names map[string]string, opts Options,
) (any, []string) {
	if field.Class == registry.ClassRefOne {
		id, ok := netbox.IDOf(obj.raw[field.API])
		if !ok {
			return nil, nil
		}

		return ref(obj, field.Target, id, names, opts)
	}

	if field.Class == registry.ClassRefMany {
		return refList(obj, field, names, opts)
	}

	if field.Class == registry.ClassObjectTypeList {
		types := netbox.ObjectTypesOf(obj.raw[field.API])
		slices.Sort(types)

		return types, nil
	}

	// ClassArray and ClassValue. An array's order is data, so it is passed through as it
	// came; a scalar arrives either bare or, for a choice column, as {"value","label"},
	// which netbox.Unwrap reduces to the value the engine would write back.
	return netbox.Unwrap(obj.raw[field.API]), nil
}

// refList is a to-many reference, sorted by the key each element is emitted under so the
// output does not depend on the order NetBox happened to return -- NetBox does not
// preserve M2M order, so that order is not data.
func refList(obj object, field registry.Field, names map[string]string, opts Options) (any, []string) {
	ids := netbox.IDsOf(obj.raw[field.API])
	slices.Sort(ids)

	refs := make([]any, 0, len(ids))
	var notes []string

	for _, id := range ids {
		value, refNotes := ref(obj, field.Target, id, names, opts)
		refs = append(refs, value)
		notes = append(notes, refNotes...)
	}

	return refs, notes
}

// ref renders one reference.
//
// By name when the target was exported too, and by id when it was not. The name is the
// only mode that expresses a dependency the operator can wait on, and it is the only one
// that survives being applied to a different NetBox -- ids are per-instance, so a manifest
// full of them is a manifest that can only ever be applied back to the machine it came
// from. The id is the honest fallback for an object outside the export set: there is no CR
// to name, and a slug would silently point at whatever that slug means on the target
// instance.
func ref(
	obj object, target schema.GroupVersionKind, id int, names map[string]string, opts Options,
) (any, []string) {
	if opts.IDRefs {
		return map[string]any{"id": id}, nil
	}

	desc, ok := registry.Get(target)
	if ok {
		if name, found := names[indexKey(desc.ObjectType, id)]; found {
			return map[string]any{"name": name}, nil
		}
	}

	return map[string]any{"id": id}, []string{fmt.Sprintf(
		"%s %q references %s %d by id: it is outside the export set",
		obj.desc.GVK.Kind, obj.name, kindOrType(target), id)}
}

// genericValue renders a polymorphic reference -- a scope, an IP address's assignment --
// as the union shape the CRD declares.
//
// Read from `(scope_type, scope_id)` and never from a cached column. `_site` is what
// NetBox denormalises the answer into, and it is wrong for anything scoped below a site: a
// Location-scoped prefix carries `_site` too, so believing it would export `siteRef` for
// an object that is not scoped to a site, which then round-trips as a silent no-op forever
// (docs/concepts/generic-refs.md, ADR-0003 rule 2).
func genericValue(
	obj object, generic registry.GenericFKSpec, names map[string]string, opts Options,
) (any, []string) {
	objectType := objectTypeOf(obj.raw[generic.TypeField])
	id, hasID := netbox.IDOf(obj.raw[generic.IDField])
	if objectType == "" || !hasID {
		return nil, nil
	}

	member, ok := scopeMemberFor(generic, objectType)
	if !ok {
		return nil, []string{fmt.Sprintf(
			"%s %q is %s %s %d, which no exported Kind covers: %s omitted",
			obj.desc.GVK.Kind, obj.name, generic.Spec, objectType, id, generic.Spec)}
	}

	value, notes := ref(obj, member.Target, id, names, opts)

	return map[string]any{member.Spec: value}, notes
}

// objectTypeOf reads an `app_label.model` string out of either shape a content-type column
// arrives in. netbox.ObjectTypesOf is the same normalisation applied to a list, and is
// reused rather than restated so the two cannot disagree about what a content type is.
func objectTypeOf(value any) string {
	types := netbox.ObjectTypesOf([]any{value})
	if len(types) == 0 {
		return ""
	}

	return types[0]
}

// customFields are the NetBox custom-field values worth putting in Git: the ones that
// hold something, minus the operator's own bookkeeping.
//
// The map the CRD declares is deliberately not exhaustive -- only the keys named are
// managed -- so emitting the populated subset is the whole of what a manifest needs
// (docs/concepts/field-ownership.md).
//
// Values are emitted as the JSON types NetBox returned them as, and this used to render
// them with fmt.Sprint because the spec field was a map[string]string and a string was the
// only thing that would round-trip. It no longer is (#303), and stringifying now would make
// the export produce manifests the operator cannot apply: a `boolean` custom field written
// back as `"true"` is refused by NetBox with *"Value must be true or false"*, which is the
// bug this export would otherwise be a generator for.
func customFields(raw netbox.Object) map[string]any {
	live, ok := raw[provenance.CustomFieldsField].(map[string]any)
	if !ok {
		return nil
	}

	skip := provenanceFields()
	out := map[string]any{}
	for name, value := range live {
		if value == nil || value == "" || slices.Contains(skip, name) {
			continue
		}
		out[name] = value
	}

	return out
}

// keep decides whether a value is worth a line in the manifest.
//
// The default drops what is empty. An omitted spec field means "do not manage this
// column", and for a column that is already empty that is the same state as writing the
// empty value into it -- so dropping it costs nothing and turns 3000 lines of
// `comments: ""` into a file a human will actually read. --full keeps the empty strings
// and lists for when the export is meant as a backup.
//
// `false` and `0` are never dropped, in either mode. NetBox's defaults are not in the
// registry, so "equal to the default" is not a question this code can answer, and guessing
// at it would drop a deliberately-false `is_pool` and hand the column back to whatever
// NetBox holds. A null is dropped in both modes: a JSON null has no representation in a
// typed spec field, and an absent field is exactly what "NetBox holds nothing here" means.
func keep(value any, full bool) bool {
	if value == nil {
		return false
	}
	if full {
		return true
	}

	switch typed := value.(type) {
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// kindOrType names a reference target in a message: its Kind, or its object type when no
// Kind is registered for it yet.
func kindOrType(target schema.GroupVersionKind) string {
	if desc, ok := registry.Get(target); ok {
		return desc.ObjectType
	}

	return strings.ToLower(target.Kind)
}
