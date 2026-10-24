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

{{- define "kubexray.validate" -}}
{{- if not (has .Values.auth.mode (list "token" "proxy")) -}}
{{- fail "auth.mode must be token or proxy: kubexray has no anonymous mode" -}}
{{- end -}}
{{- if and (eq .Values.auth.mode "proxy") (not .Values.auth.proxy.sidecar) -}}
{{- fail "auth.mode proxy needs auth.proxy.sidecar: the auth proxy container" -}}
{{- end -}}
{{- end -}}
