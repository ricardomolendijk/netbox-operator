package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestEventActionVocabulary is the (reason -> action) table as a test, so that changing
// what a user sees in `kubectl get events -o yaml` is a deliberate edit here rather than a
// side effect somewhere else. The actions are the `action` log key CONTRIBUTING.md already
// requires, UpperCamelCased; see EventAction.
func TestEventActionVocabulary(t *testing.T) {
	want := map[string]string{
		// NetBoxEndpoint. One action for the whole probe, success and failure alike.
		ReasonReady:              ActionProbe,
		ReasonAuthError:          ActionProbe,
		ReasonTokenMissing:       ActionProbe,
		ReasonSecretMissing:      ActionProbe,
		ReasonCABundleMissing:    ActionProbe,
		ReasonProbeFailed:        ActionProbe,
		ReasonVersionUnsupported: ActionProbe,
		ReasonVersionUnparseable: ActionProbe,
		ReasonInvalidConfig:      ActionProbe,
		ReasonProvisioned:        ActionBootstrap,
		ReasonBootstrapDisabled:  ActionBootstrap,
		ReasonBootstrapFailed:    ActionBootstrap,

		// NetBoxSweep.
		EventOrphansFound: ActionSweep,
		EventSweepRefused: ActionSweep,

		// The engine's write path.
		EventCreated:       ActionCreate,
		EventUpdated:       ActionUpdate,
		EventRecreated:     ActionRecreate,
		EventAdopted:       ActionAdopt,
		EventInvalid:       ActionWrite,
		EventDriftDetected: ActionReportDrift,

		// Who owns the NetBox object.
		EventConflict:          ActionClaim,
		EventConflictSustained: ActionClaim,

		// Deletion, of an object CR and of a claim.
		EventDeleted:          ActionDelete,
		EventRetained:         ActionDelete,
		EventAddressRetained:  ActionDelete,
		EventNothingToDelete:  ActionDelete,
		EventDeleteBlocked:    ActionDelete,
		EventFinalizerSkipped: ActionDelete,

		// Inline children.
		EventChildMaterialised:  ActionMaterialise,
		EventChildFieldReverted: ActionMaterialise,
		EventChildPruned:        ActionPrune,

		// Allocation.
		EventAllocated:            ActionAllocate,
		EventAllocationReclaimed:  ActionAllocate,
		EventAllocationConflict:   ActionAllocate,
		EventAllocationContended:  ActionAllocate,
		EventForeignAllocation:    ActionAllocate,
		EventPoolExhausted:        ActionAllocate,
		EventPoolNotAllocatable:   ActionAllocate,
		EventPoolUnexpectedStatus: ActionAllocate,
		EventReclaimedOutsidePool: ActionAllocate,
	}

	for reason, action := range want {
		if got := EventAction(reason); got != action {
			t.Errorf("EventAction(%q) = %q; want %q", reason, got, action)
		}
	}

	if len(eventActions) != len(want) {
		t.Errorf("eventActions holds %d reasons, the table above %d; one of the two moved",
			len(eventActions), len(want))
	}
}

// TestEventActionCoversEveryEventReason reads the package's own source and fails on an
// Event reason constant with no action, because an events.k8s.io/v1 Event whose action is
// wrong is not a build failure or a test failure -- it is a vague word in somebody's
// `kubectl get events` months later. Every `Event*` constant in this package is an Event
// reason by construction, which is what makes the sweep exhaustive.
func TestEventActionCoversEveryEventReason(t *testing.T) {
	for name, reason := range eventReasonConstants(t) {
		if EventAction(reason) == ActionReconcile {
			t.Errorf("%s = %q has no entry in eventActions, so it would be recorded under the"+
				" fallback action %q; add it there and to docs/operations/observability.md",
				name, reason, ActionReconcile)
		}
	}
}

// TestEventActionsAreWellFormed holds the actions to what the API server accepts and to
// what the events API asks for: non-empty, at most 128 bytes, UpperCamelCase.
func TestEventActionsAreWellFormed(t *testing.T) {
	for reason, action := range eventActions {
		switch {
		case action == "":
			t.Errorf("reason %q maps to the empty action; the API server refuses that Event", reason)
		case len(action) > 128:
			t.Errorf("reason %q maps to a %d-byte action; the API server caps it at 128",
				reason, len(action))
		case !unicode.IsUpper(rune(action[0])):
			t.Errorf("reason %q maps to action %q, which is not UpperCamelCase", reason, action)
		case strings.ContainsAny(action, " -_"):
			t.Errorf("reason %q maps to action %q, which is not one word", reason, action)
		}
	}
}

// TestEventNote covers the 1024-byte cap core/v1's message did not have.
func TestEventNote(t *testing.T) {
	t.Run("a note that fits is untouched", func(t *testing.T) {
		note := strings.Repeat("a", eventNoteMax)
		if got := EventNote(note); got != note {
			t.Errorf("EventNote() shortened a note that already fits: %d bytes", len(got))
		}
	})

	t.Run("a long note is cut to the limit and says so", func(t *testing.T) {
		got := EventNote(strings.Repeat("a", eventNoteMax*3))
		if len(got) > eventNoteMax {
			t.Errorf("EventNote() = %d bytes; the API server caps the note at %d",
				len(got), eventNoteMax)
		}
		if !strings.HasSuffix(got, eventNoteTruncated) {
			t.Errorf("EventNote() dropped %d bytes without saying so", eventNoteMax*3-len(got))
		}
	})

	t.Run("the cut never splits a rune", func(t *testing.T) {
		// The engine's diffs are rendered with arrows, so the byte at the limit is very
		// often the middle of a multi-byte rune.
		got := EventNote(strings.Repeat("a -> → b", eventNoteMax))
		if !utf8.ValidString(got) {
			t.Errorf("EventNote() produced invalid UTF-8: %q", got)
		}
	})
}

// eventReasonConstants returns every `Event*` string constant this package declares, by
// name. Read out of the source rather than listed by hand: a list by hand is exactly what
// somebody adding a reason would forget to extend.
func eventReasonConstants(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	reasons := map[string]string{}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}

			collectEventReasons(gen, reasons)
		}
	}

	if len(reasons) == 0 {
		t.Fatal("found no Event* constants; the source sweep is not looking where it thinks")
	}

	return reasons
}

// collectEventReasons adds the `Event*` string constants of one const block to out.
func collectEventReasons(gen *ast.GenDecl, out map[string]string) {
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		for i, name := range value.Names {
			if !strings.HasPrefix(name.Name, "Event") || i >= len(value.Values) {
				continue
			}

			literal, ok := value.Values[i].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}

			unquoted, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}

			out[name.Name] = unquoted
		}
	}
}
