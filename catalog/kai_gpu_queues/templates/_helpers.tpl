{{- define "kaiGpuQueues.unlimited" -}}
quota: {{ .Values.unlimited_quota | int }}
limit: {{ .Values.unlimited_quota | int }}
overQuotaWeight: 1
{{- end }}
