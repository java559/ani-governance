{{- define "ani-governance-redis.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}
{{- define "ani-governance-redis.fullname" -}}
{{- default (include "ani-governance-redis.name" .) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}
{{- define "ani-governance-redis.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "ani-governance-redis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: redis
{{- end }}
