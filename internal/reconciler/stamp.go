package reconciler

import (
	"context"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// stamp adds the endpoint's provenance to the payload about to be written, and records on
// the CR's status what it added (NBO-075).
//
// It runs on every write path -- create, adopt and update -- rather than only on the two the
// ticket names. Create and adopt are where a stamp first appears; running on update as well
// is what puts one back after somebody removed it in the NetBox UI, and costs nothing
// otherwise because a stamp that is already correct produces no drift.
//
// live is the object as NetBox currently holds it, and nil on a create. The stamp needs it
// because `tags` is a full-replacement list: sending only the operator's tag would strip
// every tag a human applied. Called before netbox.Changes, or the stamp would be compared
// out of the payload it is supposed to be part of.
func (p *pass) stamp(ctx context.Context, live netbox.Object) {
	status := p.obj.NetBoxStatus()

	applied, ok := p.endpoint.Provenance.Apply(p.desired, live, p.owner(), stampTarget(p.desc))
	if !ok {
		// Cleared rather than left alone. A stale stamp in status would claim an object
		// carries provenance it no longer does -- after spec.managedBy was removed from the
		// endpoint, or on a kind whose NetBox model has neither column -- and NetBoxSweep
		// (NBO-046) reads exactly this field to decide what it may touch.
		status.Provenance = nil

		return
	}

	logf.FromContext(ctx).V(1).Info("stamping netbox provenance",
		"action", "stamp", "netboxID", status.ID,
		"tag", applied.Tag, "clusterID", applied.ClusterID)

	status.Provenance = applied.DeepCopy()
}

// owner is the CR behind this pass, as the stamp names it.
func (p *pass) owner() provenance.Owner {
	return provenance.Owner{
		Kind:      p.desc.GVK.Kind,
		Namespace: p.obj.GetNamespace(),
		Name:      p.obj.GetName(),
		UID:       string(p.obj.GetUID()),
	}
}

// stampTarget reads off the descriptor which of the two stamp columns this kind's NetBox
// model carries.
//
// Data on the Descriptor and not a lookup table here, for the usual reason: extras.Tag
// carries neither column, and the alternative to a declared fact is the engine knowing the
// names of the kinds that are exceptions.
func stampTarget(d registry.Descriptor) provenance.Target {
	return provenance.Target{Taggable: d.Taggable, CustomFields: d.CustomFieldable}
}
