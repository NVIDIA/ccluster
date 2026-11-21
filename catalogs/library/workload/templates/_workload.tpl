{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{- /* The annotations of an object that pods read under a per-revision name: created
       before the workload, kept until the rollout ends. See DESIGN.md, "Objects deleted
       after rollout". */ -}}
{{- define "ccluster.deleteAfterRollout" -}}
argocd.argoproj.io/sync-wave: "-1"
argocd.argoproj.io/sync-options: Prune=false
argocd.argoproj.io/compare-options: IgnoreExtraneous
{{- end -}}

{{- /* The pod's imagePullSecrets entries: the Secret imagePullSecret creates, the one
       externalSecrets.imagePull creates, then each of imagePullSecretNames not already
       listed; empty when none is set. A repeated name can fail server-side apply, which
       keys imagePullSecrets by name. Takes the chart's values. */ -}}
{{- define "workload.imagePullSecrets" -}}
{{- $names := list -}}
{{- if .imagePullSecret -}}
{{- $names = append $names (printf "%s-image-pull-secret" .name) -}}
{{- end -}}
{{- if and .externalSecrets .externalSecrets.imagePull -}}
{{- $names = append $names (include "externalSecrets.pullName" .) -}}
{{- end -}}
{{- range uniq (concat $names (.imagePullSecretNames | default list)) }}
- name: {{ . | quote }}
{{- end }}
{{- end -}}
