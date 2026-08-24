# The admission webhook

The operator serves **one validating admission webhook**, and no defaulting webhook. This page
is what it checks, what breaks when it is off, why its `failurePolicy` is `Ignore`, and why
there is no mutator.

## Three layers, and where the line is

Validation happens in three places, cheapest first:

| Layer | Enforced by | Costs |
|---|---|---|
| 1 — CRD schema and CEL | the API server, unconditionally | nothing; it is part of the CRD |
| 2 — this webhook | the operator's own pods | a serving certificate, and one HTTP round trip per apply |
| 3 — NetBox's own `clean()` | NetBox | a reconcile, reported as `Ready=False, Reason=Invalid` |

The line between 1 and 2 is not a matter of taste. **CEL sees `self` and `oldSelf` and nothing
else** — no other CR, no namespace, no grant, no `Descriptor`. So every rule that needs a
second object is layer 2, and every rule that does not, is not.

### What is already layer 1, and therefore not here

Nothing below is duplicated in the webhook. If you are looking for where one of these is
enforced, it is a marker in `api/v1alpha1`:

- `ObjectRef`'s exactly-one-of `name` / `slug` / `lookup` / `id`, and `OptionalRef`'s
  at-most-one-of, which is how "explicitly no reference" is said (`objectref.go`).
- `namespace` only with `name`; the `lookup` key alphabet; the rejection of `q`, `limit`,
  `offset`, `format`, `brief` and `ordering` as lookup keys.
- Every union's at-most-one-of, on the scope pair and on the polymorphic references
  (`scope.go`, `genericref.go`).
- Enum membership, `MinLength`, `MaxLength`, `MaxItems`, `MaxProperties`, and every pattern:
  the slug alphabet, the DNS-label shapes, the six-hex-digit colour.
- CIDR validity **and** the host-bits check on `spec.prefix` (`isCIDR`, `cidr(self).masked()`).
- Latitude and longitude, as both a decimal pattern and a range.
- `spec.endpointRef` **immutability**, as `self == oldSelf` on the shared envelope, so it holds
  for every Kind at once. Pointing a CR at a different NetBox is not a mutation of the object,
  it is a different object.
- `NetBoxIPAddressClaim`'s immutable `prefixRef` and `allocationIdentity`.
- A grant's `namespaces: Selector` requiring a non-empty selector, and `namespaces: All`
  refusing one.

### What this webhook checks

Four rules. Two deny, two warn.

| Rule | Needs a second object because | Verdict |
|---|---|---|
| **Reference cycle** — `a.parentRef -> b`, `b.parentRef -> a`, and any longer ring | the chain has to be walked | **Deny** |
| **Natural-key collision** — another CR of the same Kind, in the same namespace, at the same endpoint, identified by the same key | the siblings have to be listed | **Deny** |
| **Missing grant** — a cross-namespace reference no `NetBoxRefGrant` covers | the grants in the target namespace have to be listed | **Warn** |
| **Endpoint not usable** — `spec.endpointRef` names an endpoint that is absent or not `Ready` | the endpoint has to be read | **Warn** |

Plus one on the grant itself: a `NetBoxRefGrant` whose `spec.to[].kinds` names a Kind this
build does not know is **warned** about, because such an entry grants nothing.

**Why the cycle rule denies.** Every other blocked state ends by itself when the thing it waits
for arrives. A ring waits for itself, forever, and only an edit to one of its members clears
it. The depth-1 case — `parentRef` naming the object it is written on — is the one depth CEL
could express, and it is here anyway so that one implementation answers "is this a cycle" for
every depth. Two implementations of that question is two answers. The walk is capped at 32 hops
and 256 objects; a graph beyond the cap is denied as `ErrRefDepthExceeded` rather than walked,
because an unbounded walk in admission is a denial-of-service.

**Why the collision rule denies.** Two CRs identifying one NetBox object is not a state that
converges: whichever reconciles second reports `Conflict` and writes nothing, indefinitely, and
its manifest looks correct in isolation. Only a sibling makes it wrong.

It is deliberately **conservative**: it compares the values two manifests *wrote*, not the
NetBox ids they will resolve to, because resolving would mean calling NetBox from the admission
path. So two objects naming one site — one by `name`, one by `slug` — do collide in NetBox and
are not reported here. It also keys on the **first applicable** natural-key candidate, which is
the one the engine actually looks an object up by: a `dcim.Region` with a parent is found by
`(parent, name)` and a top-level one by `(name) WHERE parent IS NULL`, so two regions sharing a
`name` are two legitimate regions and are admitted.

A collision **across namespaces** is neither denied nor warned. Telling a namespaced actor,
through a rejection message, what exists in a namespace they cannot read is an information leak,
and it is the residual footgun [ADR-0002](../decisions/0002-crd-scoping.md) already accepts.
The runtime `Conflict` condition reports it instead.

**Why the grant rule only warns.** Order-independence is the design: apply 500 manifests in any
order and the graph converges. A grant legitimately arrives *after* the object that needs it, so
denying here would make admission order-sensitive. It is also a bad control — one that a
different apply order bypasses is not a control. Enforcement is at reconcile, authoritatively,
as `RefsResolved=False, Reason=RefDenied` with **zero NetBox writes**. The warning is fast
feedback and nothing more.

**Why a reference to a CR that does not exist is not reported at all.** That is the ordinary
`WaitingForRef` state of an order-independent apply, not a mistake — and checking it would put
one object read per reference on the admission path in order to say so.

## What breaks when it is off

Every rule has a reconcile-time backstop, and that is the whole basis of the failure policy
below. With the webhook down, the failure moves from apply time to reconcile time. It does not
become silent, and it never corrupts data, because a blocked object performs **zero** NetBox
writes.

| Rule | Webhook up | Webhook down |
|---|---|---|
| reference cycle | rejected at apply | `RefsResolved=False, Reason=RefCycle`, no write |
| same-namespace natural-key collision | rejected at apply | `Ready=False, Reason=Conflict` on the loser, no write |
| grant missing | warning | `Reason=RefDenied`, no write — unchanged, enforcement was never here |
| endpoint absent or not `Ready` | warning | `Reason=WaitingForEndpoint` — unchanged |
| grant naming an unknown Kind | warning | the grant silently permits nothing for that Kind |
| cross-namespace collision | not checked | `Conflict` — unchanged |
| everything in layer 1 | rejected by **CEL** | rejected by CEL — unchanged |

The last row is the important one. `spec.endpointRef` immutability, `spec.prefix`'s CIDR check,
every enum and every one-of are enforced by the API server whether or not anything is serving
admission. Turning this webhook off loses two rejections and three warnings, and nothing else.

To turn it off deliberately — a cluster without cert-manager, or one that has not installed the
configuration — run the manager with `--enable-webhooks=false` and do not apply
`config/webhook`.

## `failurePolicy: Ignore`, and why

Both the reasoning and the counter-argument, because this is the setting that decides what a
broken operator costs the rest of the cluster.

1. **Every rule has a reconcile-time backstop** — the table above. The operator is
   level-triggered and NetBox's `clean()` is the final authority. A bypassed rule becomes a
   condition and an Event rather than a silent success.
2. **`Fail` on a webhook backed by the operator's own Deployment is a deadlock generator.** An
   image-pull failure, an evicted pod, a node drain, an expired certificate or a stale
   `caBundle` would make *every* write to `netbox.kubeforge.org` fail — including the GitOps
   apply that would fix the operator, and including the `NetBoxEndpoint` edit that would unblock
   it. That class of incident is far more likely than the mistakes these four rules catch.
3. **`Ignore` has no blast radius outside this API group.** The rules match
   `apiGroups: [netbox.kubeforge.org]` only, so a bypassed webhook cannot weaken anything else
   in the cluster. Nothing outside this operator depends on it for its own safety.
   `TestShippedWebhookConfiguration` holds the shipped manifest to that.
4. **There is no mutating webhook**, so there is nothing load-bearing to bypass. This is the
   counter-argument answered rather than waved: the usual reason to accept `Fail` is a mutator
   whose absence leaves an object incomplete. There is no such mutator here — see the next
   section — so the argument does not arise.
5. **Availability instead of `Fail`.** Two replicas, `minAvailable: 1`, and **both replicas
   serve the webhook**: the registration is outside the leader-election gate, and the Service
   selects every pod. A webhook gated on leader election is served by one pod, which is the
   classic high-availability bug in this shape of operator. Readiness is gated on the webhook
   server having started, which it cannot do before the serving certificate is on disk — so a
   replica that cannot complete a TLS handshake stays out of the Service instead of silently
   absorbing a share of the reviews.

The rest of the configuration follows from the same reasoning: `sideEffects: None` (nothing here
writes, so a dry run is the same review — `TestDryRunIsHonoured`), `matchPolicy: Equivalent`,
`timeoutSeconds: 5`, `operations: [CREATE, UPDATE]` and **no `status` subresource in the
rules**, so the controllers' own status writes do not pass back through admission. `DELETE` is
not registered: there is nothing to validate, and a webhook on `DELETE` is another way to make
an object undeletable.

## Why there is no defaulting webhook

[NBO-044](../../specs/NBO-044-admission-webhooks.md) proposed one, defaulting `spec.endpointRef`
from a per-endpoint `defaultForNamespaces`, and `spec.slug` from `slugify(spec.name)`. It is not
implemented, deliberately, for three reasons in increasing order of weight.

**1. It writes `spec`, which is the one invariant this project has.**
[ADR-0005 §1](../decisions/0005-gitops-coexistence.md) says the operator never writes a `spec`,
ever, and `internal/controller/specguard.go` enforces it. A webhook is a *different actor*, so
on the letter of the ADR it is not covered — and on the mechanism the ADR protects against, a
mutating webhook is genuinely different from a controller write: it mutates the object in
flight, on the same request the GitOps tool made, exactly as a `+kubebuilder:default` does. There
is no revert-and-rewrite loop in that.

**But the ADR's premise is Flux and Argo CD owning the spec, and both use server-side apply.**
Under SSA the webhook's field manager owns the field it filled, so a later apply that omits it
can strip it, and the field flaps between the two managers. NBO-044 names this and its own
mitigation is "document that SSA users should set `endpointRef` explicitly" — which is to say the
feature does not work for this project's primary deployment posture. That is incompatible with
the *spirit* of ADR-0005 even where it survives the letter.

**2. It would require deleting validation that the API server enforces unconditionally.**
`spec.endpointRef` and every `spec.slug` are **required** today, with `MinLength=1`. Defaulting
them means making them optional, which trades a guarantee the API server makes whether or not
anything is serving admission for one made by a webhook whose `failurePolicy` is `Ignore`. NBO-044's
own answer to that is a `netbox_operator_webhook_bypassed_total` metric and an alert — which is
instrumenting a hole rather than not making one.

**3. There is no field to default from.** `NetBoxEndpoint` has no `defaultForNamespaces` and no
`defaultTenantRef`. Adding either is an API decision belonging with [#173](https://github.com/ricardomolendijk/netbox-operator/issues/173)
and [#185](https://github.com/ricardomolendijk/netbox-operator/issues/185), not something to
land inside a webhook ticket.

**The alternative, if per-namespace defaults are wanted.** Resolve them at **reconcile** and
report the result in **status**, which needs no webhook, no spec write and no SSA caveat, works
with the operator's own machinery, and degrades to a condition rather than to a stripped field:

- `spec.endpointRef` optional in the type, resolved per pass from the endpoint whose
  `defaultForNamespaces` matches, with the applied endpoint in `status.endpointRef` and
  `Ready=False, Reason=EndpointUnset` when no unambiguous default exists;
- `spec.tenantRef` the same way, which is what [#205](https://github.com/ricardomolendijk/netbox-operator/issues/205)'s
  `OptionalRef` exists for: `tenantRef: {}` is "explicitly no tenant" and opts out of the
  default, and a default nobody can opt out of is the bug #185 was about;
- `slug` derived from `name` in the payload builder, never written into the spec.

## Certificates

**cert-manager only.** `config/certmanager` ships a self-signed `Issuer` and a `Certificate` for
the webhook `Service`, and `config/webhook` puts `cert-manager.io/inject-ca-from` on the
configuration so the CA injector fills in the `caBundle`. The certificate is renewed at two
thirds of its lifetime (`duration: 8760h`, `renewBefore: 2920h`). `insecureSkipTLSVerify` is
never set.

NBO-044's **self-signed fallback is not implemented**, and that is the point rather than a gap.
It would need `update` and `patch` on `validatingwebhookconfigurations` — which is patching
cluster-wide admission, a genuine privilege the operator otherwise does not hold anywhere — plus
a CA rotation that publishes the new CA in the bundle *before* serving the new leaf, or every
rotation is a brief total outage of admission. cert-manager already does all of that, correctly.
A cluster without it runs `--enable-webhooks=false` and loses two rejections and three warnings,
every one of which has a backstop in the table above.

Because there is no fallback, `config/rbac` grants nothing on `*webhookconfigurations`. That is
worth checking for if it ever appears.

## Why adding a Kind changes nothing here

The webhook configuration matches `resources: ["*"]` inside `netbox.kubeforge.org` and serves one
path, rather than one path and one `+kubebuilder:webhook` marker per Kind. The handler looks the
`Descriptor` up by `GroupVersionKind` and every rule reads the `Descriptor`'s field map and
natural keys; a Kind with no `Descriptor` is admitted by a guard clause. So there is no per-Kind
code and no `switch` on Kind under `internal/webhook/admission`, which is the same rule as
everywhere else in this operator.

`*` matches resources and **not** subresources — `netboxsites/status` is a separate entry — which
is also how the status subresource stays out of the rules.

## Not implemented

Named rather than left to be discovered:

- **The reads are not all cached.** The natural-key sibling `List` and the endpoint `Get` are
  typed, so they go through the manager's informer cache. The cycle walk and the grant check
  read through `internal/resolver`, which reads target CRs as
  `*unstructured.Unstructured` — and controller-runtime's client caches unstructured objects
  only when told to. Those reads therefore reach the API server, on the reconcile path as much
  as on the admission path. Turning it on is `Client.Cache.Unstructured: true` on the manager,
  which changes behaviour for every controller and belongs in its own change.
- **No latency gate in CI.** `netbox_operator_webhook_duration_seconds` is exported and is the
  instrument to alert on; a wall-clock percentile asserted in `envtest` on shared CI is a flake
  generator rather than a gate.
- **No `NetBoxEndpoint` `defaultForNamespaces` collision check.** The field does not exist.
