{{- /* List shape (not scalar) so rollout.yaml can iterate it like cserve's plan-config. */}}
{{- define "setExtraConfigFiles" -}}
  {{- $extraConfigFiles := list -}}
  {{- if .Values.config_file -}}
    {{- $entry := dict
          "configMapName"        (printf "inference-config-%v-r%v" .Values.deployment_id .Values.revision_number)
          "volumeMountMountPath" (printf "%s/%s" .Values.config_file.mount_path .Values.config_file.filename)
          "volumeMountSubPath"   .Values.config_file.filename
          "volumeMountReadOnly"  "true"
    -}}
    {{- $extraConfigFiles = append $extraConfigFiles $entry -}}
  {{- end -}}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles -}}
{{- end -}}
