package resolver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// anyName is the `to[].names` entry that means every name. A whole entry, never a prefix:
// see NetBoxRefGrant's RefGrantTo.Names.
const anyName = "*"

// ErrNoGrantReader is a Resolver asked to cross a namespace with no GrantReader wired.
//
// A returned error rather than a denial, and deliberately not one of the ErrRef* causes: it
// is a wiring bug in this operator, not a statement about anybody's manifest, and reporting
// it as `RefDenied` would send every affected team off to write grants that were never the
// problem. It fails closed either way -- nothing resolves -- but it fails closed *loudly*,
// with a log line and a backoff, which is what a bug in the operator should get.
var ErrNoGrantReader = errors.New("no grant reader is configured, so no cross-namespace reference can be authorised")

// GrantReader reads the NetBoxRefGrants in a namespace, and the labels of the namespace a
// reference is made from.
//
// Two methods, and the controller-runtime signatures rather than narrower ones, for the same
// reason Reader takes them: an adapter is code that can be wrong about which objects it
// fetched. Satisfied by client.Client, so production reads go through the manager's caches
// and a test needs no cluster.
//
// Separate from Reader rather than folded into it, because the two are read for opposite
// purposes: Reader fetches the object a reference points at, and this decides whether the
// reference was allowed to point there at all. A Resolver built for a caller that only ever
// resolves within one namespace can supply the first and not the second.
type GrantReader interface {
	// Get reads one object -- a Namespace, for the labels a selector matches on.
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error

	// List reads the grants in one namespace.
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

// The grant is read by this package and by nothing else, so its permission lives here
// rather than in a controller: NetBoxRefGrant has no controller to carry it (nothing about
// a grant is reconciled into NetBox), and a marker parked in an otherwise empty controller
// file would put the permission somewhere no reader of this code would look for it.
//
// Namespaces are read for their labels alone, and only when a grant actually uses
// `namespaces: Selector` -- a cluster that grants with `namespaces: All` never reads one.
// The permission is still cluster-wide and static, because RBAC cannot express "only the
// ones a selector is pointed at".
//
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrefgrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// authorise reports whether this reference is allowed to cross into the target's namespace.
//
// The common case is free and must stay free: a reference that does not leave its own
// namespace returns before anything is read, because a namespace does not need to grant
// itself access to itself and a grant list per same-namespace reference would put a LIST on
// the hot path of almost every object in the cluster.
//
// Checked before the target is read, not after. A denied reference must not be able to tell
// a missing object from a present one in a namespace it has no access to -- otherwise the
// condition message is an existence oracle for somebody else's namespace -- and the cheaper
// order happens to be the safe one.
func (r *Resolver) authorise(ctx context.Context, req Request, key types.NamespacedName) error {
	if key.Namespace == req.Referrer.Namespace {
		return nil
	}

	if r.Grants == nil {
		return fmt.Errorf("authorising %s -> %s: %w", req.Field.Spec, key, ErrNoGrantReader)
	}

	grants := &netboxv1alpha1.NetBoxRefGrantList{}
	if err := r.Grants.List(ctx, grants, client.InNamespace(key.Namespace)); err != nil {
		return fmt.Errorf("listing netboxrefgrants in %s: %w", key.Namespace, err)
	}

	check := &grantCheck{
		reader: r.Grants, from: req.Referrer.Namespace, kind: req.Field.Target.Kind, name: key.Name,
	}

	allowed, err := check.allows(ctx, grants.Items)
	if err != nil {
		return err
	}

	if allowed {
		return nil
	}

	return req.blockedTarget(ErrRefDenied, key, check.detail(key))
}

// grantCheck is one authorisation decision: the reference being made, and what was learned
// about it while looking for a grant that permits it.
//
// A struct rather than a function taking six arguments because the near misses are the
// product. "Denied" on its own is the least useful thing a default-deny feature can say, so
// what almost matched -- and any grant too malformed to match at all -- is accumulated here
// and rendered into the message.
type grantCheck struct {
	reader GrantReader

	// from is the referring namespace, kind and name the target.
	from string
	kind string
	name string

	// endpointExcluded records that a grant would have permitted this reference had the
	// target not been a NetBoxEndpoint, which is the one refusal nothing in the YAML
	// explains.
	endpointExcluded bool

	// problems are grants that could not be evaluated. Reported rather than swallowed: a
	// selector nothing can compile is a denial with no cause written anywhere else.
	problems []string

	// nsLabels caches the referring namespace's labels, read at most once and only if some
	// grant selects by them.
	nsLabels labels.Set
	nsRead   bool
}

// allows reports whether any grant in the target namespace permits this reference.
func (c *grantCheck) allows(ctx context.Context, grants []netboxv1alpha1.NetBoxRefGrant) (bool, error) {
	for i := range grants {
		admits, err := c.admits(ctx, &grants[i])
		if err != nil {
			return false, err
		}

		if admits {
			return true, nil
		}
	}

	return false, nil
}

// admits reports whether one grant permits this reference: some `from` entry covers the
// referring namespace, and some `to` entry covers the target.
func (c *grantCheck) admits(ctx context.Context, grant *netboxv1alpha1.NetBoxRefGrant) (bool, error) {
	audience, err := c.inAudience(ctx, grant)
	if err != nil || !audience {
		return false, err
	}

	return c.inTargets(grant), nil
}

// inAudience reports whether the referring namespace is one this grant admits.
func (c *grantCheck) inAudience(ctx context.Context, grant *netboxv1alpha1.NetBoxRefGrant) (bool, error) {
	for _, from := range grant.Spec.From {
		if from.Namespaces == netboxv1alpha1.NamespacesAll {
			return true, nil
		}

		selector, err := metav1.LabelSelectorAsSelector(from.Selector)
		if err != nil {
			c.problems = append(c.problems, fmt.Sprintf("netboxrefgrant %s/%s has a selector nothing can evaluate: %v",
				grant.Namespace, grant.Name, err))

			continue
		}

		known, err := c.namespaceLabels(ctx)
		if err != nil {
			return false, err
		}

		if selector.Matches(known) {
			return true, nil
		}
	}

	return false, nil
}

// inTargets reports whether the target's Kind and name are among what this grant exposes.
func (c *grantCheck) inTargets(grant *netboxv1alpha1.NetBoxRefGrant) bool {
	for _, to := range targetsOf(grant) {
		if c.kindExposed(to.Kinds) && nameExposed(to.Names, c.name) {
			return true
		}
	}

	return false
}

// kindExposed reports whether the target's Kind is one this entry exposes, and records the
// NetBoxEndpoint refusal when it is not.
func (c *grantCheck) kindExposed(kinds []string) bool {
	if len(kinds) > 0 {
		return slices.Contains(kinds, c.kind)
	}

	if c.kind == netboxv1alpha1.EndpointKind {
		// The security boundary of the whole feature: an empty kind list is the ergonomic
		// default and it stops short of the one reference that lends a token Secret. See
		// v1alpha1.EndpointKind.
		c.endpointExcluded = true

		return false
	}

	return true
}

// namespaceLabels reads the referring namespace's labels, once.
//
// Lazily, so the wildcard form costs nothing: a cluster whose grants all say
// `namespaces: All` never reads a Namespace object, and never needs the informer that
// reading one through a cache starts.
func (c *grantCheck) namespaceLabels(ctx context.Context) (labels.Set, error) {
	if c.nsRead {
		return c.nsLabels, nil
	}

	live := &corev1.Namespace{}
	if err := c.reader.Get(ctx, client.ObjectKey{Name: c.from}, live); err != nil {
		// Not the reference's fault and not something a grant can fix -- a missing
		// permission on namespaces, or an API server that said no -- so it stays an error
		// and gets the caller's backoff rather than being reported as a denial.
		return nil, fmt.Errorf("reading namespace %s for its labels: %w", c.from, err)
	}

	c.nsLabels, c.nsRead = live.GetLabels(), true

	return c.nsLabels, nil
}

// detail is what to tell somebody meeting default deny for the first time.
//
// It names the object to create, the namespace to create it in, and the two spellings worth
// knowing: the narrow one for this reference, and the wildcard that scales. Nobody should
// have to open the docs to get unstuck, and nobody should learn only the form ADR-0002 says
// will not survive four teams.
func (c *grantCheck) detail(target types.NamespacedName) string {
	rendered := fmt.Sprintf("namespace %q is not permitted to reference namespace %q: %s",
		c.from, target.Namespace, c.remedy(target))

	if len(c.problems) == 0 {
		return rendered
	}

	return rendered + "; " + strings.Join(c.problems, "; ")
}

// remedy is the YAML to write, in flow style so it fits on the one line a condition message
// is read on and pastes into a manifest unchanged.
func (c *grantCheck) remedy(target types.NamespacedName) string {
	if c.endpointExcluded {
		return fmt.Sprintf(
			"a NetBoxRefGrant in %q admits that namespace, but its spec.to names no kinds and an empty kind list never covers %s"+
				" -- lending an endpoint lends the token Secret behind it, so it has to be named:"+
				" spec.to: [{kinds: [%s], names: [%s]}]",
			target.Namespace, netboxv1alpha1.EndpointKind, netboxv1alpha1.EndpointKind, target.Name)
	}

	return fmt.Sprintf(
		"create a NetBoxRefGrant in %q with spec.from: [{namespaces: Selector, selector: {matchLabels: {%s: %s}}}]"+
			" and spec.to: [{kinds: [%s]}], or spec.from: [{namespaces: All}] to admit every namespace",
		target.Namespace, corev1.LabelMetadataName, c.from, c.kind)
}

// targetsOf is a grant's `to` entries, with the omitted list read as the one entry that
// means "everything here except an endpoint".
//
// Normalised in one place so the empty case is not a branch in every caller, and so the
// default that makes the three-line catalogue grant possible is written down exactly once.
func targetsOf(grant *netboxv1alpha1.NetBoxRefGrant) []netboxv1alpha1.RefGrantTo {
	if len(grant.Spec.To) == 0 {
		return []netboxv1alpha1.RefGrantTo{{}}
	}

	return grant.Spec.To
}

// nameExposed reports whether name is one this entry exposes.
func nameExposed(names []string, name string) bool {
	if len(names) == 0 {
		return true
	}

	return slices.Contains(names, anyName) || slices.Contains(names, name)
}
