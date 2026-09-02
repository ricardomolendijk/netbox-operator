# Deletion

Deleting a CR deletes its NetBox object, in an order that resolves itself. There is no
deletion-ordering table anywhere in this codebase, and there is not going to be one: NetBox
declares almost every foreign key `on_delete=PROTECT`, so it refuses a delete whose
dependents are still present. A refusal plus a backed-off retry **is** the topological sort,
and the server's opinion about what still references what is more reliable than a list a
human would have to keep in step with 159 models.

Implemented in `internal/reconciler/finalizer.go`.

## The two policies

`spec.deletionPolicy` is `Delete` or `Retain`, and it defaults to **`Delete` on every Kind**.

| Value | What happens when the CR is deleted |
|---|---|
| `Delete` | `DELETE /api/<endpoint>/<id>/`, then the finalizer comes off. If NetBox refuses, [the CR stays](#what-protect-looks-like) and says why. |
| `Retain` | The finalizer comes off, a `Retained` Event is recorded, and NetBox is not called at all. |

One rule, no per-Kind table, and `kubectl delete -f .` undoes `kubectl apply -f .`.

### Why this reversed

**Decided** on [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304), which
reverses [#176](https://github.com/ricardomolendijk/netbox-operator/issues/176). The IPAM
kinds — prefixes, addresses, ranges, VLANs, VRFs, RIRs, aggregates, ASNs, roles, services —
used to default to `Retain`, on the argument that they hold *state* rather than
*configuration*: deleting an `ipam.IPAddress` frees it for reallocation, deleting an
`ipam.Prefix` destroys the record of who a range belonged to, and a `kubectl delete namespace`
could do that to a whole range at once.

**The argument was right about the risk and wrong about where to put it.** What a default of
`Retain` actually produces:

1. `kubectl delete netboxvlan max-acc-vlan-1301` deletes the CR, emits a `Retained` Event, and
   leaves the VLAN in NetBox. The CR is *gone* — there is nothing left to `kubectl describe`,
   and the Event ages out of the namespace within the hour.
2. That VLAN is now unmanaged, and it is also the object NetBox cites, with a `PROTECT`, when
   you delete the **site** it belongs to.
3. The site's CR parks at `Deleting=False, Reason=Protected` naming VLANs whose CRs no longer
   exist, so the operator cannot remove them and neither can a re-apply.

A namespace torn down that way leaves a NetBox nothing can clean up through the operator at
all. Retaining by default did not protect state; it converted a recoverable deletion into an
unrecoverable one, silently, and in the direction the user had already said no to by running
`kubectl delete`.

So the risk is answered where it is visible instead. The `DELETE` goes out, and:

- **NetBox refuses what is still referenced.** Almost every IPAM foreign key is
  `on_delete=PROTECT` (`docs/netbox-schema.md`), so a prefix inside a VRF, a VLAN with
  prefixes scoped to it, or a role in use is refused by the server. The CR **stays**, with
  `Deleting=False` naming exactly what is in the way. Nothing is lost and nothing is hidden.
- **What is not referenced is deleted**, which is what deleting the manifest asked for.
- **`deletionPolicy: Retain` is one line** for the objects that should outlive their CR —
  migrating off the operator, an object shared with something else — and it says so in Git,
  where the next reader can see it. A default said nothing to anybody.

The cost, stated rather than sold: **an unreferenced IPAM object is now deleted by
`kubectl delete`, and a freed address can be reallocated immediately.** That is a real
regression for anyone who was relying on the old default as a safety net. It is traded for a
teardown that completes, and for the property that what happens to a NetBox object is
readable in the manifest rather than inferred from a table of which Kind is which.

### `NetBoxIPAddressClaim` already worked this way

Claims defaulted to `Delete` before this
([#225](https://github.com/ricardomolendijk/netbox-operator/issues/225), reversing
[#182](https://github.com/ricardomolendijk/netbox-operator/issues/182)) and the reasoning
generalises: a claim's CR is the only record its allocation exists at all, so a retained
address is not protected, it is unattributable — invisible litter by construction. An inline
`claimFrom` on a VM materialises a claim owned by that VM
([#174](https://github.com/ricardomolendijk/netbox-operator/issues/174)), so uniform `Retain`
leaked one address per VM deletion, and pool exhaustion is a wait-forever state.

`deletionPolicy: Retain` on a claim still keeps the old behaviour exactly: no NetBox call, an
`AddressRetained` Event naming the address, the id and the identity, and
`netbox_operator_allocations_retained_total{kind}` ([claims](claims.md#deleting-a-claim)).

A claim's deletion pass also gives up where the declarative engine does not: after **8
attempts** (~20 minutes) it releases its finalizer and reports the address as retained. Claims
are created by machinery rather than by hand, and a namespace full of wedged ones would have
to be unstuck one CR at a time
([the claim reference](../reference/netboxipaddressclaim.md#it-still-cannot-make-a-namespace-undeletable)).

### `Retain` also blocks a destructive *update*

On one kind, `deletionPolicy` decides more than what happens at deletion time.
[`NetBoxCable`](../reference/netboxcable.md) is the only `UpdateStrategy: Recreate` kind: some
of its fields cannot be PATCHed, so changing one means `DELETE` then `POST`
([the Descriptor](descriptor.md#updatestrategy-and-recreateon)).

A recreate destroys the NetBox object, which is precisely what `Retain` says never to do. The
two instructions contradict each other, so the operator **refuses the write** rather than
picking one:

```
Ready         False   Invalid
  spec.deletionPolicy is Retain and this change can only be applied by deleting and
  re-creating the object: b_terminations
```

Zero writes, and the object is unchanged afterwards. The fix is either
`deletionPolicy: Delete` or reverting the field the message names. The refusal is narrow —
`Retain` blocks only the destructive path, and an ordinary PATCH to the same object still goes
through.

It refuses in this direction because the asymmetry is not close: a silent recreate deletes a
cable and every `dcim.CablePath` through it to satisfy an edit, with no undo, while a refusal is
one edit away from either outcome.

### `Delete` and refused are not the same axis

`NetBoxCustomField` is the case that looks like a contradiction: its default is `Delete`, and
deleting one is *refused* by default with `Deleting=False, Reason=DataLossBlocked`
(step 4 above). Both are right, because they answer different questions.

`deletionPolicy` answers **"when the CR goes, should the NetBox object go too?"** For a custom
field the answer is yes: `Retain` would leave a column in NetBox's schema that nothing manages,
on every object type the field covered, with no CR left to say what it is for. That is the worse
end state, and it is the one a wrong default would produce silently.

The data-loss guard answers **"is this particular delete one a human should confirm?"** For a
custom field the answer is also yes, and for a different reason: NetBox strips the field's
stored value from every object that has one and does not refuse the delete, so the engine's
usual safety net — send the `DELETE`, let `PROTECT` stop it — cannot fire. The guard keeps the
finalizer on, which means the CR and the NetBox object are both still there and the decision is
still reversible. `netbox.kubeforge.org/allow-data-loss: "true"` or `deletionPolicy: Retain`
each finish it, and they finish it differently
([custom fields](../custom-fields.md#deleting-a-custom-field-destroys-data)).

So: the policy says what the end state should be, and the guard says who has to agree to it.
Every other Kind in the catalogue answers the second question with "nobody" because NetBox
answers it for us.

## The finalizer, and why the order is that way round

The finalizer is `netbox.kubeforge.org/finalizer`.

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
| 1 | The `netbox.kubeforge.org/skip-finalizer=true` annotation | Drop the finalizer, `Warning`/`FinalizerSkipped`. No NetBox call. |
| 2 | `spec.deletionPolicy: Retain` | Drop the finalizer, `Normal`/`Retained`. No NetBox call. |
| 3 | `status.id == 0`, and this Kind cannot carry a provenance stamp | Drop the finalizer, `Normal`/`NothingToDelete`. No NetBox call. |
| 3a | `status.id == 0` on a stamped endpoint | Search `?cf_k8s_uid=<this CR's uid>`. A match is deleted from step 7; no match drops the finalizer with `Normal`/`NothingToDelete`. See [step 3](#step-3--why-an-unset-statusid-deletes-nothing). |
| 4 | The Kind destroys data on delete and `netbox.kubeforge.org/allow-data-loss` is not `"true"` | `Deleting=False, Reason=DataLossBlocked`. Keep the finalizer. No NetBox call. |
| 5 | A child CR this object [materialised](inline-children.md) is still in the cluster | `Deleting=False, Reason=PendingDependents` naming it. Keep the finalizer, requeue in 15s. **This is what orders the NetBox deletes**, not `blockOwnerDeletion` — see below. |
| 6 | The endpoint is not `Ready` | `Deleting=False, Reason=WaitingForEndpoint`. Keep the finalizer, requeue in 30s. |
| 7 | `DELETE` returns success | Drop the finalizer, `Normal`/`Deleted`. |
| 8 | `DELETE` returns 404 | Drop the finalizer, `Normal`/`Deleted`. Already gone is the end state that was asked for. |
| 9 | `DELETE` returns 409, or a body naming a protected relation | `Deleting=False, Reason=Protected`, NetBox's message verbatim. Keep the finalizer, requeue with capped backoff. |
| 9a | ...and `netbox.kubeforge.org/cascade-delete: "true"` is set, and CRs reference this one | Delete those CRs, `Warning`/`CascadeDeleted` naming them, `Deleting=False, Reason=Cascading`. Keep the finalizer. [See below](#cascading-a-refused-delete). |
| 10 | Anything else | `Deleting=False`, reason and interval from the [error table](errors-and-retries.md). Keep the finalizer. |

A CR that carries no finalizer of ours is left alone entirely: something else is holding it
open, and requeueing against that would be a busy loop.

That table is the **declarative** engine's. A claim runs the same sequence in the same order,
over `status.netboxID` instead of `status.id` and reporting `AddressRetained` where this one
reports `Retained` — with one difference: the endpoint-unavailable and NetBox-failure steps are
bounded there, and the eighth attempt releases the finalizer rather than keeping it
([the claim reference](../reference/netboxipaddressclaim.md#it-still-cannot-make-a-namespace-undeletable)).

**Step 4 is why a cascade comes out in the right order.** `blockOwnerDeletion: true` is on
every materialised child, but it bites only under *foreground* propagation and `kubectl delete`
defaults to background — so under the default the garbage collector removes the parent and its
children at the same time. Step 4 is what makes the children's NetBox objects go first and the
parent's last, under both policies. Without it the end state would still be correct, because
NetBox refuses to delete a VM that still has interfaces, but it would be reached through a
queue of 409s and a `Protected` condition pointing at the wrong cause.

A child whose own NetBox delete is permanently `PROTECT`-refused therefore leaves the parent
permanently in `PendingDependents`. That is the correct outcome, and infinitely preferable to a
force-delete that orphans a NetBox object nobody is tracking any more.

The `Deleting` condition is only ever `False`. The finalizer comes off the instant the NetBox
side settles, so a `True` would have to sit on a CR that no longer exists to carry it. The
`Reason` is therefore always *what is holding the deletion up*.

Every step that drops the finalizer — 1, 2, 3, 6 and 7 — writes no status: the object is
about to stop existing, so a status update either races the delete or is never read. The
Event is the record that outlives the CR, which is why every one of those steps has one. The
steps that keep the finalizer — 4, 5, 8 and 9 — write a condition instead, because there is
still an object to describe.

### Step 4 — the delete NetBox will not refuse for you

Steps 8 and 9 are the engine's usual safety net: send the `DELETE`, and let NetBox refuse
anything that would break a relation. It works because NetBox declares `on_delete=PROTECT` on
the foreign keys that matter, so the destructive case answers itself.

`extras.CustomField` is the case where it does not. A custom field's values live inside each
object's own `custom_field_data` JSON rather than in rows pointing back at the definition, so
there is nothing to protect — and NetBox does not merely permit the delete, it *performs the
cleanup*: a `pre_delete` signal issues `custom_field_data = custom_field_data - <name>` over
every object of every type the field was assigned to
(`netbox/extras/signals.py`, `handle_cf_deleted`). One `kubectl delete` would silently drop a
column's worth of data across the whole instance, and NetBox would return 204.

So the refusal has to be the operator's. A Kind whose descriptor declares `DataLossOnDelete`
blocks here by default:

```console
Type:     Deleting
Status:   False
Reason:   DataLossBlocked
Message:  deleting netbox extras/custom-fields/42 destroys this field's stored value on every
          object in netbox that has one, and netbox does not refuse it; the finalizer stays
          on. Set the annotation netbox.kubeforge.org/allow-data-loss=true to accept the
          loss, or spec.deletionPolicy: Retain to keep the netbox object
```

The finalizer stays on, which is what makes it a decision rather than an outage: the CR is
still there, the NetBox object is still there, and both ways out are in the message.

| Way out | Effect |
|---|---|
| `netbox.kubeforge.org/allow-data-loss: "true"` on the CR | The `DELETE` goes out. The values are gone. |
| `spec.deletionPolicy: Retain` | Step 2 answers first: the finalizer comes off and NetBox is untouched. |

Only the exact string `"true"` unblocks — the annotation being absent, misspelled or set to
`yes` all block, so a typo is safe in the direction that keeps the data. And note the ordering:
this is step 4, *after* `skip-finalizer` and `Retain`, so a CR that never intended to delete
anything is not asked to make a decision about data it was not going to destroy.

Today the only Kind that declares it is [`NetBoxCustomField`](../custom-fields.md). The flag is
data on the Descriptor rather than a branch in the engine, so the next model with the same
property is one line.

### Step 3 — why an unset `status.id` deletes nothing

`status.id` is the operator's claim on a NetBox object. Without one there is nothing it can
prove it owns, and it will **not** go looking. A natural-key lookup at deletion time would
find whatever happens to match right now, which is exactly how an operator deletes somebody
else's data.

An object adopted under `spec.onConflict: Adopt` *is* owned — adoption records the id — and
is deleted normally. An object this CR never wrote is not.

Two different things produce an unset id:

- Nothing was ever created. The overwhelmingly common case: the endpoint was never `Ready`,
  or the spec never resolved.
- A create succeeded and the status write recording its id did not.

`status.id` and `status.lastAppliedHash` are written in the same update — the one that
failed — so nothing in the CR's own status can tell the two apart.

**The provenance stamp can.** On an endpoint with `spec.managedBy`, and on a Kind whose NetBox
model carries `custom_fields`, every object the operator creates carries `k8s_uid` holding the
creating CR's `metadata.uid` — written in the POST body itself, so there is no window in which
the object exists without it. `metadata.uid` is assigned by the API server, is never reused,
and is written into that field by this operator for one CR only, so `?cf_k8s_uid=<uid>` is not
a natural-key lookup in disguise: **a match was created by this CR and by nothing else.** That
is the same evidence [duplicate handling](../reference/netboxipaddress.md) uses to pick one
address out of several, and the same shape of recovery the allocation engine already performs
against a lost allocation.

So on a stamped endpoint the operator searches, and the two cases separate:

| Search result | What happens |
|---|---|
| One match | `status.id` is recovered from it and the delete goes out normally (step 7 onwards). |
| No match | The finalizer comes off with `NothingToDelete`, and the Event says so **definitively**: nothing was ever created and nothing is left behind. |
| The search fails, or matches several | `Deleting=False`, the finalizer stays on, requeued. "There may be an object of mine out there and I could not check" has one reversible answer, and orphaning is not it. |

The search needs a client, so a CR with no recorded id still releases without one — step 6
does not block it. A deletion that needs no NetBox call must not start needing one.

Without a stamp nothing has changed and nothing can. The `NothingToDelete` Event names both
possibilities and names the natural key from `status.naturalKey` when there is one, because
that is the only lead anyone has. An endpoint with no `spec.managedBy` therefore keeps the one
place where the operator can leave behind an object it will never find again — which is one
more reason to set it.

The same stamp closes the hole from the other side, and earlier: a natural-key match carrying
this CR's own `k8s_uid` is [recognised as the operator's own
object](errors-and-retries.md#a-cached-read-is-not-a-conflict) rather than
reported as somebody else's and advised for adoption. So a lost status write is normally
repaired on the very next pass, and the deletion-time search is the backstop for a CR deleted
before that pass ran.

### Step 5 — the endpoint-unavailable decision

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
  annotation netbox.kubeforge.org/skip-finalizer=true to drop it and accept the orphan
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
   succeeds on its own. When the blockers are CRs in this cluster,
   [cascade](#cascading-a-refused-delete) does it for you.
2. **Switch to `Retain`.** `spec.deletionPolicy` is read fresh on every pass and not latched
   when deletion begins, so setting it to `Retain` on a terminating object takes effect on the
   next pass: the finalizer comes off and the NetBox object stays. It is the gentle way out —
   you keep the object rather than force anything, and you keep the record of having decided
   to.
3. **Break glass.** The annotation `netbox.kubeforge.org/skip-finalizer=true` drops the
   finalizer without calling NetBox at all, and overrides every other step in the sequence.

```sh
kubectl annotate netboxprefix lab-net netbox.kubeforge.org/skip-finalizer=true
```

The annotation exists because a finalizer that is added and never removed makes a namespace
undeletable forever, and no operator should be able to do that to a cluster. It is
break-glass rather than a feature because it *guarantees* an object left behind in NetBox.
That is sometimes the right trade; it is never the default, and it records a
`Warning`/`FinalizerSkipped` Event naming what was left.

## Cascading a refused delete

`PROTECT` plus a backed-off retry sorts an order; it does not supply the deletes. So deleting
a site whose VLANs still exist parks the CR at `Reason=Protected` until somebody works out,
from NetBox's prose, which other CRs to delete — the manual topological sort this design was
supposed to remove.

The annotation supplies them:

```sh
kubectl annotate netboxsite max-acc netbox.kubeforge.org/cascade-delete=true
```

With it set, a refused delete **deletes every CR that references this one** and waits. Each of
those runs its own deletion sequence, removes its own NetBox object, and this delete retries
and finds nothing in the way.

```console
Type:     Deleting
Status:   False
Reason:   Cascading
Message:  netbox refused to delete dcim/sites/1 and netbox.kubeforge.org/cascade-delete=true,
          so the CRs referencing this one go first (deleted NetBoxVLAN netbox/max-acc-vlan-1301,
          NetBoxPrefix netbox/max-acc-10-18-0-0-24); this delete retries once their own
          finalizers have removed their netbox objects. netbox said: ...
```

Four things it deliberately is not:

- **It is not "delete whatever NetBox named."** The blockers NetBox reports are rows. The
  operator will not delete a row it has no CR for, so an object a human made in the NetBox UI
  is never touched however loudly NetBox cites it. What gets deleted are Kubernetes objects,
  and their own finalizers do the NetBox work — every safety property of an ordinary delete,
  including `PROTECT` handling, applies to each one unchanged.
- **It is not narrowed to the blockers.** Every CR referencing this one goes, not only the ones
  NetBox cited. Matching *"MAX_a_DMZ (1301) (19)"* back to a CR means parsing a Django
  translation string, and a parser between a user and their data is not a thing to build. A CR
  pointing at an object that is being deleted has a reference about to dangle either way.
- **It is not a way around `PROTECT`.** If something outside this cluster still references the
  object, the delete is still refused and the CR still says so.
- **It is not recursive.** The annotation is not copied onto what it deletes — that would mean
  writing metadata onto CRs Git owns ([ADR-0005 §1](../decisions/0005-gitops-coexistence.md)),
  and a cascade whose reach is not visible in any manifest. It costs less than it sounds:
  NetBox's graph fans out from one object rather than hanging off itself, so a site's prefixes
  and its VLANs both reference the *site* and are all in the one set. A referrer whose own
  delete is then refused says so and names what is in the way.

The referrers are found through the same reverse index the [reference
watches](references.md) are built on, so a CR the operator would re-enqueue when this object
changes is exactly one it can find here. Only "true" enables it, so a typo deletes nothing.

## Two things worth knowing

**A `Retain`ed object does not re-adopt itself.** Recreate the CR and it starts with
`status.id` unset, finds the NetBox object by natural key — and then refuses it, because
`spec.onConflict` defaults to `Fail`. That is the correct default (adoption immediately
reconciles an object towards a spec, and there is no undo), but it means `deletionPolicy:
Retain` and `onConflict: Adopt` belong together on any object you intend to hand back and
forth. Without the pair you get a `Conflict` condition naming the object it will not touch.

**A `DryRun` endpoint deletes nothing and the CR still goes away.** `Client.Delete` suppresses
the request, so the finalizer comes off and the NetBox object stays. That is right — a dry run
must not write — but it is reported as a `Warning` rather than passed off as a deletion:
*"dry run: netbox extras/tags/9 was not deleted and is left in place"*.

The engine knows which it was because all three mutating methods report suppression
identically: `Create`, `Patch` and `Delete` each return an `Object` that `netbox.Suppressed`
recognises, and a caller checks that one thing rather than the endpoint's mode. A suppressed
create or patch carries the payload that was never sent; a suppressed delete carries the
`endpoint` and `id` it would have removed, which is enough to render *"would delete
ipam/prefixes/11"*. A real delete returns a nil `Object`, because NetBox answers `204` with no
body — so nothing can mistake a suppressed delete for a completed one.

## Related

- [ADR-0003 — Ownership and references](../decisions/0003-ownership-and-references.md#deletion-policy)
  — the deletion policy and the `PROTECT` behaviour as decided
- [Object lifecycle](object-lifecycle.md) — the create/adopt/update half of the same engine
- [Errors and retries](errors-and-retries.md) — which NetBox failure becomes which typed error
- [Custom fields](../custom-fields.md) — the one Kind whose delete the operator refuses, and why
