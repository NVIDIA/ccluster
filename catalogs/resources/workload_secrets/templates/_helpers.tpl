{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{/* The name of one ExternalSecret, and of the Secret it creates: <name>-<block>-<suffix>.
     Takes the chart's values as .values and "env" or "pull" as .block. */}}
{{- define "workloadSecrets.name" -}}
{{- $name := required "name is required" .values.name -}}
{{- $suffix := required "suffix is required" .values.suffix -}}
{{- printf "%s-%s-%s" $name .block $suffix -}}
{{- end -}}

{{/* An ExternalSecret remoteRef from a RemoteRef value, without its empty fields. */}}
{{- define "workloadSecrets.remoteRef" -}}
key: {{ required "every reference needs a key" .key | quote }}
{{- with .property }}
property: {{ . | quote }}
{{- end }}
{{- with .version }}
version: {{ . | quote }}
{{- end }}
{{- end -}}
