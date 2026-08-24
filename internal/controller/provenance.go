package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The provenance bootstrap creates two kinds of NetBox object and widens one. It needs no
// Kubernetes RBAC of its own -- everything it touches is on the far side of the NetBox API
// -- but the NetBox token does need extras.add_tag, extras.add_customfield and
// extras.change_customfield, which is the permission set docs/operations/provenance.md
// lists and the reason bootstrap is opt-out.

// provision runs the provenance bootstrap for one endpoint and records the outcome on its
// conditions and status.
//
// It runs after the version gate and before the client is cached, which is the whole of
// what "before any object reconciles" means here: an object gets a client only from the
// cache, so a bootstrap that has not succeeded on a writing endpoint means no object writes
// at all -- one condition on one endpoint instead of a hundred identical 400s.
//
// The returned error is the endpoint's failure; a nil error with an inert stamp is a working
// endpoint that stamps nothing.
func (r *NetBoxEndpointReconciler) provision(ctx context.Context,
	e *netboxv1alpha1.NetBoxEndpoint, client provenance.Client,
) (provenance.Stamp, error) {
	cfg := provenance.FromSpec(e.Spec.ManagedBy)
	if !cfg.Enabled() {
		// Nothing was asked for, so there is nothing to report. Removed rather than set to
		// False: a condition has to describe this reconcile, and one left behind from before
		// spec.managedBy was deleted describes an older one.
		meta.RemoveStatusCondition(&e.Status.Conditions, netboxv1alpha1.ConditionProvenanceReady)
		e.Status.ManagedBy = nil

		return provenance.Stamp{}, nil
	}

	result, err := provenance.Bootstrap(ctx, client, cfg, provenance.ObjectTypes(registry.List()))
	if err != nil {
		return provenance.Stamp{}, fmt.Errorf("bootstrapping netbox provenance: %w", err)
	}

	r.recordProvision(ctx, e, cfg, result)

	return result.Stamp, nil
}

// recordProvision writes what the bootstrap concluded onto the endpoint: one condition, one
// status block, an Event for each transition, and a log line at info only when something
// actually changed in NetBox.
func (r *NetBoxEndpointReconciler) recordProvision(ctx context.Context,
	e *netboxv1alpha1.NetBoxEndpoint, cfg provenance.Config, result provenance.Result,
) {
	e.Status.ManagedBy = &netboxv1alpha1.ManagedByStatus{
		ClusterID:    cfg.ClusterID,
		Tag:          cfg.Tag,
		TagID:        int64(result.Stamp.TagID),
		CustomFields: slices.Clone(result.Stamp.Fields),
	}

	if len(result.Missing) > 0 {
		// Reachable only with Suppressed set: Bootstrap returns ErrIncomplete otherwise, and
		// that path never gets here.
		r.provisionCondition(e, metav1.ConditionFalse, netboxv1alpha1.ReasonBootstrapSuppressed,
			fmt.Sprintf("this endpoint cannot write, so nothing was created; missing: %s",
				strings.Join(result.Missing, ", ")))

		return
	}

	changed := len(result.Created) > 0 || len(result.Widened) > 0
	log := logf.FromContext(ctx).WithValues("action", "bootstrap",
		"tag", cfg.Tag, "tagID", result.Stamp.TagID, "clusterID", cfg.ClusterID)

	// Info only when NetBox changed. A bootstrap that found everything already there runs
	// on every resync of every endpoint for the lifetime of the process, and at info that
	// is a line per endpoint per interval saying nothing (CONTRIBUTING.md, "Logging").
	log.V(debugUnless(changed)).Info("netbox provenance provisioned",
		"created", result.Created, "widened", result.Widened,
		"customFields", result.Stamp.Fields)

	if changed && r.transitionedProvision(e) {
		r.event(e, corev1.EventTypeNormal, netboxv1alpha1.ReasonProvisioned,
			fmt.Sprintf("provisioned netbox provenance: created %v, widened %v",
				result.Created, result.Widened))
	}

	r.provisionCondition(e, metav1.ConditionTrue, netboxv1alpha1.ReasonProvisioned,
		fmt.Sprintf("tag %q is netbox id %d; custom fields %v exist",
			cfg.Tag, result.Stamp.TagID, result.Stamp.Fields))
}

// provisionCondition sets ProvenanceReady.
func (r *NetBoxEndpointReconciler) provisionCondition(e *netboxv1alpha1.NetBoxEndpoint,
	status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&e.Status.Conditions, metav1.Condition{
		Type:               netboxv1alpha1.ConditionProvenanceReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: e.Generation,
	})
}

// transitionedProvision reports whether ProvenanceReady is about to become True from
// something else, which is the only time the Event is worth emitting.
func (r *NetBoxEndpointReconciler) transitionedProvision(e *netboxv1alpha1.NetBoxEndpoint) bool {
	return transitioned(e, netboxv1alpha1.ConditionProvenanceReady, metav1.ConditionTrue,
		netboxv1alpha1.ReasonProvisioned)
}

// provenanceReason classifies a bootstrap failure. By type, never by message: "you switched
// bootstrap off and these definitions do not exist" and "NetBox refused to create them" have
// different fixes and different retry intervals.
func provenanceReason(err error) string {
	if errors.Is(err, provenance.ErrIncomplete) {
		return netboxv1alpha1.ReasonBootstrapDisabled
	}

	return netboxv1alpha1.ReasonBootstrapFailed
}
