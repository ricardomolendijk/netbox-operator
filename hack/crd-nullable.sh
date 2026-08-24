#!/usr/bin/env bash
#
# Marks the values of spec.customFields nullable in every generated CRD. Run by
# `make manifests`; `make verify` fails if the output is not committed.
#
# `null` under spec.customFields means "remove this custom field's value" (#196), and that
# null has to survive admission to mean anything. The API server *prunes* a null whose
# schema is not nullable -- silently, before validation, so without this the key would
# simply not be there when the operator read the object back
# (apiextensions-apiserver, defaulting.PruneNonNullableNullsWithoutDefaults; the CRD
# reference calls the flag out under "Defaulting and nullable").
#
# controller-gen cannot express it: `nullable` is a field marker
# (controller-tools pkg/crd/markers/validation.go, FieldOnlyMarkers) and the nullable thing
# here is the map's *values*, which no marker reaches -- only array items have the
# `+kubebuilder:validation:items:` family. Hence a post-pass, next to
# hack/credential-rbac.sh, which exists for the same class of reason.
#
# internal/controller.TestNullCustomFieldSurvivesAdmission applies a null against the real
# CRDs and is what turns red if this ever stops firing.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Matched as whole lines rather than by pattern, so the status-side
# `status.provenance.customFields` -- a map[string]string that is never null -- and the YAML
# example inside the field's own description are both untouched by construction.
key='              customFields:'
props='                additionalProperties:'
type='                  type: string'
nullable='                  nullable: true'

patched=0

for file in "$root"/config/crd/bases/*.yaml; do
  # A Kind that embeds no NetBoxObjectSpec -- NetBoxEndpoint, NetBoxRefGrant -- has no
  # spec.customFields to mark.
  grep -qxF "$key" "$file" || continue

  awk -v key="$key" -v props="$props" -v type="$type" -v nullable="$nullable" '
    $0 == key              { at = 1; print; next }
    at == 1 && $0 == props { at = 2; print; next }
    at == 2 && $0 == type  { print nullable; print; at = 0; next }
                           { at = 0; print }
  ' "$file" > "$file.tmp"
  mv "$file.tmp" "$file"

  if ! grep -qxF "$nullable" "$file"; then
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
