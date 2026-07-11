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

{{- define "gwai.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "gwai.fullname" . }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "gwai.anthropicServiceAccountName" -}}
{{- printf "%s-anthropic-adapter" (include "gwai.fullname" .) | trunc 63 | trimSuffix "-" }}
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
{{ include "gwai.fullname" . }}-admin
{{- end -}}
{{- end }}

{{- define "gwai.daprAPISecretName" -}}
{{- if .Values.security.daprAPI.existingSecret -}}
{{ .Values.security.daprAPI.existingSecret }}
{{- else -}}
{{ include "gwai.fullname" . }}-dapr-api-token
{{- end -}}
{{- end }}

{{- define "gwai.appAPISecretName" -}}
{{- if .Values.security.appAPI.existingSecret -}}
{{ .Values.security.appAPI.existingSecret }}
{{- else -}}
{{ include "gwai.fullname" . }}-app-api-token
{{- end -}}
{{- end }}

{{- define "gwai.valkeyHost" -}}
{{- if .Values.valkey.enabled -}}
{{ include "gwai.fullname" . }}-valkey:{{ .Values.valkey.port }}
{{- else -}}
{{ required "valkey.host is required when valkey.enabled=false" .Values.valkey.host }}:{{ .Values.valkey.port }}
{{- end -}}
{{- end }}

{{- define "gwai.valkeyAuthSecretName" -}}
{{- if .Values.valkey.auth.existingSecret -}}
{{ .Values.valkey.auth.existingSecret }}
{{- else -}}
{{ include "gwai.fullname" . }}-valkey-auth
{{- end -}}
{{- end }}
