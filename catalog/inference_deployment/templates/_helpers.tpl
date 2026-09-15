{{/* This an empty list with no extra config files, see cserve deployment for example. */}} 
{{- define "setExtraConfigFiles" -}}
  {{- $extraConfigFiles := (list) }}
  {{- $_ := set . "ExtraConfigFiles" $extraConfigFiles }}
{{- end -}}
