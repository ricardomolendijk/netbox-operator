// Package admission is layer 2 of the three validation layers in plan.md §11: the checks
// that need a **second object** and therefore cannot be CEL.
//
// The line is not a matter of taste. CEL on a CRD sees `self` and `oldSelf` and nothing
// else -- no other CR, no namespace, no grant, no Descriptor -- so every rule that needs
// one of those is here, and every rule that does not, is not. docs/operations/
// admission-webhooks.md carries the full split and the degradation table.
//
// There is deliberately **no defaulting webhook**. A mutating webhook writes `spec`, which
// is the one thing docs/decisions/0005-gitops-coexistence.md §1 forbids, and the two fields
// NBO-044 proposed defaulting (`endpointRef`, `slug`) are currently *required* -- with the
// API server enforcing that unconditionally. Trading that for a webhook whose failurePolicy
// is Ignore is a strictly weaker guarantee. See the ADR-0005 section of the operations doc.
//
// Everything here is driven off internal/registry. Adding a Kind adds nothing to this
// package: the webhook configuration matches `resources: ["*"]` within the API group, the
// handler looks the Descriptor up by GroupVersionKind, and a Kind with no Descriptor is
// admitted by a guard clause rather than by an edit here.
package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// Path is where the validating webhook is served, and it is one path for the whole API
// group rather than one per Kind.
//
// controller-runtime's WebhookBuilder derives a path from a type, which would mean one
// registration and one `+kubebuilder:webhook` marker per Kind -- ~120 markers, each of
// which a new Kind could forget. One path matching `resources: ["*"]` inside
// `netbox.kubeforge.org` is the same guarantee with none of the copies, and it is what makes
// "adding a Kind requires no change here" true rather than aspirational.
//
// `*` matches resources and **not** subresources (`netboxsites/status` is a separate
// entry), which is the other half of why it is spelled this way: the controllers' own status
// writes must not pass back through admission.
const Path = "/validate-netbox-kubeforge-org"

// The webhook configuration itself, generated into config/webhook by `make manifests`.
//
// failurePolicy=ignore is a decision rather than a default, argued in full in
// docs/operations/admission-webhooks.md. In short: every rule below has a reconcile-time
// backstop that is the authority anyway, so a webhook that is down moves a failure from
// apply time to reconcile time rather than losing it -- while `Fail` on a webhook backed by
// this operator's own Deployment makes an image-pull failure or a stale caBundle into a
// total write outage for the API group, including the apply that would fix it.
//
// +kubebuilder:webhook:path=/validate-netbox-kubeforge-org,mutating=false,failurePolicy=ignore,matchPolicy=Equivalent,sideEffects=None,timeoutSeconds=5,groups=netbox.kubeforge.org,resources=*,verbs=create;update,versions=*,name=validate.netbox.kubeforge.org,admissionReviewVersions=v1

// Setup registers the validating webhook on the manager's webhook server.
//
// Registered on **every** replica and never gated on leader election. A webhook served only
// by the leader is served by one pod, which is the classic high-availability bug in this
// shape of operator: the Service selects two endpoints, half the admission requests reach a
// pod that answers 404, and the failurePolicy decides whether that is an outage or a silent
// bypass.
func Setup(mgr ctrl.Manager) {
	// mgr.GetClient() is narrowed to client.Reader on the way in, so the handler is
	// structurally incapable of writing anything. That is not decoration: a *defaulting*
	// webhook is the one actor that could write a spec without going through
	// internal/controller/specguard.go, and the cheapest way to keep that impossible is to
	// hold a type with no write methods on it.
	mgr.GetWebhookServer().Register(Path, &webhook.Admission{
		Handler: &validator{reader: mgr.GetClient(), scheme: mgr.GetScheme()},
	})
}

// validator answers one admission review.
type validator struct {
	// reader reads the second objects every rule here needs: the grants authorising a
	// cross-namespace reference, the endpoint an object writes through, and the siblings a
	// natural key could collide with.
	reader client.Reader

	// scheme turns the review's GroupVersionKind into the typed object to decode into.
	// Typed rather than unstructured on purpose: a typed read goes through the manager's
	// cache, where the same read as *unstructured.Unstructured would go live to the API
	// server (controller-runtime's client caches unstructured only when asked to).
	scheme *runtime.Scheme
}

// Handle validates one object, and times itself doing so.
func (v *validator) Handle(ctx context.Context, req admission.Request) admission.Response {
	gvk := schema.GroupVersionKind{Group: req.Kind.Group, Version: req.Kind.Version, Kind: req.Kind.Kind}

	started := time.Now()
	response := v.review(ctx, req, gvk)
	metrics.ObserveWebhook(gvk.Kind, string(req.Operation), time.Since(started))

	return response
}

// review is Handle without the stopwatch.
//
// dryRun needs no handling at all, and that is a property worth stating rather than a gap:
// every rule below reads and nothing writes, so `sideEffects: None` is true and a dry-run
// review is the same review. TestDryRunIsHonoured holds it to that.
func (v *validator) review(
	ctx context.Context, req admission.Request, gvk schema.GroupVersionKind,
) admission.Response {
	obj, err := v.decode(req, gvk)
	if err != nil {
		// Admitted with a warning rather than refused. The API server has already validated
		// this body against the CRD schema; a Kind this build cannot decode is a version
		// skew between the operator and its CRDs, and turning that into a rejection would
		// make an upgrade a write outage -- the same failure mode failurePolicy: Ignore
		// exists to avoid, reached from inside the handler.
		return admission.Allowed("").WithWarnings(
			fmt.Sprintf("the netbox-operator admission webhook could not read this %s and did not check it: %v",
				gvk.Kind, err))
	}

	// The grant is the one Kind whose *own content* names Kinds, so it is checked against
	// the registry rather than through one. It has no Descriptor and never will -- nothing
	// about a grant is reconciled into NetBox -- so a registry lookup cannot reach it.
	if grant, isGrant := obj.(*netboxv1alpha1.NetBoxRefGrant); isGrant {
		return admission.Allowed("").WithWarnings(unknownKinds(grant)...)
	}

	d, registered := registry.Get(gvk)
	if !registered {
		// A NetBoxEndpoint, a claim, or a Kind whose Descriptor this build does not carry.
		// Nothing here has a second object to check it against.
		return admission.Allowed("")
	}

	return v.reviewObject(ctx, obj, d)
}

// decode turns the review's raw body into the typed object the rules read.
func (v *validator) decode(req admission.Request, gvk schema.GroupVersionKind) (client.Object, error) {
	typed, err := v.scheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("resolving %s against the scheme: %w", gvk.Kind, err)
	}

	obj, ok := typed.(client.Object)
	if !ok {
		return nil, fmt.Errorf("%s is not a cluster object", gvk.Kind)
	}

	if err := json.Unmarshal(req.Object.Raw, obj); err != nil {
		return nil, fmt.Errorf("decoding the submitted %s: %w", gvk.Kind, err)
	}

	// CREATE with a generateName has no name yet, and every rule here identifies objects by
	// name. The API server fills it in after admission, so the review's own metadata is the
	// only place it can come from.
	if obj.GetName() == "" {
		obj.SetName(req.Name)
	}

	if obj.GetNamespace() == "" {
		obj.SetNamespace(req.Namespace)
	}

	return obj, nil
}

// reviewObject runs the layer-2 rules over one object of a registered Kind.
//
// A list rather than a sequence of if-blocks, so that the two denials and the two warnings
// are one readable table and a new rule is one entry. Ordered denial-first: a reader meeting
// a rejection should meet the reason their apply failed before three warnings about
// something else.
//
// A rule that returns an *error* rather than a verdict has failed to run -- a cache read
// that errored, a spec this build cannot decode -- and is reported as a skipped check rather
// than as a denial. Denying on a failure of the check itself is how a validating webhook
// becomes an outage with `failurePolicy: Ignore` still set.
func (v *validator) reviewObject(
	ctx context.Context, obj client.Object, d registry.Descriptor,
) admission.Response {
	spec, err := resolver.SpecMap(obj)
	if err != nil {
		return admission.Allowed("").WithWarnings(
			fmt.Sprintf("the netbox-operator admission webhook could not read this spec and did not "+
				"check it: %v", err))
	}

	review := &objectReview{
		obj:    obj,
		desc:   d,
		spec:   spec,
		read:   v.reader,
		scheme: v.scheme,
		refs:   &resolver.Resolver{Objects: v.reader, Grants: v.reader},
	}

	for _, rule := range []struct {
		name  string
		check func(context.Context) (string, error)
	}{
		{"reference cycle", review.cycle},
		{"duplicate flag on a generated object", review.generatedDuplicate},
		{"natural-key collision", review.collision},
		{"reference grant", review.grants},
		{"endpoint readiness", review.endpoint},
	} {
		denial, err := rule.check(ctx)
		if err != nil {
			review.warn("the %s check could not be completed and was skipped: %v", rule.name, err)

			continue
		}

		if denial != "" {
			return admission.Denied(denial).WithWarnings(review.warnings...)
		}
	}

	return admission.Allowed("").WithWarnings(review.warnings...)
}

// objectReview is one object under review, and what the rules have found so far.
type objectReview struct {
	obj  client.Object
	desc registry.Descriptor

	// spec is the object's spec, decoded once: three of the four rules read it, and
	// re-encoding the object per rule would be the only cost in this package that grows with
	// the number of rules rather than with the object.
	spec specMap

	// read is the cached reader the rules take their second objects from.
	read client.Reader

	// scheme resolves the List Kind the collision check lists siblings into.
	scheme *runtime.Scheme

	// refs is the resolver, which already owns the two graph walks this needs: the cycle
	// check (internal/resolver/cycle.go) and the grant check (grants.go). Reusing it is what
	// keeps admission and reconcile from disagreeing about whether a manifest is legal --
	// two implementations of "is this a cycle" is two answers.
	refs *resolver.Resolver

	warnings []string
}

// warn records a warning for the response.
func (r *objectReview) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// cycle denies a reference cycle, and a chain too long to prove anything about.
//
// **Deny**, not warn, and it is the one rule here that is genuinely unrecoverable: every
// other blocked state ends by itself when the thing it waits for arrives, while a ring waits
// for itself forever. It is also the rule CEL comes closest to expressing and still cannot:
// a root-level rule can compare `self.spec.parentRef.name` with `self.metadata.name`, so
// depth 1 is expressible, but `a -> b -> a` needs to read `b`.
func (r *objectReview) cycle(ctx context.Context) (string, error) {
	err := r.refs.Check(ctx, r.obj, r.desc)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, resolver.ErrRefCycle), errors.Is(err, resolver.ErrRefDepthExceeded):
		return err.Error(), nil
	default:
		return "", fmt.Errorf("walking the reference graph: %w", err)
	}
}

// grants warns about a cross-namespace reference no NetBoxRefGrant covers.
//
// **Warn**, and that is a design decision rather than a soft touch. Order-independence is the
// property NBO-017 exists to establish: apply 500 manifests in any order and the graph
// converges. A grant legitimately arrives after the object needing it, so denying here would
// make admission order-sensitive. It is also a bad control: one that a different apply order
// bypasses is not a control. Enforcement is at reconcile, authoritatively, as
// `RefsResolved=False, Reason=RefDenied` with zero NetBox writes; this is fast feedback and
// nothing more.
func (r *objectReview) grants(ctx context.Context) (string, error) {
	ungranted, err := r.refs.UngrantedRefs(ctx, r.obj, r.desc)
	if err != nil {
		return "", fmt.Errorf("checking the reference grants: %w", err)
	}

	for _, ref := range ungranted {
		r.warn("this reference is not authorised yet and will report RefDenied until it is: %s", ref)
	}

	return "", nil
}

// endpoint warns about a spec.endpointRef naming an endpoint that is absent or not Ready.
//
// **Warn**: an endpoint applied a moment later is the ordinary case for a whole namespace
// created in one `kubectl apply`, and an endpoint that is merely not Ready *yet* is what the
// engine's WaitingForEndpoint state is for.
func (r *objectReview) endpoint(ctx context.Context) (string, error) {
	name := endpointRefOf(r.spec)
	if name == "" {
		return "", nil
	}

	live := &netboxv1alpha1.NetBoxEndpoint{}
	key := client.ObjectKey{Namespace: r.obj.GetNamespace(), Name: name}

	if err := r.read.Get(ctx, key, live); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("reading netboxendpoint %s: %w", key, err)
		}

		r.warn("no NetBoxEndpoint %q exists in namespace %q yet, so this object will report "+
			"WaitingForEndpoint until one does", name, key.Namespace)

		return "", nil
	}

	if ready := readyCondition(live); ready == nil || ready.Status != metav1.ConditionTrue {
		r.warn("NetBoxEndpoint %q is not Ready (%s), so nothing will be written through it yet",
			name, endpointState(ready))
	}

	return "", nil
}

// readyCondition is the endpoint's Ready condition, or nil when it reports none.
func readyCondition(endpoint *netboxv1alpha1.NetBoxEndpoint) *metav1.Condition {
	return apimeta.FindStatusCondition(endpoint.Status.Conditions, netboxv1alpha1.ConditionReady)
}

// endpointState renders why an endpoint is not usable, in the endpoint's own words.
func endpointState(ready *metav1.Condition) string {
	if ready == nil {
		return "it has not been reconciled yet"
	}

	return fmt.Sprintf("Ready=%s, Reason=%s: %q", ready.Status, ready.Reason, ready.Message)
}

// unknownKinds warns about a NetBoxRefGrant naming a Kind this build does not know.
//
// **Warn, where NBO-044 asked for a denial.** A typo'd Kind in a grant silently grants
// nothing, which is a security-relevant silent failure and deserves to be visible -- but
// NetBoxRefGrant's own API contract says in as many words that an unknown Kind is *inert
// rather than an error*, so a grant may be written before the Kind it names exists, exactly
// as a typed ref alias may point at a Kind with no CRD yet. Denying would break that
// documented property, and forward-compatible manifests are worth more than a rejection when
// a warning carries the same information. Recorded as this implementation's decision.
func unknownKinds(grant *netboxv1alpha1.NetBoxRefGrant) []string {
	known := map[string]bool{netboxv1alpha1.EndpointKind: true}
	for _, d := range registry.List() {
		known[d.GVK.Kind] = true
	}

	var warnings []string

	for _, to := range grant.Spec.To {
		for _, kind := range to.Kinds {
			if known[kind] {
				continue
			}

			warnings = append(warnings, fmt.Sprintf(
				"spec.to names kind %q, which this build does not know: the grant permits nothing "+
					"for it. Check the spelling, or ignore this if the Kind ships in a later release.", kind))
		}
	}

	return warnings
}
