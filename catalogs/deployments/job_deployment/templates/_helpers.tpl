{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{- /* List shape so job.yaml can range over it. */}}
{{- define "setExtraConfigFiles" -}}
  {{- $extraConfigFiles := list -}}
  {{- if .Values.configFile -}}
    {{- $entry := dict
          "configMapName"        (printf "job-config-%s" .Values.deploymentId)
          "volumeMountMountPath" (printf "%s/%s" .Values.configFile.mountPath .Values.configFile.filename)
          "volumeMountSubPath"   .Values.configFile.filename
          "volumeMountReadOnly"  "true"
    -}}
    {{- $extraConfigFiles = append $extraConfigFiles $entry -}}
  {{- end -}}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles -}}
{{- end -}}
