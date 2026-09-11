// The read-back half of the field map: a column NetBox computes for itself, reported in a
// Kind's status because there is nowhere else it could go.
//
// A file of its own rather than an addition to inline_derived_refs.go, because it is a third
// capability and not a shape of either of the first two. Both of those describe values on
// their way *into* NetBox -- children the materialiser writes, a column a parent's inline
// sugar contributes. Everything here describes a value on its way back out, which nothing in
// a spec may ever hold.
package v1alpha1

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ObservedColumns is implemented by the Kinds that mirror a NetBox-computed column into
// their own status.
//
// **A derived column is status and never spec, and this is the mechanism that says so once.**
// NetBox owns the value: `ipam.IPRange.size` is `end - start + 1`, set in `save()` and refused
// on write, so a spec field for it would be a field `kubectl apply` accepts and NetBox
// discards. registry.Descriptor.ReadOnly already stops the engine sending one. What was
// missing was the other direction -- the value is in every response the engine already reads,
// and until now it was thrown away, so the only way to see it was to ask NetBox.
//
// A capability for the reason InlineParent and InlineRefParent are ones: the engine's whole
// per-kind knowledge of it is a single type assertion, a Kind that mirrors nothing answers by
// not implementing the method, and there is no branch on Kind anywhere (CONTRIBUTING.md,
// "Extensibility").
//
// It is deliberately *not* a Descriptor field. A mirrored column needs a typed status field to
// land in, which is per-Kind Go either way; declaring the column in the registry as well would
// be a second copy of the same fact, free to disagree with the first.
//
// +kubebuilder:object:generate=false
type ObservedColumns interface {
	// ObserveColumns records what NetBox computed, from the object the engine has just read
	// or just written.
	//
	// Called on every pass that reached a live object, so an implementation must be
	// idempotent and must be cheap. It reads `live` and writes its own status, and does
	// nothing else.
	//
	// `live` may be missing the column, and an implementation must leave its status alone
	// when it is: a 204 with an empty body, a DryRun response the client fabricated and a
	// `?brief=true` read all arrive here as objects that never mentioned it, and blanking a
	// value that is still correct is worse than reporting it one pass late.
	//
	// The result reports whether the status actually changed, and it is load-bearing rather
	// than informational. The engine skips the status write on a pass whose conclusions match
	// the stored status, which is what keeps a quiet resync from churning the resourceVersion
	// of every object in the cluster -- and that comparison is over the *shared* envelope, so
	// a mirrored column it never sees would be learned in memory and thrown away. Returning
	// true is how a Kind says "write this one".
	ObserveColumns(live map[string]any) bool
}

// ObserveColumns records the NetBox-computed columns obj mirrors into its status, and does
// nothing at all for an object that mirrors none.
//
// The one entry point, for the reason DerivedSpecRefs is one: the assertion is written here
// rather than at the call site so that every place the engine settles a pass observes the same
// way, and so that adding a second such call site is a line rather than a decision.
//
// The result is whether the status changed, and it is false for a Kind that mirrors nothing --
// which is what makes the engine's write-skipping behaviour identical to what it was for every
// Kind that does not implement this.
func ObserveColumns(obj client.Object, live map[string]any) bool {
	observer, mirrors := obj.(ObservedColumns)
	if !mirrors || live == nil {
		return false
	}

	return observer.ObserveColumns(live)
}

// observedInt reads a whole number out of a NetBox response.
//
// A helper here rather than a type switch in each implementation, and both arms earn their
// place: `float64` is what encoding/json produces for every JSON number, and `int` is what a
// test fixture written by hand produces. Anything else -- absent, null, a string, a nested
// object -- reports false, which is the signal to leave the status field alone.
func observedInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}
