# Stuck references

An object that is waiting for something it references, and how to find out what.

The concept page — [references](../concepts/references.md) — says what a reference is and
what wakes one up. This page is the operational half: which condition to read, which metric
to look at, and how to find an object's referrers when you have the target and want the
list.

## Start at `RefsResolved`

Every reference outcome is on one condition on the referring object. `Ready` reports
`WaitingForRef` for all of them, because that is what a `kubectl wait` is asking; the
diagnosis is on `RefsResolved`.

```console
$ kubectl get netboxregion ams -o jsonpath='{.status.conditions[?(@.type=="RefsResolved")]}' | jq
{
  "type": "RefsResolved",
  "status": "False",
  "reason": "RefNotReady",
  "message": "parentRef -> netboxregion/netbox-catalog/emea: not ready (the target has no status.id yet)"
}
```

Read it right to left: the **reason** is the class of problem, the **message** names the
field you wrote and the object it pointed at. The
[table of reasons](../concepts/references.md#what-happens-when-it-does-not-resolve) says
what each one means and what retries it.

Two of them are worth restating here, because they are the two that look like the operator
being stuck when it is not:

- **`RefNotReady`** — the target exists and has no NetBox id yet. Nothing is wrong with the
  object you are looking at. Go and read the target's own `Ready` condition; if the message
  above already quotes one, the target is the broken object.
- **`RefDenied`** — the reference crosses a namespace with no
  [`NetBoxRefGrant`](../reference/netboxrefgrant.md) permitting it. The message names the
  grant to create and the namespace to create it in, and writing it takes effect within a
  second — there is no resync to wait out.
- **`RefTypeNotAllowed`** — a
  [polymorphic reference](../reference/genericref.md) names a target its NetBox column will
  not take. This one really is stuck, and deliberately: there is no retry, because no object
  appearing anywhere makes an illegal target legal. The message names what you gave and what
  the column accepts; fix the manifest and it clears on the next event.

When every reference resolved, the same condition says so and names them
(`reason=AllResolved`, `message="resolved parentRef, tenantRef"`), so the message is also
how you tell "no reference resolved" from "one of five did not". There is no
`status.resolvedRefs` field to read instead — the condition message is the whole record.

## Then look at the metrics

| Metric | Reads as |
|---|---|
| `netbox_operator_reconcile_total{result="waiting"}` | Objects that could not proceed and did not fail. A first apply produces a burst; a plateau is a graph that has stopped converging. |
| `netbox_operator_ref_enqueue_total{targetKind,referrerKind}` | Referrers woken by an event on something they reference. This is the watch working. |

The pair is the diagnosis. Waiting objects **and** enqueue traffic is a graph converging one
level at a time, which is normal and will end. Waiting objects and **no** enqueue traffic is
a graph that is not converging: either the thing being waited for is not changing at all, or
the reference is one nothing will ever announce (see below).

`targetKind="NetBoxRefGrant"` on the enqueue counter is a grant taking effect. If you write
a grant and see no increment, the grant is not covering the reference — check `from`,
`to.kinds` and `to.names` against the condition message rather than waiting.

## Finding an object's referrers by hand

The operator answers "who points at this object" from a field index inside the manager. That
index is **not** queryable with `kubectl --field-selector`: the API server only exposes the
handful of selectable fields a CRD declares, and a manager-internal index is not one of
them. Asking for `--field-selector spec.refs=...` gets you an error, not an empty list.

Two paths that do work.

**From the referrer, authoritative.** The `RefsResolved` message names the target of every
reference that did not resolve, and names the fields that did when they all have:

```console
$ kubectl get netboxregion -A \
    -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,PARENT:.spec.parentRef.name,REFS:.status.conditions[?(@.type=="RefsResolved")].reason'
```

**From the target, by searching the ref field.** A reference is an ordinary spec field, so a
`jsonpath` filter over the whole cluster finds the referrers of one object:

```console
$ kubectl get netboxregion -A \
    -o jsonpath='{range .items[?(@.spec.parentRef.name=="emea")]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
```

Two caveats on that second one, and they are the same two the index has to handle. An
omitted `spec.parentRef.namespace` means *the referrer's own* namespace, so a match on the
name alone can be a different `emea`; and a referrer may point at the same object by `slug`
or `id` instead, in which case there is no name to match on at all.

## References nothing will wake

Only a `name` reference has anything to wait for. A `slug`, a `lookup` or an `id` resolves
against NetBox, which announces nothing to Kubernetes — so those are retried on a timer and
never woken by an event. An object stuck on `RefNotFound` for a `slug` is waiting for
somebody to create the object in NetBox, and it will notice within a minute of them doing
it.

The other case with no event behind it is a target that exists and never becomes usable: the
referrer sits at `RefNotReady` for as long as that lasts. That is deliberate — the fix is on
the target, and polling would hide a stuck graph rather than reveal it. `RefsResolved` names
the target; go there.

## Related

- [References](../concepts/references.md) — the four modes, the reason vocabulary, and
  [how ordering converges](../concepts/references.md#ordering-and-convergence)
- [`NetBoxRefGrant`](../reference/netboxrefgrant.md) — the cross-namespace half, and its own
  troubleshooting table
- [Observability](observability.md) — every metric, its labels and its cardinality
