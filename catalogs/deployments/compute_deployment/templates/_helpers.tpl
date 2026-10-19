{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{/* Render a pod's labels or annotations: the chart's own keys in .chart, then each key
     of .caller (podLabels or podAnnotations) the chart does not set. The chart's value
     wins, so a caller cannot move a selector or a key another chart reads. */}}
{{- define "podMetadata" -}}
{{- $merged := .chart -}}
{{- range $key, $value := .caller -}}
{{- if not (hasKey $merged $key) -}}
{{- $_ := set $merged $key (toString $value) -}}
{{- end -}}
{{- end -}}
{{- toYaml $merged -}}
{{- end -}}
