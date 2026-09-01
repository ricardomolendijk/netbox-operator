package harness

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// ObjectState is what one CR's status says, reduced to the fields a convergence assertion
// cares about.
type ObjectState struct {
	Key             string
	NetBoxID        int64
	DeferredPending []string
	Conditions      map[string]metav1.Condition
}

// Condition returns the named condition, or the zero value.
func (s ObjectState) Condition(name string) metav1.Condition { return s.Conditions[name] }

// Is reports whether the named condition has that status and that reason. An empty reason
// matches any.
func (s ObjectState) Is(name string, status metav1.ConditionStatus, reason string) bool {
	condition, ok := s.Conditions[name]
	if !ok || condition.Status != status {
		return false
	}
	return reason == "" || condition.Reason == reason
}

// String is the one-line form printed on a timeout: everything needed to tell "waiting on a
// ref" from "NetBox refused the payload" without a second run.
func (s ObjectState) String() string {
	parts := []string{s.Key, fmt.Sprintf("id=%d", s.NetBoxID)}

	names := make([]string, 0, len(s.Conditions))
	for name := range s.Conditions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		condition := s.Conditions[name]
		part := fmt.Sprintf("%s=%s/%s", name, condition.Status, condition.Reason)
		if condition.Message != "" {
			part += fmt.Sprintf(" (%s)", condition.Message)
		}
		parts = append(parts, part)
	}
	if len(s.DeferredPending) > 0 {
		parts = append(parts, "deferredPending="+strings.Join(s.DeferredPending, ","))
	}
	return strings.Join(parts, " ")
}

// ReadState reads one fixture's CR and reduces it to an ObjectState.
//
// Unstructured, so one function serves every kind: the fields it reads are the ones
// NetBoxObjectStatus declares for all of them.
func ReadState(ctx context.Context, c client.Client, fixture Fixture) (ObjectState, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(fixture.Object.GroupVersionKind())
	key := types.NamespacedName{
		Namespace: fixture.Object.GetNamespace(),
		Name:      fixture.Object.GetName(),
	}
	if err := c.Get(ctx, key, obj); err != nil {
		return ObjectState{Key: fixture.Key()}, fmt.Errorf("getting %s: %w", fixture.Key(), err)
	}

	state := ObjectState{Key: fixture.Key(), Conditions: map[string]metav1.Condition{}}
	id, _, err := unstructured.NestedInt64(obj.Object, "status", "id")
	if err != nil {
		return state, fmt.Errorf("reading status.id of %s: %w", fixture.Key(), err)
	}
	state.NetBoxID = id

	pending, _, err := unstructured.NestedStringSlice(obj.Object, "status", "deferredPending")
	if err != nil {
		return state, fmt.Errorf("reading status.deferredPending of %s: %w", fixture.Key(), err)
	}
	state.DeferredPending = pending

	conditions, _, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		return state, fmt.Errorf("reading status.conditions of %s: %w", fixture.Key(), err)
	}
	for _, raw := range conditions {
		condition, ok := asCondition(raw)
		if !ok {
			continue
		}
		state.Conditions[condition.Type] = condition
	}
	return state, nil
}

func asCondition(raw any) (metav1.Condition, bool) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return metav1.Condition{}, false
	}
	name, ok := entry["type"].(string)
	if !ok {
		return metav1.Condition{}, false
	}
	condition := metav1.Condition{Type: name}
	condition.Status = metav1.ConditionStatus(stringField(entry, "status"))
	condition.Reason = stringField(entry, "reason")
	condition.Message = stringField(entry, "message")
	return condition, true
}

func stringField(entry map[string]any, name string) string {
	value, _ := entry[name].(string)
	return value
}

// ReadStates reads every fixture's CR. A missing CR is an error rather than a skip: a
// convergence assertion over a set that is one object short is not the assertion.
func ReadStates(ctx context.Context, c client.Client, fixtures []Fixture) ([]ObjectState, error) {
	states := make([]ObjectState, 0, len(fixtures))
	for _, fixture := range fixtures {
		if fixture.Object.GetKind() == "NetBoxRefGrant" {
			// A grant has no status and no NetBox object; it is authorisation, not data.
			continue
		}
		state, err := ReadState(ctx, c, fixture)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

// Diagnostics is the multi-line dump printed when a convergence wait times out: every
// object's conditions and its status.deferredPending, so a CI failure is diagnosable
// without re-running the suite.
func Diagnostics(states []ObjectState) string {
	lines := make([]string, 0, len(states)+1)
	lines = append(lines, fmt.Sprintf("%d objects:", len(states)))
	for _, state := range states {
		lines = append(lines, "  "+state.String())
	}
	return strings.Join(lines, "\n")
}

// WaitConverged waits for every non-grant fixture to report Ready=True with
// Reason=Synced -- NBO-017's definition of a converged run.
//
// The error carries Diagnostics for the whole set, not just the object that failed: an
// object waiting on a ref is diagnosed by what its *target* is doing.
func WaitConverged(ctx context.Context, c client.Client, fixtures []Fixture, timeout time.Duration) error {
	var last []ObjectState

	err := WaitFor(ctx, "every object to reach Ready=True/Synced", timeout,
		func(ctx context.Context) (bool, string, error) {
			states, err := ReadStates(ctx, c, fixtures)
			if err != nil {
				return transient(err)
			}
			last = states

			var waiting []string
			for _, state := range states {
				if state.Is(netboxv1alpha1.ConditionReady, metav1.ConditionTrue, netboxv1alpha1.ReasonSynced) {
					continue
				}
				waiting = append(waiting, state.Key)
			}
			if len(waiting) == 0 {
				return true, "", nil
			}
			return false, fmt.Sprintf("%d not ready: %s", len(waiting), strings.Join(waiting, ", ")), nil
		})
	if err != nil {
		return fmt.Errorf("%w\n%s", err, Diagnostics(last))
	}
	return nil
}
