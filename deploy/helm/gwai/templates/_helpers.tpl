{{- define "gwai.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "gwai.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "gwai.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "gwai.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "gwai.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gwai.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Keep long generated names unique instead of truncating distinct suffixes. */}}
{{- define "gwai.boundedName" -}}
{{- $raw := .name -}}
{{- if gt (len $raw) 63 -}}
{{- printf "%s-%s" (trunc 54 $raw | trimSuffix "-") (trunc 8 (sha256sum $raw)) -}}
{{- else -}}
{{- $raw -}}
{{- end -}}
{{- end }}

{{- define "gwai.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "gwai.fullname" . }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "gwai.gatewayName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-%s" (include "gwai.fullname" .root) .gateway.name)) }}
{{- end }}

{{- define "gwai.controlPlaneName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-control-plane" (include "gwai.fullname" .))) }}
{{- end }}

{{- define "gwai.virtualKeyControlPlaneName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-virtual-key-control-plane" (include "gwai.fullname" .))) }}
{{- end }}

{{- define "gwai.adapterName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-%s-%s" (include "gwai.fullname" .root) .adapter.kind .adapter.name)) }}
{{- end }}

{{- define "gwai.adapterServiceAccountName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-sa" (include "gwai.adapterName" .))) }}
{{- end }}

{{- define "gwai.adapterSecretsRoleName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-secrets" (include "gwai.adapterName" .))) }}
{{- end }}

{{- define "gwai.valkeyName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-valkey" (include "gwai.fullname" .))) }}
{{- end }}

{{- define "gwai.resiliencyName" -}}
{{- include "gwai.boundedName" (dict "name" (printf "%s-service-invocation" (include "gwai.fullname" .))) }}
{{- end }}

{{- define "gwai.image" -}}
{{- $registry := .root.Values.global.image.registry -}}
{{- $repository := .repository -}}
{{- $tag := .root.Values.global.image.tag -}}
{{- if $registry -}}
{{ printf "%s/%s:%s" (trimSuffix "/" $registry) $repository $tag }}
{{- else -}}
{{ printf "%s:%s" $repository $tag }}
{{- end -}}
{{- end }}

{{- define "gwai.adminSecretName" -}}
{{- if .Values.admin.existingSecret -}}
{{ .Values.admin.existingSecret }}
{{- else -}}
{{ include "gwai.boundedName" (dict "name" (printf "%s-admin" (include "gwai.fullname" .))) }}
{{- end -}}
{{- end }}

{{- define "gwai.daprAPISecretName" -}}
{{- if .Values.security.daprAPI.existingSecret -}}
{{ .Values.security.daprAPI.existingSecret }}
{{- else -}}
{{ include "gwai.boundedName" (dict "name" (printf "%s-dapr-api-token" (include "gwai.fullname" .))) }}
{{- end -}}
{{- end }}

{{- define "gwai.appAPISecretName" -}}
{{- if .Values.security.appAPI.existingSecret -}}
{{ .Values.security.appAPI.existingSecret }}
{{- else -}}
{{ include "gwai.boundedName" (dict "name" (printf "%s-app-api-token" (include "gwai.fullname" .))) }}
{{- end -}}
{{- end }}

{{- define "gwai.virtualKeyAppAPISecretName" -}}
{{- if .Values.security.virtualKeyAppAPI.existingSecret -}}
{{ .Values.security.virtualKeyAppAPI.existingSecret }}
{{- else -}}
{{ include "gwai.boundedName" (dict "name" (printf "%s-virtual-key-app-api-token" (include "gwai.fullname" .))) }}
{{- end -}}
{{- end }}

{{- define "gwai.valkeyHost" -}}
{{- if .Values.valkey.enabled -}}
{{ include "gwai.valkeyName" . }}:{{ .Values.valkey.port }}
{{- else -}}
{{ required "valkey.host is required when valkey.enabled=false" .Values.valkey.host }}:{{ .Values.valkey.port }}
{{- end -}}
{{- end }}

{{- define "gwai.valkeyAuthSecretName" -}}
{{- if .Values.valkey.auth.existingSecret -}}
{{ .Values.valkey.auth.existingSecret }}
{{- else -}}
{{ include "gwai.boundedName" (dict "name" (printf "%s-valkey-auth" (include "gwai.fullname" .))) }}
{{- end -}}
{{- end }}
