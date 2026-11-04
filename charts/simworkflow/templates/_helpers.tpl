{{- define "simworkflow.name" -}}
{{- .Release.Name | trunc 40 | trimSuffix "-" -}}
{{- end -}}

{{- define "simworkflow.labels" -}}
app.kubernetes.io/name: simworkflow
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "simworkflow.executor" -}}
{{- required "workflow.serviceAccount.name is required" .Values.workflow.serviceAccount.name -}}
{{- end -}}

{{- define "simworkflow.postgresHost" -}}
{{- if .Values.postgresql.enabled -}}
{{ include "simworkflow.name" . }}-postgresql
{{- else -}}
{{ required "postgresql.externalHost is required for external PostgreSQL" .Values.postgresql.externalHost }}
{{- end -}}
{{- end -}}

{{- define "simworkflow.s3URL" -}}
{{- if .Values.s3.externalURL -}}
{{- .Values.s3.externalURL | trimSuffix "/" -}}
{{- else if .Values.s3.enabled -}}
http://{{ include "simworkflow.name" . }}-s3.{{ .Release.Namespace }}.svc:9000
{{- else -}}
{{- fail "s3.externalURL is required when s3.enabled=false" -}}
{{- end -}}
{{- end -}}

{{- define "simworkflow.registryURL" -}}
{{- if .Values.registry.externalURL -}}
{{- .Values.registry.externalURL | trimSuffix "/" -}}
{{- else if .Values.registry.enabled -}}
http://{{ include "simworkflow.name" . }}-registry.{{ .Release.Namespace }}.svc:5000
{{- else -}}
{{- fail "registry.externalURL is required when registry.enabled=false" -}}
{{- end -}}
{{- end -}}

{{- define "simworkflow.argoURL" -}}
{{- if .Values.cli.argoURL -}}
{{- .Values.cli.argoURL | trimSuffix "/" -}}
{{- else if .Values.argo.enabled -}}
{{- $argo := index .Subcharts "argo-workflows" -}}
{{- $scheme := ternary "https" "http" $argo.Values.server.secure -}}
{{ $scheme }}://{{ include "argo-workflows.server.fullname" $argo }}.{{ .Release.Namespace }}.svc:{{ $argo.Values.server.servicePort }}
{{- else -}}
{{- required "argo.externalURL is required when argo.enabled=false" .Values.argo.externalURL -}}
{{- end -}}
{{- end -}}
