package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(wirelessWirelessLinkDescriptor()) }

// wirelessWirelessLinkDescriptor is wireless.WirelessLink as data.
//
// The first kind whose identity is a **pair of references to one Kind**, and the first where
// the interesting question is whether that pair is ordered.
//
// It is. The single constraint is
// `UniqueConstraint(fields=('interface_a', 'interface_b'),
// name='%(app_label)s_%(class)s_unique_interfaces')` (netbox/wireless/models.py:190-195), with
// no expression and no second conditional form, so Postgres will store `(a,b)` and `(b,a)` as
// two distinct rows. `WirelessLink.clean` does not close the gap either -- it validates that
// both interfaces are of a wireless type and nothing else
// (netbox/wireless/models.py:205-220). A link from A to B and a link from B to A are two
// objects to NetBox and one physical link to everybody else.
//
// **Two candidates, the declared orientation and its reverse.** The second is the crossed one:
// `interface_a_id` filtered from `interfaceBRef` and `interface_b_id` from `interfaceARef`.
// Nothing about a KeyField requires the filter and the spec field to correspond -- `Filter` is
// the query parameter and `Spec` is where the value comes from -- and declaresSpecField still
// checks both names at boot, so a misspelling fails there rather than on the wire.
//
// What that buys is the acceptance criterion, without a line of engine code:
//
//   - This CR's own row, found by `status.id`: reconciled as usual, whichever orientation it
//     was created in.
//   - The row exists as declared and this CR did not create it: candidate one matches, and the
//     ordinary adoption rule applies -- `onConflict: Adopt` takes it over, the default `Fail`
//     reports Conflict (internal/reconciler/errors.go, refusedAdoption). Nothing kind-specific.
//   - **The row exists only reversed**: candidate one finds nothing and candidate two finds it,
//     so the second CR sees the first CR's row instead of concluding the link does not exist.
//     Under the default that is Conflict with nothing written, and under `onConflict: Adopt`
//     one PATCH normalises the orientation. Either way: one physical link, one NetBox row, and
//     the second CR says why it is not Ready.
//
// Without candidate two that last case is a silent duplicate -- the reverse-declared CR would
// look up `(b,a)`, find nothing, and POST a second row for the same radio path, which NetBox's
// ordered constraint is perfectly happy to store.
//
// Canonicalising to ascending resolved id instead -- always filtering and writing `min(a,b)`
// first -- was the other option and is worse: it would silently rewrite which endpoint the user
// called A, and two CRs declaring opposite orientations would then match the same candidate,
// adopt each other's row and PATCH the pair back and forth on every resync. Two candidates
// keep the user's orientation and make the collision loud.
//
// Both endpoints are plain foreign keys, unlike dcim.Cable's terminations, so changing one is
// an ordinary PATCH: UpdateStrategy stays Patch and there is no RecreateOn.
//
// **No `Deferred` entries, and none possible.** validateDeferred refuses to defer a field a
// natural key matches on, and both references are matched on by both candidates -- correctly:
// a deferred reference is by construction unresolved when the lookup runs, and this Kind's
// whole identity is those two references. While either is unresolved no candidate is
// applicable, so the engine waits rather than creating a link with an endpoint it could not
// name (docs/concepts/references.md).
func wirelessWirelessLinkDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxWirelessLink"),
		Endpoint:   "wireless/wireless-links",
		ObjectType: "wireless.wirelesslink",
		Scope:      apiextensionsv1.NamespaceScoped,

		// wireless.WirelessLink is a PrimaryModel (netbox/wireless/models.py:134), which mixes
		// in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// No `auth_psk` entry, for the reason wireless_wirelesslan.go gives: a pre-shared key
		// may not be inline in a spec, and sourcing one from a Secret is shared machinery
		// rather than descriptor data. A column no spec field maps onto cannot reach a payload.
		Fields: []Field{
			{Spec: "ssid", API: "ssid"},
			{Spec: "status", API: "status"},
			{Spec: "authType", API: "auth_type"},
			{Spec: "authCipher", API: "auth_cipher"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// `distance DecimalField decimal(8,2)` and `distance_unit CharField len=50` from
			// DistanceMixin (netbox/netbox/models/mixins.py:77-91). EmptyIsNull on `distance`
			// because it is a nullable non-text column: DRF parses `""` as a number and
			// rejects it, so `distance: ""` would be admission-legal and fail on every write
			// (#170). `distance_unit` is a char column and takes the empty string, so it needs
			// nothing.
			{Spec: "distance", API: "distance", EmptyIsNull: true},
			{Spec: "distanceUnit", API: "distance_unit"},
			{
				Spec: "interfaceARef", API: "interface_a", Class: ClassRefOne,
				Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
				// `interface_a ForeignKey -> dcim.Interface on_delete=PROTECT`
				// (netbox/wireless/models.py:138-143). PROTECT, so not eligible to be the
				// containment parent -- see below for why this Kind has none.
			},
			{
				Spec: "interfaceBRef", API: "interface_b", Class: ClassRefOne,
				Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
				// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`
				// (netbox/wireless/models.py:161-167).
			},
		},

		// The pair, then the pair reversed. `interface_a_id` and `interface_b_id` are both
		// ModelMultipleChoiceFilters on WirelessLinkFilterSet
		// (netbox/wireless/filtersets.py:102-109).
		//
		// No null pins and no fallback candidate: both fields are required, so there is no
		// state in which one is missing and a narrower identity applies. `tenant` is
		// deliberately not a term either -- unlike wireless.WirelessLAN this Kind *has* a real
		// uniqueness constraint and the interface pair is the whole of it, so a tenant filter
		// would only narrow a key that already identifies one row.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "interface_a_id", Spec: "interfaceARef"},
					{Filter: "interface_b_id", Spec: "interfaceBRef"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "interface_a_id", Spec: "interfaceBRef"},
					{Filter: "interface_b_id", Spec: "interfaceARef"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// **No ContainmentRef, and it is a consequence rather than a gap.** All three of this
		// Kind's writable foreign keys are `on_delete=PROTECT`, and validateContainment refuses
		// a containment ref whose Field.CascadeOnDelete is false -- naming one here would fail
		// the boot.
		//
		// There *is* a server-side cascade in the model, and it is worth writing down why it
		// cannot be used: `_interface_a_device` and `_interface_b_device` are
		// `on_delete=CASCADE` to dcim.Device (netbox/wireless/models.py:171-184), which is
		// precisely how deleting a Device collects the link instead of hitting the PROTECT on
		// its interfaces. But they are caches NetBox recomputes in save()
		// (netbox/wireless/models.py:222-227), they are in ReadOnly, and no spec field maps
		// onto either -- and ContainmentRef names a *spec field*, because the owner reference
		// is built from a resolved reference's target CR. There is nothing to own.
		//
		// Nothing resurrects as a result, which is the failure a missing containment parent
		// usually causes (#203). Both natural-key candidates match on both interface
		// references, so once the device and its interfaces are gone neither candidate is
		// applicable, the engine has no identity to look up, and create-if-absent never runs.
		// The CR sits at RefsResolved=False naming the interface that disappeared -- which is
		// the correct report, and the same reason the dcim nested groups are safe without one.

		// The four columns every ChangeLoggedModel carries, plus the two device caches and
		// DistanceMixin's normalised metres. `_abs_distance` is derived from `distance` and
		// `distance_unit` on every save (netbox/netbox/models/mixins.py:108-117) and
		// `_interface_a_device` / `_interface_b_device` from the two interfaces
		// (netbox/wireless/models.py:222-227); writing any of the three does not fail, it
		// silently no-ops, so the next reconcile finds the same difference and PATCHes again
		// forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display",
			"_abs_distance", "_interface_a_device", "_interface_b_device",
		},
	}
}
