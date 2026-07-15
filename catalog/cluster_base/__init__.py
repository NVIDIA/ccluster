# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Typed schema for the cluster_base ApplicationSet input.

The platform controller (api/src/services/infra.py) consumes a
`ClusterComponents` and emits one ArgoCD ApplicationSet per cluster whose
list generator carries one element per enabled component. Per-component
override schemas live in `cluster_base.components.<name>.<Name>Overrides`.
Each override schema declares explicit Pydantic fields only for the
values that are tunable per-cluster. Unmodeled JSON fields are silently
dropped (Pydantic v2 default).
"""

import logging
from enum import StrEnum
from typing import Any, ClassVar, Optional

from pydantic import BaseModel, ConfigDict, Field, SerializationInfo, SerializeAsAny, field_validator, model_serializer

from catalog.cluster_base.components.argo_rollouts import ArgoRolloutsOverrides
from catalog.cluster_base.components.cert_manager import CertManagerOverrides
from catalog.cluster_base.components.external_dns import ExternalDnsOverrides
from catalog.cluster_base.components.final_stack import FinalStackOverrides
from catalog.cluster_base.components.fluent_bit import FluentBitOverrides
from catalog.cluster_base.components.gpu_operator import GpuOperatorOverrides
from catalog.cluster_base.components.ingress_nginx import IngressNginxOverrides
from catalog.cluster_base.components.istio import IstioOverrides
from catalog.cluster_base.components.keda import KedaOverrides
from catalog.cluster_base.components.kube_prometheus_stack import KubePrometheusStackOverrides
from catalog.cluster_base.components.longhorn import LonghornOverrides
from catalog.cluster_base.components.metrics_server import MetricsServerOverrides
from catalog.cluster_base.components.network_operator import NetworkOperatorOverrides
from catalog.cluster_base.components.opentelemetry_collector import OpentelemetryCollectorOverrides
from catalog.cluster_base.components.prometheus_adapter import PrometheusAdapterOverrides
from catalog.cluster_base.components.spire import SpireOverrides
from catalog.cluster_base.components.spire_crds import SpireCrdsOverrides
from catalog.cluster_base.components.teleport_kube_agent import TeleportKubeAgentOverrides

logger = logging.getLogger(__name__)

__all__ = [
    "CloudProvider",
    "ClusterComponents",
    "ClusterRef",
    "ComponentEntry",
    "Shared",
    # per-component models (each binds chart metadata + typed Overrides)
    "ArgoRollouts",
    "CertManager",
    "ExternalDns",
    "FinalStack",
    "FluentBit",
    "GpuOperator",
    "IngressNginx",
    "Istio",
    "Keda",
    "KubePrometheusStack",
    "Longhorn",
    "MetricsServer",
    "NetworkOperator",
    "OpentelemetryCollector",
    "PrometheusAdapter",
    "Spire",
    "SpireCrds",
    "TeleportKubeAgent",
    # per-component override schemas
    "ArgoRolloutsOverrides",
    "CertManagerOverrides",
    "ExternalDnsOverrides",
    "FinalStackOverrides",
    "FluentBitOverrides",
    "GpuOperatorOverrides",
    "IngressNginxOverrides",
    "IstioOverrides",
    "KedaOverrides",
    "KubePrometheusStackOverrides",
    "LonghornOverrides",
    "MetricsServerOverrides",
    "NetworkOperatorOverrides",
    "OpentelemetryCollectorOverrides",
    "PrometheusAdapterOverrides",
    "SpireOverrides",
    "SpireCrdsOverrides",
    "TeleportKubeAgentOverrides",
]


# ---------------------------------------------------------------------------
# cloud provider: stamped on the cluster row at registration time, threaded
# in as serialization context so ComponentEntry subclasses can inject
# provider-conditional defaults (e.g. the CNI bin dir for istio).
# ---------------------------------------------------------------------------


class CloudProvider(StrEnum):
    GCP = "gcp"
    AWS = "aws"
    AZURE = "azure"
    NEBIUS = "nebius"


_SPIFFE_TOKEN_DIR = "/var/run/secrets/spiffe"
_SPIFFE_TOKEN_FILE = f"{_SPIFFE_TOKEN_DIR}/token"


# ---------------------------------------------------------------------------
# SPIFFE JWT-injection primitives. The pieces below are identical in content
# across every component that mounts a SPIFFE token (cert-manager, external-dns,
# fluent-bit, kube-prometheus-stack); only the Helm value *keys* a given chart
# exposes them under differ. Each component's cluster_defaults composes these
# under the keys its chart expects:
#   - bitnami-style charts -> extraVolumes/extraVolumeMounts/extraContainers/env
#   - kube-prometheus-stack -> prometheusSpec.volumes/containers/securityContext
# ---------------------------------------------------------------------------


def _spiffe_pod_security_context(run_as_user: int) -> dict:
    """Pod-level security context for a SPIFFE-injected workload."""
    return {
        "runAsNonRoot": True,
        "runAsUser": run_as_user,
        "runAsGroup": run_as_user,
        "fsGroup": run_as_user,
    }


def _spiffe_volumes(chart_name: str) -> list[dict]:
    """The three volumes backing the spiffe-helper sidecar.

    `spiffe-helper-config` points at the `<chart_name>-spiffe-helper-config`
    ConfigMap, so the name is chart-specific.
    """
    return [
        {"name": "spiffe-workload-api", "csi": {"driver": "csi.spiffe.io", "readOnly": True}},
        {"name": "spiffe-token", "emptyDir": {"medium": "Memory"}},
        {"name": "spiffe-helper-config", "configMap": {"name": f"{chart_name}-spiffe-helper-config"}},
    ]


def _spiffe_volume_mounts() -> list[dict]:
    """Volume mounts the SPIFFE token onto the main application container."""
    return [
        {"name": "spiffe-token", "mountPath": _SPIFFE_TOKEN_DIR, "readOnly": True},
    ]


def _spiffe_helper_sidecar(run_as_user: Optional[int] = None) -> dict:
    """The spiffe-helper sidecar container that fetches/refreshes the JWT.

    When `run_as_user` is None the security context omits the runAs* fields
    (the chart/pod default applies); otherwise it pins them.
    """
    security_context: dict = {
        "allowPrivilegeEscalation": False,
        "readOnlyRootFilesystem": True,
        "capabilities": {"drop": ["ALL"]},
    }
    if run_as_user is not None:
        security_context = {
            "runAsNonRoot": True,
            "runAsUser": run_as_user,
            "runAsGroup": run_as_user,
            **security_context,
        }
    return {
        "name": "spiffe-helper",
        "image": "ghcr.io/spiffe/spiffe-helper:0.11.0",
        "imagePullPolicy": "IfNotPresent",
        "args": ["-config", "/etc/spiffe-helper/spiffe-helper.conf"],
        "securityContext": security_context,
        "volumeMounts": [
            {"name": "spiffe-workload-api", "mountPath": "/spiffe-workload-api", "readOnly": True},
            {"name": "spiffe-token", "mountPath": _SPIFFE_TOKEN_DIR},
            {"name": "spiffe-helper-config", "mountPath": "/etc/spiffe-helper", "readOnly": True},
        ],
    }


def _aws_creds_env(role_arn: str, region: str) -> list[dict]:
    """AWS IRSA-via-SPIFFE credential env vars for the main container."""
    return [
        {"name": "AWS_WEB_IDENTITY_TOKEN_FILE", "value": _SPIFFE_TOKEN_FILE},
        {"name": "AWS_ROLE_ARN", "value": role_arn},
        {"name": "AWS_DEFAULT_REGION", "value": region},
    ]


def _deep_merge(base: dict, overlay: dict) -> dict:
    """Recursive dict merge. `overlay` wins on conflict at every level.

    Used by `ComponentEntry._serialize_with_defaults` to layer
    user values on top of provider defaults at nested paths
    (e.g. `istio-cni.cni.cniBinDir`). Returns a new dict; does not
    mutate either input.
    """
    out: dict = dict(base)
    for key, overlay_value in overlay.items():
        base_value = out.get(key)
        if isinstance(base_value, dict) and isinstance(overlay_value, dict):
            out[key] = _deep_merge(base_value, overlay_value)
        else:
            out[key] = overlay_value
    return out


# ---------------------------------------------------------------------------
# shared: globals injected into every child app's helm.valuesObject. Children
# read these as `.Values.global.*`. `cluster.name` doubles as the dynamic
# cluster identifier consumed by templates/applications.yaml when synthesizing
# child Application names and destinations.
# ---------------------------------------------------------------------------


class ClusterRef(BaseModel):
    name: str = "ccluster"
    domain: str = "cluster.example.com"


class Route53Config(BaseModel):
    hostedZoneId: str = "Z0000000000000000000"
    certManager: str = "arn:aws:iam::000000000000:role/cert-manager"
    externalDns: str = "arn:aws:iam::000000000000:role/external-dns"


class CloudWatchConfig(BaseModel):
    fluentbit: str = "arn:aws:iam::000000000000:role/fluentbit"


class AmpConfig(BaseModel):
    baseUrl: str = "https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-00000000-0000-0000-0000-000000000000"
    prometheus: str = "arn:aws:iam::000000000000:role/prometheus"


class SnsConfig(BaseModel):
    alertsTopicArn: str = "arn:aws:sns:us-east-1:000000000000:alerts"
    alertmanager: str = "arn:aws:iam::000000000000:role/alertmanager"


class AWSConfig(BaseModel):
    accountId: str = "000000000000"
    region: str = "us-east-1"
    route53: Route53Config = Field(default_factory=Route53Config)
    cloudwatch: CloudWatchConfig = Field(default_factory=CloudWatchConfig)
    amp: AmpConfig = Field(default_factory=AmpConfig)
    sns: SnsConfig = Field(default_factory=SnsConfig)


class SharedGlobal(BaseModel):
    cluster: ClusterRef = Field(default_factory=ClusterRef)
    aws: AWSConfig = Field(default_factory=AWSConfig)


class Shared(BaseModel):
    # `global` is a Python reserved word; alias only on the wire.
    model_config = ConfigDict(populate_by_name=True)

    global_: SharedGlobal = Field(default_factory=SharedGlobal, alias="global")
    # Excluded from serialization — infrastructure metadata, not a Helm value.
    cloud_provider: Optional[CloudProvider] = Field(default=None, exclude=True)

    @field_validator("cloud_provider", mode="before")
    @classmethod
    def _parse_cloud_provider(cls, raw: Any) -> Optional[CloudProvider]:
        """Best-effort coercion of a free-form value into CloudProvider.

        Cluster rows carry `cloud_provider` as an unconstrained DB string, so
        normalize case/whitespace and fall back to None for missing or
        unrecognized values instead of raising — legacy rows (or anything the
        schema layer hasn't tightened to reject) get "no provider defaults"
        rather than failing the stack run.
        """
        if raw is None or isinstance(raw, CloudProvider):
            return raw
        if not isinstance(raw, str) or not raw.strip():
            return None
        try:
            return CloudProvider(raw.strip().lower())
        except ValueError:
            logger.warning("Unrecognized cloud_provider %r; no provider defaults will apply", raw)
            return None


# ---------------------------------------------------------------------------
# components: one ApplicationSet list-generator element per enabled subclass.
# ---------------------------------------------------------------------------


class ComponentEntry(BaseModel):
    """Base for one component in a cluster_base ApplicationSet.

    Subclasses bind three pieces of chart metadata as `ClassVar`s (Pydantic
    excludes ClassVars from validation and serialization) and narrow
    `values` to the component's typed Overrides model:

      - `chart_name`: kebab-case, ends up in the child Application's
        metadata.name (must be DNS-1123).
      - `namespace`: destination namespace for the child Application.
      - `wave`: ArgoCD sync-wave annotation; lower runs first.
    """

    chart_name: ClassVar[str]
    namespace: ClassVar[str]
    wave: ClassVar[int]

    enabled: bool = True
    # SerializeAsAny so subclass-narrowed Overrides survive model_dump()
    # — without it Pydantic serializes against the base class schema and
    # drops fields declared on the subclass.
    values: Optional[SerializeAsAny[BaseModel]] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        """Helm-value defaults derived from cluster context.

        Receives the full `Shared` object so subclasses can read cluster
        identity (`shared.global_.cluster.*`), cloud provider
        (`shared.cloud_provider`), AWS config, etc. Override in subclasses
        that need to inject cluster-specific values (e.g. Spire's
        trustDomain/clusterName, Istio's CNI bin dir).
        """
        return {}

    @model_serializer(mode="wrap")
    def _serialize_with_defaults(self, handler: Any, info: SerializationInfo) -> dict:
        """Inject cluster_defaults under the `values` key.

        The caller passes the `Shared` object via
        `model_dump(..., context={"shared": shared})`.
        Without context, behavior is unchanged.
        """
        out = handler(self)
        shared = (info.context or {}).get("shared") if info.context else None
        if shared is None:
            return out
        defaults = self.cluster_defaults(shared)
        if defaults:
            out["values"] = _deep_merge(defaults, out.get("values") or {})
        return out


class KubePrometheusStack(ComponentEntry):
    chart_name: ClassVar[str] = "kube-prometheus-stack"
    namespace: ClassVar[str] = "prometheus"
    wave: ClassVar[int] = -60
    values: Optional[KubePrometheusStackOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        role_arn = shared.global_.aws.amp.prometheus
        region = shared.global_.aws.region
        # kube-prometheus-stack exposes the SPIFFE pieces under prometheusSpec
        # (securityContext/volumes/volumeMounts/containers) rather than the
        # bitnami-style extra* keys, and the AWS creds env rides on the existing
        # `prometheus` container — so compose the shared primitives by hand here.
        return {
            "kube-prometheus-stack": {
                "prometheus": {
                    "prometheusSpec": {
                        "securityContext": _spiffe_pod_security_context(65532),
                        "volumes": _spiffe_volumes(cls.chart_name),
                        "volumeMounts": _spiffe_volume_mounts(),
                        "containers": [
                            {"name": "prometheus", "env": _aws_creds_env(role_arn, region)},
                            _spiffe_helper_sidecar(65532),
                        ],
                    }
                }
            }
        }


class Istio(ComponentEntry):
    chart_name: ClassVar[str] = "istio"
    namespace: ClassVar[str] = "istio-system"
    wave: ClassVar[int] = -50
    values: Optional[IstioOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        # The chart's values.yaml targets k3s; with no provider set
        # (e.g. local k3s/kind dev), keep those defaults. For each known
        # cloud, assume that cloud's dominant managed-K8s offering:
        #   gcp -> GKE COS, where /opt/cni/bin is read-only and the
        #          writable CNI bin dir is /home/kubernetes/bin
        #   aws -> EKS, azure -> AKS, nebius -> mk8s, all of which use
        #          the standard /opt/cni/bin
        # Self-hosted clusters (e.g. kubeadm on GCE) that don't fit the
        # managed-K8s assumption can override `cniBinDir` per-cluster.
        if shared.cloud_provider is None:
            return {}
        bin_dir = "/home/kubernetes/bin" if shared.cloud_provider is CloudProvider.GCP else "/opt/cni/bin"
        return {"istio-cni": {"cni": {"cniBinDir": bin_dir, "cniConfDir": "/etc/cni/net.d"}}}


class IngressNginx(ComponentEntry):
    chart_name: ClassVar[str] = "ingress-nginx"
    namespace: ClassVar[str] = "ingress-nginx"
    wave: ClassVar[int] = -40
    values: Optional[IngressNginxOverrides] = None


class SpireCrds(ComponentEntry):
    chart_name: ClassVar[str] = "spire-crds"
    namespace: ClassVar[str] = "spire-system"
    wave: ClassVar[int] = -70
    values: Optional[SpireCrdsOverrides] = None


class Spire(ComponentEntry):
    chart_name: ClassVar[str] = "spire"
    namespace: ClassVar[str] = "spire-system"
    wave: ClassVar[int] = -65
    values: Optional[SpireOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        name = shared.global_.cluster.name
        domain = shared.global_.cluster.domain
        return {"spire": {"global": {"spire": {"clusterName": name, "trustDomain": domain}}}}


class CertManager(ComponentEntry):
    chart_name: ClassVar[str] = "cert-manager"
    namespace: ClassVar[str] = "cert-manager"
    wave: ClassVar[int] = -45
    values: Optional[CertManagerOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        return {
            "cert-manager": {
                "podSecurityContext": _spiffe_pod_security_context(65532),
                "extraVolumes": _spiffe_volumes(cls.chart_name),
                "extraVolumeMounts": _spiffe_volume_mounts(),
                "extraContainers": [_spiffe_helper_sidecar(65532)],
                "extraEnv": _aws_creds_env(shared.global_.aws.route53.certManager, shared.global_.aws.region),
            }
        }


class ExternalDns(ComponentEntry):
    chart_name: ClassVar[str] = "external-dns"
    namespace: ClassVar[str] = "external-dns"
    wave: ClassVar[int] = 10
    values: Optional[ExternalDnsOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        return {
            "external-dns": {
                "domainFilters": [shared.global_.cluster.domain],
                "podSecurityContext": _spiffe_pod_security_context(65532),
                "extraVolumes": _spiffe_volumes(cls.chart_name),
                "extraVolumeMounts": _spiffe_volume_mounts(),
                "extraContainers": [_spiffe_helper_sidecar(65532)],
                "env": _aws_creds_env(shared.global_.aws.route53.externalDns, shared.global_.aws.region),
            }
        }


class PrometheusAdapter(ComponentEntry):
    chart_name: ClassVar[str] = "prometheus-adapter"
    namespace: ClassVar[str] = "prometheus"
    wave: ClassVar[int] = 30
    values: Optional[PrometheusAdapterOverrides] = None


class Keda(ComponentEntry):
    chart_name: ClassVar[str] = "keda"
    namespace: ClassVar[str] = "keda"
    wave: ClassVar[int] = 30
    values: Optional[KedaOverrides] = None


class FluentBit(ComponentEntry):
    chart_name: ClassVar[str] = "fluent-bit"
    namespace: ClassVar[str] = "fluent-bit"
    wave: ClassVar[int] = 30
    values: Optional[FluentBitOverrides] = None

    @classmethod
    def cluster_defaults(cls, shared: "Shared") -> dict:
        return {
            "fluent-bit": {
                "extraVolumes": _spiffe_volumes(cls.chart_name),
                "extraVolumeMounts": _spiffe_volume_mounts(),
                "extraContainers": [_spiffe_helper_sidecar()],
                "env": _aws_creds_env(shared.global_.aws.cloudwatch.fluentbit, shared.global_.aws.region),
            }
        }


class ArgoRollouts(ComponentEntry):
    chart_name: ClassVar[str] = "argo-rollouts"
    namespace: ClassVar[str] = "argo-rollouts"
    wave: ClassVar[int] = 30
    values: Optional[ArgoRolloutsOverrides] = None


class NetworkOperator(ComponentEntry):
    chart_name: ClassVar[str] = "network-operator"
    namespace: ClassVar[str] = "network-operator"
    wave: ClassVar[int] = 40
    values: Optional[NetworkOperatorOverrides] = None


class OpentelemetryCollector(ComponentEntry):
    chart_name: ClassVar[str] = "opentelemetry-collector"
    namespace: ClassVar[str] = "opentelemetry"
    wave: ClassVar[int] = 40
    values: Optional[OpentelemetryCollectorOverrides] = None


class GpuOperator(ComponentEntry):
    chart_name: ClassVar[str] = "gpu-operator"
    namespace: ClassVar[str] = "gpu-operator"
    wave: ClassVar[int] = 45
    values: Optional[GpuOperatorOverrides] = None


class Longhorn(ComponentEntry):
    chart_name: ClassVar[str] = "longhorn"
    namespace: ClassVar[str] = "longhorn-system"
    wave: ClassVar[int] = 46
    values: Optional[LonghornOverrides] = None


class MetricsServer(ComponentEntry):
    chart_name: ClassVar[str] = "metrics-server"
    namespace: ClassVar[str] = "kube-system"
    wave: ClassVar[int] = 47
    values: Optional[MetricsServerOverrides] = None


class TeleportKubeAgent(ComponentEntry):
    chart_name: ClassVar[str] = "teleport-kube-agent"
    namespace: ClassVar[str] = "teleport"
    wave: ClassVar[int] = 50
    values: Optional[TeleportKubeAgentOverrides] = None


class FinalStack(ComponentEntry):
    chart_name: ClassVar[str] = "final-stack"
    namespace: ClassVar[str] = "final-stack"
    wave: ClassVar[int] = 20
    values: Optional[FinalStackOverrides] = None


class ClusterComponents(BaseModel):
    """Per-component ApplicationSet entries — the caller-facing surface.

    This is the request-body schema for cluster updates: callers may flip
    `enabled` and supply per-component `values`, but nothing here is
    cluster identity. Cluster identity lives in `Shared` and is passed as
    serialization context when building ApplicationSet elements.

    Field declaration order is for code locality only — actual bring-up
    order is driven by each subclass's `wave` ClassVar (rendered into the
    child Application's argocd.argoproj.io/sync-wave annotation).
    """

    kube_prometheus_stack: KubePrometheusStack = Field(default_factory=KubePrometheusStack)
    istio: Istio = Field(default_factory=Istio)
    cert_manager: CertManager = Field(default_factory=CertManager)
    ingress_nginx: IngressNginx = Field(default_factory=IngressNginx)
    spire_crds: SpireCrds = Field(default_factory=SpireCrds)
    spire: Spire = Field(default_factory=Spire)
    external_dns: ExternalDns = Field(default_factory=ExternalDns)
    prometheus_adapter: PrometheusAdapter = Field(default_factory=PrometheusAdapter)
    keda: Keda = Field(default_factory=Keda)
    fluent_bit: FluentBit = Field(default_factory=FluentBit)
    argo_rollouts: ArgoRollouts = Field(default_factory=ArgoRollouts)
    network_operator: NetworkOperator = Field(default_factory=NetworkOperator)
    opentelemetry_collector: OpentelemetryCollector = Field(default_factory=OpentelemetryCollector)
    gpu_operator: GpuOperator = Field(default_factory=GpuOperator)
    longhorn: Longhorn = Field(default_factory=Longhorn)
    metrics_server: MetricsServer = Field(default_factory=MetricsServer)
    teleport_kube_agent: TeleportKubeAgent = Field(default_factory=TeleportKubeAgent)
    final_stack: FinalStack = Field(default_factory=FinalStack)
