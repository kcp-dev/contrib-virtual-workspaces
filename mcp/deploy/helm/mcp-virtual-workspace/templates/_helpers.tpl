{{- define "mcp-virtual-workspace.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mcp-virtual-workspace.labels" -}}
app.kubernetes.io/name: {{ include "mcp-virtual-workspace.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "mcp-virtual-workspace.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mcp-virtual-workspace.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
