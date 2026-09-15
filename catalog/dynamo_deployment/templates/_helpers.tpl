{{/* Frontend Service auto-created by the Dynamo operator follows the convention
     `${dgd-name}-frontend`. Centralize the lookup here so ingress/service templates
     stay in sync with any future operator naming change. */}}
{{- define "dynamo.frontendServiceName" -}}
{{- printf "%s-frontend" .Values.name -}}
{{- end -}}

{{/* Runtime env shared by the Frontend and every worker role. */}}
{{- define "dynamo.runtimeEnv" -}}
- name: DYN_EVENT_PLANE
  value: {{ .Values.event_plane | default "nats" | quote }}
- name: DYN_REQUEST_PLANE
  value: {{ .Values.request_plane | quote }}
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
{{- range $key, $value := ($root.Values.env_vars | default dict) }}
{{- if and (not (has $key $owned)) (not (and (eq $component "frontend") (eq $key "DYN_ROUTER_MODE"))) }}
- name: {{ $key | quote }}
  value: {{ $value | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{/* Keep chart-owned vLLM options after user options but before a positional separator. */}}
{{- define "dynamo.vllmExtraArgs" -}}
{{- $root := .root -}}
{{- $tensorParallelSize := get . "tensorParallelSize" -}}
{{- $runnerNormalized := $root.Values.platform_vllm_runner_normalized -}}
{{- $platformArgsRendered := false -}}
{{- range $root.Values.extra_argv }}
{{- if and (not $platformArgsRendered) (eq . "--") }}
{{- if $tensorParallelSize }}
- "--tensor-parallel-size"
- {{ $tensorParallelSize | int | quote }}
{{- end }}
{{- if not $runnerNormalized }}
- "--runner"
- "generate"
{{- end }}
{{- $platformArgsRendered = true }}
{{- end }}
- {{ . | quote }}
{{- end }}
{{- if not $platformArgsRendered }}
{{- if $tensorParallelSize }}
- "--tensor-parallel-size"
- {{ $tensorParallelSize | int | quote }}
{{- end }}
{{- if not $runnerNormalized }}
- "--runner"
- "generate"
{{- end }}
{{- end }}
{{- end -}}

{{- define "dynamo.kaiQueueLabel" -}}
{{- if .kai_queue }}
kai.scheduler/queue: {{ .kai_queue | quote }}
{{- end }}
{{- end -}}

{{- define "dynamo.kaiPodScheduling" -}}
{{- if .scheduler_name }}
schedulerName: {{ .scheduler_name }}
{{- end }}
{{- if .priority_class_name }}
priorityClassName: {{ .priority_class_name | quote }}
{{- end }}
{{- end -}}

{{/* Render the autoscaled aggregated vLLM worker. */}}
{{- define "dynamo.aggregatedWorker" -}}
{{- /* Support Helm values persisted before worker_pool. */ -}}
{{- $pool := .Values.worker_pool | default .Values -}}
- name: VllmDecodeWorker
  type: worker
  # Omit replicas because KEDA owns the aggregated worker count.
  scalingAdapter: {}
  podTemplate:
    metadata:
      annotations:
        # Worker-level rollout settings consumed by the operator.
        nvidia.com/deployment-rolling-update-max-surge: "{{ .Values.rollout_strategy.max_surge | int }}"
        nvidia.com/deployment-rolling-update-max-unavailable: "{{ .Values.rollout_strategy.max_unavailable | int }}"
        # Raw Frontend/Worker TCP is incompatible with STRICT mesh mTLS.
        sidecar.istio.io/inject: "false"
        platform.centml.ai/deployment-id: "{{ .Values.deployment_id | int }}"
        platform.centml.ai/revision-number: "{{ .Values.revision_number | int }}"
        platform.centml.ai/user-id: "{{ .Values.user_id | int }}"
        {{- if not .Values.enable_logging }}
        platform.centml.ai/log-exclude: "true"
        {{- end }}
        {{- if .Values.user_annotations }}
        {{- range $key, $value := .Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
      labels:
        platform.centml.ai/deployment-id: "{{ .Values.deployment_id | int }}"
        platform.centml.ai/revision-number: "{{ .Values.revision_number | int }}"
        ccluster.nvidia.com/component-type: worker
        {{- include "dynamo.kaiQueueLabel" .Values | nindent 8 }}
        {{- if .Values.user_annotations }}
        {{- range $key, $value := .Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
    spec:
      {{- include "dynamo.kaiPodScheduling" .Values | nindent 6 }}
      {{- if .Values.enable_node_model_cache }}
      # Prepare the shared cache for the non-root worker.
      initContainers:
        - name: prepare-model-cache
          image: {{ .Values.image_url | quote }}
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
          image: {{ .Values.image_url | quote }}
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
            {{- if .Values.served_model_name }}
            - "--served-model-name"
            - {{ .Values.served_model_name | quote }}
            {{- end }}
            # Resource selection owns TP; the chart owns the text-generation runner.
            {{- include "dynamo.vllmExtraArgs" (dict
              "root" .
              "tensorParallelSize" $pool.resources.requests.accelerator_count
            ) | nindent 12 }}
          env:
            {{- include "dynamo.runtimeEnv" . | nindent 12 }}
            - name: DYN_SELF_HOST_METADATA
              value: "0"
            {{- if .Values.enable_node_model_cache }}
            - name: HF_HUB_CACHE
              value: /tmp/dynamo-hf-cache
            {{- end }}
            {{- include "dynamo.userEnv" (dict "root" . "component" "worker") | nindent 12 }}
          {{- if .Values.hf_token }}
          envFrom:
            - secretRef:
                name: {{ .Values.name }}-hf-token
          {{- end }}
          # v1beta1 accepts native Kubernetes resource requirements.
          resources:
            limits:
              cpu: "{{ $pool.resources.limits.cpu }}"
              memory: "{{ $pool.resources.limits.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.limits.accelerator_key }}: "{{ $pool.resources.limits.accelerator_count }}"
            requests:
              cpu: "{{ $pool.resources.requests.cpu }}"
              memory: "{{ $pool.resources.requests.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.requests.accelerator_key }}: "{{ $pool.resources.requests.accelerator_count }}"
          volumeMounts:
            # Use tmpfs because Kubernetes defaults /dev/shm to 64Mi.
            - name: dshm
              mountPath: /dev/shm
            {{- if .Values.enable_node_model_cache }}
            - name: model-cache
              mountPath: /tmp/dynamo-hf-cache
              subPath: dynamo-hf
            {{- end }}
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: {{ default "512Mi" $pool.dev_shm_size }}
        {{- if .Values.enable_node_model_cache }}
        # Init writes the PVC root; runtime mounts the private subPath.
        - name: model-cache
          persistentVolumeClaim:
            claimName: node-model-cache-pvc
        {{- end }}
      imagePullSecrets:
        - name: ecr-image-pull
        {{- if .Values.image_pull_secret }}
        - name: {{ .Values.name }}-image-pull-secret
        {{- end }}
      tolerations:
        - effect: NoSchedule
          key: dedicated_compute
          operator: Equal
          value: "true"
      {{- if $pool.node_affinity_labels }}
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  {{- range $key, $value := $pool.node_affinity_labels }}
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
- name: {{ .name }}
  type: {{ $role }}
  replicas: {{ $pool.replicas }}
  podTemplate:
    metadata:
      annotations:
        nvidia.com/deployment-rolling-update-max-surge: "{{ $root.Values.rollout_strategy.max_surge | int }}"
        nvidia.com/deployment-rolling-update-max-unavailable: "{{ $root.Values.rollout_strategy.max_unavailable | int }}"
        sidecar.istio.io/inject: "false"
        platform.centml.ai/deployment-id: "{{ $root.Values.deployment_id | int }}"
        platform.centml.ai/revision-number: "{{ $root.Values.revision_number | int }}"
        platform.centml.ai/user-id: "{{ $root.Values.user_id | int }}"
        {{- if not $root.Values.enable_logging }}
        platform.centml.ai/log-exclude: "true"
        {{- end }}
        {{- if $root.Values.user_annotations }}
        {{- range $key, $value := $root.Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
      labels:
        platform.centml.ai/deployment-id: "{{ $root.Values.deployment_id | int }}"
        platform.centml.ai/revision-number: "{{ $root.Values.revision_number | int }}"
        ccluster.nvidia.com/component-type: {{ $role }}
        {{- include "dynamo.kaiQueueLabel" $root.Values | nindent 8 }}
        {{- if $root.Values.user_annotations }}
        {{- range $key, $value := $root.Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
    spec:
      {{- include "dynamo.kaiPodScheduling" $root.Values | nindent 6 }}
      {{- if $root.Values.enable_node_model_cache }}
      initContainers:
        - name: prepare-model-cache
          image: {{ $root.Values.image_url | quote }}
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
          image: {{ $root.Values.image_url | quote }}
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
            - {{ $pool.tensor_parallel_size | int | quote }}
            - "--kv-transfer-config"
            - '{"kv_connector":"NixlConnector","kv_role":"kv_both"}'
            {{- if eq $role "prefill" }}
            - "--kv-events-config"
            - '{"publisher":"zmq","topic":"kv-events","endpoint":"tcp://*:20080","enable_kv_cache_events":true}'
            {{- end }}
            {{- if $root.Values.served_model_name }}
            - "--served-model-name"
            - {{ $root.Values.served_model_name | quote }}
            {{- end }}
            {{- include "dynamo.vllmExtraArgs" (dict "root" $root) | nindent 12 }}
          env:
            {{- include "dynamo.runtimeEnv" $root | nindent 12 }}
            - name: DYN_SELF_HOST_METADATA
              value: "0"
            {{- if $root.Values.enable_node_model_cache }}
            - name: HF_HUB_CACHE
              value: /tmp/dynamo-hf-cache
            {{- end }}
            {{- include "dynamo.userEnv" (dict "root" $root "component" "worker") | nindent 12 }}
          {{- if $root.Values.hf_token }}
          envFrom:
            - secretRef:
                name: {{ $root.Values.name }}-hf-token
          {{- end }}
          resources:
            # Do not infer cluster-specific RDMA resources from the hardware profile.
            limits:
              cpu: "{{ $pool.resources.limits.cpu }}"
              memory: "{{ $pool.resources.limits.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.limits.accelerator_key }}: "{{ $pool.resources.limits.accelerator_count }}"
            requests:
              cpu: "{{ $pool.resources.requests.cpu }}"
              memory: "{{ $pool.resources.requests.memory }}"
              {{ default "nvidia.com/gpu" $pool.resources.requests.accelerator_key }}: "{{ $pool.resources.requests.accelerator_count }}"
          volumeMounts:
            - name: dshm
              mountPath: /dev/shm
            {{- if $root.Values.enable_node_model_cache }}
            - name: model-cache
              mountPath: /tmp/dynamo-hf-cache
              subPath: dynamo-hf
            {{- end }}
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: {{ default "512Mi" $pool.dev_shm_size }}
        {{- if $root.Values.enable_node_model_cache }}
        - name: model-cache
          persistentVolumeClaim:
            claimName: node-model-cache-pvc
        {{- end }}
      imagePullSecrets:
        - name: ecr-image-pull
        {{- if $root.Values.image_pull_secret }}
        - name: {{ $root.Values.name }}-image-pull-secret
        {{- end }}
      tolerations:
        - effect: NoSchedule
          key: dedicated_compute
          operator: Equal
          value: "true"
      {{- if $pool.node_affinity_labels }}
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  {{- range $key, $value := $pool.node_affinity_labels }}
                  - key: {{ $key | quote }}
                    operator: In
                    values:
                      - {{ $value | quote }}
                  {{- end }}
      {{- end }}
{{- end -}}
