package registry

// The NBO-060 coverage audit: what of NetBox this operator does not cover yet, derived
// rather than written down.
//
// It is a test and not a script because the implemented set lives in this package -- one
// init() per kind registering a Descriptor -- and the only honest way to read it is to ask
// the registry. A Python script would have to parse Go, and a parser that guesses wrong
// reports a kind as missing that ships, which is the failure mode the audit exists to rule
// out.
//
// The other input is the schema IR (irPath below, docs/regenerating.md):
// 134 kinds, each with its endpoint, its object type, its fields with writability and
// requiredness, and its natural-key candidates. The audit is a join between the two, plus
// the exclusions file (exclusionsPath below) for the handful of facts neither source can
// hold -- a deliberate decision not to implement something.
//
// The gate is docs/coverage.md: the audit regenerates it and fails when the committed copy
// differs, so a kind quietly dropped from the registry, a column quietly dropped from a
// field map, and a NetBox version that adds models all show up as a reviewable diff that
// somebody has to commit on purpose. `make coverage` rewrites it.
//
// A plain "no uncovered endpoint" gate cannot work here and would be a lie if it did: 93 of
// the 112 in-scope endpoints have no Kind yet, that is what M9 and M10 are for, and a new
// NetBox minor legitimately adds models. So the invariant is "the numbers are what the last
// commit said they were", plus four things that are bugs at any coverage level and fail on
// their own: a missing required column, a stale exclusion, a registry entry that disagrees
// with the schema, and a Taggable/CustomFieldable flag the model does not bear out.

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	irPath          = "../../hack/testdata/ir-4.6.8.json.gz"
	exclusionsPath  = "../../hack/coverage-exclusions.yaml"
	coverageDocPath = "../../docs/coverage.md"
)

// updateCoverage rewrites docs/coverage.md instead of comparing against it. `make coverage`
// passes it; CI does not, so the committed document is the gate.
var updateCoverage = flag.Bool("update", false, "rewrite docs/coverage.md from this run")

// envelopeColumns are the columns no field map declares because the engine owns them: the
// four every ChangeLoggedModel carries plus the two hyperlinks. `custom_fields` is not here
// -- it is covered by Descriptor.CustomFieldable, which the audit checks against the model.
var envelopeColumns = []string{"id", "url", "display", "display_url", "created", "last_updated"}

// irRef is a foreign key's or many-to-many's target, `dcim.Region`. Present on both classes.
type irRef struct {
	Target string `json:"target"`
}

// irSQL is the subset of a column's Django field kwargs the audit reads.
type irSQL struct {
	// Unique is the column-level `unique=True`. It is the uniqueness `natural_keys` does not
	// carry: that list is built from `meta.constraints` alone, so a model whose identity is
	// one UNIQUE column -- every `OrganizationalModel.slug` -- has an empty one.
	Unique bool `json:"unique"`
}

type irField struct {
	Name        string `json:"name"`
	Class       string `json:"class"`
	Type        string `json:"type"`
	InWritePath bool   `json:"in_write_path"`
	ReadOnly    bool   `json:"read_only"`
	Required    bool   `json:"required"`
	Ref         *irRef `json:"ref"`
	SQL         irSQL  `json:"sql"`

	// DeclaredBy is the mixin or base class the column comes from, or empty when the model
	// declares it itself. It is what tells `tags` from `tags`: see mixesInTagsMixin.
	DeclaredBy string `json:"declared_by"`
}

// irNullField is one column a natural-key candidate pins to null.
type irNullField struct {
	Column string `json:"column"`
}

type irNaturalKey struct {
	Constraint string        `json:"constraint"`
	NullFields []irNullField `json:"null_fields"`

	// Unusable is the IR's reason the candidate cannot be issued as a query, or empty.
	Unusable string `json:"unusable"`
}

type irKind struct {
	Endpoint    string         `json:"endpoint"`
	ObjectType  string         `json:"object_type"`
	Fields      []irField      `json:"fields"`
	NaturalKeys []irNaturalKey `json:"natural_keys"`

	// WritePath is the serializer's Meta.fields, in order. It is the only place
	// `custom_fields` appears: the ORM column behind it is `custom_field_data`, so the
	// field list has no entry under the name the API takes.
	WritePath []string `json:"write_path"`
}

type netboxIR struct {
	NetBoxVersion         string            `json:"netbox_version"`
	Kinds                 map[string]irKind `json:"kinds"`
	EndpointsWithoutModel []string          `json:"endpoints_without_model"`
	Unresolved            []map[string]any  `json:"unresolved"`
}

// columnEntry is one hand-written statement about a column an implemented Kind does not
// map. `kind: "*"` says the statement is about the column on every Kind that has it, which
// is what keeps `tags` one line instead of eighteen.
type columnEntry struct {
	Kind   string `json:"kind"`
	Column string `json:"column"`
	Reason string `json:"reason"`
}

// matches reports whether row is one the entry speaks for.
func (e columnEntry) matches(row columnRow, status string) bool {
	return (e.Kind == "*" || e.Kind == row.Model) && e.Column == row.Column && row.Status == status
}

// endpointEntry is one endpoint the operator will not implement.
type endpointEntry struct {
	Endpoint string `json:"endpoint"`
	Reason   string `json:"reason"`
}

type exclusions struct {
	Endpoints []endpointEntry `json:"endpoints"`

	// Columns are deliberate omissions: the audit stops calling them gaps.
	Columns []columnEntry `json:"columns"`

	// Notes annotate a gap without excusing it -- which ticket owes the column, or that
	// none does. A note leaves the row MISSING, so the count a reader looks at does not
	// move because somebody explained it.
	Notes []columnEntry `json:"notes"`
}

// Statuses. MISSING is upper-case in the document for the same reason it is here: it is the
// one a reader is looking for.
const (
	statusImplemented = "implemented"
	statusExcluded    = "excluded"
	statusBlocked     = "blocked"
	statusMissing     = "MISSING"

	// keyUsable and keyUnusable are the two verdicts on a natural-key candidate the IR
	// calls unusable, re-derived against what #216 lets the operator actually send.
	keyUsable   = "usable via #216"
	keyUnusable = "unusable"
)

type endpointRow struct {
	Endpoint string
	Model    string
	Kind     string
	Status   string
	Detail   string
}

type columnRow struct {
	Model    string
	Column   string
	Class    string
	Required bool
	Status   string
	Detail   string
}

type keyRow struct {
	Model       string
	Constraint  string
	Status      string
	Detail      string
	Implemented bool
}

type audit struct {
	ir        *netboxIR
	endpoints []endpointRow
	columns   []columnRow
	keys      []keyRow

	// covered counts the writable columns an implemented Kind does declare, which is the
	// denominator the missing counts only make sense against.
	covered int

	unresolvedOnImplemented int
}

// TestCoverage runs the audit, asserts the invariants that hold at any coverage level, and
// gates docs/coverage.md.
func TestCoverage(t *testing.T) {
	ir := loadIR(t)
	excl := loadExclusions(t)

	a := newAudit(t, ir, excl)

	for _, problem := range a.staleDeclarations(excl) {
		t.Errorf("%s: %s", exclusionsPath, problem)
	}

	a.checkRequiredColumns(t)
	a.checkRegistryAgreesWithSchema(t)

	gateDocument(t, a.render())
}

func loadIR(t *testing.T) *netboxIR {
	t.Helper()

	f, err := os.Open(irPath)
	if err != nil {
		t.Fatalf("open %s: %v", irPath, err)
	}
	defer f.Close() //nolint:errcheck // read-only

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", irPath, err)
	}
	defer gz.Close() //nolint:errcheck // read-only

	ir := &netboxIR{}
	if err := json.NewDecoder(gz).Decode(ir); err != nil {
		t.Fatalf("decode %s: %v", irPath, err)
	}

	if len(ir.Kinds) == 0 {
		t.Fatalf("%s carries no kinds", irPath)
	}

	return ir
}

func loadExclusions(t *testing.T) *exclusions {
	t.Helper()

	raw, err := os.ReadFile(exclusionsPath)
	if err != nil {
		t.Fatalf("read %s: %v", exclusionsPath, err)
	}

	excl := &exclusions{}
	if err := yaml.UnmarshalStrict(raw, excl); err != nil {
		t.Fatalf("parse %s: %v", exclusionsPath, err)
	}

	return excl
}

// newAudit joins the IR against the registry.
func newAudit(t *testing.T, ir *netboxIR, excl *exclusions) *audit {
	t.Helper()

	a := &audit{ir: ir}

	byObjectType := map[string]string{}
	for model, kind := range ir.Kinds {
		byObjectType[kind.ObjectType] = model
	}

	// Implemented model names, so a reference's target can be asked whether it has a Kind.
	implemented := map[string]Descriptor{}

	for _, d := range List() {
		model, known := byObjectType[d.ObjectType]
		if !known {
			t.Errorf("descriptor %s: object type %q is not in %s; the registry and the schema disagree about what exists",
				d.GVK.Kind, d.ObjectType, irPath)

			continue
		}

		implemented[model] = d
	}

	excludedEndpoints := map[string]string{}
	for _, e := range excl.Endpoints {
		excludedEndpoints[e.Endpoint] = e.Reason
	}

	a.buildEndpoints(ir, implemented, excludedEndpoints)
	a.buildColumns(ir, implemented, excl)
	a.buildKeys(ir, implemented)

	for _, row := range ir.Unresolved {
		if _, ok := implemented[fmt.Sprint(row["kind"])]; ok {
			a.unresolvedOnImplemented++
		}
	}

	return a
}

func (a *audit) buildEndpoints(ir *netboxIR, implemented map[string]Descriptor, excluded map[string]string) {
	for model, kind := range ir.Kinds {
		row := endpointRow{Endpoint: kind.Endpoint, Model: model, Status: statusMissing}

		if d, ok := implemented[model]; ok {
			row.Kind, row.Status = d.GVK.Kind, statusImplemented
		}

		if reason, ok := excluded[kind.Endpoint]; ok {
			row.Status, row.Detail = statusExcluded, reason
		}

		a.endpoints = append(a.endpoints, row)
	}

	// The endpoints with no model at all: RQ introspection views and dcim/connected-device.
	// They are in the table because 138 has to add up, and because an endpoint that stops
	// being model-less is a Kind about to be missed.
	for _, endpoint := range ir.EndpointsWithoutModel {
		row := endpointRow{Endpoint: endpoint, Model: "-", Status: statusMissing}
		if reason, ok := excluded[endpoint]; ok {
			row.Status, row.Detail = statusExcluded, reason
		}

		a.endpoints = append(a.endpoints, row)
	}

	slices.SortFunc(a.endpoints, func(x, y endpointRow) int { return strings.Compare(x.Endpoint, y.Endpoint) })
}

// buildColumns diffs each implemented Kind's field map against its model's writable
// columns.
//
// A column a reference points at is classified rather than judged: if the target model has
// no Kind, the column cannot be written at all and the audit says "blocked" and names what
// it waits on. That derivation is why the exclusions file is six lines long instead of
// twenty-three -- `owner` on every kind is one FK to users.Owner, whose endpoint is
// excluded, and no human had to write that down.
func (a *audit) buildColumns(ir *netboxIR, implemented map[string]Descriptor, excl *exclusions) {
	for model, d := range implemented {
		mapped := mappedColumns(d)

		for _, f := range ir.Kinds[model].Fields {
			if !f.InWritePath || f.ReadOnly || slices.Contains(envelopeColumns, f.Name) {
				continue
			}

			if f.Name == "custom_fields" && d.CustomFieldable {
				a.covered++

				continue
			}

			if slices.Contains(mapped, f.Name) {
				a.covered++

				continue
			}

			a.columns = append(a.columns, classifyColumn(ir, model, f, implemented, excl))
		}
	}

	slices.SortFunc(a.columns, func(x, y columnRow) int {
		if c := strings.Compare(x.Column, y.Column); c != 0 {
			return c
		}

		return strings.Compare(x.Model, y.Model)
	})
}

func classifyColumn(ir *netboxIR, model string, f irField, implemented map[string]Descriptor, excl *exclusions) columnRow {
	row := columnRow{Model: model, Column: f.Name, Class: f.Class, Required: f.Required, Status: statusMissing}

	for _, e := range excl.Columns {
		if e.matches(row, statusMissing) {
			row.Status, row.Detail = statusExcluded, e.Reason

			return row
		}
	}

	if f.Ref != nil && f.Ref.Target != "" {
		if _, ok := implemented[f.Ref.Target]; !ok {
			row.Status, row.Detail = statusBlocked, blockedOn(ir, excl, f.Ref.Target)

			return row
		}
	}

	for _, e := range excl.Notes {
		if e.matches(row, statusMissing) {
			row.Detail = e.Reason

			break
		}
	}

	return row
}

// blockedOn says what a reference is waiting for. A target whose own endpoint is excluded is
// not waiting for anything: it is a column this operator will never write, and saying so is
// the difference between a queue and a dead end. Derived, so `owner` -- eighteen rows
// pointing at users.Owner, whose endpoint users/* excludes -- needs nothing hand-written.
func blockedOn(ir *netboxIR, excl *exclusions, target string) string {
	endpoint := ir.Kinds[target].Endpoint

	for _, e := range excl.Endpoints {
		if e.Endpoint == endpoint {
			return "`" + target + "` is an excluded endpoint, so nothing will ever write this"
		}
	}

	return "waits on a Kind for `" + target + "`"
}

// mappedColumns are every NetBox column an implemented Kind accounts for: the field map,
// the read-only list, and the two halves plus the cached columns of each generic FK.
func mappedColumns(d Descriptor) []string {
	mapped := make([]string, 0, len(d.Fields)+len(d.ReadOnly))
	for _, f := range d.Fields {
		mapped = append(mapped, f.API)
	}

	mapped = append(mapped, d.ReadOnly...)

	for _, g := range d.GenericFKs {
		mapped = append(mapped, g.TypeField, g.IDField)
		mapped = append(mapped, g.Cached...)
	}

	return mapped
}

// buildKeys re-derives the IR's unusable natural-key verdict against what the operator can
// now actually send.
//
// The IR calls a candidate unusable when a column it pins to null has no `__empty` filter
// parameter, which was true of the spelling #206 proposed. #216 answered it differently: a
// foreign key's filter is a MultipleChoiceFilter, so the pin goes over the wire as
// `?parent_id=null` through django-filter's own null_value, and NullColumnRef is where that
// is written down. So a candidate whose null pins are all foreign keys is expressible today,
// and the audit says so structurally -- by asking the IR for each pinned column's class --
// rather than by matching the reason string.
func (a *audit) buildKeys(ir *netboxIR, implemented map[string]Descriptor) {
	for model, kind := range ir.Kinds {
		_, isImplemented := implemented[model]

		for _, key := range kind.NaturalKeys {
			if key.Unusable == "" {
				continue
			}

			row := keyRow{
				Model: model, Constraint: key.Constraint,
				Status: keyUnusable, Detail: key.Unusable, Implemented: isImplemented,
			}

			if allRefPins(kind, key) {
				row.Status = keyUsable
				row.Detail = "null pins are all foreign keys: `?<column>_id=null` (registry.NullColumnRef)"
			}

			a.keys = append(a.keys, row)
		}
	}

	slices.SortFunc(a.keys, func(x, y keyRow) int {
		if c := strings.Compare(x.Model, y.Model); c != 0 {
			return c
		}

		return strings.Compare(x.Constraint, y.Constraint)
	})
}

// allRefPins reports whether a candidate pins at least one column to null and every column
// it pins is a foreign key.
func allRefPins(kind irKind, key irNaturalKey) bool {
	if len(key.NullFields) == 0 {
		return false
	}

	for _, pin := range key.NullFields {
		index := slices.IndexFunc(kind.Fields, func(f irField) bool { return f.Name == pin.Column })
		if index < 0 || kind.Fields[index].Class != "Ref" {
			return false
		}
	}

	return true
}

// staleDeclarations reports every entry in the exclusions file the schema no
// longer bears out. A stale exclusion is as much a bug as a missing kind: it is a claim
// about NetBox that stopped being true, and it silently excuses whatever moved in behind it.
//
// It returns the problems rather than failing, so the same code can be unit-tested without
// a fake *testing.T -- and so one bad entry does not hide the next.
func (a *audit) staleDeclarations(excl *exclusions) []string {
	problems := []string{}

	for _, e := range excl.Endpoints {
		index := slices.IndexFunc(a.endpoints, func(r endpointRow) bool { return r.Endpoint == e.Endpoint })
		switch {
		case index < 0:
			problems = append(problems, fmt.Sprintf("endpoint %q is excluded but is not in %s any more", e.Endpoint, irPath))
		case a.endpoints[index].Kind != "":
			problems = append(problems, fmt.Sprintf("endpoint %q is excluded and implemented as %s; one of the two is wrong",
				e.Endpoint, a.endpoints[index].Kind))
		case e.Reason == "":
			problems = append(problems, fmt.Sprintf("endpoint %q is excluded with no reason", e.Endpoint))
		}
	}

	problems = append(problems, a.staleColumnEntries(excl.Columns, statusExcluded, "excluded")...)

	return append(problems, a.staleColumnEntries(excl.Notes, statusMissing, "annotated")...)
}

// staleColumnEntries reports every column entry no row bears out. Both sections are checked
// the same way and for the same reason: an entry that matches nothing is a sentence about a
// column that has moved, and the next reader believes it.
func (a *audit) staleColumnEntries(entries []columnEntry, status, verb string) []string {
	problems := []string{}

	for _, e := range entries {
		if !slices.ContainsFunc(a.columns, func(r columnRow) bool { return e.matches(r, status) }) {
			problems = append(problems, fmt.Sprintf(
				"%s.%s is %s, but no %s column of an implemented Kind matches "+
					"-- either a Kind now maps it, or the column is gone, or the Kind does not ship",
				e.Kind, e.Column, verb, status))
		}

		if e.Reason == "" {
			problems = append(problems, fmt.Sprintf("%s.%s is %s with no reason", e.Kind, e.Column, verb))
		}
	}

	return problems
}

// checkRequiredColumns fails on a writable column NetBox requires on create that an
// implemented Kind does not map. Unlike an absent optional column, that is not a gap in the
// catalogue: it is a Kind that cannot create its object at all.
func (a *audit) checkRequiredColumns(t *testing.T) {
	t.Helper()

	for _, row := range a.columns {
		if row.Required && row.Status != statusExcluded {
			t.Errorf("%s.%s is required on create and no spec field writes it (%s)", row.Model, row.Column, row.Status)
		}
	}
}

// checkRegistryAgreesWithSchema fails when a Descriptor states something about its model
// that the schema contradicts.
//
// Taggable and CustomFieldable are documented as not derivable, which is true of *deriving*
// them -- the mixin is not a column and the flag gates the provenance stamp. It is not true
// of checking them: `tags` and `custom_fields` are writable columns in the REST schema on
// exactly the models that mix the two mixins in, so a flag that disagrees with the IR is
// either a stamp that vanishes on write or one NetBox rejects.
func (a *audit) checkRegistryAgreesWithSchema(t *testing.T) {
	t.Helper()

	byObjectType := map[string]irKind{}
	for _, kind := range a.ir.Kinds {
		byObjectType[kind.ObjectType] = kind
	}

	for _, d := range List() {
		kind, known := byObjectType[d.ObjectType]
		if !known {
			continue // already reported by newAudit
		}

		if kind.Endpoint != d.Endpoint {
			t.Errorf("descriptor %s: endpoint %q, schema says %q", d.GVK.Kind, d.Endpoint, kind.Endpoint)
		}

		assertFlag(t, d.GVK.Kind, "Taggable", d.Taggable, mixesInTagsMixin(kind))
		assertFlag(t, d.GVK.Kind, "CustomFieldable", d.CustomFieldable, slices.Contains(kind.WritePath, "custom_fields"))
	}
}

func assertFlag(t *testing.T, kind, name string, declared, schema bool) {
	t.Helper()

	if declared != schema {
		t.Errorf("descriptor %s: %s = %t, the REST schema says %t", kind, name, declared, schema)
	}
}

// mixesInTagsMixin reports whether this model's `tags` column is the object's *own* tags --
// the column the provenance stamp writes into -- rather than a plain many-to-many that
// happens to point at extras.Tag.
//
// A writable `tags` column is not enough, and extras.ConfigContext is why: it declares
// `tags` itself, as a `ManyToManyField -> extras.Tag` with `related_name='+'` selecting which
// tagged objects the context applies to, and it mixes in no TagsMixin at all. Deriving
// Taggable from the column alone would make the operator append `k8s-managed` to that
// selector and silently change which objects in NetBox receive the configuration, so the IR's
// own record of *where the column comes from* is what the flag is checked against.
func mixesInTagsMixin(kind irKind) bool {
	return slices.ContainsFunc(kind.Fields, func(f irField) bool {
		return f.Name == "tags" && f.DeclaredBy == "TagsMixin" && f.InWritePath && !f.ReadOnly
	})
}

// gateDocument compares the rendered audit against the committed docs/coverage.md, or
// rewrites it under -update.
func gateDocument(t *testing.T, doc string) {
	t.Helper()

	if *updateCoverage {
		if err := os.WriteFile(coverageDocPath, []byte(doc), 0o644); err != nil { //nolint:gosec // a document
			t.Fatalf("write %s: %v", coverageDocPath, err)
		}

		return
	}

	committed, err := os.ReadFile(coverageDocPath)
	if err != nil {
		t.Fatalf("read %s: %v (run `make coverage`)", coverageDocPath, err)
	}

	if string(committed) == doc {
		return
	}

	t.Errorf("%s is stale. Coverage changed, which is either progress to record or a regression "+
		"to explain -- run `make coverage` and commit the result.\n\n%s",
		coverageDocPath, differences(string(committed), doc))
}

// differences lists the lines a fresh run added and dropped, capped, because the failure
// has to say *what* moved: a Kind that left the registry, a column a field map stopped
// mapping, a model a NetBox release added. A set difference rather than a positional one --
// one inserted row shifts every line after it, and a diff that reports three hundred changes
// reports nothing. A quadratic scan over three hundred lines, which is free.
func differences(committed, fresh string) string {
	was, now := strings.Split(committed, "\n"), strings.Split(fresh, "\n")

	var b strings.Builder

	const cap = 12

	shown := 0

	for _, prefix := range []string{"+", "-"} {
		from, against := now, was
		if prefix == "-" {
			from, against = was, now
		}

		for _, l := range from {
			if l == "" || slices.Contains(against, l) {
				continue
			}

			if shown == cap {
				fmt.Fprintf(&b, "  ... and more; run `make coverage` and read the diff\n")

				return b.String()
			}

			fmt.Fprintf(&b, "  %s %s\n", prefix, l)
			shown++
		}
	}

	return b.String()
}

// countStatus returns how many rows have a status.
func countStatus[T any](rows []T, status func(T) string, want string) int {
	n := 0

	for _, row := range rows {
		if status(row) == want {
			n++
		}
	}

	return n
}

// render is docs/coverage.md. Deterministic: every table is sorted and every number is
// counted from the rows above it, so the document is a function of the two inputs and
// nothing else.
func (a *audit) render() string {
	var b strings.Builder

	a.renderHeader(&b)
	a.renderSummary(&b)
	a.renderColumnRollup(&b)
	a.renderColumnDetail(&b)
	a.renderKeys(&b)
	a.renderEndpoints(&b)

	return b.String()
}

func (a *audit) renderHeader(b *strings.Builder) {
	fmt.Fprintf(b, "# Coverage\n\n")
	fmt.Fprintf(b, "<!-- Generated by `make coverage` from %s and internal/registry. Do not edit:\n"+
		"     the audit rewrites this file and `make test` fails when the committed copy is stale\n"+
		"     (internal/registry/coverage_test.go, NBO-060). -->\n\n", strings.TrimPrefix(irPath, "../../"))
	fmt.Fprintf(b, "What of NetBox `%s` this operator covers and what it does not, joined from the schema IR\n"+
		"and the Descriptor registry. Deliberate absences are declared in\n"+
		"[`hack/coverage-exclusions.yaml`](../hack/coverage-exclusions.yaml); everything else in a\n"+
		"`MISSING` or `blocked` row is a gap, and a gap is a ticket somebody still owes.\n\n"+
		"Regenerate with `make coverage` after every schema regeneration (`docs/regenerating.md`).\n\n",
		a.ir.NetBoxVersion)
}

func (a *audit) renderSummary(b *strings.Builder) {
	endpointStatus := func(r endpointRow) string { return r.Status }
	columnStatus := func(r columnRow) string { return r.Status }

	implemented := countStatus(a.endpoints, endpointStatus, statusImplemented)
	excludedEndpoints := countStatus(a.endpoints, endpointStatus, statusExcluded)
	missingEndpoints := countStatus(a.endpoints, endpointStatus, statusMissing)

	excludedColumns := countStatus(a.columns, columnStatus, statusExcluded)
	blockedColumns := countStatus(a.columns, columnStatus, statusBlocked)
	missingColumns := countStatus(a.columns, columnStatus, statusMissing)

	requiredMissing := 0

	for _, row := range a.columns {
		if row.Required && row.Status != statusExcluded {
			requiredMissing++
		}
	}

	usableSince216 := countStatus(a.keys, func(r keyRow) string { return r.Status }, keyUsable)
	stillUnusableShipped := 0

	for _, row := range a.keys {
		if row.Status == keyUnusable && row.Implemented {
			stillUnusableShipped++
		}
	}

	fmt.Fprintf(b, "## Summary\n\n| | count |\n|---|--:|\n")
	fmt.Fprintf(b, "| NetBox REST endpoints | %d |\n", len(a.endpoints))
	fmt.Fprintf(b, "| — implemented as a Kind | %d |\n", implemented)
	fmt.Fprintf(b, "| — excluded, with a reason | %d |\n", excludedEndpoints)
	fmt.Fprintf(b, "| — **not implemented** | %d |\n", missingEndpoints)
	fmt.Fprintf(b, "| in scope (endpoints − excluded) | %d |\n", len(a.endpoints)-excludedEndpoints)
	fmt.Fprintf(b, "| | |\n")
	fmt.Fprintf(b, "| writable columns on the implemented Kinds | %d |\n", a.covered+len(a.columns))
	fmt.Fprintf(b, "| — written by a spec field, or engine-owned | %d |\n", a.covered)
	fmt.Fprintf(b, "| — deliberately omitted, with a reason | %d |\n", excludedColumns)
	fmt.Fprintf(b, "| — blocked: a reference whose target model has no Kind | %d |\n", blockedColumns)
	fmt.Fprintf(b, "| — **MISSING**: nothing declares it and nothing blocks it | %d |\n", missingColumns)
	fmt.Fprintf(b, "| — of those, required on create (fails the audit) | %d |\n", requiredMissing)
	fmt.Fprintf(b, "| | |\n")
	fmt.Fprintf(b, "| natural-key candidates the IR calls unusable | %d |\n", len(a.keys))
	fmt.Fprintf(b, "| — expressible since #216 (`?<fk>_id=null`) | %d |\n", usableSince216)
	fmt.Fprintf(b, "| — still unusable | %d |\n", len(a.keys)-usableSince216)
	fmt.Fprintf(b, "| — still unusable on an implemented Kind | %d |\n", stillUnusableShipped)
	fmt.Fprintf(b, "| | |\n")
	fmt.Fprintf(b, "| IR `unresolved` rows naming an implemented Kind | %d |\n\n", a.unresolvedOnImplemented)
}

// renderColumnRollup groups the uncovered columns by column name, which is the section that
// turns fifty-seven rows into a dozen facts: a column absent from eighteen Kinds is one
// decision nobody has taken, not eighteen oversights.
func (a *audit) renderColumnRollup(b *strings.Builder) {
	order := []string{}
	members := map[string][]string{}

	for _, row := range a.columns {
		key := strings.Join([]string{row.Column, row.Status, row.Detail}, "\x00")
		if _, seen := members[key]; !seen {
			order = append(order, key)
		}

		members[key] = append(members[key], row.Model)
	}

	slices.SortFunc(order, func(x, y string) int {
		if c := len(members[y]) - len(members[x]); c != 0 {
			return c
		}

		return strings.Compare(x, y)
	})

	fmt.Fprintf(b, "## Uncovered columns, by column\n\n| column | status | Kinds | detail |\n|---|---|--:|---|\n")

	for _, key := range order {
		parts := strings.Split(key, "\x00")
		fmt.Fprintf(b, "| `%s` | %s | %d | %s |\n", parts[0], parts[1], len(members[key]), detailCell(parts[2]))
	}

	fmt.Fprintf(b, "\n")
}

// detailCell is a table cell that is never empty.
func detailCell(detail string) string {
	if detail == "" {
		return "—"
	}

	return detail
}

func (a *audit) renderColumnDetail(b *strings.Builder) {
	fmt.Fprintf(b, "## Uncovered columns, per Kind\n\n"+
		"| model | column | class | required | status | detail |\n|---|---|---|---|---|---|\n")

	for _, row := range a.columns {
		required := "—"
		if row.Required {
			required = "**REQ**"
		}

		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s | %s |\n",
			row.Model, row.Column, row.Class, required, row.Status, detailCell(row.Detail))
	}

	fmt.Fprintf(b, "\n")
}

func (a *audit) renderKeys(b *strings.Builder) {
	fmt.Fprintf(b, "## Natural-key candidates the IR calls unusable\n\n")
	fmt.Fprintf(b, "The IR's verdict is against the `__empty` spelling #206 proposed. #216 pins a foreign key\n"+
		"to null with django-filter's own `null_value` instead (`?parent_id=null`, "+
		"`registry.NullColumnRef`),\nso a candidate whose null pins are all foreign keys is expressible today. "+
		"Re-derived here from\neach pinned column's class rather than from the IR's reason string.\n\n")
	fmt.Fprintf(b, "| model | shipped | constraint | verdict | detail |\n|---|---|---|---|---|\n")

	for _, row := range a.keys {
		shipped := "—"
		if row.Implemented {
			shipped = "yes"
		}

		fmt.Fprintf(b, "| `%s` | %s | `%s` | %s | %s |\n", row.Model, shipped, row.Constraint, row.Status, row.Detail)
	}

	fmt.Fprintf(b, "\n")
}

func (a *audit) renderEndpoints(b *strings.Builder) {
	fmt.Fprintf(b, "## Endpoints\n\n| endpoint | model | Kind | status | reason |\n|---|---|---|---|---|\n")

	for _, row := range a.endpoints {
		kind := "—"
		if row.Kind != "" {
			kind = "`" + row.Kind + "`"
		}

		fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s |\n",
			row.Endpoint, row.Model, kind, row.Status, detailCell(row.Detail))
	}
}

// TestCoverageClassification pins the four verdicts a column can get, because everything
// the audit reports and every count in the document is one of them. The real inputs are
// 134 kinds and 19 descriptors, where a rule that fires in the wrong order is invisible.
func TestCoverageClassification(t *testing.T) {
	ir := &netboxIR{Kinds: map[string]irKind{
		"app.Shipped":  {Endpoint: "app/shipped"},
		"app.Waiting":  {Endpoint: "app/waiting"},
		"app.Excluded": {Endpoint: "app/excluded"},
	}}
	excl := &exclusions{
		Endpoints: []endpointEntry{{Endpoint: "app/excluded", Reason: "out of scope"}},
		Columns: []columnEntry{
			{Kind: "app.Thing", Column: "comments", Reason: "decided against"},
			{Kind: "*", Column: "weight", Reason: "deferred everywhere"},
		},
		Notes: []columnEntry{{Kind: "*", Column: "tags", Reason: "one systematic gap"}},
	}
	implemented := map[string]Descriptor{"app.Shipped": {}}

	ref := func(target string) irField {
		return irField{Name: "link", Class: "Ref", Ref: &irRef{Target: target}}
	}

	tests := []struct {
		name       string
		field      irField
		wantStatus string
		wantDetail string
	}{
		{"excluded by name", irField{Name: "comments"}, statusExcluded, "decided against"},
		{"excluded by wildcard", irField{Name: "weight"}, statusExcluded, "deferred everywhere"},
		{"blocked on a Kind that may come", ref("app.Waiting"), statusBlocked, "waits on a Kind for `app.Waiting`"},
		{
			"blocked forever", ref("app.Excluded"), statusBlocked,
			"`app.Excluded` is an excluded endpoint, so nothing will ever write this",
		},
		{"target ships, so nothing blocks it", ref("app.Shipped"), statusMissing, ""},
		{"annotated but still missing", irField{Name: "tags", Class: "M2M"}, statusMissing, "one systematic gap"},
		{"plain gap", irField{Name: "time_zone"}, statusMissing, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyColumn(ir, "app.Thing", tc.field, implemented, excl)
			if got.Status != tc.wantStatus || got.Detail != tc.wantDetail {
				t.Errorf("classifyColumn(%s) = %q / %q, want %q / %q",
					tc.field.Name, got.Status, got.Detail, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}

// TestCoverageNullPinVerdict pins the #216 re-derivation. It is structural on purpose: the
// IR's own reason is a sentence, and a gate that greps a sentence breaks on rewording.
func TestCoverageNullPinVerdict(t *testing.T) {
	kind := irKind{Fields: []irField{
		{Name: "parent", Class: "Ref"},
		{Name: "rd", Class: "Scalar"},
	}}

	tests := []struct {
		name string
		pins []string
		want bool
	}{
		{"no pins at all", nil, false},
		{"one foreign key", []string{"parent"}, true},
		{"a char column cannot use the id spelling", []string{"rd"}, false},
		{"mixed, so not all of them", []string{"parent", "rd"}, false},
		{"a column the model does not have", []string{"nope"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := irNaturalKey{Unusable: "whatever the IR said"}
			for _, pin := range tc.pins {
				key.NullFields = append(key.NullFields, irNullField{Column: pin})
			}

			if got := allRefPins(kind, key); got != tc.want {
				t.Errorf("allRefPins(%v) = %t, want %t", tc.pins, got, tc.want)
			}
		})
	}
}

// TestCoverageStaleExclusion is the acceptance criterion that a declaration the schema no
// longer bears out fails the audit. A stale exclusion is worse than a missing one: it
// excuses whatever moved in behind it, silently and forever.
func TestCoverageStaleExclusion(t *testing.T) {
	a := &audit{
		endpoints: []endpointRow{
			{Endpoint: "app/live", Model: "app.Live", Status: statusMissing},
			{Endpoint: "app/shipped", Model: "app.Shipped", Kind: "NetBoxShipped", Status: statusImplemented},
		},
		columns: []columnRow{
			{Model: "app.Live", Column: "colour", Status: statusMissing},
			{Model: "app.Live", Column: "comments", Status: statusExcluded},
		},
	}

	tests := []struct {
		name string
		excl *exclusions
		want bool
	}{
		{"endpoint that is gone", &exclusions{Endpoints: []endpointEntry{{Endpoint: "app/gone", Reason: "why"}}}, true},
		{
			"endpoint excluded and implemented",
			&exclusions{Endpoints: []endpointEntry{{Endpoint: "app/shipped", Reason: "why"}}}, true,
		},
		{"endpoint with no reason", &exclusions{Endpoints: []endpointEntry{{Endpoint: "app/live"}}}, true},
		{
			"column on a Kind that does not ship",
			&exclusions{Columns: []columnEntry{{Kind: "app.Other", Column: "comments", Reason: "why"}}}, true,
		},
		{
			"column that is now mapped",
			&exclusions{Columns: []columnEntry{{Kind: "app.Live", Column: "mapped", Reason: "why"}}}, true,
		},
		{"note nothing matches", &exclusions{Notes: []columnEntry{{Kind: "app.Live", Column: "gone", Reason: "why"}}}, true},
		{
			"a live exclusion and a live note",
			&exclusions{
				Endpoints: []endpointEntry{{Endpoint: "app/live", Reason: "why"}},
				Columns:   []columnEntry{{Kind: "app.Live", Column: "comments", Reason: "why"}},
				Notes:     []columnEntry{{Kind: "*", Column: "colour", Reason: "why"}},
			}, false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			problems := a.staleDeclarations(tc.excl)
			if stale := len(problems) > 0; stale != tc.want {
				t.Errorf("staleDeclarations() = %v, want stale = %t", problems, tc.want)
			}
		})
	}
}
