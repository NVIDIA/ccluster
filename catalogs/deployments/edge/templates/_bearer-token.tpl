{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/* The Secret the Gateway's authorization service finds by its label; it requires
     "Authorization: Bearer <token>" on requests to the annotated host. A host with no
     such Secret needs no token. Renders nothing without endpointBearerToken. */}}
{{- define "edge.bearerTokenSecret" -}}
{{- if .Values.endpointBearerToken }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ .Values.name }}-bearer-token
  namespace: {{ .Release.Namespace }}
  labels:
    ccluster.nvidia.com/bearer-token: "true"
  annotations:
    ccluster.nvidia.com/host: {{ include "edge.host" . | quote }}
type: Opaque
data:
  token: {{ .Values.endpointBearerToken | b64enc | quote }}
{{- end }}
{{- end -}}
