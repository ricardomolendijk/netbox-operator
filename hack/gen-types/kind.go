package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Emission failures. Each is a sentinel so a caller classifies by type rather than by matching
// a message, and every one of them stops the whole run: a generator that skips what it cannot
// map ships a kind that is quietly missing a column.
var (
	errUnmappedField    = errors.New("no Go type for field")
	errUnknownEnum      = errors.New("field cites a choices class the IR does not define")
	errUnpinnableColumn = errors.New("null-pinned column has no filter class")
	errShortNameClash   = errors.New("two kinds claim one short name")
	errUnknownKind      = errors.New("no such kind in the IR")
	errFieldShadowed    = errors.New("field name collides with the embedded NetBoxObjectSpec")
	errUnstamped        = errors.New("CustomFieldable kind is missing from stampedObjectTypes")
	errNoNaturalKey     = errors.New("kind has no natural-key candidate")
	errEnumCollision    = errors.New("two choice values spell one Go constant")
	errUnknownLookup    = errors.New("natural key uses a lookup a natural key may not use")
	errUnresolvedUnion  = errors.New("polymorphic reference resolved no object types")
	errNoFieldMap       = errors.New("kind has no writable column")
	errUnknownDefer     = errors.New("deferral mode the engine does not implement")
	errUnknownStrategy  = errors.New("update strategy the engine does not implement")
	errRecreateOnPatch  = errors.New("recreateOn declared on a kind that updates in place")
	errDeferredUnmapped = errors.New("deferred column has no spec field")
	errKeyFieldUnmapped = errors.New("natural key reads a spec field the kind does not have")
)

// deferModes are the two registry.DeferMode values, as the constant names the template emits,
// plus `Never` -- which is not a mode but the way overrides.yaml switches the derived deferral
// of a self-reference back off. A closed set with no default: whether a reference reaches the
// create payload decides whether a fresh object is briefly wrong in NetBox, so a typo here has
// to be a failure rather than a silent Always.
var deferModes = map[string]string{
	"Always":       "DeferAlways",
	"IfUnresolved": "DeferIfUnresolved",
	"Never":        "",
}

// updateStrategies are the two registry.UpdateStrategy values. The zero value is deliberately
// absent from registry's own list, so the emitter states one explicitly on every kind.
var updateStrategies = map[string]string{
	"Patch":    "UpdatePatch",
	"Recreate": "UpdateRecreate",
}

// envelopeFields are the JSON names the embedded NetBoxObjectSpec already occupies. A kind
// column landing on one of them would shadow it silently, so it is a named failure instead
// (api/v1alpha1/netboxobject_types.go).
var envelopeFields = []string{"endpointRef", "onConflict", "deletionPolicy", "customFields"}

// skippedTypes are the Django classes no spec field is ever derived from. `tags` and
// `custom_fields` are the envelope's, declared once on NetBoxObjectSpec and driven by the
// descriptor's Taggable / CustomFieldable flags rather than by a per-kind column.
var skippedTypes = []string{
	"GenericRelation", "NetBoxTaggableManagerField", "CounterCacheField", "BigAutoField",
	"ImageField", "BinaryField", "ChoiceSetField", "GenericForeignKey",
}

// baseReadOnly are the columns every ChangeLoggedModel carries and the operator must never
// write. Writing one does not fail, it silently no-ops, so the next reconcile finds the same
// difference and PATCHes again forever.
func baseReadOnly() []string { return []string{"created", "last_updated", "url", "display"} }

// union is one polymorphic reference as descriptor data.
//
// Emitted as a literal rather than through registry.ScopeFK, and that is not a style choice:
// ScopeFK is the *scope* union, hard-coding `scope_type` / `scope_id` and the four dcim targets.
// A kind whose polymorphic reference is `assigned_object` or `component` has a different pair
// and different targets, and putting it through ScopeFK gives it the scope union's column names
// -- which then fails Validate, because its natural key matches on columns the field map does
// not have.
type union struct {
	Doc       []string
	SpecField string
	TypeField string
	IDField   string

	// GoType is the shared union struct the spec field is typed as: `ScopeRef` for NetBox's
	// `(scope_type, scope_id)` pair, and whatever `unionTypes` names for any other pair.
	//
	// Read from the view rather than written into the template, and that is the whole point:
	// a template that spelled `*ScopeRef` would be right for the four scoped kinds and wrong
	// for every other polymorphic reference in the catalogue -- a kind conditional with the
	// condition left out (specs/NBO-042-codegen-emitters.md).
	GoType string

	AllowedTypes []string
	Members      []unionMember
	Cached       []string
}

// unionMember is one target the type half may name.
type unionMember struct {
	Spec       string
	Target     string
	ObjectType string

	// Cascade is empty when the overrides do not state it. Left unstated rather than defaulted
	// to false, because "unstated" is what Validate holds to all-or-none: a false here would
	// make a forgotten member indistinguishable from one that genuinely does not cascade, and a
	// member wrongly reading "no cascade" leaves the CR behind when NetBox deletes the row
	// (#214).
	Cascade string
}

// kindView is everything the three per-kind templates read. There is no method on it that
// asks which kind it is: a template that needed to know would mean the fact belongs in the
// IR or in the descriptor, never in a conditional (specs/NBO-042-codegen-emitters.md).
type kindView struct {
	Header      header
	Kind        string
	Spec        string
	App         string
	Model       string
	Endpoint    string
	ObjectType  string
	SourceFile  string
	Bases       []string
	Plural      string
	ShortNames  string
	Columns     []printerColumn
	ExtraCEL    []celRule
	Fields      []field
	MapFields   []mapField
	NaturalKeys []naturalKey
	Unions      []union
	ReadOnly    []string
	Taggable    bool
	CustomField bool
	Containment string

	// Deferred are the columns the engine may leave out of a create payload. Derived for a
	// self-reference and declared for every other case -- see kindOverride.Deferred.
	Deferred []deferredField

	// Strategy is the registry.UpdateStrategy constant name, and RecreateOn the columns whose
	// change forces a recreate. Stated on every kind rather than defaulted in the template:
	// whether an update is destructive is too important to arrive by omission.
	Strategy   string
	RecreateOn []string

	// Retain makes spec.deletionPolicy default to Retain on this kind.
	Retain bool

	// DuplicateSpec is the spec field that lets one natural key match more than one row.
	DuplicateSpec string

	// FileStem is the `<app>_<kind>` basename every per-kind output shares.
	FileStem string

	// NeedsPtr reports that the descriptor states a union member's cascade, which is the one
	// import the registry template cannot decide from the struct alone. Computed here rather
	// than left to goimports so the emitted buffer is compilable before it is formatted --
	// an unused import is a compile error, and format.Source will not remove one.
	NeedsPtr bool

	// FuncName is the descriptor constructor's name. Not FileStem: an underscore in a Go
	// identifier is what staticcheck's ST1003 exists to catch.
	FuncName string
}

// mapField is one row of the descriptor's field table: the CR spec field and the NetBox
// column it is written under. The two differ often enough that an explicit table is the only
// safe form -- NetBox ignores a field name it does not know rather than rejecting it, so
// `markUtilized` sent verbatim would write nothing and report success.
type mapField struct {
	Spec    string
	API     string
	Class   string
	Target  string
	Cascade bool

	// Plain reports that the row is a spec name and an API name and nothing else, so it is
	// emitted on one line. Most rows are, and a nine-line struct literal per scalar column
	// turns a readable table into something nobody reads.
	Plain bool
}

// buildKind assembles one kind's view.
func (b *builder) buildKind(name string) (kindView, error) {
	kind, ok := b.ir.Kinds[name]
	if !ok {
		return kindView{}, fmt.Errorf("%w: %s", errUnknownKind, name)
	}

	over := b.over.of(name)

	view := kindView{
		Header: b.header, Kind: kubeKind(kind.Model), App: kind.App, Model: kind.Model,
		Endpoint: kind.Endpoint, ObjectType: kind.ObjectType, SourceFile: kind.SourceFile,
		Bases: kind.Bases, Columns: over.PrinterColumns, ExtraCEL: over.ExtraCEL,
		Containment: over.ContainmentRef,
		FileStem:    kind.App + "_" + strings.ToLower(kind.Model),
		FuncName:    kind.App + kind.Model,
	}
	view.Spec = view.Kind + "Spec"
	view.Plural = pluralise(strings.ToLower(view.Kind))

	shortNames, err := b.shortNames(name, over, kind)
	if err != nil {
		return kindView{}, err
	}

	view.ShortNames = shortNames
	view.Taggable, view.CustomField = mixesIn(kind, "TagsMixin"), mixesIn(kind, "CustomFieldsMixin")
	view.Unions = b.buildUnions(kind, over)

	for _, u := range view.Unions {
		for _, member := range u.Members {
			view.NeedsPtr = view.NeedsPtr || member.Cascade != ""
		}
	}

	if err := b.buildSpec(&view, kind, over); err != nil {
		return kindView{}, err
	}

	view.ReadOnly = append(b.readOnly(kind), over.ReadOnlyExtra...)
	view.Retain, view.DuplicateSpec = over.RetainOnDelete, over.DuplicateSpec
	view.RecreateOn = over.RecreateOn

	strategy, err := updateStrategy(kind, over)
	if err != nil {
		return kindView{}, err
	}

	view.Strategy = strategy

	deferred, err := b.buildDeferred(kind, over, view.NaturalKeys)
	if err != nil {
		return kindView{}, err
	}

	view.Deferred = deferred

	return view, validateView(kind, view)
}

// validateView refuses a view that would compile into a descriptor Validate() rejects at boot.
// Failing here names the kind and the missing fact; failing at boot names neither, and takes
// the whole operator down with it.
func validateView(kind irKind, view kindView) error {
	var errs []error

	// A union with no resolvable targets. The permitted `app_label.model` strings live only in
	// the serializer's ContentTypeField queryset, and the IR reports an empty list when that
	// queryset is not a literal it can evaluate -- `ContentType.objects.all()` and the
	// dynamically-built ones. There is nothing to guess: an empty AllowedTypes means the type
	// half accepts anything, which is the opposite of what the union is for.
	for _, u := range view.Unions {
		if len(u.AllowedTypes) == 0 || len(u.Members) == 0 {
			errs = append(errs, fmt.Errorf("%w: %s.%s's `%s` union resolved no object types; "+
				"the serializer's ContentTypeField queryset is not a literal the IR can read",
				errUnresolvedUnion, kind.App, kind.Model, u.SpecField))
		}
	}

	// A kind whose every column is read-only, inherited or part of a union pair. The spec
	// struct would be legal -- just the envelope and the unions -- but Descriptor.Validate
	// refuses an empty field map, so the file would not boot.
	if len(view.MapFields) == 0 {
		errs = append(errs, fmt.Errorf("%w: %s.%s has no writable column outside its union pair",
			errNoFieldMap, kind.App, kind.Model))
	}

	// A lookup candidate reading a spec field the kind does not have. Derived candidates cannot
	// hit this -- they are built from the column-to-spec map -- so it is always a declared one,
	// and it is the failure that matters most on a kind whose columns the IR is missing.
	//
	// extras.Tag is the worked example: `name` and `slug` are declared on taggit's `TagBase`,
	// which lives outside the NetBox source tree, so the AST walk never sees either and the IR
	// carries neither. Without this check the emitter would produce a NetBoxTag with a colour
	// and no name, whose declared `slug` key reads a field that does not exist -- and the
	// failure would be a Descriptor that cannot boot rather than one anybody read.
	for _, key := range view.NaturalKeys {
		errs = append(errs, unmappedKeySpecs(kind, view, key)...)
	}

	return errors.Join(errs...)
}

// unmappedKeySpecs reports the spec fields one candidate reads that the kind does not emit.
func unmappedKeySpecs(kind irKind, view kindView, key naturalKey) []error {
	specs := make([]string, 0, len(key.Fields)+len(key.NullFields))

	for _, f := range key.Fields {
		specs = append(specs, f.Spec)
	}

	for _, f := range key.NullFields {
		specs = append(specs, f.Spec)
	}

	errs := make([]error, 0, len(specs))

	for _, spec := range specs {
		// A union contributes three legal names: its spec field, and each half of the column
		// pair -- the halves because the engine reads a polymorphic key by column, exactly as
		// the hand-written descriptors do (internal/registry/ipam_vlangroup.go,
		// `{Filter: ScopeTypeField, Spec: ScopeTypeField}`).
		emitted := spec == "" ||
			slices.ContainsFunc(view.MapFields, func(m mapField) bool { return m.Spec == spec }) ||
			slices.ContainsFunc(view.Unions, func(u union) bool {
				return u.SpecField == spec || u.TypeField == spec || u.IDField == spec
			})

		if !emitted {
			errs = append(errs, fmt.Errorf("%w: %s.%s keys on `%s`, which no column of that kind "+
				"produces; either the IR is missing the column or the naturalKeys entry in "+
				"overrides.yaml names the wrong spec field",
				errKeyFieldUnmapped, kind.App, kind.Model, spec))
		}
	}

	return errs
}

// buildSpec fills the spec fields, the descriptor's field table and the natural keys, which
// share one walk because the natural keys need the column-to-spec-field map the walk builds.
func (b *builder) buildSpec(view *kindView, kind irKind, over kindOverride) error {
	specOf := map[string]string{}

	for _, f := range kind.Fields {
		if b.skip(f, over, unionIDColumns(view.Unions)) {
			continue
		}

		emitted, ok, err := b.buildField(kind, f)
		if err != nil {
			return err
		}

		if !ok {
			continue
		}

		jsonName := b.jsonFieldName(f)
		if slices.Contains(envelopeFields, jsonName) {
			return fmt.Errorf("%w: %s.%s -> %s; declare it inherited in overrides.yaml",
				errFieldShadowed, kind.App, f.Name, jsonName)
		}

		specOf[f.Name] = jsonName
		view.Fields = append(view.Fields, emitted)
		view.MapFields = append(view.MapFields, b.mapField(f, jsonName, over))
	}

	for _, u := range view.Unions {
		specOf[u.TypeField], specOf[u.IDField] = u.TypeField, u.IDField
	}

	keys, err := b.buildNaturalKeys(kind, specOf)
	view.NaturalKeys = keys

	return err
}

// mapField is the descriptor row for one emitted field.
func (b *builder) mapField(f irField, jsonName string, over kindOverride) mapField {
	row := mapField{Spec: jsonName, API: f.Name, Cascade: over.ContainmentRef == jsonName}

	switch f.Class {
	case "Ref":
		row.Class, row.Target = "ClassRefOne", b.names.refTypeName(f.Ref.Target)
	case "M2M":
		row.Class, row.Target = "ClassRefMany", b.names.refTypeName(f.Ref.Target)
	case "ObjectTypeList":
		row.Class = "ClassObjectTypeList"
	case "Array":
		row.Class = "ClassArray"
	}

	row.Plain = row.Class == "" && row.Target == "" && !row.Cascade

	return row
}

// deferredField is one row of the descriptor's Deferred list.
type deferredField struct {
	// API is the NetBox column as written, not the filter name: the engine strips it from a
	// payload, and a payload is keyed on columns.
	API string

	// Mode is the registry.DeferMode constant name.
	Mode string

	// Doc says why the column is deferred, since why is the whole of the reviewable content
	// here -- the column name is already in the code.
	Doc []string
}

// buildDeferred is the columns the engine may leave out of a create payload: every
// self-reference the kind's identity does not depend on, derived, plus whatever the overrides
// declare.
func (b *builder) buildDeferred(kind irKind, over kindOverride, keys []naturalKey) ([]deferredField, error) {
	out := make([]deferredField, 0, len(kind.Fields))

	var errs []error

	for _, f := range kind.Fields {
		row, deferred, err := b.deferralOf(kind, over, keys, f)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if deferred {
			out = append(out, row)
		}
	}

	return out, errors.Join(append(errs, unmappedDeferrals(kind, over, out)...)...)
}

// deferralOf is one column's deferral, or reports that it has none.
//
// The derived mode is IfUnresolved and never Always, and the difference is visible state
// rather than taste. Stripping a resolved `parent` would create the object top-level for one
// pass, where it can adopt an unrelated top-level object of the same name -- and the follow-up
// PATCH would then reparent that object (NBO-015, internal/reconciler/deferred.go).
//
// A self-reference a natural-key candidate matches on is *not* deferred, and that exclusion is
// what makes the derivation safe rather than merely plausible. dcim.DeviceRole keys on
// `(parent_id, slug)` and on `slug` with `parent_id` pinned null, so stripping `parent` from
// the create would change the identity the lookup had already decided on -- which is why
// registry.Descriptor.Validate refuses the pair outright (ErrDeferredNaturalKey), and why a
// child role writes nothing and waits instead (docs/reference/netboxdevicerole.md).
func (b *builder) deferralOf(
	kind irKind, over kindOverride, keys []naturalKey, f irField,
) (deferredField, bool, error) {
	mode, stated := over.Deferred[f.Name]

	if !stated {
		self := f.Ref != nil && f.Ref.Self && f.Class == "Ref"
		if !self || matchedByKey(keys, b.jsonFieldName(f)) {
			return deferredField{}, false, nil
		}

		mode = "IfUnresolved"
	}

	constant, known := deferModes[mode]
	if !known {
		return deferredField{}, false, fmt.Errorf(
			"%w: %s.%s is deferred %q; it is Always, IfUnresolved or Never",
			errUnknownDefer, kind.App, f.Name, mode)
	}

	// `Never`: the column is a self-reference whose derived deferral the overrides switch off,
	// so it is emitted as an ordinary reference and not listed at all.
	if constant == "" {
		return deferredField{}, false, nil
	}

	doc := fmt.Sprintf("`%s` is a foreign key to %s -- this kind's own model -- so it cannot "+
		"be satisfied on create until the parent object exists.", f.Name, kind.App+"."+kind.Model)
	if stated {
		doc = fmt.Sprintf("`%s` is deferred %s, declared in overrides.yaml.", f.Name, mode)
	}

	return deferredField{API: f.Name, Mode: constant, Doc: wrap(doc)}, true, nil
}

// unmappedDeferrals reports a declared column that names nothing the kind has.
//
// It is the worst failure this file can produce, because it looks like it worked: the deferral
// silently does not happen and the create carries a reference the server cannot satisfy.
func unmappedDeferrals(kind irKind, over kindOverride, emitted []deferredField) []error {
	declared := make([]string, 0, len(over.Deferred))
	for column := range over.Deferred {
		declared = append(declared, column)
	}

	slices.Sort(declared)

	errs := make([]error, 0, len(declared))

	for _, column := range declared {
		mapped := slices.ContainsFunc(emitted,
			func(d deferredField) bool { return d.API == column })

		if mapped || over.Deferred[column] == "Never" {
			continue
		}

		errs = append(errs, fmt.Errorf("%w: %s.%s is deferred in overrides.yaml and is not a "+
			"column of that kind", errDeferredUnmapped, kind.App, column))
	}

	return errs
}

// matchedByKey reports whether any lookup candidate reads or pins the given spec field.
func matchedByKey(keys []naturalKey, spec string) bool {
	for _, key := range keys {
		for _, f := range key.Fields {
			if f.Spec == spec {
				return true
			}
		}

		for _, f := range key.NullFields {
			if f.Spec == spec {
				return true
			}
		}
	}

	return false
}

// updateStrategy resolves the kind's update strategy, defaulting to Patch: every kind PATCHes
// unless its identity lives somewhere a PATCH cannot reach.
func updateStrategy(kind irKind, over kindOverride) (string, error) {
	declared := over.UpdateStrategy
	if declared == "" {
		declared = "Patch"
	}

	constant, known := updateStrategies[declared]
	if !known {
		return "", fmt.Errorf("%w: %s.%s declares %q; it is Patch or Recreate",
			errUnknownStrategy, kind.App, kind.Model, declared)
	}

	if declared == "Patch" && len(over.RecreateOn) > 0 {
		return "", fmt.Errorf("%w: %s.%s lists recreateOn without updateStrategy: Recreate",
			errRecreateOnPatch, kind.App, kind.Model)
	}

	return constant, nil
}

// skip reports whether a column yields no spec field: read-only in the API, off the write
// path, a denormalised cache, an envelope column, or one the overrides remove.
func (b *builder) skip(f irField, over kindOverride, unionIDColumns []string) bool {
	switch {
	case f.ReadOnly, !f.InWritePath, strings.HasPrefix(f.Name, "_"):
		return true
	case slices.Contains(skippedTypes, f.Type), f.Class == "ReverseRelation", f.Class == "JSON":
		return true
	case f.Class == "GenericFKType", f.Class == "GenericFK":
		return true
	case slices.Contains(over.Inherited, f.Name), slices.Contains(over.Omit, f.Name):
		return true
	case slices.Contains(unionIDColumns, f.Name):
		return true
	case f.Name == "id" || f.Name == "url" || f.Name == "display" || f.Name == "owner":
		return true
	}

	return slices.Contains(baseReadOnly(), f.Name)
}

// buildUnions is the polymorphic references as descriptor data, one per `(x_type, x_id)` pair
// the IR carries.
func (b *builder) buildUnions(kind irKind, over kindOverride) []union {
	out := make([]union, 0, len(kind.Fields))

	for _, f := range kind.Fields {
		if f.Class != "GenericFKType" {
			continue
		}

		spec := strings.TrimSuffix(f.Name, "_type")

		out = append(out, union{
			Doc: wrap(fmt.Sprintf("`(%s, %s)` is one reference written as two columns and diffed "+
				"as a unit, so moving the object between target types is one change and one PATCH "+
				"carrying both keys. The permitted object types are the serializer's own "+
				"ContentTypeField queryset (%s), which is the only place they are written down.",
				f.Name, spec+"_id", kind.SourceFile)),
			SpecField: spec, TypeField: f.Name, IDField: spec + "_id",
			GoType:       unionGoType(spec, over),
			AllowedTypes: f.ObjectTypes, Members: b.unionMembers(f, over),
			Cached: b.cachedColumns(kind, spec),
		})
	}

	return out
}

// unionGoType is the Go type behind one polymorphic pair, defaulting to the pair's own name
// with a `Ref` suffix -- so NetBox's `scope_type` / `scope_id` is `ScopeRef` with nothing
// declared. The default is keyed on the NetBox column and never on the kind, which is what
// keeps it a fact about NetBox rather than a per-kind branch; a pair whose shared type is
// spelled differently names it in `unionTypes`.
func unionGoType(spec string, over kindOverride) string {
	if declared, ok := over.UnionTypes[spec]; ok {
		return declared
	}

	return title(spec) + "Ref"
}

// unionMembers are the CR spec fields that select each permitted target.
func (b *builder) unionMembers(f irField, over kindOverride) []unionMember {
	out := make([]unionMember, 0, len(f.ObjectTypes))

	for _, objectType := range f.ObjectTypes {
		app, model, _ := strings.Cut(objectType, ".")

		kind, known := b.ir.Kinds[app+"."+strings.ToLower(model)]
		if !known {
			kind = b.kindByObjectType(objectType)
		}

		spec := b.names.jsonName(kind.Model) + "Ref"
		if kind.Model == "" {
			spec = b.names.jsonName(model) + "Ref"
		}

		member := unionMember{Spec: spec, Target: b.names.refTypeName(app + "." + kind.Model), ObjectType: objectType}
		if cascade, stated := over.Cascades[spec]; stated {
			member.Cascade = fmt.Sprintf("%t", cascade)
		}

		b.refs[member.Target] = app + "." + kind.Model
		out = append(out, member)
	}

	return out
}

// kindByObjectType finds a kind by its `app_label.model` spelling, which is the model name
// lower-cased and unpunctuated -- `dcim.sitegroup`, never `dcim.SiteGroup`.
func (b *builder) kindByObjectType(objectType string) irKind {
	for _, kind := range b.ir.Kinds {
		if kind.ObjectType == objectType {
			return kind
		}
	}

	return irKind{}
}

// cachedColumns are the denormalised columns NetBox maintains from a union's pair. Derived
// from the `_`-prefixed columns the model actually declares, so a kind that has the pair and
// no caches -- ipam.VLANGroup declares both columns on the model itself -- gets an empty list
// rather than four columns its table does not have.
func (b *builder) cachedColumns(kind irKind, spec string) []string {
	var out []string

	for _, f := range kind.Fields {
		if strings.HasPrefix(f.Name, "_") && f.Ref != nil && spec != "" {
			out = append(out, f.Name)
		}
	}

	return out
}

// unionIDColumns are the id halves of a kind's polymorphic pairs. They carry no spec field of
// their own: the pair is written from the union's single spec field, so a `scopeID` next to
// `scope` would be a second way to write the same column and the two could disagree.
func unionIDColumns(unions []union) []string {
	out := make([]string, 0, len(unions))

	for _, u := range unions {
		out = append(out, u.IDField)
	}

	return out
}

// readOnly is every column the operator must never write: the ChangeLoggedModel four, the
// columns the serializer marks read-only, every `_`-prefixed cache and every counter.
func (b *builder) readOnly(kind irKind) []string {
	out := baseReadOnly()

	for _, f := range kind.Fields {
		cached := strings.HasPrefix(f.Name, "_")
		if !f.ReadOnly && !cached && f.Type != "CounterCacheField" {
			continue
		}

		if f.Class == "ReverseRelation" || f.Class == "GenericFK" || slices.Contains(out, f.Name) {
			continue
		}

		out = append(out, f.Name)
	}

	slices.Sort(out[len(baseReadOnly()):])

	return dedupe(out)
}

// shortNames is the `kubectl get` abbreviation, defaulting to `nb` + the model. A clash is a
// hard failure: `kubectl get nbip` has to be unambiguous, which is the entire point of
// ADR-0001.
func (b *builder) shortNames(name string, over kindOverride, kind irKind) (string, error) {
	names := over.ShortNames
	if len(names) == 0 {
		names = []string{"nb" + strings.ToLower(kind.Model)}
	}

	for _, short := range names {
		if owner, taken := b.shorts[short]; taken && owner != name {
			return "", fmt.Errorf("%w: %q is claimed by %s and %s", errShortNameClash, short, owner, name)
		}

		b.shorts[short] = name
	}

	return strings.Join(names, ";"), nil
}

// mixesIn reports whether a base is in the kind's MRO as the IR flattened it.
func mixesIn(kind irKind, base string) bool {
	for _, declared := range kind.Bases {
		if declared == base || slices.Contains(baseMixins[declared], base) {
			return true
		}
	}

	return false
}

// baseMixins is what NetBox's own abstract bases mix in, so a kind declaring `PrimaryModel`
// is known to be taggable without the IR flattening every ancestor
// (docs/netbox-schema.md, the `netbox.*` entries).
var baseMixins = map[string][]string{
	"PrimaryModel":                  {"TagsMixin", "CustomFieldsMixin"},
	"OrganizationalModel":           {"TagsMixin", "CustomFieldsMixin"},
	"NestedGroupModel":              {"TagsMixin", "CustomFieldsMixin"},
	"NetBoxModel":                   {"TagsMixin", "CustomFieldsMixin"},
	"ComponentModel":                {"TagsMixin", "CustomFieldsMixin"},
	"ModularComponentModel":         {"TagsMixin", "CustomFieldsMixin"},
	"ComponentTemplateModel":        {"CustomFieldsMixin"},
	"ModularComponentTemplateModel": {"CustomFieldsMixin"},
}

// pluralise is the CRD's plural, which is the lower-cased kind with an `es` where English
// needs one. It exists only for the RBAC markers; controller-gen derives its own.
func pluralise(kind string) string {
	for _, suffix := range []string{"s", "x", "ch", "sh"} {
		if strings.HasSuffix(kind, suffix) {
			return kind + "es"
		}
	}

	if strings.HasSuffix(kind, "y") {
		return strings.TrimSuffix(kind, "y") + "ies"
	}

	return kind + "s"
}

// dedupe removes repeats while keeping first-seen order.
func dedupe(in []string) []string {
	out := make([]string, 0, len(in))

	for _, item := range in {
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}

	return out
}
