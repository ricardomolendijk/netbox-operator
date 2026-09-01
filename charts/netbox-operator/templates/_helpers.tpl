{{/* Chart name, overridable. */}}
{{- define "netbox-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "netbox-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "netbox-operator.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "netbox-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "netbox-operator.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels. app.kubernetes.io/name is netbox-operator to match the label
config/base already puts on every shipped object, so a `kubectl get -l` from the docs
works against either install path.
*/}}
{{- define "netbox-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "netbox-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "netbox-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "netbox-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
The credential namespace list, validated.

`*` is refused here rather than in the schema alone, because the schema is skipped by
`helm template --validate=false` and by an older Helm, and a chart that silently rendered
a cluster-wide grant would undo #100. An empty list is refused for the same reason
hack/credential-rbac.sh refuses it: the operator would read no credential Secret at all
and exit at startup naming a file this install does not have.
*/}}
{{- define "netbox-operator.credentialNamespaces" -}}
{{- $ns := .Values.credentialNamespaces | default list -}}
{{- if not $ns -}}
{{- fail "credentialNamespaces is empty: the operator could read no endpoint credential Secret and would exit at startup. List the namespaces holding your NetBoxEndpoints' token Secrets. See docs/operations/rbac.md." -}}
{{- end -}}
{{- range $ns -}}
{{- if eq . "*" -}}
{{- fail "credentialNamespaces: '*' is not a namespace. Reading Secrets cluster-wide needs a cluster-wide grant this chart does not ship; the overlay to add it yourself is in docs/operations/rbac.md#reading-secrets-cluster-wide-anyway." -}}
{{- end -}}
{{- end -}}
{{- $ns | uniq | sortAlpha | join "," -}}
{{- end -}}

{{/*
The annotation set stamped onto CRs the operator materialises (ADR-0005 section 2),
as a comma-separated k=v list on NETBOX_GENERATED_ANNOTATIONS.

Sorted, so a values change is the only thing that moves the rendered Deployment -- the
golden-manifest check in CI would otherwise diff on Go's map order.
*/}}
{{- define "netbox-operator.generatedAnnotations" -}}
{{- $a := dict -}}
{{- if .Values.gitops.argocd.enabled -}}
{{- $_ := set $a "argocd.argoproj.io/compare-options" "IgnoreExtraneous" -}}
{{- end -}}
{{- if .Values.gitops.flux.enabled -}}
{{- $_ := set $a "kustomize.toolkit.fluxcd.io/reconcile" "disabled" -}}
{{- end -}}
{{- range $k, $v := .Values.gitops.extraAnnotations -}}
{{- $_ := set $a $k $v -}}
{{- end -}}
{{- $pairs := list -}}
{{- range $k := keys $a | sortAlpha -}}
{{- $pairs = append $pairs (printf "%s=%s" $k (get $a $k)) -}}
{{- end -}}
{{- $pairs | join "," -}}
{{- end -}}

{{- define "netbox-operator.endpointNamespace" -}}
{{- default .Release.Namespace .Values.endpoint.namespace -}}
{{- end -}}

{{/*
Whether the validating webhook is actually served: non-empty when the value asked for it
*and* this cluster can hold its serving certificate.

Gated on cert-manager's CRD and not on the value alone, for the reason the ServiceMonitor is
gated on the Prometheus Operator's -- except that here the value is on by default, so getting
it wrong is worse than a skipped scrape. cert-manager is the only certificate path by design
(docs/operations/admission-webhooks.md#certificates), and the manager serves the webhook by
default and *exits* when the certificate is not on disk. So a chart that rendered a
Certificate on a cluster without cert-manager would fail the install, and one that rendered
nothing while leaving the manager's default alone would CrashLoop it -- which is exactly what
#249 was. Every template that renders a webhook object, and the manager's own
--enable-webhooks, read this one answer, so the four cannot disagree.
*/}}
{{- define "netbox-operator.webhookEnabled" -}}
{{- if .Values.webhook.enabled -}}
{{- if .Capabilities.APIVersions.Has "cert-manager.io/v1" -}}
true
{{- else if .Values.webhook.certManager.required -}}
{{- fail "webhook.certManager.required is set and cert-manager.io/v1 is not installed on this cluster, so the webhook's serving certificate cannot be issued. Install cert-manager, or set webhook.enabled=false to run without admission validation -- every rule it enforces has a reconcile-time backstop (docs/operations/admission-webhooks.md#what-breaks-when-it-is-off)." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The webhook Service's name, and the Certificate's.

Helpers rather than three literals, because the Certificate's dnsNames, the Service and the
configuration's clientConfig all have to name the same thing: a mismatch is a failed TLS
handshake on every review, which failurePolicy: Ignore absorbs without an error anybody sees.
*/}}
{{- define "netbox-operator.webhookServiceName" -}}
{{- printf "%s-webhook" (include "netbox-operator.fullname" .) -}}
{{- end -}}

{{- define "netbox-operator.webhookCertName" -}}
{{- printf "%s-webhook-cert" (include "netbox-operator.fullname" .) -}}
{{- end -}}

{{- define "netbox-operator.webhookIssuerName" -}}
{{- printf "%s-webhook-selfsigned" (include "netbox-operator.fullname" .) -}}
{{- end -}}
