{{- define "kaiGpuQueues.unlimited" -}}
quota: {{ .Values.unlimitedQuota | int }}
limit: {{ .Values.unlimitedQuota | int }}
overQuotaWeight: 1
{{- end }}
