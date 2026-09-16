{{/* Frontend Service auto-created by the Dynamo operator follows the convention
     `${dgd-name}-frontend`. Centralize the lookup here so ingress/service templates
     stay in sync with any future operator naming change. */}}
{{- define "dynamo.frontendServiceName" -}}
{{- printf "%s-frontend" .Values.name -}}
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
        {{- include "dynamo.kaiQueueLabel" $root.Values | nindent 8 }}
        {{- if $root.Values.userAnnotations }}
        {{- range $key, $value := $root.Values.userAnnotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}
    spec:
      {{- include "dynamo.kaiPodScheduling" $root.Values | nindent 6 }}
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
            - '{"kv_connector":"NixlConnector","kv_role":"kv_both"}'
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
            # Do not infer cluster-specific RDMA resources from the hardware profile.
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
