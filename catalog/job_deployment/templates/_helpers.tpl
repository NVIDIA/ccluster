{{- /* List shape (not scalar) so job.yaml can iterate it like inference_v3's rollout. */}}
{{- define "setExtraConfigFiles" -}}
  {{- $extraConfigFiles := list -}}
  {{- if .Values.config_file -}}
    {{- $entry := dict
          "configMapName"        (printf "job-config-%v" .Values.deployment_id)
          "volumeMountMountPath" (printf "%s/%s" .Values.config_file.mount_path .Values.config_file.filename)
          "volumeMountSubPath"   .Values.config_file.filename
          "volumeMountReadOnly"  "true"
    -}}
    {{- $extraConfigFiles = append $extraConfigFiles $entry -}}
  {{- end -}}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles -}}
{{- end -}}
