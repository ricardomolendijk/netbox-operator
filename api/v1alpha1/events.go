package v1alpha1

import "unicode/utf8"

// The Events this operator emits go to events.k8s.io/v1, which is stricter than the
// core/v1 Events it replaces in two ways the operator has to answer for (#294):
//
//   - `action` is required. An Event without one is rejected by the API server with a 422,
//     which is a line in the manager's log and a missing Event -- nothing fails loudly.
//   - `note` is capped at 1024 bytes, where core/v1's `message` had no limit at all. The
//     engine's `Updated` note is the whole `field: old -> new` diff, which for a wide kind
//     is comfortably longer than that.
//
// Both are checks the API server makes and no unit test does, so both live here as one
// function each, next to the reason constants they are about.

// Event actions: what the operator *did*, where the reason says how it turned out.
//
// This is deliberately not a new vocabulary. It is the `action` log key CONTRIBUTING.md
// already requires on every log line, UpperCamelCased as the events API asks for -- so the
// Event and the log line that accompanies it name the same operation, and a reader has one
// vocabulary to learn rather than two. `kubectl describe` shows the reason; `kubectl get
// events -o yaml` and anything consuming Events programmatically get the action.
//
// Success and failure of one operation therefore share an action: `Probe` covers both
// `Ready` and `AuthError`, and `Delete` covers all six ways a deletion can end. That is
// also how upstream uses the field -- the scheduler's `Scheduled` and `FailedScheduling`
// are both action `Scheduling` -- and it is what makes the action worth having next to the
// reason instead of a restatement of it.
const (
	// ActionProbe is the endpoint controller reaching NetBox: the token, the version and
	// the client cache.
	ActionProbe = "Probe"

	// ActionBootstrap is the provenance bootstrap -- the tag and custom fields the
	// operator needs before any object controller may write.
	ActionBootstrap = "Bootstrap"

	// ActionSweep is one sweep run over NetBox looking for objects this cluster left
	// behind.
	ActionSweep = "Sweep"

	// ActionAdopt is taking over a NetBox object that already matched a CR's natural key.
	ActionAdopt = "Adopt"

	// ActionCreate, ActionUpdate and ActionRecreate are the three shapes of a write the
	// engine makes to NetBox, and are the same three values applyWrite already logs under
	// the `action` key.
	ActionCreate   = "Create"
	ActionUpdate   = "Update"
	ActionRecreate = "Recreate"

	// ActionWrite is a write that never got as far as choosing between those three,
	// because the payload could not be rendered or NetBox refused it.
	ActionWrite = "Write"

	// ActionReportDrift is `driftMode: Report` doing what it was configured to do: drift
	// found, named, and deliberately not written.
	ActionReportDrift = "ReportDrift"

	// ActionClaim is deciding whether this CR may own a NetBox object -- which is what
	// both a natural-key conflict and another writer's provenance stamp are about.
	ActionClaim = "Claim"

	// ActionDelete is a CR going away, whatever that turned out to mean for the NetBox
	// object behind it: deleted, retained, never there, refused, or skipped.
	ActionDelete = "Delete"

	// ActionMaterialise and ActionPrune are the two halves of inline children: an entry in
	// the parent's spec becoming a child CR, and a child CR going away because its entry
	// did.
	ActionMaterialise = "Materialise"
	ActionPrune       = "Prune"

	// ActionAllocate is the allocation engine handing a claim an address, prefix or range
	// out of a pool -- or failing to.
	ActionAllocate = "Allocate"

	// ActionReconcile is the fallback for a reason with no entry in eventActions. It exists
	// so that a reason somebody forgot to map still produces an Event the API server
	// accepts, rather than a 422 nobody sees; TestEventActionCoversEveryReason is what
	// keeps it from being the answer for anything real.
	ActionReconcile = "Reconcile"
)

// eventActions is every Event reason this operator emits, mapped to its action.
//
// A new Event reason belongs here and in docs/operations/observability.md.
// TestEventActionCoversEveryReason fails if one is missing.
var eventActions = map[string]string{
	// NetBoxEndpoint: the probe, and the provenance bootstrap that follows it.
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

	// Deletion, of an object CR and of a claim. Every outcome is one action, because they
	// are all the same operation ending differently.
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

	// Allocation. The refusals are failures of the same operation as the successes, so
	// they carry the same action.
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

// EventAction returns the action to record an Event with, given its reason.
//
// An unmapped reason gets ActionReconcile rather than the empty string: an Event with no
// action is refused by the API server, and losing the Event is a worse answer than
// recording it under a vague verb.
func EventAction(reason string) string {
	if action, ok := eventActions[reason]; ok {
		return action
	}

	return ActionReconcile
}

// eventNoteMax is the API server's limit on an events.k8s.io/v1 note, in bytes.
const eventNoteMax = 1024

// eventNoteTruncated marks a note that did not fit. Counted against eventNoteMax, so the
// result is always within the limit.
const eventNoteTruncated = " [truncated]"

// EventNote fits a formatted Event message into what events.k8s.io/v1 accepts.
//
// Over the limit the API server refuses the whole Event, so the choice is a shortened note
// or none at all. The cut is on a rune boundary -- the engine's diffs contain arrows and
// user-supplied names -- and marked, so a reader can tell a message that was clipped from
// one that ended there. The full detail is in the object's conditions either way.
func EventNote(note string) string {
	if len(note) <= eventNoteMax {
		return note
	}

	cut := eventNoteMax - len(eventNoteTruncated)
	for cut > 0 && !utf8.RuneStart(note[cut]) {
		cut--
	}

	return note[:cut] + eventNoteTruncated
}
