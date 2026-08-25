{{/*
Expand the name of the chart.
*/}}
{{- define "distort.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "distort.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "distort.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "distort.labels" -}}
helm.sh/chart: {{ include "distort.chart" . }}
{{ include "distort.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "distort.selectorLabels" -}}
app.kubernetes.io/name: {{ include "distort.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create a component-specific service account name.
*/}}
{{- define "distort.componentServiceAccountName" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{- $configured := index $root.Values.serviceAccount.names $component -}}
{{- $suffix := kebabcase $component -}}
{{- if $root.Values.serviceAccount.create }}
{{- default (printf "%s-%s" (include "distort.fullname" $root) $suffix) $configured }}
{{- else }}
{{- required (printf "serviceAccount.names.%s is required when serviceAccount.create is false" $component) $configured }}
{{- end }}
{{- end }}

{{- define "distort.managerServiceAccountName" -}}
{{- include "distort.componentServiceAccountName" (list . "manager") }}
{{- end }}

{{- define "distort.agentServiceAccountName" -}}
{{- include "distort.componentServiceAccountName" (list . "agent") }}
{{- end }}

{{- define "distort.csiControllerServiceAccountName" -}}
{{- include "distort.componentServiceAccountName" (list . "csiController") }}
{{- end }}

{{- define "distort.csiNodeServiceAccountName" -}}
{{- include "distort.componentServiceAccountName" (list . "csiNode") }}
{{- end }}

{{/* Backward-compatible alias for extensions that referenced the old helper. */}}
{{- define "distort.serviceAccountName" -}}
{{- include "distort.managerServiceAccountName" . }}
{{- end }}
