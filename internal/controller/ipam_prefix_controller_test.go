package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// prefixKind points the shared stub at ipam.Prefix. The first kind whose natural-key field
// is not `slug`, which is what the stub was parameterised for.
var prefixKind = stubKind{endpoint: "ipam/prefixes", key: "prefix"}

// sitedColumns are the keys that must never appear in a request body sent to
// `ipam/prefixes`, for two different reasons.
//
// `site` and `site_id` do not exist on ipam.Prefix since NetBox 4.2, and NetBox drops a field
// it does not know rather than rejecting it -- so a write containing one returns 201, creates
// an unscoped prefix, and never drifts. That is the netbox-populator bug this kind exists to
// make unrepresentable.
//
// The other six are real columns NetBox maintains itself: the four scope caches from
// `(scope_type, scope_id)` and the two hierarchy counters derived from the prefix value. An
// attempt to set one is dropped exactly like `site`, so the next read finds it unchanged and
// the operator PATCHes it again on every resync, forever.
var sitedColumns = []string{
	"site", "site_id",
	"_site", "_region", "_site_group", "_location",
	"_depth", "_children",
}

// newScopedNetBoxStub is the prefix stub fronted by a handler that also answers the reads an
// id-mode scope reference is verified against.
//
// The scope union's four targets live at four dcim endpoints, and the shared stub serves one
// endpoint by design -- it is parameterised by the kind under test, not by that kind's
// references. Rather than teach it about four more, this fronts it with the smallest thing
// that makes an id-mode ref resolvable: any `GET /api/dcim/<collection>/<id>/` answers with
// that id, and everything else, the version probe included, falls through to the real stub.
//
// It deliberately cannot serve a *write*, so a test that accidentally started managing a Site
// or a Region through this path would fail rather than pass quietly.
func newScopedNetBoxStub(t *testing.T) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, prefixKind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := dcimObjectID(r); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

// dcimObjectID reports the primary key of a `GET /api/dcim/<collection>/<id>/`, and false for
// anything else.
func dcimObjectID(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}

	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	if len(parts) != 3 || parts[0] != "dcim" {
		return 0, false
	}

	id, err := strconv.ParseInt(parts[2], 10, 64)

	return id, err == nil
}

// makePrefix applies a NetBoxPrefix and removes it afterwards so the finalizer does not
// outlive the stub it needs in order to come off. It returns nothing: every caller reads the
// object back through fetchPrefix, because the copy handed to Create carries no status.
func makePrefix(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxPrefix)) {
	t.Helper()

	prefix := &netboxv1alpha1.NetBoxPrefix{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxPrefixSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Prefix:           "10.0.20.0/24",
			Status:           netboxv1alpha1.PrefixStatusActive,
		},
	}
	if mutate != nil {
		mutate(prefix)
	}
	if err := k8sClient.Create(context.Background(), prefix); err != nil {
		t.Fatalf("creating prefix %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), prefix) })
}

func fetchPrefix(ns, name string) *netboxv1alpha1.NetBoxPrefix {
	prefix := &netboxv1alpha1.NetBoxPrefix{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, prefix); err != nil {
		return nil
	}

	return prefix
}

func prefixIsReady(ns, name string) bool {
	prefix := fetchPrefix(ns, name)
	if prefix == nil {
		return false
	}
	for _, c := range prefix.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// assertNothingSited is the acceptance criterion asserted against what was actually sent,
// across every request the engine made, rather than by reading the descriptor.
func assertNothingSited(t *testing.T, stub *netboxStubServer) {
	t.Helper()

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range sitedColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: %v", i, write.Method, column, write.Payload)
			}
		}
	}
}

// TestPrefixIsScopedNeverSited is the populator regression, end to end. A site-scoped prefix
// POSTs the polymorphic pair, a read-back of NetBox shows the scope actually set, and no
// request body anywhere mentions `site`.
//
// The reference is in `id` mode because NetBoxSite lives at a different endpoint and the stub
// serves one. That costs nothing here: the assertion is about what reaches `ipam/prefixes`,
// and an id-mode ref exercises the same GenericFK rendering a name-mode one ends up in.
func TestPrefixIsScopedNeverSited(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newScopedNetBoxStub(t)
	readyEndpoint(t, ns, target)

	siteID := int64(41)
	makePrefix(t, ns, "home-lan", func(p *netboxv1alpha1.NetBoxPrefix) {
		p.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: &siteID}}
		p.Spec.Description = "Home LAN"
	})

	eventually(t, "the prefix to be Ready", func() bool { return prefixIsReady(ns, "home-lan") })

	prefix := fetchPrefix(ns, "home-lan")
	if prefix.Status.ID == 0 {
		t.Fatal("status.id is unset on a Ready prefix")
	}

	live := stub.get(prefix.Status.ID)
	if live["scope_type"] != "dcim.site" {
		t.Errorf("scope_type = %v, want dcim.site -- the prefix is unscoped, which is the populator bug", live["scope_type"])
	}
	if live["scope_id"] != float64(siteID) {
		t.Errorf("scope_id = %v, want %d", live["scope_id"], siteID)
	}

	assertNothingSited(t, stub)
}

// TestPrefixScopeTargetsResolveOrSayWhy walks all four members of the union, and then the
// case where the member names a target that is not there.
//
// The last row is issue #195, and it is the row whose expectation was reversed. It used to
// assert that such a prefix **is created**, with `scope_type`/`scope_id` left out and
// `RefsResolved=False` naming the field -- an unscoped prefix in NetBox for as long as the
// target was missing, because ipam.Prefix's identity is `prefix` and a candidate is
// applicable without the scope. It now asserts that **nothing is written at all**: a
// reference the spec declares is a precondition for the write, so a prefix that asks for a
// scope waits for it.
//
// The assertion is on the recorded traffic and not on the conditions, deliberately. "Neither
// column is in any payload" was the old check and it passes for free once no payload exists,
// so it could not tell the two answers apart.
//
// Reachable reasons only. Every member of the union has a Descriptor in this build since
// NBO-066, so `RefKindUnavailable` -- the reason that made #195 a question -- has no live
// target here; internal/reconciler covers the reason plumbing over a descriptor a test
// controls. What matters end to end is that the *rule* does not care which of the eight
// reasons it was, and that the reason is still reported rather than flattened.
func TestPrefixScopeTargetsResolveOrSayWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scope    netboxv1alpha1.ScopeRef
		wantType string
		wantRefs string
	}{
		{
			name:     "region",
			scope:    netboxv1alpha1.ScopeRef{RegionRef: &netboxv1alpha1.RegionRef{ID: idOf(11)}},
			wantType: "dcim.region",
		},
		{
			name:     "site",
			scope:    netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: idOf(41)}},
			wantType: "dcim.site",
		},
		// These two were the "no Descriptor, so no mode resolves" cases when this test was
		// written. NBO-066 registered both Kinds, so they now resolve like any other member
		// -- which is the whole point of that ticket, and is asserted here rather than in a
		// separate test so that the four members stay one table.
		{
			name:     "site-group",
			scope:    netboxv1alpha1.ScopeRef{SiteGroupRef: &netboxv1alpha1.SiteGroupRef{ID: idOf(21)}},
			wantType: "dcim.sitegroup",
		},
		{
			name:     "location",
			scope:    netboxv1alpha1.ScopeRef{LocationRef: &netboxv1alpha1.LocationRef{ID: idOf(31)}},
			wantType: "dcim.location",
		},
		{
			// `name` mode, so the target is a CR rather than a NetBox row -- and there is no
			// such CR. This is the row that used to expect a create.
			name:     "a member whose target does not exist",
			scope:    netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "nowhere"}},
			wantRefs: netboxv1alpha1.ReasonRefNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := newNamespace(t)
			stub, target := newScopedNetBoxStub(t)
			readyEndpoint(t, ns, target)

			scope := tc.scope
			makePrefix(t, ns, "scoped", func(p *netboxv1alpha1.NetBoxPrefix) { p.Spec.Scope = &scope })

			if tc.wantRefs != "" {
				eventually(t, "the prefix to report the unresolvable member", func() bool {
					prefix := fetchPrefix(ns, "scoped")

					return prefix != nil && prefixRefsReason(prefix) == tc.wantRefs
				})

				if prefixIsReady(ns, "scoped") {
					t.Error("the prefix is Ready with a scope that was never written")
				}

				if got := stub.recorded(); len(got) != 0 {
					t.Errorf("netbox writes = %+v, want none: the spec declares a scope that did not resolve", got)
				}

				if id := fetchPrefix(ns, "scoped").Status.ID; id != 0 {
					t.Errorf("status.id = %d, want 0: nothing was created", id)
				}

				return
			}

			eventually(t, "the prefix to be Ready", func() bool { return prefixIsReady(ns, "scoped") })

			live := stub.get(fetchPrefix(ns, "scoped").Status.ID)
			if live["scope_type"] != tc.wantType {
				t.Errorf("scope_type = %v, want %v", live["scope_type"], tc.wantType)
			}

			assertNothingSited(t, stub)
		})
	}
}

// TestUnscopedPrefixIsCreatedImmediately is the half of #195 that keeps option C from
// collapsing into option B: the precondition is a reference the spec *declares*, not every
// reference the kind could carry.
//
// A prefix with no `scope` key is created on the first pass, Ready, with neither column in the
// body. Without this, one unimplemented or unreachable target Kind would hold up every object
// that merely *could* have referenced it, and an optional field would have become required.
func TestUnscopedPrefixIsCreatedImmediately(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newScopedNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makePrefix(t, ns, "unscoped", nil)

	eventually(t, "the prefix to be Ready", func() bool { return prefixIsReady(ns, "unscoped") })

	live := stub.get(fetchPrefix(ns, "unscoped").Status.ID)
	for _, column := range []string{"scope_type", "scope_id"} {
		if value, present := live[column]; present && value != nil {
			t.Errorf("%s = %v, want an absent scope left alone", column, value)
		}
	}

	assertNothingSited(t, stub)
}

// idOf is the escape-hatch ref mode as a one-liner, so a table entry stays one line.
func idOf(id int64) *int64 { return &id }

// prefixRefsReason returns the RefsResolved reason, which is where an unresolvable union
// member says so.
func prefixRefsReason(prefix *netboxv1alpha1.NetBoxPrefix) string {
	for _, c := range prefix.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionRefsResolved {
			return c.Reason
		}
	}

	return ""
}

// TestPrefixScopeMovesAsOnePair is NBO-003 item 7 on the kind it was written for. Moving a
// prefix from a Region to a Site is one change to one reference, so it must be one PATCH
// carrying both columns -- not two independent diffs a partial write could apply
// inconsistently, leaving `scope_type: dcim.site` against a Region's id.
func TestPrefixScopeMovesAsOnePair(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newScopedNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makePrefix(t, ns, "moving", func(p *netboxv1alpha1.NetBoxPrefix) {
		p.Spec.Scope = &netboxv1alpha1.ScopeRef{RegionRef: &netboxv1alpha1.RegionRef{ID: idOf(11)}}
	})
	eventually(t, "the prefix to be Ready", func() bool { return prefixIsReady(ns, "moving") })

	writesBefore := len(stub.recorded())

	prefix := fetchPrefix(ns, "moving")
	prefix.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{ID: idOf(41)}}
	if err := k8sClient.Update(context.Background(), prefix); err != nil {
		t.Fatalf("moving the scope: %v", err)
	}

	eventually(t, "the move to reach NetBox", func() bool {
		return stub.get(prefix.Status.ID)["scope_type"] == "dcim.site"
	})

	patches := stub.recorded()[writesBefore:]
	if len(patches) != 1 {
		t.Fatalf("the move took %d writes, want exactly 1: %+v", len(patches), patches)
	}

	for _, column := range []string{"scope_type", "scope_id"} {
		if _, present := patches[0].Payload[column]; !present {
			t.Errorf("the PATCH does not carry %q: %v", column, patches[0].Payload)
		}
	}

	assertNothingSited(t, stub)
}

// TestPrefixCELGuardsTheCIDR is the admission half. NetBox canonicalises a prefix to its
// network address on write, so `10.0.20.5/24` would be silently rewritten and then compared
// against a value the user never wrote. Rejecting it at `kubectl apply` turns that into an
// immediate message.
//
// Every v4 case has a v6 twin, because `IPNetworkField` is one column and a v4-shaped regex
// is the mistake this rule exists instead of.
func TestPrefixCELGuardsTheCIDR(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		prefix     string
		wantReject string
	}{
		{prefix: "10.0.20.0/24"},
		{prefix: "fd00:10::/64"},
		{prefix: "0.0.0.0/0"},
		{prefix: "::/0"},
		// A full-length mask is an ordinary prefix, distinct from an ipam.IPAddress of the
		// same value. No special-casing, in either family.
		{prefix: "10.0.20.10/32"},
		{prefix: "fd00:10::a/128"},

		{prefix: "10.0.20.5/24", wantReject: "host bits"},
		{prefix: "fd00:10::a/64", wantReject: "host bits"},
		{prefix: "10.0.20.0", wantReject: "must be a CIDR"},
		{prefix: "fd00:10::", wantReject: "must be a CIDR"},
		{prefix: "10.0.20.0/33", wantReject: "must be a CIDR"},
		{prefix: "not-a-prefix", wantReject: "must be a CIDR"},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			obj := &netboxv1alpha1.NetBoxPrefix{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "cel-"},
				Spec: netboxv1alpha1.NetBoxPrefixSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Prefix:           tc.prefix,
				},
			}

			// A server-side dry run, so admission runs in full and nothing is stored.
			err := apiClient.Create(context.Background(), obj, client.DryRunAll)

			if tc.wantReject == "" {
				if err != nil {
					t.Fatalf("Create was rejected: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Create was accepted, want a rejection naming %q", tc.wantReject)
			}
			if !strings.Contains(err.Error(), tc.wantReject) {
				t.Errorf("rejection %q does not name %q", err, tc.wantReject)
			}
		})
	}
}

// TestPrefixBooleansAreTriState is why `is_pool` and `mark_utilized` are pointers. Both
// columns carry `def=False`, so a plain bool cannot tell "not managed" from "managed as
// false" -- and adopting a prefix a human had marked as a pool would silently clear it.
//
// Three states in one test, because the middle one is the one a bool loses: omitted keeps the
// key out of the payload entirely, `false` sends false, and a value flipped in the NetBox UI
// is corrected.
func TestPrefixBooleansAreTriState(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, prefixKind)
	readyEndpoint(t, ns, target)

	// Absent: the engine must not send either key, or it claims a field nobody asked it to
	// manage.
	makePrefix(t, ns, "unmanaged", nil)
	eventually(t, "the unmanaged prefix to be Ready", func() bool { return prefixIsReady(ns, "unmanaged") })

	for _, write := range stub.recorded() {
		for _, column := range []string{"is_pool", "mark_utilized"} {
			if _, present := write.Payload[column]; present {
				t.Errorf("%s carries %q though the spec never set it: %v", write.Method, column, write.Payload)
			}
		}
	}

	// Set, and set to the column's own default. This is the state a plain bool cannot express.
	pool, utilized := false, true
	makePrefix(t, ns, "managed", func(p *netboxv1alpha1.NetBoxPrefix) {
		p.Spec.Prefix = "10.0.21.0/24"
		p.Spec.IsPool = &pool
		p.Spec.MarkUtilized = &utilized
	})
	eventually(t, "the managed prefix to be Ready", func() bool { return prefixIsReady(ns, "managed") })

	managed := fetchPrefix(ns, "managed")
	live := stub.get(managed.Status.ID)
	if live["is_pool"] != false {
		t.Errorf("is_pool = %v, want false explicitly sent", live["is_pool"])
	}
	if live["mark_utilized"] != true {
		t.Errorf("mark_utilized = %v, want true", live["mark_utilized"])
	}

	// Drift: a human flips it in the NetBox UI and the operator puts it back.
	stub.setField(managed.Status.ID, "is_pool", true)
	eventually(t, "the operator to correct is_pool", func() bool {
		return stub.get(managed.Status.ID)["is_pool"] == false
	})

	assertNothingSited(t, stub)
}
