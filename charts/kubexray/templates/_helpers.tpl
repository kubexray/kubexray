{{- define "kubexray.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "kubexray.labels" -}}
app.kubernetes.io/name: kubexray
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "kubexray.selector" -}}
app.kubernetes.io/name: kubexray
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kubexray.tokenSecret" -}}
{{- .Values.auth.existingSecret | default (include "kubexray.fullname" .) -}}
{{- end -}}

{{- /* Where people open kubexray: publicURL, or the Ingress host. */ -}}
{{- define "kubexray.publicURL" -}}
{{- if .Values.publicURL -}}
{{- .Values.publicURL -}}
{{- else if and .Values.ingress.enabled .Values.ingress.host -}}
{{- printf "%s://%s" (ternary "https" "http" (not (empty .Values.ingress.tls))) .Values.ingress.host -}}
{{- end -}}
{{- end -}}

{{- define "kubexray.validate" -}}
{{- if not (has .Values.auth.mode (list "token" "oidc" "proxy")) -}}
{{- fail "auth.mode must be token, oidc or proxy: kubexray has no anonymous mode" -}}
{{- end -}}
{{- if eq .Values.auth.mode "oidc" -}}
{{- $oidc := .Values.auth.oidc | default dict -}}
{{- $secret := $oidc.clientSecret | default dict -}}
{{- if not (and $oidc.issuer $oidc.clientId $secret.existingSecret) -}}
{{- fail "auth.mode oidc needs auth.oidc.issuer, auth.oidc.clientId and auth.oidc.clientSecret.existingSecret" -}}
{{- end -}}
{{- $allow := .Values.auth.allow | default dict -}}
{{- if not (or $allow.emails $allow.domains $allow.groups) -}}
{{- fail "auth.mode oidc needs at least one of auth.allow.emails, auth.allow.domains or auth.allow.groups: signing in alone would let in anyone with an account at the provider" -}}
{{- end -}}
{{- with .Values.auth.existingSecret -}}
{{- $existing := lookup "v1" "Secret" $.Release.Namespace . -}}
{{- if and $existing (not (hasKey ($existing.data | default dict) "session-key")) -}}
{{- fail (printf "auth.mode oidc needs a session-key (at least 32 characters) in the Secret %s, next to the token" .) -}}
{{- end -}}
{{- end -}}
{{- if not (hasPrefix "https://" (include "kubexray.publicURL" .)) -}}
{{- fail "auth.mode oidc needs an https address: set publicURL, or enable the Ingress with a host and TLS" -}}
{{- end -}}
{{- end -}}
{{- if and (eq .Values.auth.mode "proxy") (or .Values.alerts.slack.existingSecret .Values.alerts.webhook.existingSecret) -}}
{{- fail "alerts need auth.mode token: behind an auth proxy, Alertmanager has no way to authenticate to kubexray" -}}
{{- end -}}
{{- if and (eq .Values.auth.mode "proxy") (not .Values.auth.proxy.sidecar) -}}
{{- fail "auth.mode proxy needs auth.proxy.sidecar: the auth proxy container" -}}
{{- end -}}
{{- end -}}
