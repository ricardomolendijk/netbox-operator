# Deletion

Deleting a CR deletes its NetBox object, in an order that resolves itself. There is no
deletion-ordering table anywhere in this codebase, and there is not going to be one: NetBox
declares almost every foreign key `on_delete=PROTECT`, so it refuses a delete whose
dependents are still present. A refusal plus a backed-off retry **is** the topological sort,
and the server's opinion about what still references what is more reliable than a list a
human would have to keep in step with 159 models.

Implemented in `internal/reconciler/finalizer.go`.

## The two policies

`spec.deletionPolicy` is `Delete` or `Retain`, and defaults to `Delete`.

| Value | What happens when the CR is deleted |
|---|---|
| `Delete` (default) | `DELETE /api/<endpoint>/<id>/`, then the finalizer comes off. |
| `Retain` | The finalizer comes off, a `Retained` Event is recorded, and NetBox is not called at all. |

`Delete` is the default because a CR that creates an object and then walks away from it is a
leak nobody asked for. `Retain` is for the two cases where the NetBox object outliving the CR
is the *point*: migrating off the operator, and an object that is shared with something else.

```yaml
spec:
  endpointRef: homelab
  deletionPolicy: Retain
```

The field is read fresh on every pass, not latched when deletion starts. That is deliberate,
and it is documented under [getting out of a blocked delete](#getting-out-of-a-blocked-delete).

It is not called `reclaimPolicy`. That is PersistentVolume vocabulary, where it decides what
happens to *storage* after a claim is released and carries a `Recycle` value with no analogue
here. `deletionPolicy` is the spelling in
[ADR-0003](../decisions/0003-ownership-and-references.md#deletion-policy).

### `deletionPolicy` is not sent to NetBox

It is a field of `NetBoxObjectSpec`, the envelope every kind embeds, and the engine excludes
every envelope field from every payload by reflecting over that struct rather than by keeping
a list (`internal/reconciler/payload.go`, `envelopeFields`). This matters more than it looks:
NetBox *ignores* a column it does not know rather than rejecting it, so a leaked envelope
field would not fail — it would just quietly travel over the wire forever.

## The finalizer, and why the order is that way round

The finalizer is `netbox.populator.io/finalizer`.

**It is added, and persisted by a real API write, before the engine writes anything to
NetBox** — before the endpoint is even resolved. **It is removed only after the NetBox side
is settled.**

Both halves are load-bearing, and each prevents a different failure:

| Step | If it happened the other way round |
|---|---|
| The finalizer is persisted before the first `POST` | The process dies between creating a NetBox object and recording that something has to clean it up. The CR has no finalizer, so deleting it runs no deletion sequence, and the NetBox object is orphaned with nothing left that knows it exists. |
| The finalizer is removed only after the `DELETE` succeeds | The CR disappears the moment deletion starts. If the `DELETE` then fails — NetBox down, the object protected — nothing is left to retry it, and the object is orphaned. |

"Persisted" is the word that does the work in the first row. A finalizer added to the
in-memory object and written out at the end of the pass is the add-after-create window
wearing a disguise: the `POST` still goes out first. So the write is synchronous, and a pass
that cannot persist the finalizer returns an error and issues no NetBox call at all
(`TestEngineClaimFailureWritesNothing`).

Claiming responsibility that early has a cost: a CR can carry the finalizer while nothing
exists in NetBox yet. That is what step 3 below pays for.

## The deletion sequence

Everything that needs no NetBox call is decided first. That ordering is not tidiness — it is
what makes a `Retain` migration and a never-created object both complete **while NetBox is
unreachable**. An escape hatch that only works when it is not needed is not an escape hatch.

| # | Condition | Action |
|---|---|---|
| 1 | The `netbox.populator.io/skip-finalizer=true` annotation | Drop the finalizer, `Warning`/`FinalizerSkipped`. No NetBox call. |
| 2 | `spec.deletionPolicy: Retain` | Drop the finalizer, `Normal`/`Retained`. No NetBox call. |
| 3 | `status.id == 0` | Drop the finalizer, `Normal`/`NothingToDelete`. No NetBox call. |
| 4 | The endpoint is not `Ready` | `Deleting=False, Reason=WaitingForEndpoint`. Keep the finalizer, requeue in 30s. |
| 5 | `DELETE` returns success | Drop the finalizer, `Normal`/`Deleted`. |
| 6 | `DELETE` returns 404 | Drop the finalizer, `Normal`/`Deleted`. Already gone is the end state that was asked for. |
| 7 | `DELETE` returns 409, or a body naming a protected relation | `Deleting=False, Reason=Protected`, NetBox's message verbatim. Keep the finalizer, requeue with capped backoff. |
| 8 | Anything else | `Deleting=False`, reason and interval from the [error table](errors-and-retries.md). Keep the finalizer. |

A CR that carries no finalizer of ours is left alone entirely: something else is holding it
open, and requeueing against that would be a busy loop.

The `Deleting` condition is only ever `False`. The finalizer comes off the instant the NetBox
side settles, so a `True` would have to sit on a CR that no longer exists to carry it. The
`Reason` is therefore always *what is holding the deletion up*.

Every step that drops the finalizer — 1, 2, 3, 5 and 6 — writes no status: the object is
about to stop existing, so a status update either races the delete or is never read. The
Event is the record that outlives the CR, which is why every one of those steps has one. The
steps that keep the finalizer — 4, 7 and 8 — write a condition instead, because there is
still an object to describe.

### Step 3 — why an unset `status.id` deletes nothing

`status.id` is the operator's claim on a NetBox object. Without one there is nothing it can
prove it owns, and it will **not** go looking. A natural-key lookup at deletion time would
find whatever happens to match right now, which is exactly how an operator deletes somebody
else's data.

An object adopted under `spec.onConflict: Adopt` *is* owned — adoption records the id — and
is deleted normally. An object this CR never wrote is not.

The honest part: two different things produce an unset id, and the operator cannot tell them
apart.

- Nothing was ever created. The overwhelmingly common case: the endpoint was never `Ready`,
  or the spec never resolved.
- A create succeeded and the status write recording its id did not.

They are indistinguishable because `status.id` and `status.lastAppliedHash` are written in
the same update — the one that failed. So the `NothingToDelete` Event says both, and names
the natural key from `status.naturalKey` when there is one, because that is the only lead
anyone has. This is the single place where the operator can leave behind an object it will
never find again, and it says so rather than picking the flattering reading.

### Step 4 — the endpoint-unavailable decision

**Decision: block the deletion, keep the finalizer, and say so in the condition.**

Both options are bad. Blocking means a CR that will not go away while NetBox is down, and a
namespace that will not terminate. Dropping the finalizer means a NetBox object that nothing
will ever clean up — there is no orphan sweeper yet (NBO-046), so "nothing" is literal.

Blocking wins on one argument: **it is the reversible one.** The object is real, its id is
known, and it will still be deletable when the endpoint comes back — a delete deferred for
five minutes costs nothing that is not recovered automatically. An orphan is permanent, it is
invisible, and it will eventually block somebody else's delete with a `PROTECT` error whose
cause is a CR that was removed weeks ago.

It also keeps the choice with the human who has the context. The condition names the escape
hatch in its own message:

```
Deleting   False   WaitingForEndpoint
  cannot delete netbox extras/tags/9: netboxendpoint "homelab" in namespace "team-a" has
  no ready client; the finalizer stays on rather than leaving the object behind. Set the
  annotation netbox.populator.io/skip-finalizer=true to drop it and accept the orphan
```

Note where this sits in the table: **after** `Retain` and after `status.id == 0`. Neither of
those needs NetBox, so neither is affected by this decision. What blocks is only the case
where there is a real object, with a known id, that the CR asked to have deleted.

## What `PROTECT` looks like

NetBox refuses the delete and names what is in the way. That message is carried through
verbatim, because "cannot delete" without a reason is the worst possible operator experience.

```console
$ kubectl describe netboxprefix lab-net
...
Status:
  Conditions:
    Type:     Deleting
    Status:   False
    Reason:   Protected
    Message:  netbox refused the delete, object is referenced (409): {"detail":"Unable to
              delete object. Cannot delete some instances of model 'Prefix' because they
              are referenced through protected foreign keys: 'IPAddress.vrf'."};
              attempt 3, retrying in 40s
Events:
  Type     Reason         Message
  Warning  DeleteBlocked  netbox has refused to delete ipam/prefixes/12 3 times: ...
```

Detection covers both shapes NetBox uses: a 409, and a non-409 whose body names the
protection. Django raises `ProtectedError` and DRF flattens it into a `detail` string, so
that second case is a wording match — confined to one function and deliberately broad
(`internal/netbox/errors.go`, `isProtected`).

### Nothing is ever forced

No cascade parameter, no dependent-hunting, no ordering table. The dependent's own deletion
unblocks this one and the next attempt finds it unblocked. Forcing would delete data the user
never asked to delete, and it would do so on the strength of the operator's guess about what
the `PROTECT` was for.

### The backoff, and why it is capped

Each refusal doubles the wait, from 10s, capped at 5 minutes:

| Refusals | Next attempt |
|---|---|
| 1 | 10s |
| 2 | 20s |
| 3 | 40s |
| 4 | 80s |
| 5 | 160s |
| 6+ | 300s (capped) |

Starting short is what makes `kubectl delete -f` on a whole dependency chain converge in
seconds rather than minutes: the dependent usually goes away almost immediately, and the
first or second retry finds the parent free. Capping is what stops a *permanently* blocked
delete — a dependent nobody is going to remove — from either spinning at the base interval or
backing off past a horizon where nobody would notice it recovering.

The count lives in `status.deletionAttempts` rather than in memory. A controller has no
memory between passes, so a backoff that survives a requeue, a leader election and a restart
needs the count on the object. It is non-zero only while a CR is terminating.

After three refusals the block is reported as a `Warning`/`DeleteBlocked` Event — **once**.
Every attempt would be noise at cluster scale; never at all would make a permanently stuck
deletion silent, which is worse.

### Getting out of a blocked delete

In order of preference:

1. **Delete the blocker.** This is the intended path, and usually it is already happening —
   the message names the model and the field. Nothing else is required: the next retry
   succeeds on its own.
2. **Switch to `Retain`.** `spec.deletionPolicy` is read fresh on every pass and not latched
   when deletion begins, so setting it to `Retain` on a terminating object takes effect on the
   next pass: the finalizer comes off and the NetBox object stays. It is the gentle way out —
   you keep the object rather than force anything, and you keep the record of having decided
   to.
3. **Break glass.** The annotation `netbox.populator.io/skip-finalizer=true` drops the
   finalizer without calling NetBox at all, and overrides every other step in the sequence.

```sh
kubectl annotate netboxprefix lab-net netbox.populator.io/skip-finalizer=true
```

The annotation exists because a finalizer that is added and never removed makes a namespace
undeletable forever, and no operator should be able to do that to a cluster. It is
break-glass rather than a feature because it *guarantees* an object left behind in NetBox.
That is sometimes the right trade; it is never the default, and it records a
`Warning`/`FinalizerSkipped` Event naming what was left.

## Two things worth knowing

**A `Retain`ed object does not re-adopt itself.** Recreate the CR and it starts with
`status.id` unset, finds the NetBox object by natural key — and then refuses it, because
`spec.onConflict` defaults to `Fail`. That is the correct default (adoption immediately
reconciles an object towards a spec, and there is no undo), but it means `deletionPolicy:
Retain` and `onConflict: Adopt` belong together on any object you intend to hand back and
forth. Without the pair you get a `Conflict` condition naming the object it will not touch.

**A `DryRun` endpoint deletes nothing and the CR still goes away.** `Client.Delete` suppresses
the request and returns success, so the finalizer comes off and the NetBox object stays. That
is right — a dry run must not write — but it is reported as a `Warning` rather than passed off
as a deletion: *"dry run: netbox extras/tags/9 was not deleted and is left in place"*.

## Related

- [ADR-0003 — Ownership and references](../decisions/0003-ownership-and-references.md#deletion-policy)
  — the deletion policy and the `PROTECT` behaviour as decided
- [Object lifecycle](object-lifecycle.md) — the create/adopt/update half of the same engine
- [Errors and retries](errors-and-retries.md) — which NetBox failure becomes which typed error
