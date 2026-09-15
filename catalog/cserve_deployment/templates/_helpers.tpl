{{/* This references a config map by name and makes sure it gets mounted into an inference deployment */}} 
 {{- define "setExtraConfigFiles" -}} 
 {{- $extraConfigFiles := list 
      (dict "configMapName" (print "plan-config-" (.Values.deployment_id | int)) "volumeMountMountPath" (print .Values.plan_yaml_mount_path "/" .Values.plan_yaml_filename) "volumeMountSubPath" .Values.plan_yaml_filename "volumeMountReadOnly" "true" )
  -}}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles }}
{{- end -}}
