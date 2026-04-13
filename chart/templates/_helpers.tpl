{{/*
Expand the chart name.
*/}}
{{- define "managed-agents.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create the release-scoped base name.
*/}}
{{- define "managed-agents.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart label.
*/}}
{{- define "managed-agents.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Release-wide labels.
*/}}
{{- define "managed-agents.labels" -}}
helm.sh/chart: {{ include "managed-agents.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{/*
Component fullname.
*/}}
{{- define "managed-agents.componentFullname" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{- printf "%s-%s" (include "managed-agents.fullname" $root) $component | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Component selector labels.
*/}}
{{- define "managed-agents.componentSelectorLabels" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
app.kubernetes.io/name: {{ include "managed-agents.name" $root }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ $component }}
{{- end -}}

{{/*
Component labels.
*/}}
{{- define "managed-agents.componentLabels" -}}
{{ include "managed-agents.labels" (index . 0) }}
{{ include "managed-agents.componentSelectorLabels" . }}
{{- end -}}

{{/*
Runtime callback base URL reachable from sandbox runtimes.
*/}}
{{- define "managed-agents.callbackBaseURL" -}}
{{- if .Values.agentGateway.env.runtimeCallbackBaseURL -}}
{{- .Values.agentGateway.env.runtimeCallbackBaseURL -}}
{{- else -}}
{{- $ingressHost := "" -}}
{{- with .Values.agentGateway.ingress.hosts -}}
{{- with index . 0 -}}
{{- $ingressHost = .host -}}
{{- end -}}
{{- end -}}
{{- if and .Values.agentGateway.ingress.enabled $ingressHost -}}
{{- $scheme := "http" -}}
{{- if .Values.agentGateway.ingress.tls -}}
{{- $scheme = "https" -}}
{{- end -}}
{{- printf "%s://%s" $scheme $ingressHost -}}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" (include "managed-agents.componentFullname" (list . "agent-gateway")) .Release.Namespace .Values.agentGateway.service.port -}}
{{- end -}}
{{- end -}}
{{- end -}}
