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
)

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
	Doc          []string
	SpecField    string
	TypeField    string
	IDField      string
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

	return errors.Join(errs...)
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

	return row
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
				"as a unit. The permitted object types are the serializer's own ContentTypeField "+
				"queryset (%s), which is the only place they are written down.",
				f.Name, spec+"_id", kind.SourceFile)),
			SpecField: spec, TypeField: f.Name, IDField: spec + "_id",
			AllowedTypes: f.ObjectTypes, Members: b.unionMembers(f, over),
			Cached: b.cachedColumns(kind, spec),
		})
	}

	return out
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
