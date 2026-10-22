{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/*
argocd-server's Service name, following the argo-cd chart's own fullname rule.
*/}}
{{- define "argocd-token.server" -}}
{{- if .Values.server -}}
{{- .Values.server -}}
{{- else if contains "argocd" .Release.Name -}}
{{- printf "%s-server" .Release.Name -}}
{{- else -}}
{{- printf "%s-argocd-server" .Release.Name -}}
{{- end -}}
{{- end -}}

{{- define "argocd-token.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}
