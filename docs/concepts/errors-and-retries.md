# Errors and retries

Every NetBox failure is classified into one Go type, so the reconcile engine can decide
what to do without parsing error strings. NetBox's wording changes between releases;
the type does not.

| HTTP | Type | What the engine does |
|---|---|---|
| 400 | `ValidationError` | Long backoff. Retrying an unchanged payload cannot succeed — the spec has to change first. Field-level detail is preserved in `.Fields`. |
| 401, 403 | `AuthError` | Fails the **`NetBoxEndpoint`**, not the object. One bad token otherwise scatters identical failures across every CR in the cluster. |
| 404 | `NotFoundError` | On a GET by id: the object was deleted server-side, so `status.id` is cleared and the object is re-created. |
| 409, or a body naming a protected foreign key | `ProtectedError` | The delete is blocked until something else is deleted. Backoff, and a `Protected` condition naming the blocker. |
| 429 | `RateLimitError` | Requeue after the server's `Retry-After`, not after a guess. |
| 5xx, transport failure | `TransientError` | Exponential backoff with jitter. |
| >1 match on a lookup that must identify one object | `AmbiguousError` | Never a silent choice — see below. |
| a list that hit the page cap | `TruncatedError` | `Ready=False, Reason=Truncated`, a **10-minute** requeue and no write of any kind — see [Runaway lists](#runaway-lists). |

Match with `errors.As`, never by comparing messages:

```go
var validation *netbox.ValidationError
if errors.As(err, &validation) {
    // validation.Fields["slug"] == []string{"This field must be unique."}
}
```

## Why ambiguity is an error

Several NetBox models have no database uniqueness to lean on. `ipam.Prefix` and
`ipam.IPAddress` have **no** `meta.constraints` at all, and `ipam.VRF.name` is not
unique. So a natural-key lookup can legitimately return more than one row.

Taking the first match silently adopts an unrelated object. For a VRF that is not a
cosmetic problem: every prefix and address keyed on that VRF gets reparented. The
populator this operator replaces does exactly that. Here it is an `AmbiguousError` and a
`Conflict` condition naming what matched.

### What the error tells you

Refusing is only half an answer, because the next question is always "which ones?". So
`AmbiguousError` carries the matched set rather than a count — a count leaves you to
reproduce the query by hand:

| Field | What it holds |
|---|---|
| `Endpoint`, `Params` | the lookup that was ambiguous, so it can be re-run as written |
| `Matched` | how many objects matched, including any whose body carried no `id` |
| `IDs` | the NetBox primary key of every match that carried one, in NetBox's order |
| `Display` | NetBox's own `display` for each of `IDs`, at the same index — `10.0.0.0/24` is what a human recognises, `11` is what they then have to go and look up |

`Error()` renders all of it, and that string is the `Conflict` condition's message verbatim:

```
ambiguous lookup on ipam/prefixes: map[prefix:10.0.0.0/24] matched 2 netbox objects,
id 11 (10.0.0.0/24), id 12 (10.0.0.0/24 (VRF prod)); refusing to guess which one was meant
```

So `kubectl describe` is enough to open both objects in NetBox and decide which one the
manifest meant — the fix is usually a narrower natural key, a `vrf` pinned rather than
omitted, or deleting the duplicate.

Both callers that need the matched set read it off this error. `Client.GetOne` is built on
`Client.List` and is what the engine's natural-key lookup and the resolver's `slug`/`lookup`
modes call; neither counts results for itself, because a second place deciding when a lookup
is ambiguous is a second place that can disagree with this one. A reference reports the same
matches under `RefAmbiguous` on `RefsResolved` — see
[References](references.md#what-happens-when-it-does-not-resolve).

## What is retried, and where

The client retries **only** `TransientError` and `RateLimitError`, with full jitter
(uniform in `[0, backoff]`) so that several controllers hitting the same failing endpoint
do not retry in lockstep.

It never retries a 400 or a 409. Those fail identically every time, and retrying them
inside the client would hide the failure from the engine — the component that knows
whether to back off, fail the object, or fail the endpoint.

`MaxRetries` is a pointer, and `0` means zero:

```go
netbox.Config{MaxRetries: netbox.Retries(0)}   // fail fast; the engine's requeue is the retry
netbox.Config{}                                // nil -> DefaultMaxRetries
```

That distinction exists because with a plain `int`, a caller asking to fail fast would
silently get the default instead.

## Cancellation is not a NetBox failure

A cancelled or timed-out context surfaces as `context.Canceled` /
`context.DeadlineExceeded`, not as `TransientError`. Reporting it as transient would make
the engine requeue a reconcile that was deliberately abandoned.

## Runaway lists

`List` follows pagination up to `MaxPages` (default 1000). Hitting that cap returns a
**`TruncatedError`** and **no results at all** — not a short slice.

The cap is not negotiable: a NetBox that always reports a `next` page must not be able to
exhaust the manager's memory. Returning partial data was. A caller cannot tell a truncated
result from a complete one, so the engine's natural-key lookup found no match in the pages
it received and took the **create** path, creating an object that already existed. A safety
limit that silently duplicates data is worse than no limit, so the limit stayed and the
silence went.

`TruncatedError` is deliberately **not** retryable — the same request truncates in the same
place, so a retry burns API budget and never converges. It means either the filter did not
apply (a natural-key lookup expects a handful of results, so paginating past the cap says
the query was wrong) or the endpoint genuinely holds more objects than `MaxPages` allows.
Either way a human raises `MaxPages` or narrows the filter, which is what `error` means.

The same reasoning applies with more force to anything acting on *absence*: a prune or a
sweep deleting what it did not see in a truncated list would delete real data.

### What the engine reports

`Ready=False, Reason=Truncated` — its own reason rather than the generic `APIError` a
failure the table does not cover would get. From the outside "the lookup paginated past the
cap" and "NetBox is unreachable" look alike and are not: one is fixed by narrowing the filter
or raising `MaxPages`, the other by waiting, and a reader sent to the wrong one of those
loses the afternoon.

The condition message carries the endpoint, the cap, how many objects had been collected when
it was hit, and both fixes:

```
list of dcim/sites truncated at the 1000-page cap after 50000 objects; results would be
incomplete; nothing was written; either the filter did not apply and the lookup has to be
narrowed, or dcim/sites holds more objects than the 1000-page cap allows and MaxPages has to
be raised
```

The requeue is **10 minutes**, the same tier as an unsupported NetBox version and for the
same reason: the request is not retryable at all, so the only thing that clears it is a
person. It is deliberately not the endpoint's `resyncInterval`, which a cluster is free to
set to seconds — that would poll a query that cannot succeed.

## Secrets

Request and response bodies are logged at `debug` only, through a tested redaction pass —
not a convention. `auth_psk`, `psk`, `preshared_key`, `password`, `token`, `secret`,
`private_key` and `api_key` are masked wherever they appear, including nested inside a
`results` array, since masking only the top level would put every PSK on a list page into
the log. `custom_fields` are collapsed to their key names, because the names help debugging
and the values are arbitrary user data.

See [Observability](../operations/observability.md) for the log levels, the stable key set
and the metrics these errors move.
