{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/* Frontend Service auto-created by the Dynamo operator follows the convention
     `${dgd-name}-frontend`. Centralize the lookup here so ingress/service templates
     stay in sync with any future operator naming change. */}}
{{- define "dynamo.frontendServiceName" -}}
{{- printf "%s-frontend" .Values.name -}}
{{- end -}}

{{/* The Dynamo operator defaults the worker system/metrics port to 9090. */}}
{{- define "dynamo.workerMetricsPort" -}}
{{- index (.Values.envVars | default dict) "DYN_SYSTEM_PORT" | default "9090" | int -}}
{{- end -}}

{{/* Target relabeling also labels Prometheus-generated up. Read revision and role from
     each discovered Pod so old and new revisions remain distinct during a rollout. */}}
{{- define "dynamo.metricsLabels" -}}
- targetLabel: deployment_id
  replacement: {{ .Values.deploymentId | quote }}
  action: replace
- sourceLabels: [__meta_kubernetes_pod_label_platform_centml_ai_revision_number]
  targetLabel: revision_number
  action: replace
- sourceLabels: [__meta_kubernetes_pod_label_ccluster_nvidia_com_component_type]
  targetLabel: component
  action: replace
{{- end -}}

{{/* Runtime env shared by the Frontend and every worker role. */}}
{{- define "dynamo.runtimeEnv" -}}
- name: DYN_EVENT_PLANE
  value: {{ .Values.eventPlane | default "nats" | quote }}
- name: DYN_REQUEST_PLANE
  value: {{ .Values.requestPlane | quote }}
- name: PYTHONHASHSEED
  value: "0"
{{- end -}}

{{/* Forward user env without duplicating variables owned by this chart. */}}
{{- define "dynamo.userEnv" -}}
{{- $root := .root -}}
{{- $component := .component -}}
{{- $owned := list
  "DYN_EVENT_PLANE"
  "DYN_REQUEST_PLANE"
  "PYTHONHASHSEED"
  "DYN_SELF_HOST_METADATA"
  "DYN_ROUTER_QUEUE_THRESHOLD"
  "DYN_DISABLE_FRONTEND_ADMIN_API"
  "DYN_DISABLE_FRONTEND_NVEXT"
  "DYN_VLLM_ENABLE_INFERENCE_V1_GENERATE"
-}}
{{- range $key, $value := ($root.Values.envVars | default dict) }}
{{- if and (not (has $key $owned)) (not (and (eq $component "frontend") (eq $key "DYN_ROUTER_MODE"))) }}
- name: {{ $key | quote }}
  value: {{ $value | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{/* Chart-owned vLLM options go after the user's options but before a "--" positional
     separator. The chart owns --runner (Values.Validate rejects a user-supplied one) and,
     for the aggregated pool, --tensor-parallel-size. */}}
{{- define "dynamo.vllmExtraArgs" -}}
{{- $root := .root -}}
{{- $tensorParallelSize := get . "tensorParallelSize" -}}
{{- $platformArgsRendered := false -}}
{{- range $root.Values.extraArgv }}
{{- if and (not $platformArgsRendered) (eq . "--") }}
{{- if $tensorParallelSize }}
- "--tensor-parallel-size"
- {{ $tensorParallelSize | int | quote }}
{{- end }}
- "--runner"
- "generate"
{{- $platformArgsRendered = true }}
{{- end }}
- {{ . | quote }}
{{- end }}
{{- if not $platformArgsRendered }}
{{- if $tensorParallelSize }}
- "--tensor-parallel-size"
- {{ $tensorParallelSize | int | quote }}
{{- end }}
- "--runner"
- "generate"
{{- end }}
{{- end -}}

{{- define "dynamo.kaiQueueLabel" -}}
{{- if .kaiQueue }}
kai.scheduler/queue: {{ .kaiQueue | quote }}
{{- end }}
{{- end -}}

{{- define "dynamo.kaiPodScheduling" -}}
{{- if .schedulerName }}
schedulerName: {{ .schedulerName }}
{{- end }}
{{- if .priorityClassName }}
priorityClassName: {{ .priorityClassName | quote }}
{{- end }}
{{- end -}}

{{/* --kv-transfer-config of both disaggregated pools. kvTransfer adds its NIXL backend;
     LIBFABRIC also keeps the KV buffer in GPU memory. dgd.yaml validates kvTransfer. */}}
{{- define "dynamo.kvTransferConfig" -}}
{{- $config := dict "kv_connector" "NixlConnector" "kv_role" "kv_both" -}}
{{- with .kvTransfer -}}
{{- $_ := set $config "kv_connector_extra_config" (dict "backends" (list .backend)) -}}
{{- if eq .backend "LIBFABRIC" -}}
{{- $_ := set $config "kv_buffer_device" "cuda" -}}
{{- end -}}
{{- end -}}
{{- $config | toJson | quote -}}
{{- end -}}

{{- define "dynamo.efaClaimTemplateName" -}}
{{- printf "%s-r%d-efa" .Values.name (.Values.revisionNumber | int64) -}}
{{- end -}}

{{/* Claims in volumeMounts, mounted at the same paths on the Frontend and every
     worker. One pod volume per distinct claim, however many times it is mounted. */}}
{{- define "dynamo.volumeNameByClaim" -}}
{{- $names := dict -}}
{{- range $index, $mount := .volumeMounts -}}
{{- if not (hasKey $names $mount.name) -}}
{{- $_ := set $names $mount.name (printf "external-volume-%d" $index) -}}
{{- end -}}
{{- end -}}
{{- $names | toJson -}}
{{- end -}}

{{- define "dynamo.volumeMounts" -}}
{{- $names := include "dynamo.volumeNameByClaim" . | fromJson -}}
{{- range .volumeMounts }}
- mountPath: {{ .mountPath | quote }}
  name: {{ get $names .name }}
  readOnly: {{ .readOnly | default false }}
{{- end }}
{{- end -}}

{{- define "dynamo.pvcVolumes" -}}
{{- $names := include "dynamo.volumeNameByClaim" . | fromJson -}}
{{- $rendered := dict -}}
{{- range .volumeMounts }}
{{- if not (hasKey $rendered .name) }}
{{- $_ := set $rendered .name true }}
- name: {{ get $names .name }}
  persistentVolumeClaim:
    claimName: {{ .name | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{/* Render the autoscaled aggregated vLLM worker. */}}
{{- define "dynamo.aggregatedWorker" -}}
{{- $pool := .Values.workerPool -}}
- name: VllmDecodeWorker
  type: worker
  # Omit replicas because KEDA owns the aggregated worker count.
  scalingAdapter: {}
  podTemplate:
    metadata:
      annotations:
        # Worker-level rollout settings consumed by the operator.
        nvidia.com/deployment-rolling-update-max-surge: "{{ .Values.rolloutStrategy.maxSurge | int }}"
        nvidia.com/deployment-rolling-update-max-unavailable: "{{ .Values.rolloutStrategy.maxUnavailable | int }}"
        # Raw Frontend/Worker TCP is incompatible with STRICT mesh mTLS.
        sidecar.istio.io/inject: "false"
        platform.centml.ai/deployment-id: "{{ .Values.deploymentId }}"
        platform.centml.ai/revision-number: "{{ .Values.revisionNumber | int64 }}"
        {{- if not .Values.enableLogging }}
        platform.centml.ai/log-exclude: "true"
        {{- end }}
        {{- if .Values.userAnnotations }}
        {{- range $key, $value := .Values.userAnnotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
      labels:
        platform.centml.ai/deployment-id: "{{ .Values.deploymentId }}"
        platform.centml.ai/revision-number: "{{ .Values.revisionNumber | int64 }}"
        ccluster.nvidia.com/component-type: worker
        {{- include "dynamo.kaiQueueLabel" .Values | nindent 8 }}
        {{- if .Values.userAnnotations }}
        {{- range $key, $value := .Values.userAnnotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
    spec:
      {{- include "dynamo.kaiPodScheduling" .Values | nindent 6 }}
      {{- if .Values.enableNodeModelCache }}
      # Prepare the shared cache for the non-root worker.
      initContainers:
        - name: prepare-model-cache
          image: {{ .Values.imageUrl | quote }}
          command:
            - sh
            - -c
            - 'mkdir -p /cache/dynamo-hf && chown -R 1000:1000 /cache/dynamo-hf'
          securityContext:
            runAsUser: 0
          volumeMounts:
            - name: model-cache
              mountPath: /cache
      {{- end }}
      containers:
        - name: main
          image: {{ .Values.imageUrl | quote }}
          # Render user input as literal argv; never invoke a shell.
          command: ["python3"]
          # Aggregated KV routing requires explicit vLLM KV event publishing.
          args:
            - "-m"
            - "dynamo.vllm"
            - "--model"
            - {{ .Values.model | quote }}
            - "--kv-events-config"
            - '{"publisher":"zmq","topic":"kv-events","endpoint":"tcp://*:20080","enable_kv_cache_events":true}'
            {{- if .Values.servedModelName }}
            - "--served-model-name"
            - {{ .Values.servedModelName | quote }}
            {{- end }}
            # Resource selection owns TP; the chart owns the text-generation runner.
            {{- include "dynamo.vllmExtraArgs" (dict
              "root" .
              "tensorParallelSize" $pool.resources.requests.acceleratorCount
            ) | nindent 12 }}
          env:
            {{- include "dynamo.runtimeEnv" . | nindent 12 }}
            - name: DYN_SELF_HOST_METADATA
              value: "0"
            {{- if .Values.enableNodeModelCache }}
            - name: HF_HUB_CACHE
              value: /tmp/dynamo-hf-cache
            {{- end }}
            {{- include "dynamo.userEnv" (dict "root" . "component" "worker") | nindent 12 }}
          {{- if .Values.hfToken }}
          envFrom:
            - secretRef:
                name: {{ .Values.name }}-hf-token
          {{- end }}
          # v1beta1 accepts native Kubernetes resource requirements.
          resources:
            limits:
              cpu: "{{ $pool.resources.limits.cpu }}"
              memory: "{{ $pool.resources.limits.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.limits.acceleratorKey }}: {{ $pool.resources.limits.acceleratorCount | int | quote }}
            requests:
              cpu: "{{ $pool.resources.requests.cpu }}"
              memory: "{{ $pool.resources.requests.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.requests.acceleratorKey }}: {{ $pool.resources.requests.acceleratorCount | int | quote }}
          volumeMounts:
            # Use tmpfs because Kubernetes defaults /dev/shm to 64Mi.
            - name: dshm
              mountPath: /dev/shm
            {{- if .Values.enableNodeModelCache }}
            - name: model-cache
              mountPath: /tmp/dynamo-hf-cache
              subPath: dynamo-hf
            {{- end }}
            {{- if .Values.volumeMounts }}
            {{- include "dynamo.volumeMounts" .Values | trim | nindent 12 }}
            {{- end }}
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: {{ default "512Mi" $pool.devShmSize }}
        {{- if .Values.enableNodeModelCache }}
        # Init writes the PVC root; runtime mounts the private subPath.
        - name: model-cache
          persistentVolumeClaim:
            claimName: node-model-cache-pvc
        {{- end }}
        {{- if .Values.volumeMounts }}
        {{- include "dynamo.pvcVolumes" .Values | trim | nindent 8 }}
        {{- end }}
      imagePullSecrets:
        - name: ecr-image-pull
        {{- if .Values.imagePullSecret }}
        - name: {{ .Values.name }}-image-pull-secret
        {{- end }}
      tolerations:
        - effect: NoSchedule
          key: dedicated_compute
          operator: Equal
          value: "true"
      {{- if $pool.nodeAffinityLabels }}
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  {{- range $key, $value := $pool.nodeAffinityLabels }}
                  - key: {{ $key | quote }}
                    operator: In
                    values:
                      - {{ $value | quote }}
                  {{- end }}
      {{- end }}
{{- end -}}

{{/* Render a fixed pool; both roles use NIXL, while only Prefill publishes KV events. */}}
{{- define "dynamo.disaggregatedWorker" -}}
{{- $root := .root -}}
{{- $pool := .pool -}}
{{- $role := .role -}}
{{- $efa := hasKey ($root.Values.kvTransfer | default dict) "efa" -}}
- name: {{ .name }}
  type: {{ $role }}
  replicas: {{ $pool.replicas | default 1 | int }}
  podTemplate:
    metadata:
      annotations:
        nvidia.com/deployment-rolling-update-max-surge: "{{ $root.Values.rolloutStrategy.maxSurge | int }}"
        nvidia.com/deployment-rolling-update-max-unavailable: "{{ $root.Values.rolloutStrategy.maxUnavailable | int }}"
        sidecar.istio.io/inject: "false"
        platform.centml.ai/deployment-id: "{{ $root.Values.deploymentId }}"
        platform.centml.ai/revision-number: "{{ $root.Values.revisionNumber | int64 }}"
        {{- if not $root.Values.enableLogging }}
        platform.centml.ai/log-exclude: "true"
        {{- end }}
        {{- if $root.Values.userAnnotations }}
        {{- range $key, $value := $root.Values.userAnnotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
      labels:
        platform.centml.ai/deployment-id: "{{ $root.Values.deploymentId }}"
        platform.centml.ai/revision-number: "{{ $root.Values.revisionNumber | int64 }}"
        ccluster.nvidia.com/component-type: {{ $role }}
        {{- if $efa }}
        ccluster.nvidia.com/kv-transfer: efa
        {{- end }}
        {{- include "dynamo.kaiQueueLabel" $root.Values | nindent 8 }}
        {{- if $root.Values.userAnnotations }}
        {{- range $key, $value := $root.Values.userAnnotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
    spec:
      {{- include "dynamo.kaiPodScheduling" $root.Values | nindent 6 }}
      {{- if $efa }}
      resourceClaims:
        - name: efa
          resourceClaimTemplateName: {{ include "dynamo.efaClaimTemplateName" $root | quote }}
      {{- end }}
      {{- if $root.Values.enableNodeModelCache }}
      initContainers:
        - name: prepare-model-cache
          image: {{ $root.Values.imageUrl | quote }}
          command:
            - sh
            - -c
            - 'mkdir -p /cache/dynamo-hf && chown -R 1000:1000 /cache/dynamo-hf'
          securityContext:
            runAsUser: 0
          volumeMounts:
            - name: model-cache
              mountPath: /cache
      {{- end }}
      containers:
        - name: main
          image: {{ $root.Values.imageUrl | quote }}
          # Render user input as literal argv; never invoke a shell.
          command: ["python3"]
          args:
            - "-m"
            - "dynamo.vllm"
            - "--model"
            - {{ $root.Values.model | quote }}
            - "--disaggregation-mode"
            - {{ $role | quote }}
            - "--tensor-parallel-size"
            - {{ $pool.tensorParallelSize | int | quote }}
            - "--kv-transfer-config"
            - {{ include "dynamo.kvTransferConfig" $root.Values }}
            {{- if eq $role "prefill" }}
            - "--kv-events-config"
            - '{"publisher":"zmq","topic":"kv-events","endpoint":"tcp://*:20080","enable_kv_cache_events":true}'
            {{- end }}
            {{- if $root.Values.servedModelName }}
            - "--served-model-name"
            - {{ $root.Values.servedModelName | quote }}
            {{- end }}
            {{- include "dynamo.vllmExtraArgs" (dict "root" $root) | nindent 12 }}
          env:
            {{- include "dynamo.runtimeEnv" $root | nindent 12 }}
            - name: DYN_SELF_HOST_METADATA
              value: "0"
            {{- if $efa }}
            - name: FI_PROVIDER
              value: "efa"
            - name: FI_EFA_USE_DEVICE_RDMA
              value: "1"
            - name: FI_EFA_ENABLE_SHM_TRANSFER
              value: "0"
            {{- end }}
            {{- if $root.Values.enableNodeModelCache }}
            - name: HF_HUB_CACHE
              value: /tmp/dynamo-hf-cache
            {{- end }}
            {{- include "dynamo.userEnv" (dict "root" $root "component" "worker") | nindent 12 }}
          {{- if $root.Values.hfToken }}
          envFrom:
            - secretRef:
                name: {{ $root.Values.name }}-hf-token
          {{- end }}
          resources:
            {{- if $efa }}
            claims:
              - name: efa
            {{- end }}
            limits:
              cpu: "{{ $pool.resources.limits.cpu }}"
              memory: "{{ $pool.resources.limits.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.limits.acceleratorKey }}: {{ $pool.resources.limits.acceleratorCount | int | quote }}
            requests:
              cpu: "{{ $pool.resources.requests.cpu }}"
              memory: "{{ $pool.resources.requests.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.requests.acceleratorKey }}: {{ $pool.resources.requests.acceleratorCount | int | quote }}
          volumeMounts:
            - name: dshm
              mountPath: /dev/shm
            {{- if $root.Values.enableNodeModelCache }}
            - name: model-cache
              mountPath: /tmp/dynamo-hf-cache
              subPath: dynamo-hf
            {{- end }}
            {{- if $root.Values.volumeMounts }}
            {{- include "dynamo.volumeMounts" $root.Values | trim | nindent 12 }}
            {{- end }}
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: {{ default "512Mi" $pool.devShmSize }}
        {{- if $root.Values.enableNodeModelCache }}
        - name: model-cache
          persistentVolumeClaim:
            claimName: node-model-cache-pvc
        {{- end }}
        {{- if $root.Values.volumeMounts }}
        {{- include "dynamo.pvcVolumes" $root.Values | trim | nindent 8 }}
        {{- end }}
      imagePullSecrets:
        - name: ecr-image-pull
        {{- if $root.Values.imagePullSecret }}
        - name: {{ $root.Values.name }}-image-pull-secret
        {{- end }}
      tolerations:
        - effect: NoSchedule
          key: dedicated_compute
          operator: Equal
          value: "true"
      {{- if or $pool.nodeAffinityLabels $efa }}
      affinity:
        {{- if $pool.nodeAffinityLabels }}
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  {{- range $key, $value := $pool.nodeAffinityLabels }}
                  - key: {{ $key | quote }}
                    operator: In
                    values:
                      - {{ $value | quote }}
                  {{- end }}
        {{- end }}
        {{- if $efa }}
        # Every EFA worker matches this selector itself, so the first one scheduled picks
        # the zone. The selector omits the revision, so the zone holds across rollouts.
        podAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - labelSelector:
                matchLabels:
                  platform.centml.ai/deployment-id: {{ $root.Values.deploymentId | quote }}
                  ccluster.nvidia.com/kv-transfer: efa
              topologyKey: topology.kubernetes.io/zone
        {{- end }}
      {{- end }}
{{- end -}}
