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

`List` follows pagination up to `MaxPages` (default 1000), then logs a warning and
returns what it has. A NetBox that always reports a `next` page cannot exhaust the
manager's memory. The truncation is logged rather than silent, because incomplete list
results that look complete are how a prune deletes the wrong things.

## Secrets

Request bodies are logged at `debug` only, through a tested redaction pass — not a
convention. `auth_psk`, `psk`, `preshared_key`, `password`, `token`, `secret`,
`private_key` and `api_key` are masked. `custom_fields` are collapsed to their key names,
because the names help debugging and the values are arbitrary user data.
