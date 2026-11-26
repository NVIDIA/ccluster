{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{- /* istiod's Gateway API implementation. */ -}}

{{/* istiod takes the upstream protocol from appProtocol, at the Gateway and at a meshed
     pod's sidecar, and its protocol sniffing does not recognise gRPC. */}}
{{- define "edge.istio.appProtocol" -}}
{{- ternary "grpc" "http" (eq (.Values.backendProtocol | default "HTTP") "GRPC") -}}
{{- end -}}

{{/* istio_requests_total that the Gateway reports for the Service. It keeps
     destination_service_name and destination_service_namespace while the Service has no
     endpoints, when the Gateway answers 503 with response_flags UH. Requests the
     Gateway's authorization service denied carry response_flags UAEX and are left out,
     so a client without the bearer token can neither wake the deployment nor keep it
     running. The envoy-stats PodMonitor of catalogs/cluster_base/kube_prometheus_stack
     scrapes the metric from the Gateway's pods. */}}
{{- define "edge.istio.activationQuery" -}}
sum(
  rate(
    istio_requests_total{
      destination_service_namespace="{{ .Release.Namespace }}",
      destination_service_name="{{ .Values.name }}",
      response_flags!~".*UAEX.*"
    }[5m]
  )
)
{{- end -}}

{{/* What istiod needs beyond the common objects: with sessionAffinity, a consistent-hash
     DestinationRule on the Service. Argo CD prunes it when sessionAffinity is unset. */}}
{{- define "edge.istio.objects" -}}
{{- if .Values.sessionAffinity }}
apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata:
  name: {{ .Values.name | quote }}
  namespace: {{ .Release.Namespace }}
  labels:
    platform.centml.ai/deployment-id: "{{ .Values.deploymentId }}"
spec:
  host: {{ .Values.name }}.{{ .Release.Namespace }}.svc.cluster.local
  trafficPolicy:
    loadBalancer:
      # Pin locality LB to off so the consistent-hash invariant stays
      # self-contained: if the mesh-level default is ever flipped on,
      # Gateway replicas in different zones would build different endpoint
      # rings and the same X-Session-Id could route to different pods.
      localityLbSetting:
        enabled: false
      # Field is `consistentHash` (not `consistentHashLB` — the type name);
      # wrong name is silently dropped.
      consistentHash:
        httpHeaderName: "X-Session-Id"
  # outlierDetection deliberately omitted: it silently enables Istio
  # locality-aware routing (istio/istio#41674, #48084), which breaks
  # consistent hash across multi-zone Gateway replicas. Pod health is
  # handled by the rollout's readinessProbe + endpoints controller.
  {{- end }}
{{- end -}}
