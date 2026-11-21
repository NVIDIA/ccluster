{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{- /* The first 8 hex characters of a SHA-256 of the value. toJson sorts map keys, so
       the order the caller wrote them in does not change it. */ -}}
{{- define "externalSecrets.hash" -}}
{{- toJson . | sha256sum | trunc 8 -}}
{{- end -}}

{{- /* The name of the env Secret and of its ExternalSecret: <name>-env-<hash of the store
       and the env references>. A new reference or version gives a new name, so the pods
       roll onto a new Secret while the old one stays for the pods not replaced yet.
       Takes the chart's values. */ -}}
{{- define "externalSecrets.envName" -}}
{{- $hashed := dict "store" .externalSecrets.secretStoreRef "env" .externalSecrets.env -}}
{{- printf "%s-env-%s" .name (include "externalSecrets.hash" $hashed) -}}
{{- end -}}

{{- /* The name of the image pull Secret, the same way from the store and imagePull. */ -}}
{{- define "externalSecrets.pullName" -}}
{{- $hashed := dict "store" .externalSecrets.secretStoreRef "imagePull" .externalSecrets.imagePull -}}
{{- printf "%s-pull-%s" .name (include "externalSecrets.hash" $hashed) -}}
{{- end -}}

{{- /* An ExternalSecret remoteRef without its empty fields. */ -}}
{{- define "externalSecrets.remoteRef" -}}
key: {{ required "every reference needs a key" .key | quote }}
{{- with .property }}
property: {{ . | quote }}
{{- end }}
{{- with .version }}
version: {{ . | quote }}
{{- end }}
{{- end -}}

{{- /* One ExternalSecret, and the immutable Secret ESO creates from it once.
       target.template.metadata is always set, so the Secret carries only these labels:
       without a template, ESO copies the ExternalSecret's labels and annotations onto it.
       Takes a dict: root, the chart's top-level context; name; and pull, the imagePull
       value for the pull Secret or unset for the env Secret. */ -}}
{{- define "externalSecrets.document" -}}
{{- $values := .root.Values -}}
{{- $store := $values.externalSecrets.secretStoreRef | default dict -}}
{{- $labels := dict "app.kubernetes.io/instance" .root.Release.Name -}}
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: {{ .name }}
  namespace: {{ .root.Release.Namespace }}
  labels:
    {{- toYaml $labels | nindent 4 }}
  annotations:
    {{- include "ccluster.deleteAfterRollout" . | nindent 4 }}
spec:
  refreshPolicy: CreatedOnce
  secretStoreRef:
    name: {{ required "externalSecrets.secretStoreRef.name is required" $store.name | quote }}
    kind: {{ required "externalSecrets.secretStoreRef.kind is required" $store.kind | quote }}
  target:
    name: {{ .name }}
    creationPolicy: Owner
    immutable: true
    template:
      {{- with .pull }}
      type: kubernetes.io/dockerconfigjson
      {{- end }}
      metadata:
        labels:
          {{- toYaml $labels | nindent 10 }}
      {{- with .pull }}
      {{- /* ESO renders .dockerconfigjson when it creates the Secret, so the password
             never enters the values. quote turns server and username into Go string
             literals in ESO's template. The |- block scalar keeps YAML from reading the
             leading "{" as a flow mapping. */}}
      data:
        .dockerconfigjson: |-
          {{ "{{" }} dict "auths" (dict {{ .server | quote }} (dict "username" {{ .username | quote }} "password" .password "auth" (printf "%s:%s" {{ .username | quote }} .password | b64enc))) | toJson {{ "}}" }}
      {{- end }}
  data:
    {{- if .pull }}
    - secretKey: password
      remoteRef:
        {{- include "externalSecrets.remoteRef" .pull.password | nindent 8 }}
    {{- else }}
    {{- range $key, $ref := $values.externalSecrets.env }}
    - secretKey: {{ $key | quote }}
      remoteRef:
        {{- include "externalSecrets.remoteRef" $ref | nindent 8 }}
    {{- end }}
    {{- end }}
{{- end -}}

{{- /* The workload's ExternalSecrets: one for externalSecrets.env, one for
       externalSecrets.imagePull. Takes the chart's top-level context. */ -}}
{{- define "externalSecrets.manifests" -}}
{{- with .Values.externalSecrets }}
{{- if .env }}
---
{{ include "externalSecrets.document" (dict "root" $ "name" (include "externalSecrets.envName" $.Values)) }}
{{- end }}
{{- with .imagePull }}
---
{{ include "externalSecrets.document" (dict "root" $ "name" (include "externalSecrets.pullName" $.Values) "pull" .) }}
{{- end }}
{{- end }}
{{- end -}}

{{- /* The container's env entries for externalSecrets.env: each variable from the key
       of the same name in the env Secret. Takes the chart's values. */ -}}
{{- define "externalSecrets.env" -}}
{{- with .externalSecrets }}
{{- if .env }}
{{- $secret := include "externalSecrets.envName" $ }}
{{- range $key, $_ := .env }}
- name: {{ $key | quote }}
  valueFrom:
    secretKeyRef:
      name: {{ $secret | quote }}
      key: {{ $key | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}
