# `NetBoxRefGrant`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRefGrant` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbgrant` |
| Status subresource | **no** |
| Lands with | NBO-014 (M2) |

A `NetBoxRefGrant` permits references **from other namespaces into the namespace it lives
in**. Without one, a `name` reference that names another namespace is refused:
`RefsResolved=False, Reason=RefDenied`.

Two things about it are worth getting straight before anything else.

**It lives in the namespace being referenced, and that direction is the whole design.** A
grant is a capability the target namespace hands out about *itself*. The same object in the
referring namespace would be a claim anybody could write about somebody else's objects,
which authorises nothing. Read every grant as "*this* namespace is readable by …".

**It is not an edge case.** Every kind is namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)),
so a team namespace pointing at a shared `netbox-catalog` namespace is the ordinary shape of
a reference — `deviceTypeRef`, `manufacturerRef`, `tags` and every other catalogue reference
crosses a namespace. This kind is therefore on the path of almost every object in the
cluster, which is why the wildcard and selector forms below exist and why a grant per
(namespace, kind) pair is a design failure rather than a strict configuration.

And one thing it is **not**: see
[a grant is not NetBox authorisation](#a-grant-is-not-netbox-authorisation).

## Minimal example

This is the one most clusters want, and it is the whole object: the catalogue namespace is
readable by every namespace in the cluster.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readable-by-all
  namespace: netbox-catalog
spec:
  from:
    - namespaces: All
```

Three lines of spec, one object, and it does not need editing when a team is onboarded or
when this operator learns a new kind. It covers every Kind in `netbox-catalog` under every
name — **except `NetBoxEndpoint`**, which is never covered by an omitted `kinds` list. See
[why `NetBoxEndpoint` is the exception](#why-netboxendpoint-is-the-exception).

## Full example

Every field set explicitly, and both axes narrowed.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue
  namespace: netbox-catalog
spec:
  from:                                        # who may reference into this namespace
    - namespaces: Selector                     # All | Selector
      selector:
        matchLabels:
          netbox.kubeforge.org/tier: gold
    - namespaces: Selector
      selector:
        matchExpressions:
          - key: kubernetes.io/metadata.name   # selecting by namespace *name*
            operator: In
            values: [team-a, team-b]

  to:                                          # what they may reference here
    - kinds: [NetBoxTag, NetBoxRegion]
      names: ["*"]
    - kinds: [NetBoxEndpoint]                  # never covered by an empty kinds list
      names: [shared]
```

A reference is permitted when **some `from` entry** covers the referring namespace **and
some `to` entry** covers the target's Kind and name. Both halves have to match in the *same*
`to` entry: the object above lets `team-a` reference the endpoint called `shared` and every
tag and region, and does **not** let it reference an endpoint called anything else.

## `spec`

| Field | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `from` | `[]object` | **yes**, `MinItems=1` | none | The audiences this grant admits. `MaxItems=16` |
| `from[].namespaces` | `string` enum | **yes** | none | `All` or `Selector` |
| `from[].selector` | `metav1.LabelSelector` | with `Selector` | none | Matched against the **referring Namespace's labels** |
| `to` | `[]object` | no | every Kind but `NetBoxEndpoint`, every name | What the audiences may reference here. `MaxItems=16` |
| `to[].kinds` | `[]string` | no | every Kind **except `NetBoxEndpoint`** | `MaxItems=64`, each `^NetBox[A-Za-z0-9]+$` |
| `to[].names` | `[]string` | no | every name | `MaxItems=128`, each a DNS name or the single entry `*` |

There is no `endpointRef`, no `onConflict` and no `deletionPolicy`. A grant is not a NetBox
object, so it does not embed the shared object envelope and never appears in a payload.

### `spec.from[].namespaces`

| | |
|---|---|
| Type | `string` (`FromNamespaces`) |
| Required | yes |
| Default | none |
| Validation | `+kubebuilder:validation:Enum=All;Selector` |

`All` admits every namespace in the cluster. `Selector` admits the namespaces whose
**labels** match `selector`.

It is an enum with no default on purpose. The permissive case has to be a word somebody
typed and a reviewer can grep for — not a field somebody forgot. There is no `Same` value:
a same-namespace reference needs no grant and never consults one.

**If it is wrong.** An unknown value is rejected at admission by the enum.

### `spec.from[].selector`

| | |
|---|---|
| Type | `metav1.LabelSelector` |
| Required | with `namespaces: Selector` |
| Default | none |
| Validation | two CEL rules; see below |

Matched against the labels of the referring **`Namespace` object**, not against the
referring CR.

A label selector rather than a list of namespace names, because names are what does not
scale: a name list has to be edited by the catalogue owner every time a team is onboarded,
which is exactly the churn [ADR-0002](../decisions/0002-crd-scoping.md) warns about.
Selecting by name is still available and needs no extra field, because the API server labels
every `Namespace` with `kubernetes.io/metadata.name`:

```yaml
# one namespace
selector:
  matchLabels:
    kubernetes.io/metadata.name: team-a

# several
selector:
  matchExpressions:
    - key: kubernetes.io/metadata.name
      operator: In
      values: [team-a, team-b]
```

Two CEL rules apply:

| Rejected | Message |
|---|---|
| `selector` with `namespaces: All` | `selector may only be set with namespaces: Selector` |
| `namespaces: Selector` with no selector, or an empty one | `namespaces: Selector requires a non-empty selector; write namespaces: All to admit every namespace` |

The second one is there because Kubernetes reads an empty selector as *everything*, and in a
default-deny feature the broad case must not be reachable by leaving a field blank. That
meaning already has a spelling, and it is `All`.

**If it is wrong.** A selector the API server accepts but nothing can evaluate — a
`matchExpressions` operator that is not `In`, `NotIn`, `Exists` or `DoesNotExist` — makes
that one entry non-matching, and the denial message names the grant: `netboxrefgrant
netbox-catalog/broken has a selector nothing can evaluate: …`. It fails closed and says so;
it never fails open.

### `spec.to[].kinds`

| | |
|---|---|
| Type | `[]string` |
| Required | no |
| Default | every Kind **except `NetBoxEndpoint`** |
| Validation | `MaxItems=64`, items `MaxLength=63`, items `Pattern=^NetBox[A-Za-z0-9]+$` |

Empty-means-all is the ergonomic default and it is deliberate. A catalogue namespace holds
`NetBoxManufacturer`, `NetBoxDeviceType`, `NetBoxTag` and a few dozen more, and a grant that
had to enumerate them would go stale on every kind this operator adds.

A Kind this build has never heard of is **inert, not an error**, so a grant may be written
before the kind it names exists — the same reason a typed ref alias may point at a Kind with
no CRD yet.

**If it is wrong.** A name that is not `NetBox`-prefixed is rejected at admission. A Kind
that is spelled right but is not the one being referenced simply does not match, and the
reference is denied.

### `spec.to[].names`

| | |
|---|---|
| Type | `[]string` |
| Required | no |
| Default | every name |
| Validation | `MaxItems=128`, items `MaxLength=253`, items `Pattern=^(\*\|[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.…)*)$` |

Empty, or the single entry `*`, means every name.

`*` is a **whole entry and never a prefix**. `web-*` is rejected at admission. A prefix glob
makes a grant's meaning depend on a naming discipline nobody enforces, and turns a rename
into a silent permission change.

## `status`

**There is none.** `NetBoxRefGrant` has no `status` field and no status subresource.

Nothing about a grant is reconciled into NetBox, so there is no outcome to report; and a
status counting the namespaces a selector currently matches would be a cache of something
the resolver has to recompute per reference anyway, stale the moment a namespace is labelled.

## Conditions

**There are none**, for the same reason. A grant's effect is reported on the **referring
object**, not on the grant:

| Where | What you see |
|---|---|
| The referring CR's `RefsResolved` | `False`, `Reason=RefDenied`, message naming the grant to create |
| The referring CR's `Ready` | `False`, `Reason=WaitingForRef` |

`RefDenied` is not retried on a timer, and does not need to be. Writing the grant is the
fix, and the event that arrives when somebody writes it is what wakes the referring objects
up: the operator watches grants and re-enqueues the objects whose references reach into the
grant's namespace (NBO-013, and see
[ordering and convergence](../concepts/references.md#ordering-and-convergence)).

A denied reference makes **zero** reads in the target namespace and **zero** NetBox requests.
The grant is checked before the target is read, so a denial cannot be told apart from a
missing object — otherwise the condition message would be an existence oracle for a
namespace the referrer has no access to.

## Kind-specific behaviour

### Why `NetBoxEndpoint` is the exception

An empty `to[].kinds` covers every Kind **except `NetBoxEndpoint`**. That is the security
boundary of this whole feature, and it is the one asymmetry in the API.

A catalogue reference hands over a NetBox **id**. A cross-namespace `endpointRef` hands over
use of another namespace's **token Secret**: the referring namespace gets to make
authenticated writes against that NetBox, with that token's NetBox permissions, without ever
being able to read — or appearing in any RBAC review of — the Secret itself. That is a
capability, not a lookup, and it is the only thing in this API that can be escalated by
reference.

So it is excluded from the ergonomic default deliberately. "Readable by everything" is a
sentence a catalogue owner will write without thinking hard, and it must not be the same
sentence as "and anyone may borrow my credentials". Lending an endpoint costs one more line:

```yaml
spec:
  from:
    - namespaces: Selector
      selector:
        matchLabels:
          kubernetes.io/metadata.name: team-a
  to:
    - kinds: [NetBoxEndpoint]
      names: [shared]
```

and that line is the audit trail. `grep -r NetBoxEndpoint` over the grants in a cluster is
the complete list of who is lending credentials to whom.

Note what this does **not** do. `spec.endpointRef` is a plain string today and resolves in
the object's own namespace, so there is no cross-namespace `endpointRef` to refuse yet: the
rule is in place before the field that can trip it, because an exception that lands after
the hole is not an exception. ADR-0002 records cross-namespace `endpointRef` as intended to
work with a grant, and this is the grant it will need.

### A grant is not NetBox authorisation

**A grant protects the Kubernetes reference graph. It protects nothing in NetBox.**

Only the `name` mode is authorised, because it is the only mode with a Kubernetes namespace
on the far side:

| Mode | Gated by a grant | Why |
|---|---|---|
| `name` | **yes**, when it names another namespace | It reads a CR in that namespace |
| `slug` | no | `GET /api/…/?slug=…` against the **referring** namespace's own endpoint and token |
| `lookup` | no | Same |
| `id` | no | Same |

So a namespace that is denied `{name: emea, namespace: netbox-catalog}` can write
`{slug: emea}` instead and get the same NetBox id, as long as its own token may read
`dcim.region`. That is not a hole in the grant — it is the correct boundary. The grant says
who may depend on *your CRs*; NetBox's own object permissions are the only thing that says
who may read *NetBox*. If a NetBox object must not be readable from a namespace, that is a
NetBox permission on that namespace's token, and no Kubernetes object can substitute for it.

The same applies to a reference that resolved yesterday: revoking a grant stops the referrer
from resolving the reference again, and does **not** clear the value already written to
NetBox. An unresolved reference is not a cleared one — clearing is something you ask for by
writing an empty value ([references](../concepts/references.md),
[field ownership](../concepts/field-ownership.md)).

### Same-namespace references are free

A reference that does not leave its own namespace returns before anything is read: no grant
list, no `Namespace` read. That is the common case, and a `LIST` per same-namespace reference
would put a request on the hot path of almost every object in the cluster.

The check keys on the namespace the reference **resolves to**, not on whether the field was
written — so `{name: emea, namespace: team-a}` from an object in `team-a` is still a
same-namespace reference and still free.

### The wildcard form is the cheap one too

The referring `Namespace` object is read for its labels **only when some grant actually uses
`namespaces: Selector`**. A cluster whose grants all say `namespaces: All` never reads a
`Namespace`, and so never starts the cluster-wide informer that reading one through a cache
would.

The operator's `ClusterRole` still carries `get;list;watch` on `namespaces` statically, since
RBAC cannot express "only the ones a selector is pointed at".

### No controller, on purpose

`NetBoxRefGrant` is the one kind with no controller and no `Descriptor`.

Every other kind has three files — a spec type, a `Descriptor` naming its NetBox endpoint,
and a controller of about one line delegating to the engine. A grant is not a NetBox object:
there is no endpoint to write it to, no natural key to adopt it by, and no drift to correct.
Reconciling it would mean waking up, reading it, and doing nothing. It is read by
`internal/resolver` at the moment a reference is resolved, and by nothing else — which is
also where its RBAC marker lives, next to the only code that needs the permission.

Having no controller does not mean having no effect on the queue, though. The object
controllers watch grants: writing one re-enqueues the objects whose references cross into
that namespace, found through an index over the namespaces each object's references reach
into. So a grant takes effect when it is applied rather than on the next resync, without a
grant controller existing to make that happen.

## Printer columns

```
$ kubectl get netboxrefgrants -n netbox-catalog
NAME                        FROM       KINDS             NAMES    AGE
catalogue-readable-by-all   All                                   9d
lend-the-shared-endpoint    Selector   [NetBoxEndpoint]  [shared] 3d
```

| Column | Type | Source |
|---|---|---|
| `NAME` | string | `metadata.name` |
| `FROM` | string | `.spec.from[*].namespaces` |
| `KINDS` | string | `.spec.to[*].kinds` |
| `NAMES` | string | `.spec.to[*].names` |
| `AGE` | date | `.metadata.creationTimestamp` |

An empty `KINDS` is the wide grant: every Kind but `NetBoxEndpoint`. `FROM=All` with an
empty `KINDS` is the one to look at twice — it is also the one most clusters should have
exactly one of, per catalogue namespace.

## Troubleshooting

| Symptom | Condition you would see | Cause | Fix |
|---|---|---|---|
| A reference will not resolve, message says `is not permitted to reference` | `RefsResolved=False, Reason=RefDenied` on the **referring** object | no grant in the target namespace admits the referring one | Create the grant the message names, in the namespace the message names |
| Denied, and a grant exists that looks right | same | the grant is in the **referring** namespace | Move it to the namespace being referenced. A grant is a capability, not a claim |
| Denied, and the grant says `namespaces: All` | same | the target is a `NetBoxEndpoint`; an empty `kinds` never covers it | Add `to: [{kinds: [NetBoxEndpoint], names: [<name>]}]`. The message says this outright |
| Denied, message ends `has a selector nothing can evaluate` | same | a `matchExpressions` operator that is not `In`, `NotIn`, `Exists` or `DoesNotExist` | Fix that grant. It is failing closed, so nothing it should permit is being permitted |
| Denied, and the selector looks right | same | it is matched against the **`Namespace`'s** labels, not the CR's | `kubectl get ns team-a --show-labels`, and label the namespace |
| `kubectl apply` rejected, `selector may only be set with namespaces: Selector` | none; admission refused it | a `selector` next to `namespaces: All` | Drop one of the two |
| `kubectl apply` rejected, `requires a non-empty selector` | none; admission refused it | `namespaces: Selector` with `selector: {}` | Write `namespaces: All` if that is what you meant |
| `kubectl apply` rejected, `should match '^NetBox[A-Za-z0-9]+$'` | none; admission refused it | a `kinds` entry that is a NetBox model name (`dcim.region`) rather than a CRD Kind | Use the Kind: `NetBoxRegion` |
| `kubectl apply` rejected on `names` | none; admission refused it | a prefix glob such as `web-*` | Name them, or use `*` alone |
| The grant is written and the object is still denied a second later | same | the grant does not cover this reference: check `from`, `to.kinds` and `to.names` against the message | Widen the grant, or read the rows above |
| A denied reference still reaches NetBox by `slug` | `Ready=True` on the referrer | working as designed — see [a grant is not NetBox authorisation](#a-grant-is-not-netbox-authorisation) | Use NetBox object permissions on that namespace's token |
| Every cross-namespace reference in the cluster is `Reason=Invalid`, message `no grant reader is configured` | `RefsResolved=False, Reason=Invalid` | a bug in this operator's wiring, not in any manifest | File an issue. It fails closed, so nothing was permitted that should not have been |

## Related

- [References](../concepts/references.md) — the four modes, and where the grant check sits in
  resolution.
- [ADR-0002](../decisions/0002-crd-scoping.md) — why everything is namespaced, why that makes
  catalogue references cross-namespace, and why this kind needs a wildcard form to survive
  more than three teams.
- [ADR-0003](../decisions/0003-ownership-and-references.md) — ownership versus reference, and
  why a cross-namespace *owner* reference is illegal where a cross-namespace *reference* is
  merely gated.
- [`NetBoxEndpoint`](netboxendpoint.md) — the kind an empty `kinds` list never covers, and the
  token Secret behind it.
- [Errors and retries](../concepts/errors-and-retries.md) — why `RefDenied` has no timer.
