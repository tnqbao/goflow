{{- define "kubereel.name" -}}
{{- "kubereel" -}}
{{- end -}}

{{- define "kubereel.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "kubereel.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubereel.claimName" -}}
{{- default (include "kubereel.fullname" .) .Values.storage.existingClaim -}}
{{- end -}}

{{- define "kubereel.labels" -}}
app.kubernetes.io/name: {{ include "kubereel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "kubereel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubereel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
