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
