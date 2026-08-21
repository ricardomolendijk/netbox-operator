package registry

import (
	"errors"
	"fmt"
	"slices"
)

// Field-map validation failures. Each is a distinct sentinel so callers and tests
// classify by type rather than by matching a message.
var (
	// ErrNoFields is returned for a descriptor with no field map. Without one the engine
	// cannot turn a CR spec into a NetBox payload at all, so it is as fatal as a missing
	// endpoint.
	ErrNoFields = errors.New("no field map")

	// ErrDuplicateSpecField is returned when two entries claim the same CR spec field.
	ErrDuplicateSpecField = errors.New("duplicate spec field")

	// ErrDuplicateAPIField is returned when two entries write the same NetBox field.
	ErrDuplicateAPIField = errors.New("duplicate api field")

	// ErrFieldReadOnly is returned for a spec field mapped onto a read-only column.
	// Writing one silently no-ops, which is a PATCH loop rather than an error.
	ErrFieldReadOnly = errors.New("spec field is mapped onto a read-only api field")

	// ErrUnknownSpecField is returned when a natural key, a null pin or the containment
	// ref names a CR spec field the field map does not declare. This is the check that
	// makes a misspelled or irregularly-cased spec name fail at boot instead of producing
	// a lookup with a missing filter.
	ErrUnknownSpecField = errors.New("spec field is not in the field map")

	// ErrContainmentNotRef is returned when the containment ref names a spec field that
	// is not a reference: a containment parent is by definition another object
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	ErrContainmentNotRef = errors.New("containment ref is not a reference field")

	// ErrDeferredNotRef is returned for a deferred field that no reference in the field
	// map writes. Deferral exists for references that cannot be set at create time; a
	// deferred scalar would simply never be written.
	ErrDeferredNotRef = errors.New("deferred field is not written by a reference in the field map")

	// ErrGenericFKNotSpecField is returned for a generic FK with no CR spec field behind
	// it, or one whose columns are also mapped as ordinary fields.
	ErrGenericFKNotSpecField = errors.New("generic FK spec field is missing or conflicts with the field map")
)

// Field maps one CR spec field to the NetBox field it is written as.
//
// This table, and not a naming convention, is what bridges the two vocabularies. The
// engine reads a spec through its JSON representation, so its field names are CR spec
// names (`primaryIP4Ref`, `vrfRef`, `objectTypes`), while every other list on a
// Descriptor — Deferred, ReadOnly, M2M, ObjectTypeLists, RecreateOn — is NetBox API names
// (`primary_ip4`, `vrf`, `object_types`). Something has to join them.
//
// A convention could do most of it: strip `Ref`, then camelCase to snake_case. It is
// smaller, and it is wrong exactly where being wrong is expensive. `wirelessLANs` becomes
// `wireless_l_a_ns`, `primaryIP4Ref` becomes `primary_i_p4`, and `oobIPRef` becomes
// `oob_i_p`; each needs an acronym list, and an acronym list is a per-kind fact wearing a
// convention's clothes. Worse, the failure is silent: a wrong field name is not rejected
// by NetBox, it is *ignored* by NetBox, so the operator reports success while writing
// nothing. An explicit pair cannot be silently wrong, is emitted by the M7 generator from
// the same OpenAPI schema it derives the rest of the Descriptor from, and is visible in a
// diff. See docs/concepts/engine.md.
type Field struct {
	// Spec is the CR spec field name, which is its JSON name — the spelling a user
	// writes in YAML and the spelling KeyField.Spec uses.
	Spec string

	// API is the NetBox field it is written as, in the same spelling as ReadOnly, M2M and
	// Deferred. For a foreign key that is the column without `_id`: `vrf`, not `vrf_id`.
	API string

	// Ref marks a value that is a reference to another object rather than a value to
	// write as-is. The engine cannot put one in a payload by itself — a ref becomes an id
	// only in internal/resolver (NBO-012) — so until then a declared ref is reported and
	// left out, which is the M1 contract NBO-009 states.
	Ref bool
}

// FieldFor returns the mapping for a CR spec field. A linear scan over a few dozen
// entries, because the alternative is a map that has to be kept in sync with the slice
// the generator emits.
func (d Descriptor) FieldFor(spec string) (Field, bool) {
	for _, field := range d.Fields {
		if field.Spec == spec {
			return field, true
		}
	}

	return Field{}, false
}

// GenericFKFor returns the polymorphic pair a CR spec field feeds. One spec field writes
// two columns, so such a field is declared on the GenericFKSpec rather than in Fields.
func (d Descriptor) GenericFKFor(spec string) (GenericFKSpec, bool) {
	for _, generic := range d.GenericFKs {
		if generic.Spec == spec {
			return generic, true
		}
	}

	return GenericFKSpec{}, false
}

// declaresSpecField reports whether spec is a CR spec field this descriptor knows, either
// as an ordinary field or as the one behind a generic FK.
func (d Descriptor) declaresSpecField(spec string) bool {
	if _, ok := d.FieldFor(spec); ok {
		return true
	}

	_, ok := d.GenericFKFor(spec)

	return ok
}

// isRefSpecField reports whether spec names a reference to another object.
func (d Descriptor) isRefSpecField(spec string) bool {
	if field, ok := d.FieldFor(spec); ok {
		return field.Ref
	}

	_, ok := d.GenericFKFor(spec)

	return ok
}

// validateFieldMap checks the field map itself and every reference into it. It is where a
// spec name that does not round-trip to an API name is caught, which is the whole reason
// the map is explicit.
func (d Descriptor) validateFieldMap() error {
	if len(d.Fields) == 0 {
		return ErrNoFields
	}

	return errors.Join(d.validateFieldEntries(), d.validateSpecReferences(), d.validateDeferredFields())
}

func (d Descriptor) validateFieldEntries() error {
	errs := make([]error, 0, len(d.Fields))
	seenSpec := make(map[string]struct{}, len(d.Fields))
	seenAPI := make(map[string]struct{}, len(d.Fields))

	for _, field := range d.Fields {
		if field.Spec == "" || field.API == "" {
			errs = append(errs, fmt.Errorf("%w: fields %+v", ErrEmptyField, field))

			continue
		}

		if _, dup := seenSpec[field.Spec]; dup {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateSpecField, field.Spec))
		}

		if _, dup := seenAPI[field.API]; dup {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateAPIField, field.API))
		}

		if slices.Contains(d.ReadOnly, field.API) {
			errs = append(errs, fmt.Errorf("%w: %s -> %s", ErrFieldReadOnly, field.Spec, field.API))
		}

		seenSpec[field.Spec], seenAPI[field.API] = struct{}{}, struct{}{}
	}

	return errors.Join(errs...)
}

// validateSpecReferences checks that everything naming a CR spec field names one that
// exists. A natural key on an undeclared spec field cannot be built, and would otherwise
// silently send a lookup with one filter missing — which matches the wrong object rather
// than none.
func (d Descriptor) validateSpecReferences() error {
	errs := make([]error, 0, len(d.NaturalKeys)+1)

	for i, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			if !d.declaresSpecField(field.Spec) {
				errs = append(errs, fmt.Errorf("%w: natural key %d matches on %s", ErrUnknownSpecField, i, field.Spec))
			}
		}

		for _, field := range key.NullFields {
			if !d.declaresSpecField(field.Spec) {
				errs = append(errs, fmt.Errorf("%w: natural key %d pins %s", ErrUnknownSpecField, i, field.Spec))
			}
		}
	}

	return errors.Join(append(errs, d.validateContainment())...)
}

func (d Descriptor) validateContainment() error {
	if d.ContainmentRef == "" {
		return nil
	}

	if !d.declaresSpecField(d.ContainmentRef) {
		return fmt.Errorf("%w: containment ref %s", ErrUnknownSpecField, d.ContainmentRef)
	}

	if !d.isRefSpecField(d.ContainmentRef) {
		return fmt.Errorf("%w: %s", ErrContainmentNotRef, d.ContainmentRef)
	}

	return nil
}

// validateDeferredFields ties the deferred API names back to the field map. Without it,
// `Deferred: primary_ip4` on a kind whose field map writes `primaryIp4` is accepted, and
// the field is then neither written at create time nor at any point after.
func (d Descriptor) validateDeferredFields() error {
	refAPI := make(map[string]struct{}, len(d.Fields))

	for _, field := range d.Fields {
		if field.Ref {
			refAPI[field.API] = struct{}{}
		}
	}

	errs := make([]error, 0, len(d.Deferred))

	for _, deferred := range d.Deferred {
		if _, ok := refAPI[deferred.APIField]; !ok {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDeferredNotRef, deferred.APIField))
		}
	}

	return errors.Join(errs...)
}

// validateGenericFKSpecFields checks the CR spec field behind each polymorphic pair. The
// pair's two columns are written together from one spec field, so declaring that field in
// Fields as well would give it two conflicting renderings.
func (d Descriptor) validateGenericFKSpecFields() error {
	errs := make([]error, 0, len(d.GenericFKs))

	for _, generic := range d.GenericFKs {
		if generic.Spec == "" {
			errs = append(errs, fmt.Errorf("%w: %s/%s", ErrGenericFKNotSpecField, generic.TypeField, generic.IDField))

			continue
		}

		if _, mapped := d.FieldFor(generic.Spec); mapped {
			errs = append(errs, fmt.Errorf("%w: %s is also an ordinary field", ErrGenericFKNotSpecField, generic.Spec))
		}

		for _, column := range []string{generic.TypeField, generic.IDField} {
			if slices.ContainsFunc(d.Fields, func(f Field) bool { return f.API == column }) {
				errs = append(errs, fmt.Errorf("%w: %s is also an ordinary field", ErrGenericFKNotSpecField, column))
			}
		}
	}

	return errors.Join(errs...)
}
