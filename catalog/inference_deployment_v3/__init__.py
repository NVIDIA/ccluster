# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
from enum import StrEnum
from pathlib import PurePosixPath
from typing import Annotated, Dict, Optional, Self

from pydantic import (
    BaseModel,
    Field,
    StringConstraints,
    field_serializer,
    field_validator,
    model_validator,
)

from catalog.components import BaseAppConfig, DeploymentBaseHelmValues

# K8s IsConfigMapKey caps a data key at DNS1123SubdomainMaxLength = 253.
CONFIGMAP_KEY_MAX_LENGTH = 253

# Linux PATH_MAX. K8s itself doesn't bound mountPath, but this catches absurd inputs early.
LINUX_PATH_MAX = 4096

# Kernel pseudo-filesystems / device nodes; shadowing them with a file mount is incoherent.
CONFIG_FILES_FORBIDDEN_MOUNT_ROOTS: tuple[str, ...] = ("/proc", "/sys", "/dev")

# UTF-8 bytes. content_base64 travels inline in the ArgoCD Application spec
# (spec.source.helm.values) and lands in a ConfigMap: K8s caps a ConfigMap object
# at 1 MiB and the Application manifest must clear the same etcd/api-server order
# of limits; 64 KiB raw (~86 KiB base64) keeps both far below their caps.
CONFIG_FILE_CONTENT_MAX_BYTES = 64 * 1024

# Ports the Istio sidecar / HPA monitoring already bind on every v3 pod.
# User-provided app and metrics ports cannot collide with these.
ISTIO_RESERVED_PORTS = frozenset({15000, 15001, 15004, 15006, 15020, 15021, 15090})


def reject_reserved_port(v: int) -> int:
    """Shared guard: reject any port that collides with the Istio sidecar / HPA
    monitoring ports."""
    if v in ISTIO_RESERVED_PORTS:
        raise ValueError(f"{v} is a reserved port")
    return v


# Note: HPA metrics, KEDA poll interval, and Prometheus server URL are
# hardcoded in the Helm template (scaled-object.yaml).
# KEDA cooldown is user-configurable via HPA.cooldown_period (default 1800s / 30 min).
# 1→N scaling uses envoy_http_downstream_rq_active (in-flight requests per pod).
# 0→1 scaling uses nginx_ingress_controller_requests rate (since no envoy sidecar exists at zero replicas).
#
# PREREQUISITE for scale-to-zero (min_replicas=0):
#   The cluster's Prometheus must be scraping nginx ingress controller metrics
#   (nginx_ingress_controller_requests). Without this, the 0→1 trigger query
#   falls back to `or on() vector(0)`, which never exceeds the activation
#   threshold, leaving the deployment stuck at zero replicas indefinitely.


class BackendProtocol(StrEnum):
    HTTP = "HTTP"
    GRPC = "GRPC"


class ImagePullSecret(BaseModel):
    server: str
    username: str
    password: str

    # Base64-encoded on the wire; the image-pull Secret template b64decodes them.
    # Retained for compatibility, including deployments pinned to older chart revisions.
    username_base64: str = ""
    password_base64: str = ""

    @staticmethod
    def base64_encode(value: str) -> str:
        return base64.b64encode(value.encode("utf-8")).decode("utf-8")

    @model_validator(mode="after")
    def _encode_credentials(self) -> Self:
        """Populate the base64 shadow fields for username and password.

        Deployments pinned to old chart revisions render credentials from these
        fields (their templates b64dec them), so both stay populated for
        compatibility until pinned revisions are retired (#3998).
        """
        self.username_base64 = self.base64_encode(self.username)
        self.password_base64 = self.base64_encode(self.password)
        return self


class HPA(BaseModel):
    # Min/max replicasare set to exactly on replica by default.
    min_replicas: int = 1
    max_replicas: int = 1

    # Average value is the target average value for scaling.
    #
    # Currently we don't use 'prune' with ArgoCD, so if you enable
    # autoscaling then you're stuck with it. It's easier for every
    # deployment to have an HPA, even if it's essentially disabled.
    # Then everything is configured the same way, with no special cases
    # in the chart. Using a huge value essentially disables autoscaling.
    #
    # We use 2^53 (max float64 safe integer) instead of 2^63-1 because Helm
    # decodes the values through float64. Values above 2^53 lose precision, and
    # 2^63-1 rounds to 2^63 which overflows int64 in Go.
    average_value: int = 2**53

    # KEDA ScaledObject cooldown in seconds. Controls how long KEDA waits
    # after the last trigger activation before scaling down to minReplicaCount.
    # Sets both initialCooldownPeriod and cooldownPeriod in the ScaledObject.
    cooldown_period: int = Field(default=1800, ge=0)

    @field_validator("min_replicas")
    @classmethod
    def min_replicas_must_be_positive(cls, v: int) -> int:
        # Setting min_replicas=0 enables scale-to-zero via KEDA.
        # See PREREQUISITE note at the top of this file: the target cluster
        # must have nginx ingress controller metrics scraped into Prometheus,
        # otherwise the deployment will never wake up from zero.
        if v < 0:
            raise ValueError("min_replicas must be >= 0")
        return v

    @model_validator(mode="after")
    def max_replicas_must_be_gte_min(self) -> Self:
        if self.max_replicas < self.min_replicas:
            raise ValueError(f"max_replicas ({self.max_replicas}) must be >= min_replicas ({self.min_replicas})")
        return self


class RolloutStrategy(BaseModel):
    # MaxUnavailable is the maximum number of pods that can be unavailable during
    # the update.
    #
    # See: https://github.com/argoproj/argo-rollouts/blob/master/pkg/apis/rollouts/v1alpha1/types.go
    max_unavailable: int = Field(default=1, ge=0)

    # MaxSurge is the maximum number of pods that can be scheduled above the
    # original number of pods.
    #
    # See: https://github.com/argoproj/argo-rollouts/blob/master/pkg/apis/rollouts/v1alpha1/types.go
    max_surge: int = Field(default=0, ge=0)

    @model_validator(mode="after")
    def validate_surge_unavailable_sum(self) -> Self:
        if self.max_surge + self.max_unavailable < 1:
            raise ValueError(
                f"Max Surge ({self.max_surge}) and Max Unavailable ({self.max_unavailable}) must add up to at least 1"
            )
        return self


class ConfigFileMount(BaseModel):
    filename: Annotated[
        str,
        StringConstraints(
            min_length=1,
            max_length=CONFIGMAP_KEY_MAX_LENGTH,
            pattern=r"^[a-zA-Z0-9._-]+$",
        ),
    ]
    mount_path: Annotated[str, StringConstraints(min_length=1)]
    content: Annotated[str, StringConstraints(min_length=1)]

    @field_validator("filename")
    @classmethod
    def validate_filename(cls, v: str) -> str:
        # Pydantic's Rust regex engine lacks lookahead, so this rule lives here.
        if v in (".", "..") or v.startswith(".."):
            raise ValueError("filename must not be '.', '..', or start with '..'")
        return v

    @field_validator("mount_path")
    @classmethod
    def validate_mount_path(cls, v: str) -> str:
        if not v.startswith("/"):
            raise ValueError("mount_path must be an absolute path (start with '/')")
        # Linux PATH_MAX is a byte limit; multibyte chars (e.g. CJK) inflate well past it.
        n = len(v.encode("utf-8"))
        if n > LINUX_PATH_MAX:
            raise ValueError(f"mount_path must be at most {LINUX_PATH_MAX} UTF-8 bytes (got {n})")
        parts = PurePosixPath(v).parts
        # `..` segments would let "/etc/../proc/foo" slip past forbidden-roots
        # string matching and resolve to /proc/foo at mount time.
        if ".." in parts:
            raise ValueError("mount_path must not contain '..' segments")
        normalized = str(PurePosixPath(v))
        if normalized == "/":
            raise ValueError("mount_path must not be the root '/'")
        # Component-aligned so "/devtmp" is not treated as under "/dev".
        for root in CONFIG_FILES_FORBIDDEN_MOUNT_ROOTS:
            if normalized == root or normalized.startswith(root + "/"):
                raise ValueError(f"mount_path must not be under {root}")
        return normalized

    @field_validator("content")
    @classmethod
    def validate_content_size(cls, v: str) -> str:
        # Pydantic max_length counts Unicode characters; the ConfigMap size bound is on bytes.
        n = len(v.encode("utf-8"))
        if n > CONFIG_FILE_CONTENT_MAX_BYTES:
            raise ValueError(f"content must be at most {CONFIG_FILE_CONTENT_MAX_BYTES} UTF-8 bytes (got {n})")
        return v

    @model_validator(mode="after")
    def _combined_path_within_path_max(self) -> Self:
        # The chart concatenates mount_path + "/" + filename for the volumeMount target;
        # individually they're each bounded but together they can exceed PATH_MAX.
        # PATH_MAX is a byte limit (filename is ASCII-only by pattern, so its byte len
        # equals char len; mount_path can carry multibyte chars).
        combined_len = len(self.mount_path.encode("utf-8")) + 1 + len(self.filename)
        if combined_len > LINUX_PATH_MAX:
            raise ValueError(
                f"mount_path + '/' + filename must be at most {LINUX_PATH_MAX} UTF-8 bytes (got {combined_len})"
            )
        return self


class ConfigFileMountValues(ConfigFileMount):
    # The chart mounts this as ConfigMap binaryData, which k8s decodes verbatim into the
    # file, so the base64 payload is load-bearing (it preserves the content byte-for-byte).
    # helm_values_yaml() emits it on the wire; the API response schema is the parent
    # ConfigFileMount, so FastAPI strips this field on serialization.
    content_base64: str = ""

    @model_validator(mode="after")
    def _encode_content(self) -> Self:
        self.content_base64 = base64.b64encode(self.content.encode("utf-8")).decode("ascii")
        return self

    @field_serializer("content")
    def _serialize_content(self, value: str, info) -> str:
        # Drop the raw payload from the helm wire only. The chart consumes only
        # content_base64, so an empty wire value is harmless. by_alias=True is set by
        # HelmValues.helm_values_yaml(); DB persistence and API responses use
        # by_alias=False and see the original content.
        return "" if info.by_alias else value


class MetricsConfig(BaseModel):
    """User-application Prometheus metrics endpoint.

    Atomic: both `port` and `path` must be supplied together. To omit the
    endpoint entirely, leave the parent request's `metrics` field unset — it
    persists as null and the Helm chart applies the defaults (`container_port`
    + `/metrics`) at render time. The API never synthesizes these values.
    """

    port: int = Field(ge=1, le=65535)
    path: Annotated[str, StringConstraints(pattern=r"^/[^\s?#]*$")]

    @field_validator("port")
    @classmethod
    def validate_port_not_reserved(cls, v: int) -> int:
        return reject_reserved_port(v)


# InferenceDeploymentValues is a Pydantic model that represents the values
# for an inference deployment. The HelmValues base allows the model to be converted
# to a dictionary that can be used as values in a Helm chart in ArgoCD.
class InferenceDeploymentValues(DeploymentBaseHelmValues):
    user_id: int
    deployment_id: int
    name: str
    image_url: str
    domain: str
    deployment_url: Optional[str] = None
    container_port: int = Field(ge=1, le=65535)
    endpoint_certificate_authority: Optional[str] = None
    endpoint_bearer_token: Optional[str] = None

    image_pull_secret: Optional[ImagePullSecret] = None

    # HorizontalPodAutoscaler config
    hpa: HPA

    healthcheck: Optional[str] = "/"
    command: Optional[list[str]] = None
    command_args: Optional[list[str]] = None
    original_command: Optional[str] = None
    node_affinity_labels: Dict[str, str]
    env_vars: Dict[str, str] = {}
    # Optional Hugging Face token rendered into the container as HF_TOKEN env var.
    # The chart renders it after `env_vars`, so K8s last-wins makes this dedicated
    # value the authoritative source whenever it's set.
    hf_token: Optional[str] = None
    enable_node_model_cache: bool = False

    # For now we just use the defaults, but in the future
    # we can make this more configurable.
    rollout_strategy: RolloutStrategy = RolloutStrategy()

    revision_number: int

    # to pause or resume the rollout
    rollout_paused: bool = False

    # Backend protocol for ingress routing (HTTP or GRPC)
    backend_protocol: BackendProtocol = BackendProtocol.HTTP

    # Default True so old deployments (without this field in app_config) retain logging on resume
    enable_logging: bool = True

    # When True, an Istio DestinationRule routes requests with the same
    # `X-Session-Id` header to the same pod via consistent-hash LB.
    # Requests without the header fall back to random selection.
    #
    # Best-effort affinity for cache locality (e.g. KV cache reuse), NOT hard
    # stickiness. The pod a session lands on can change when the endpoint set
    # changes: autoscaling, rollouts, pod restarts, or readiness transitions
    # all remap sessions. Do not rely on this for irreplaceable in-pod state.
    session_affinity: bool = False

    config_file: Optional[ConfigFileMountValues] = None

    # Inherited by CServeV3InferenceDeploymentValues, but cserve's request
    # schema does not expose the field and its response schema does not
    # declare it — so cserve persists null here and never surfaces it.
    metrics: Optional[MetricsConfig] = None

    @field_validator("container_port")
    @classmethod
    def validate_container_port_not_reserved(cls, v: int) -> int:
        return reject_reserved_port(v)


class InferenceDeploymentConfig(BaseAppConfig):
    values: InferenceDeploymentValues
    app_path: str = "catalog/src/catalog/inference_deployment_v3"
