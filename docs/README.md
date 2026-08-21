# Documentation

Start with [the root README](../README.md) for what this operator is. This page is the
index of everything under `docs/`.

Docs ship in the same pull request as the code they describe — a feature PR that touches
neither `docs/` nor `README.md` is incomplete ([`CONTRIBUTING.md`](../CONTRIBUTING.md),
definition of done). Every kind gets a reference page; every concept gets a concept page.

## Concepts

How the engine behaves, and why.

| Page | Answers |
|---|---|
| [The Descriptor](concepts/descriptor.md) | What per-kind facts the engine needs, why they are data rather than code, and how natural keys establish identity before a `status.id` exists |
| [Deletion](concepts/deletion.md) | What `deletionPolicy: Delete` and `Retain` each do, why the finalizer goes on before the first write and comes off after the last one, what a `PROTECT`-blocked delete looks like and how to get out of it |
| [Drift detection](concepts/drift.md) | Why what NetBox returns is not what you wrote, and the eight comparison rules that stop a reconcile loop from PATCHing forever |
| [Errors and retries](concepts/errors-and-retries.md) | Which NetBox failure becomes which typed error, what gets retried and where, and why more than one lookup match is an error rather than a guess |
| [Lookups](concepts/lookups.md) | How a natural key becomes a query string, why `?name__ie=` exists, and why a null filter is pinned rather than omitted |

## Reference

One page per CRD: every field, every condition, every way it fails.

| Page | Answers |
|---|---|
| [`NetBoxEndpoint`](reference/netboxendpoint.md) | How to point the operator at a NetBox: URL, token Secret, TLS, dry run, rate limit, and the `>=4.2, <5.0` version gate |

### The shape of a reference page

Around 112 CRDs will follow, so the shape is settled here rather than after twenty pages
have diverged. [`reference/netboxendpoint.md`](reference/netboxendpoint.md) is the
template — copy its headings in this order:

1. **Header table** — API version, kind, scope, short names, status subresource, milestone.
2. **Minimal example** — the fewest fields that actually work, valid YAML, with any Secret
   or prerequisite object it needs.
3. **Full example** — every field set, with defaults written out explicitly and commented
   as defaults.
4. **`spec`** — one subsection per field, keyed by full path (`spec.tokenSecretRef.key`),
   each with a table giving type, required, default *taken from the `+kubebuilder:default`
   marker*, and validation *quoted from the `+kubebuilder:validation:` marker*; then one
   sentence on what it does; then a **"If it is wrong"** paragraph naming the condition
   type, `Reason` constant and message the user will actually see, and separating what
   admission rejects from what fails later at reconcile.
5. **`status`** — a table of field, type, what populates it, and when. Say explicitly which
   fields are *not* cleared on failure.
6. **Conditions** — a table of type, when `True`, when `False`, and every `Reason` it can
   carry; then a reason glossary; then retry intervals.
7. **Kind-specific behaviour** — the one or two things about this kind that are not
   obvious. Cite `docs/netbox-schema.md` or a NetBox source path for every NetBox claim.
8. **Printer columns** — real `kubectl get <kind>` output, plus a table mapping column to
   JSONPath.
9. **Troubleshooting** — symptom → condition → cause → fix, driven off the `Reason`
   constants, since those enumerate the real failure modes.
10. **Related** — links to the concept pages and ADRs that explain the *why*.

Document only what is in the code. If a spec and the code disagree, the code wins and the
divergence gets reported.

## Decisions

Dated records of decisions that are expensive to reverse. Index and status:
[`decisions/README.md`](decisions/README.md).

| Page | Answers |
|---|---|
| [0001 — API group and kind naming](decisions/0001-api-group-and-kind-naming.md) | Why the group is `netbox.populator.io` and every kind is prefixed `NetBox` |
| [0002 — CRD scoping](decisions/0002-crd-scoping.md) | Why every kind is namespaced in `v1alpha1`, what that costs, and what would have to change to revisit it |
| [0003 — Ownership and references](decisions/0003-ownership-and-references.md) | How a NetBox foreign key differs from a Kubernetes owner reference, and where the operator adds each |
| [0004 — Claims-first allocation](decisions/0004-claims-first-allocation.md) | Why "allocate me an address" is a separate kind rather than a mode of `NetBoxIPAddress` |
| [0005 — Coexisting with Flux and Argo CD](decisions/0005-gitops-coexistence.md) | Why Git is authoritative, why a NetBox UI edit is drift rather than a competing opinion, and why there is no write-back |

## Operations

| Page | Answers |
|---|---|
| [NetBox schema reference](netbox-schema.md) | The authoritative field list every CRD is derived from: 159 models, 138 endpoints, machine-extracted from NetBox 4.6.8. Grep it; do not read it |
| [Regenerating the schema](regenerating.md) | How to retarget a newer NetBox release, how to test the extraction pipeline without a NetBox checkout, and how to cross-check the AST walk against a live instance |

## Examples

| Page | Answers |
|---|---|
| [Examples](examples/README.md) | Runnable manifests, and which milestone each one becomes real in |
