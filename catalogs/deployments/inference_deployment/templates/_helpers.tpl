{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{- /* List shape so rollout.yaml can range over it. */}}
{{- define "setExtraConfigFiles" -}}
  {{- $extraConfigFiles := list -}}
  {{- if .Values.configFile -}}
    {{- $entry := dict
          "configMapName"        (printf "inference-config-%s-r%d" .Values.deploymentId (.Values.revisionNumber | int64))
          "volumeMountMountPath" (printf "%s/%s" .Values.configFile.mountPath .Values.configFile.filename)
          "volumeMountSubPath"   .Values.configFile.filename
          "volumeMountReadOnly"  "true"
    -}}
    {{- $extraConfigFiles = append $extraConfigFiles $entry -}}
  {{- end -}}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles -}}
{{- end -}}

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

{{/* The pod's imagePullSecrets entries: the chart's own pull Secrets, then each of
     imagePullSecretNames not already listed. A repeated name can fail server-side
     apply, which keys imagePullSecrets by name. */}}
{{- define "imagePullSecrets" -}}
{{- $names := list "ecr-image-pull" -}}
{{- if .imagePullSecret -}}
{{- $names = append $names (printf "%s-image-pull-secret" .name) -}}
{{- end -}}
{{- range uniq (concat $names (.imagePullSecretNames | default list)) }}
- name: {{ . | quote }}
{{- end }}
{{- end -}}
