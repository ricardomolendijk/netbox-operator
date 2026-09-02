#!/usr/bin/env bash
#
# Marks the values of spec.customFields nullable in every generated CRD, and publishes the
# CRDs into config/crd/bases. Run by `make manifests`; `make verify` fails if the output is
# not committed.
#
# `null` under spec.customFields means "remove this custom field's value" (#196), and that
# null has to survive admission to mean anything. The API server *prunes* a null whose
# schema is not nullable -- silently, before validation, so without this the key would
# simply not be there when the operator read the object back
# (apiextensions-apiserver, defaulting.PruneNonNullableNullsWithoutDefaults; the CRD
# reference calls the flag out under "Defaulting and nullable"). A *server-side apply*
# carrying one does not even get that far: it is rejected with "must be of type string:
# null", which is what #276 spent a run chasing.
#
# controller-gen cannot express it: `nullable` is a field marker
# (controller-tools pkg/crd/markers/validation.go, FieldOnlyMarkers) and the nullable thing
# here is the map's *values*, which no marker reaches -- only array items have the
# `+kubebuilder:validation:items:` family. Hence a post-pass, next to
# hack/credential-rbac.sh, which exists for the same class of reason.
#
# internal/controller.TestServerSideApplyCanRemoveACustomField applies a null against the
# real CRDs and is what turns red if this ever stops firing.
set -euo pipefail

# An unmatched glob expands to itself, and the staging directory below is one a failed
# generator can leave empty. Without this the loop would try to publish a file named
# `*.yaml` and fail on that instead of on the "no CRD carries a spec.customFields" message
# at the bottom, which is the one that says what to do.
shopt -s nullglob

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Where the CRDs are read from, and where they are published to. `make manifests` points
# controller-gen at a staging directory and names it here, so the two differ on every
# generated run; a hand-run with no argument patches config/crd/bases in place, which is
# what it always did.
#
# The split is the fix for #276. A CRD is only correct once this script has been over it,
# and pointing controller-gen straight at config/crd/bases published every one of them
# incorrect first, for the two-and-a-half seconds this script and hack/credential-rbac.sh
# take to run -- longer on a busy machine, and longer again for the half-written file
# controller-gen leaves behind while it is still writing. `build`, `run`, `install`,
# `deploy`, `verify` and `test` all depend on `manifests`, so any two of them in two
# terminals is enough for one to publish CRDs the other is reading: both envtest suites
# read this directory at testEnv.Start(), and one that reads it inside the window installs
# a spec.customFields whose values are not nullable and fails on a feature that works. In
# the run that found it the suite read netboxsites.yaml at 19:43:09 and this script reached
# that file at 19:43:11 -- two seconds, and only the CRDs the walk had already passed were
# installed correct. Staging plus the rename below means a reader sees the previous correct
# CRD or the next one, never neither.
src=${1:-$root/config/crd/bases}
out=$root/config/crd/bases

# Matched as whole lines rather than by pattern, so the status-side
# `status.provenance.customFields` -- a map[string]string that is never null -- and the YAML
# example inside the field's own description are both untouched by construction.
key='              customFields:'
props='                additionalProperties:'
type='                  type: string'
nullable='                  nullable: true'

patched=0

for file in "$src"/*.yaml; do
  published=$out/$(basename "$file")

  # A Kind that embeds no NetBoxObjectSpec -- NetBoxEndpoint, NetBoxRefGrant -- has no
  # spec.customFields to mark. It still has to be published: staging holds every CRD
  # controller-gen generated, not only the ones this script rewrites.
  if ! grep -qxF "$key" "$file"; then
    [ "$file" = "$published" ] || mv "$file" "$published"
    continue
  fi

  # Written beside the destination rather than beside the source, because a rename is only
  # atomic within one filesystem and it is the destination's that has to be the one.
  awk -v key="$key" -v props="$props" -v type="$type" -v nullable="$nullable" '
    $0 == key              { at = 1; print; next }
    at == 1 && $0 == props { at = 2; print; next }
    at == 2 && $0 == type  { print nullable; print; at = 0; next }
                           { at = 0; print }
  ' "$file" > "$published.tmp"
  mv "$published.tmp" "$published"

  if ! grep -qxF "$nullable" "$published"; then
    echo "crd-nullable: $file has a spec.customFields this script could not mark nullable." >&2
    echo "crd-nullable: controller-gen's output must have changed shape. Fix the line" >&2
    echo "crd-nullable: literals above, or a null custom-field value will be pruned by the" >&2
    echo "crd-nullable: API server and removal will silently stop working." >&2
    exit 1
  fi

  patched=$((patched + 1))
done

if [ "$patched" -eq 0 ]; then
  echo "crd-nullable: no CRD carries a spec.customFields. Either the field was removed" >&2
  echo "crd-nullable: from NetBoxObjectSpec -- in which case delete this script and its" >&2
  echo "crd-nullable: make target -- or controller-gen's indentation changed." >&2
  exit 1
fi

# Nothing may be left in staging: a CRD for a Kind that has since been deleted would be
# republished by the next run, long after `git rm` took it out of config/crd/bases.
[ "$src" = "$out" ] || rm -rf "$src"
