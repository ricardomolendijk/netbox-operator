package registry

import (
	"errors"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
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

	// ErrTagsFieldOnTaggableKind is returned when a Taggable descriptor also maps a spec
	// field onto `tags`.
	//
	// Two writers for one column, and the loudest possible version of it: `tags` is a
	// full-replacement list, so the provenance stamp appends its tag to whatever the payload
	// carries (reconciler/stamp.go). On a kind where `tags` is `TagsMixin` that is correct.
	// On extras.ConfigContext it is not `TagsMixin` at all -- it is a plain M2M selecting
	// *which tagged objects the context applies to* (docs/netbox-schema.md ->
	// extras.ConfigContext) -- so a Taggable declaration there would quietly add
	// `k8s-managed` to the selector and change which objects in NetBox receive the
	// configuration. The two readings of `tags` cannot both be right on one kind, so the
	// boot fails rather than the cluster drifting.
	ErrTagsFieldOnTaggableKind = errors.New("a taggable kind maps a spec field onto the tags column")

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

	// ErrTargetNotRef is returned for a non-reference field carrying a target Kind. It is
	// almost always a class left at ClassValue, and left alone it produces a field the
	// resolver ignores and the engine writes to NetBox verbatim.
	ErrTargetNotRef = errors.New("non-reference field declares a target kind")

	// ErrUnknownFieldClass is returned for a field class the engine does not implement.
	// The zero value is not one of them: ClassValue is the class almost every field has,
	// and defaulting it is what keeps a scalar entry to a spec name and an API name.
	ErrUnknownFieldClass = errors.New("unknown field class")

	// ErrToManyNaturalKey is returned when a natural-key candidate matches on, or pins to
	// null, a to-many reference. A filter carries one value and a to-many field has none,
	// so such a candidate can never be applicable: the object would wait for an identity
	// that cannot be built rather than fail, which is the worst of the three outcomes.
	ErrToManyNaturalKey = errors.New("natural-key filter reads a to-many reference")

	// ErrContainmentNotCascade is returned when the containment ref names a foreign key
	// whose target's deletion does *not* cascade server-side.
	//
	// On a polymorphic pair it is returned when *no* union member cascades; a union whose
	// members disagree is legal, and reconciler/owners.go then refuses the owner reference
	// per object, for the member that object resolved through (#214).
	//
	// The containment parent is whichever FK the server cascades
	// (docs/decisions/0003-ownership-and-references.md rule 4), so this is the check that
	// turns the modelling error into a boot failure. An owner reference on a
	// `PROTECT`-ed FK would promise a cluster-side cascade that NetBox refuses to perform:
	// deleting the parent CR would garbage-collect the child CR, whose finalizer would then
	// try a DELETE NetBox rejects, and the object would be gone from Kubernetes and still
	// in NetBox. A `SET_NULL` FK is the same mistake in the other direction -- the row
	// survives with the column cleared, and the CR that described it has been deleted.
	ErrContainmentNotCascade = errors.New("containment ref does not cascade on delete")

	// ErrCascadeNotRef is returned for a non-reference field declaring CascadeOnDelete.
	// `on_delete` is a property of a foreign key, so on a scalar the flag is data nothing
	// reads -- the same class of quietly-ignored declaration ErrTargetNotRef exists for.
	ErrCascadeNotRef = errors.New("non-reference field declares CascadeOnDelete")

	// ErrContainmentToMany is returned for a to-many containment ref. Kubernetes garbage
	// collection waits for every owner, so two containment owners silently turn "delete the
	// site" into "delete the site or the VRF" -- which is the argument
	// docs/decisions/0003-ownership-and-references.md rule 4 makes for exactly one, and a
	// list of parents is that mistake with no upper bound.
	ErrContainmentToMany = errors.New("containment ref is a to-many reference")
)

// tagsColumn is NetBox's name for the column the provenance stamp writes into. Spelled here
// rather than imported from internal/provenance, which reads this package to derive the
// bootstrap's `object_types` and cannot be depended on back.
const tagsColumn = "tags"

// FieldClass is what one spec field is: how many references it carries, and how its value
// is compared.
//
// One class per field, rather than a bool plus a list of API names. `Ref bool` said that a
// field was a reference and nothing about whether it took one or many, while Descriptor.M2M
// said how the same field's *value* compared -- so a to-many reference was described twice,
// by two declarations that could disagree, with nothing joining them (NBO-088). A class says
// it once: cardinality and comparison rule are two readings of one fact, and the M2M,
// object-type-list and array sets are derived from it rather than declared beside it.
//
// It is also what the M7 generator emits (NBO-042). Cardinality is already in the OpenAPI
// schema the generator reads, so emitting one class per field is strictly less work than
// keeping three lists consistent with a bool.
type FieldClass string

const (
	// ClassValue is a scalar written as-is and compared as one. It is the zero value
	// because it is what almost every field is, and a field that is silently treated as a
	// value is treated exactly as it would have been before classes existed.
	ClassValue FieldClass = ""

	// ClassRefOne is one reference to another object, written as one NetBox id.
	ClassRefOne FieldClass = "RefOne"

	// ClassRefMany is a list of references, written as a list of NetBox ids and compared as
	// an order-independent id set: `tags`, ipam.VRF's `import_targets` and
	// `export_targets`, dcim.Site's `asns`, dcim.Interface's `wireless_lans`
	// (docs/netbox-schema.md). NetBox does not preserve M2M order, so the order the spec
	// lists them in is not data and the resolver must not make it look like data.
	ClassRefMany FieldClass = "RefMany"

	// ClassObjectTypeList is a many-to-many onto contenttypes.ContentType, whose values are
	// `app_label.model` strings rather than references to NetBox objects:
	// extras.Tag.object_types (docs/netbox-schema.md -> extras.Tag). Compared as an
	// order-independent string set, and resolved against nothing -- a resolver told to
	// resolve one would go looking for a CR named `dcim.device`, which cannot exist.
	ClassObjectTypeList FieldClass = "ObjectTypeList"

	// ClassArray is a Postgres ArrayField, whose order is data rather than incidental:
	// ipam.VLANGroup.vid_ranges, ipam.Service.ports (docs/netbox-schema.md). It cannot
	// share a class with ClassRefMany even though both arrive as JSON lists: comparing an
	// array order-independently misses a reordering the user asked for, and comparing an
	// M2M order-sensitively PATCHes forever.
	ClassArray FieldClass = "Array"

	// ClassJSON is a Postgres JSONField whose value is a whole document:
	// extras.SavedFilter.parameters, extras.CustomField.default and .validation_schema,
	// extras.CustomFieldChoiceSet.choice_colors (docs/netbox-schema.md).
	//
	// It cannot be ClassValue, and that is not a nicety. The scalar comparison unwraps any
	// JSON object carrying an `id` or a `value` key, because that is how NetBox renders a
	// foreign key and a choice on read -- so `parameters: {"id": ["3"]}`, an ordinary NetBox
	// filter, would be compared as `[3]` against the whole document, never settle, and PATCH
	// forever (netbox.FieldRules.JSON). It cannot be ClassArray either: a document is not a
	// list, and the array rule compares element by element with the same scalar rule.
	ClassJSON FieldClass = "JSON"
)

// fieldClasses is the closed set, with ClassValue in it: Validate rejects anything else, so
// a class invented in a descriptor fails the boot rather than being treated as a scalar.
var fieldClasses = []FieldClass{
	ClassValue, ClassRefOne, ClassRefMany, ClassObjectTypeList, ClassArray, ClassJSON,
}

// Ref reports whether this class is a reference internal/resolver turns into an id.
func (c FieldClass) Ref() bool {
	return c == ClassRefOne || c == ClassRefMany
}

// ToMany reports whether this class carries a list of references rather than one.
//
// Only ClassRefMany. ClassObjectTypeList and ClassArray also arrive as JSON lists, and
// neither is a reference: their values are the value.
func (c FieldClass) ToMany() bool {
	return c == ClassRefMany
}

// Field maps one CR spec field to the NetBox field it is written as.
//
// This table, and not a naming convention, is what bridges the two vocabularies. The
// engine reads a spec through its JSON representation, so its field names are CR spec
// names (`primaryIP4Ref`, `vrfRef`, `objectTypes`), while every other list on a
// Descriptor — Deferred, ReadOnly, RecreateOn — is NetBox API names (`primary_ip4`,
// `vrf`, `object_types`). Something has to join them.
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

	// Class is what this field is: a value, one reference, a list of references, a list of
	// content types, or an ordered array. It is the single declaration of both the field's
	// cardinality and its comparison rule — see FieldClass for why those are one fact.
	//
	// The zero value is ClassValue, so a scalar entry stays a spec name and an API name.
	Class FieldClass

	// Target is the Kind a reference resolves against, and must be empty on anything else.
	//
	// Data rather than the accessor closure NBO-011's spec proposed. Nothing in a
	// Descriptor is a func — see this package's comment — because a closure cannot be
	// emitted by the M7 generator, printed in a diff or linted. It is also unnecessary:
	// the engine already reads a spec through its JSON representation rather than through
	// per-kind getters, so the target Kind is the only thing the resolver needs from here.
	//
	// Write it as the matching typed alias's own answer,
	// `v1alpha1.RegionRef{}.TargetGVK()`, so the alias stays the single source of truth
	// for what it points at.
	//
	// A reference without a Target is not rejected yet. It will have to be, since the resolver
	// cannot dispatch without one, but typed aliases exist for five Kinds so far and the
	// descriptors for the rest name targets that have no alias to point at — requiring it
	// now would mean inventing GVKs for Kinds nobody has built. NBO-012 lands the resolver
	// and the remaining aliases together, and turns the check on with them.
	Target schema.GroupVersionKind

	// CascadeOnDelete says NetBox deletes *this* object when the target of this reference
	// is deleted -- the Django field's `on_delete=CASCADE`, read straight off
	// docs/netbox-schema.md.
	//
	// It exists because it was the one thing a Descriptor could not express (#192), and the
	// rule that needs it is not a matter of taste: the containment parent of ADR-0003 rule 4
	// is *whichever FK the server cascades*. A `CASCADE` FK qualifies because the row goes
	// away without the operator asking, so the CR has to go away with it or the engine's
	// create-if-absent step resurrects what NetBox deliberately deleted. A `PROTECT` or
	// `SET_NULL` FK does not qualify, because nothing on the server side disappears.
	// validateContainment enforces that, so a Descriptor naming a non-cascading containment
	// parent fails the boot rather than shipping a cascade that does the wrong thing.
	//
	// Meaningful only on a reference class -- ErrCascadeNotRef rejects it elsewhere -- and
	// only load-bearing on the field ContainmentRef names. It is still worth declaring on
	// every FK: it is the fact the M7 generator can emit from the OpenAPI schema's
	// `on_delete` for ~90 Kinds, and a Kind whose only cascading FK is left undeclared is a
	// Kind that silently gets no containment parent.
	CascadeOnDelete bool

	// EmptyIsNull says this column is cleared with `null` rather than with the empty
	// string, so an emptied spec field is sent as JSON null.
	//
	// It exists for NetBox's nullable non-text columns. dcim.Site.latitude and .longitude
	// are DecimalFields (docs/netbox-schema.md -> dcim.Site): the API returns them as
	// strings, so a spec holds one as a string, but DRF parses `""` as a number and
	// rejects it -- which would make `latitude: ""` an admission-legal value that fails on
	// every write, and the field still unclearable (#170). A text column needs nothing
	// here: `description: ""` is how NetBox spells an empty description.
	//
	// On a ClassRefOne field it means the same thing about the same column: the reference
	// may be written empty, and an empty one is the foreign key cleared with null rather
	// than a reference that failed to resolve (#185). The spec field is then typed
	// v1alpha1.OptionalRef rather than a strict ref alias, so `{}` is admissible in the
	// first place -- the flag and the type are two halves of one decision, and a field that
	// sets only the flag simply never receives an empty reference to act on. The resolver
	// answers such a reference with the zero Result, and internal/reconciler writes null
	// for it (resolver.Resolve, reconciler.applyRef).
	//
	// Meaningless on ClassRefMany, ClassObjectTypeList and ClassArray, where the empty
	// statement is already `[]` and an empty *element* selects nothing at all: the resolver
	// refuses one as malformed rather than clearing the column. Validate does not reject
	// the combination yet.
	EmptyIsNull bool
}

// M2MFields are the NetBox columns compared as an order-independent id set: every to-many
// reference this kind declares.
//
// Derived from the field map rather than declared beside it, which is what makes the
// contradiction NBO-088 was filed for unrepresentable -- there is no second list left to
// disagree with a field's class about how many objects it holds. The engine's own `tags`
// column is not here and cannot be, since no spec field maps onto it, so
// internal/reconciler adds it from Descriptor.Taggable (see fieldRules).
func (d Descriptor) M2MFields() []string {
	return d.apiFieldsOf(ClassRefMany)
}

// ObjectTypeListFields are the NetBox columns compared as an order-independent set of
// `app_label.model` strings.
func (d Descriptor) ObjectTypeListFields() []string {
	return d.apiFieldsOf(ClassObjectTypeList)
}

// ArrayFields are the NetBox columns compared order-sensitively, because for a Postgres
// ArrayField the order is data.
func (d Descriptor) ArrayFields() []string {
	return d.apiFieldsOf(ClassArray)
}

// JSONFields are the NetBox columns compared as whole JSON documents, without the
// nested-object unwrapping the scalar rule applies.
func (d Descriptor) JSONFields() []string {
	return d.apiFieldsOf(ClassJSON)
}

// apiFieldsOf is the NetBox columns of every field in one class, in descriptor order.
func (d Descriptor) apiFieldsOf(class FieldClass) []string {
	out := make([]string, 0, len(d.Fields))

	for _, field := range d.Fields {
		if field.Class == class {
			out = append(out, field.API)
		}
	}

	return out
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
// as an ordinary field, as the one behind a generic FK, or as one of that pair's two
// columns.
//
// The third case is the one #180 is about. A natural key on a polymorphic pair needs two
// filters and the union's own spec field has no single value to offer, so the pair's halves
// are named by *column* -- `scope_type` and `scope_id`, matching what
// reconciler.applyGenericFK writes into the decoded spec once the union resolves.
// ipam.VLANGroup is unique on `(scope_type, scope_id, slug)` and could not state its own
// identity otherwise.
func (d Descriptor) declaresSpecField(spec string) bool {
	if _, ok := d.FieldFor(spec); ok {
		return true
	}

	if _, ok := d.GenericFKFor(spec); ok {
		return true
	}

	return slices.ContainsFunc(d.GenericFKs, func(pair GenericFKSpec) bool {
		return pair.TypeField == spec || pair.IDField == spec
	})
}

// isRefSpecField reports whether spec names a reference to another object.
func (d Descriptor) isRefSpecField(spec string) bool {
	if field, ok := d.FieldFor(spec); ok {
		return field.Class.Ref()
	}

	_, ok := d.GenericFKFor(spec)

	return ok
}

// cascadesOnDelete reports whether NetBox deletes this object when the target of spec is
// deleted. Read off the ordinary field map or off the generic-FK pair, the same two places
// declaresSpecField looks.
//
// For a union it is "some member cascades", which is the most a boot check can ask: which
// member an object uses is a fact about that object, not about the descriptor. The per-object
// question is CascadesFrom.
func (d Descriptor) cascadesOnDelete(spec string) bool {
	if field, ok := d.FieldFor(spec); ok {
		return field.CascadeOnDelete
	}

	generic, ok := d.GenericFKFor(spec)

	return ok && generic.anyCascades()
}

// CascadesFrom reports whether NetBox deletes this object when the object spec *resolved to*
// is deleted, given that object's Kind.
//
// The reconcile-time form of the containment rule, and the reason it takes a target at all:
// on a polymorphic pair the cascade is per member (#214), so "does this reference cascade" has
// no answer until the reference has resolved. An ordinary reference has one target and gives
// the same answer for it that validateContainment already checked at boot.
//
// An unknown spec field, or a union member for a Kind this pair does not declare, is false:
// the caller is asking about a reference this descriptor cannot have produced, and the safe
// answer to that is no cascade rather than an assumed one.
func (d Descriptor) CascadesFrom(spec string, target schema.GroupVersionKind) bool {
	if field, ok := d.FieldFor(spec); ok {
		return field.CascadeOnDelete
	}

	generic, ok := d.GenericFKFor(spec)

	return ok && generic.Cascades(target)
}

// ContainmentTargets are the Kinds ContainmentRef may resolve to: the one target of an
// ordinary reference, or every member's target for a union. Empty for a kind with no
// containment parent.
//
// It exists so reconciler/owners.go can recognise a containment owner reference it set on an
// earlier pass without storing which one it set. An object that moves from one member of the
// union to another -- `regionRef` to `siteRef` -- has to *lose* the owner reference naming the
// member it left, and the only durable way to know that a NetBoxRegion owner reference is the
// containment slot rather than somebody else's is that NetBoxRegion is a Kind this ref points
// at (#214).
func (d Descriptor) ContainmentTargets() []schema.GroupVersionKind {
	if d.ContainmentRef == "" {
		return nil
	}

	if field, ok := d.FieldFor(d.ContainmentRef); ok {
		// A reference with no target Kind has no slot to recognise -- and no owner reference
		// to remove either, since the resolver dispatches on Target and cannot resolve one
		// without it. Nil rather than a failure for the reason Field.Target documents: the
		// requirement is turned on for every reference at once, with the last typed aliases.
		if field.Target.Empty() {
			return nil
		}

		return []schema.GroupVersionKind{field.Target}
	}

	generic, ok := d.GenericFKFor(d.ContainmentRef)
	if !ok {
		return nil
	}

	targets := make([]schema.GroupVersionKind, 0, len(generic.Members))
	for _, member := range generic.Members {
		targets = append(targets, member.Target)
	}

	return targets
}

// validateFieldMap checks the field map itself and every reference into it. It is where a
// spec name that does not round-trip to an API name is caught, which is the whole reason
// the map is explicit.
func (d Descriptor) validateFieldMap() error {
	if len(d.Fields) == 0 {
		return ErrNoFields
	}

	return errors.Join(d.validateFieldEntries(), d.validateSpecReferences(),
		d.validateDeferredFields(), d.validateCardinality())
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

		errs = append(errs, d.validateFieldEntry(field))

		seenSpec[field.Spec], seenAPI[field.API] = struct{}{}, struct{}{}
	}

	return errors.Join(errs...)
}

// validateFieldEntry is the checks on one entry that need no knowledge of the others, split out
// from the loop that also has to spot duplicates. Every one of them is a declaration NetBox
// would accept and then ignore, which is a PATCH loop rather than an error.
func (d Descriptor) validateFieldEntry(field Field) error {
	errs := make([]error, 0, 4)

	if slices.Contains(d.ReadOnly, field.API) {
		errs = append(errs, fmt.Errorf("%w: %s -> %s", ErrFieldReadOnly, field.Spec, field.API))
	}

	if d.Taggable && field.API == tagsColumn {
		errs = append(errs, fmt.Errorf("%w: %s -> %s", ErrTagsFieldOnTaggableKind, field.Spec, field.API))
	}

	if !slices.Contains(fieldClasses, field.Class) {
		errs = append(errs, fmt.Errorf("%w: %s is %q", ErrUnknownFieldClass, field.Spec, field.Class))
	}

	if !field.Class.Ref() && !field.Target.Empty() {
		errs = append(errs, fmt.Errorf("%w: %s -> %s", ErrTargetNotRef, field.Spec, field.Target))
	}

	if !field.Class.Ref() && field.CascadeOnDelete {
		errs = append(errs, fmt.Errorf("%w: %s", ErrCascadeNotRef, field.Spec))
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

	if d.ReservedKeySpec != "" && !d.declaresSpecField(d.ReservedKeySpec) {
		errs = append(errs, fmt.Errorf("%w: reserved key %s", ErrUnknownSpecField, d.ReservedKeySpec))
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

	// The cascade check, and the reason it is here rather than in a review checklist: an
	// owner reference on a foreign key NetBox does not cascade is a cluster-side cascade
	// with no server-side counterpart, which deletes the CR and leaves the row.
	//
	// For a union this asks whether *any* member cascades, and that is the strongest form a
	// boot check can take: a union with one cascading member is a legal containment parent
	// for the objects that use it, and refusing the descriptor would take the cascade away
	// from those too. A union where no member cascades can never produce an owner reference,
	// so naming it here is a modelling error no runtime state redeems (#214).
	if !d.cascadesOnDelete(d.ContainmentRef) {
		return fmt.Errorf("%w: %s", ErrContainmentNotCascade, d.ContainmentRef)
	}

	return nil
}

// validateDeferredFields ties the deferred API names back to the field map. Without it,
// `Deferred: primary_ip4` on a kind whose field map writes `primaryIp4` is accepted, and
// the field is then neither written at create time nor at any point after.
func (d Descriptor) validateDeferredFields() error {
	refAPI := make(map[string]struct{}, len(d.Fields))

	for _, field := range d.Fields {
		if field.Class.Ref() {
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

// validateCardinality rejects a to-many reference used where exactly one object is
// required.
//
// This is the check that survives making the comparison sets derived. A field can no longer
// be to-many for resolution and scalar for comparison -- one class decides both -- but it
// can still be declared to-many and then read by something that has only ever had room for
// one value, and both of those readers fail quietly rather than loudly:
//
//   - A natural-key filter carries one value. A to-many field renders none
//     (payload.filterValue takes scalars only), so the candidate is never applicable and
//     the engine waits forever for an identity it cannot build.
//   - A containment parent is one object, because Kubernetes garbage collection waits for
//     every owner reference (docs/decisions/0003-ownership-and-references.md rule 4).
func (d Descriptor) validateCardinality() error {
	errs := make([]error, 0, len(d.NaturalKeys)+1)

	for i, key := range d.NaturalKeys {
		for _, spec := range key.specFields() {
			if field, ok := d.FieldFor(spec); ok && field.Class.ToMany() {
				errs = append(errs, fmt.Errorf("%w: natural key %d reads %s", ErrToManyNaturalKey, i, spec))
			}
		}
	}

	if field, ok := d.FieldFor(d.ContainmentRef); ok && field.Class.ToMany() {
		errs = append(errs, fmt.Errorf("%w: %s", ErrContainmentToMany, d.ContainmentRef))
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

		columns := []string{generic.TypeField, generic.IDField}
		if generic.ToMany() {
			// The one field a to-many pair really does write, and the one an ordinary Field
			// entry could collide with: TypeField and IDField are filter names on such a pair
			// and reach no payload at all (GenericFKList).
			columns = append(columns, generic.List.APIField)
		}

		for _, column := range columns {
			if slices.ContainsFunc(d.Fields, func(f Field) bool { return f.API == column }) {
				errs = append(errs, fmt.Errorf("%w: %s is also an ordinary field", ErrGenericFKNotSpecField, column))
			}
		}
	}

	return errors.Join(errs...)
}
