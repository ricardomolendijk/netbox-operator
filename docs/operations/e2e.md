# The e2e suite

`make test-e2e` runs the operator against a **kind cluster and a real NetBox**. It is the
only suite in the repository that does, and it is where the claim in
[references](../concepts/references.md#ordering-and-convergence) — apply order does not
matter — is actually proved rather than asserted.

It is not part of `make test`, it does not run on every pull request, and it needs Docker.

```console
$ make test-e2e
```

That one command does all of it: installs the pinned `kind` and `helm` into `./bin`, creates
the cluster, brings up NetBox with its Postgres and Redis, builds this checkout into an image
and side-loads it, installs the chart, and tears everything down again at the end.

## What it needs

| | Why |
|---|---|
| A Docker daemon | kind's node, and the NetBox stack |
| The Docker Compose v2 plugin | `docker compose`, not the standalone `docker-compose` |
| ~4 GB of free memory | NetBox, Postgres, Redis and a single-node Kubernetes |
| Outbound access to Docker Hub and `get.helm.sh` | The NetBox, Postgres and Valkey images, and the pinned Helm |

Nothing has to be installed by hand. `kind` comes from `make kind` (a pinned `go install`) and
`helm` from `make helm-bin` (the pinned release tarball), both into `./bin`, and the suite
prefers `./bin` over `PATH` for the same reason every other tool here does: your global
toolchain must not be able to change the result.

**When a prerequisite is missing the suite skips and says which.** `make test-e2e` prints the
reason and succeeds — a suite that reported a pass because it could not run would be worse
than one that did not run.

```console
$ make test-e2e
e2e needs a Docker daemon for kind and for NetBox, and none is reachable.
See docs/operations/e2e.md. Skipping.
```

## What it runs

One `Describe`, `Ordered` and `ContinueOnFailure` — a gate that stops at the first red spec
answers one question where a maintainer needs all of them. Seven runs, all over the same
fixture graph in
[`test/e2e/fixtures/graph/`](https://github.com/ricardomolendijk/netbox-operator/tree/main/test/e2e/fixtures/graph)
— one object per file, file-name order being dependency order.

| Run | What it proves |
|---|---|
| **forward** | Dependency order converges. This run's canonical NetBox dump is the baseline every other run is compared against. |
| **reverse** | Exactly the reverse order converges to a byte-identical dump. Kept separate from the random runs because it is the worst case for a naive implementation and the one a human can reason about. |
| **random** ×N | N seeded permutations, each object applied on its own with 0–500 ms of jitter, all converging to the same dump. |
| **grant-last** | Every referrer applied first sits at `RefsResolved=False, Reason=RefDenied` and has written nothing; the `NetBoxRefGrant` arriving afterwards moves them all to `Ready` — with `resyncPeriod: 1h` set, so a resync cannot be what did it. |
| **restart** | The manager Pod is deleted partway through a random-order apply, and the run still converges to the same dump. Reconciliation is level-triggered, so no in-memory state may be load-bearing. |
| **cycle** | Two regions whose `parentRef`s point at each other both report `Reason=RefCycle`, write nothing to NetBox, and stay under ten reconciles between them in sixty seconds. Fixing one spec converges both. |
| **teardown** | Deleting the whole graph in random order leaves NetBox empty, with no CR held by a finalizer and NetBox's own `PROTECT` 409s resolving themselves as dependents go away — [one retry per refusal](#the-teardown-run-is-also-a-rate-assertion), not one per wake-up. |

### The teardown run is also a rate assertion

The sentence above was true of the design and false of the code until
[#289](https://github.com/ricardomolendijk/netbox-operator/issues/289). A refusal *did*
resolve itself as the dependents went away — but the retry was not on the backoff the engine
had chosen. The status write recording each refusal is an event on the object, so the
controller woke immediately and tried again, at roughly a refused `DELETE` and a status write
every three milliseconds per blocked object. Five CRs the rest of the graph references doing
that at once saturated NetBox and the API server, so the deletes that would have unblocked
them never got through inside the two-minute budget and the run failed with those five still
holding their finalizers — with nothing in the log that looked like a delete being refused
in an orderly way, because there was nothing orderly about it.

What makes the claim true is `status.lastDeletionAttempt` beside `status.deletionAttempts`
(see [Deletion → the backoff](../concepts/deletion.md#the-backoff-and-why-it-is-capped)): the
schedule is read off the clock, so a wake-up between two attempts costs a cached read and
nothing else. The regression test for it is
`TestARefusedDeleteBacksOffInsteadOfStorming` in `internal/controller`, where the assertion is
a *rate* — a real API server is the only place the self-triggering exists, and a
single-`Reconcile` unit test cannot see it.

Each of forward, reverse and every random run also asserts the two things an end-state check
would miss:

- **Quiescence.** Two full `resyncPeriod`s after convergence produce **zero** mutating NetBox
  requests. A resolver that converges by re-`PATCH`ing forever passes every other assertion
  in the suite and fails this one.
- **Write economy.** The whole run costs at most `objects + deferred fields` mutating
  requests. Convergence that costs forty `PATCH`es for seventeen objects is churn, not
  convergence.

A final spec then asserts, over **every** run at once, that
`netbox_operator_reconcile_total{result="error"}` never moved and the manager never logged an
`error`-level line. Every waiting state in this graph is a legitimate intermediate state, so
any of them arriving through an error path is a bug.

Once, at the end, rather than inside each run. It was written that way while
[#252](https://github.com/ricardomolendijk/netbox-operator/issues/252) made it red: the second
reconcile of almost every object read a stale `status.id` from the informer cache, briefly
accused the operator of a foreign NetBox object, and lost its own status write to a 409.
Asserting that inside each pass would have stopped the suite at the forward run and left the
twenty permutations, the dump equality, the quiescence and the write economy unexecuted — which
is most of the gate. One spec at the end names a defect once and lets the rest run, which is
worth keeping now that #252 is fixed and the spec passes.

!!! note "The stale-read family, and what is left of it"

    **#252** is fixed and this spec passes. Its fix made a pass that *reads* a stale status
    recover; a pass whose own status write was refused had nothing to recover from, so a create
    whose id was lost that way left the CR `Ready=False/Conflict` on the object the operator
    itself had made, for ever. That was
    [#289](https://github.com/ricardomolendijk/netbox-operator/issues/289) root cause B and
    [#291](https://github.com/ricardomolendijk/netbox-operator/issues/291), and it is fixed by
    persisting `status.id` through a writer that carries no `resourceVersion`. Note that no
    endpoint in this graph sets `spec.managedBy`
    (`test/e2e/fixtures/graph/README.md`), so nothing here is stamped and the provenance route
    back is deliberately not the one under test.

    [#249](https://github.com/ricardomolendijk/netbox-operator/issues/249) — a default
    `helm install` CrashLooping because the manager served the admission webhook with no
    certificate — is fixed, and the harness no longer passes
    `--set-json 'extraArgs=["--enable-webhooks=false"]'` to work around it. This kind cluster
    has no cert-manager, so the chart skips the webhook and sets that flag itself: the suite
    runs the same degraded path a default install without cert-manager gets, and the rules the
    webhook would have enforced are asserted at reconcile time, which is where their authority
    lives ([admission-webhooks.md](admission-webhooks.md#what-breaks-when-it-is-off)).

## Reproducing a failure

The seed is printed at the top of the run and attached to the report:

```
PRNG seed 15563871266320121237 (randomly chosen). Reproduce this run with NBO_E2E_SEED=15563871266320121237
```

Set it and every order and every jitter is the same again:

```console
$ NBO_E2E_SEED=15563871266320121237 make test-e2e
```

Each run draws from its own PRNG stream, so adding a run later does not change the orders the
other runs get from the same seed.

## Reading the dump diff

The equality assertion compares a **canonicalised** dump of everything in NetBox: one line per
object, sorted, with `id`, `url`, `display`, `display_url`, `created`, `last_updated`, every
`_`-prefixed cached column and every `*_id` column stripped — each of those legitimately
differs between two runs that produced the same state. An embedded foreign key is reduced to
its `slug`, `name` or `value`, because the embedded object carries the target's `id` and `url`
and nothing else that varies. A to-many is sorted, because NetBox returns a many-to-many in its
own order and that order is not data.

`*_id` is the id half of a [generic FK](../concepts/generic-refs.md) — `scope_id` next to
`scope`, `object_id` next to `object` — rendered as a sibling column rather than inside the
embedded object. Stripping it is safe as a blanket rule because NetBox renders an ordinary
foreign key as a nested object (`vrf`, `tenant`) and never as `vrf_id`, and the `*_type` half
survives, which is what carries the identity together with the reduced object.

A mismatch prints only the differing lines, `-` for the baseline and `+` for this run:

```
random-07 produced a different NetBox state (seed 1556…, NBO_E2E_SEED=1556…):
- ipam/prefixes/ {"description":"","prefix":"10.117.0.0/24","scope":"e2e-hall-1","vrf":"E2E ACME"}
+ ipam/prefixes/ {"description":"","prefix":"10.117.0.0/24","scope":null,"vrf":"E2E ACME"}
```

Read that as: this permutation left `scope` unwritten. A `-` with no matching `+` is an object
that was not created at all.

A convergence **timeout** prints every object's conditions and its `status.deferredPending`
instead, so the diagnosis is in the CI log rather than in a re-run:

```
timed out after 2m0s waiting for every object to reach Ready=True/Synced: 1 not ready: NetBoxPrefix team-a/acme-net
17 objects:
  NetBoxRegion netbox-catalog/nl id=2 Ready=True/Synced RefsResolved=True/AllResolved
  NetBoxPrefix team-a/acme-net id=0 Ready=False/WaitingForRef RefsResolved=False/RefNotReady (spec.vlanRef: …)
  …
```

## Environment variables

All optional. The defaults are what CI uses.

| | Default | |
|---|---|---|
| `NBO_E2E_SEED` | random | Reproduce a run exactly. |
| `NBO_E2E_PERMUTATIONS` | `20` | How many random permutations. Lower it while iterating. |
| `NBO_E2E_CLUSTER` | `nbo-e2e` | The kind cluster's name. |
| `NBO_E2E_NETBOX_TAG` | `v4.6.8` | The NetBox image tag. Kept equal to the release [`netbox-schema.md`](../netbox-schema.md) was extracted from; `NetBoxEndpoint` gates on `>=4.2, <5.0`. |
| `NBO_E2E_NETBOX_PORT` | `18080` | Where NetBox is published for the test process. |
| `NBO_E2E_IMAGE` | `netbox-operator:e2e` | The image built from the checkout. |
| `NBO_E2E_SKIP_BUILD` | `false` | Reuse an image already in the local daemon. |
| `NBO_E2E_RETAIN` | `false` | Leave the cluster and NetBox running afterwards. The next run reuses them. |
| `NBO_E2E_READY_TIMEOUT` | `120s` | How long any one object may take to converge. NBO-017 fixed the figure. |

### Iterating

Retain the environment and cut the permutations down:

```console
$ NBO_E2E_RETAIN=true NBO_E2E_PERMUTATIONS=1 make test-e2e
$ NBO_E2E_RETAIN=true NBO_E2E_PERMUTATIONS=1 NBO_E2E_SKIP_BUILD=true make test-e2e   # and again, faster
```

The bring-up is idempotent, so the second run reuses the cluster and the NetBox and takes
seconds to get going. Clean up with:

```console
$ ./bin/kind delete cluster --name nbo-e2e
$ docker compose -p nbo-e2e-netbox -f test/e2e/netbox/docker-compose.yaml down --volumes
```

Order matters: deleting the cluster deletes the Docker network the NetBox containers are
attached to, and Docker will not remove a network that still has endpoints on it.

## Where it runs

| | |
|---|---|
| **Locally** | `make test-e2e`. Skips with a reason when Docker is absent. |
| **CI, every PR** | Nothing of the suite itself. `make vet` compiles `test/e2e`, and `make test` runs `test/e2e/harness`'s own unit tests — the canonical dump and the fixture ordering are pure functions and the load-bearing half of this gate, so they are covered where the tests run on every PR. |
| **CI, nightly** | `.github/workflows/e2e.yaml`, 03:17 UTC, 20 permutations. |
| **CI, on a PR labelled `area/refs`** | The same workflow. A PR without the label skips the job, and a skipped job is green. |
| **On demand** | `workflow_dispatch` on the same workflow, with the permutation count and the seed as inputs. |

## How the pieces fit

Two addresses for one NetBox, which is the part worth knowing before reading the harness:

- the **test process** reaches NetBox on `127.0.0.1:18080`, the published port;
- the **manager**, running inside the cluster, reaches it at the container's address on kind's
  Docker network.

The compose project attaches to that network — `kind`, a single shared network kind puts every
cluster on — rather than creating its own, so the manager's Pods and NetBox share one L3 domain
with no NodePort or port-forward in the path.

The manager's `/metrics` comes back the other way and cannot use the same trick: a container
IP is only routable *from the host* on plain Linux Docker, and times out under Docker Desktop
or a nested daemon. So the harness puts a NodePort in front of the manager's metrics port and
`test/e2e/kind/cluster.yaml` publishes that port to `127.0.0.1:30081`. The two numbers have to
agree; both are constants, one in the cluster config and one in `harness/operator.go`.

`test/e2e/kind/cluster.yaml` also mounts `/dev/null` over `/dev/kmsg` in the node. kubelet
opens that file unconditionally and refuses to start without it — a plain Linux host has one,
a nested or sandboxed runtime often does not, and there the node stays `NotReady` forever with
nothing in `kind create`'s output to say why.

The API token is minted by
[`test/e2e/netbox/mint-token.py`](https://github.com/ricardomolendijk/netbox-operator/blob/main/test/e2e/netbox/mint-token.py)
rather than by the image's `SUPERUSER_API_TOKEN`. The image only ever creates a **v2** token,
and a v2 token is presented as `Bearer nbt_<key>.<secret>` while the operator's client sends
`Authorization: Token <token>` — NetBox 4.6 routes that to its v1 path, so a v2 token would
authenticate nothing the operator sends. The script creates a v1 token instead.

## Building another gate on the harness

`test/e2e/harness` is a package, not a test file, and it is meant to be reused —
NBO-017 is the first gate that needs a live cluster and is not the last.

| | |
|---|---|
| `harness.Preflight` | Every reason the suite cannot run, as sentences. Call it and skip. |
| `harness.New(...).Up` / `.Down` | kind, NetBox and the operator, idempotent, and a `Down` that reports every failure rather than stopping at the first. |
| `Harness.SeedEndpoints` | A namespace, a labelled token Secret and a `NetBoxEndpoint` per fixture namespace, waited to `Ready`. |
| `Harness.SetResyncPeriod` | Rewrites every endpoint's `spec.resyncPeriod`, for a run that needs polling ruled out. |
| `harness.LoadFixtures`, `Reverse`, `Permute`, `SplitGrants` | A fixture directory as an ordered list, and the orders to apply it in. |
| `harness.Apply`, `DeleteAll`, `WaitGone` | One request per object, with jitter and an optional hook between applies. |
| `harness.WaitConverged`, `ReadStates`, `Diagnostics` | Wait on `Ready`/`RefsResolved`, and the per-object dump printed on a timeout. |
| `harness.DumpNetBox`, `Diff`, `NetBoxEmpty` | The canonical NetBox dump, its diff, and the teardown assertion. |
| `Operator.Scrape`, `WaitQuiet`, `Logs`, `Restart` | Counters, the quiescence window, the pod log and the pod kill. |

A new gate is a fixture directory and a `Describe` block. The NetBox-side assertions go
through `internal/netbox`, the operator's own client, rather than raw HTTP — so a test cannot
disagree with the operator about pagination, ambiguity or error classification.

## What this suite does not cover

- **Scale.** Around seventeen objects. A 500-object soak belongs with NBO-026, where enough
  kinds exist to make it representative.
- **The webhook.** The chart renders no `ValidatingWebhookConfiguration`, so the suite installs
  none and the CEL rules on the CRDs are what the API server enforces. Admission is
  [its own suite's](admission-webhooks.md) problem.
- **Upgrade.** Installing the chart, not upgrading from a previous release.
