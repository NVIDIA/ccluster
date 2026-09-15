"""Catalog wiring for NVIDIA Dynamo deployments.

Aggregated mode uses one KEDA-scaled worker pool; disaggregated mode uses fixed
prefill and decode pools. Both use the KV router and vLLM backend.
"""

import json
import shlex
from enum import StrEnum
from typing import Any, Dict, Literal, Optional

from pydantic import ConfigDict, Field, computed_field, field_validator, model_validator

from catalog.components import (
    BaseAppConfig,
    DeploymentSharedHelmValues,
    DevShm,
    ResourceRequirements,
)
from catalog.inference_deployment_v3 import (
    HPA,
    BackendProtocol,
    ImagePullSecret,
    RolloutStrategy,
)

# Dynamo's `--grpc-metrics-port` default: with `--kserve-grpc-server` enabled the
# Frontend starts a second HTTP metrics/health listener on this port, so the
# inference listener must not bind it. Keep in sync with the hardcoded
# `--grpc-metrics-port` argument in `templates/dgd.yaml` (the chart test pins
# the rendered value to this constant).
KSERVE_GRPC_METRICS_PORT = 8788
_YAML_LINE_BREAK_TRANSLATION = str.maketrans({separator: " " for separator in "\n\r\u0085\u2028\u2029"})
_PLATFORM_VLLM_RUNNER_NORMALIZED_KEY = "platform_vllm_runner_normalized"
_VLLM_RUNNER_FLAG = "--runner"
_VLLM_RUNNER_MIN_PREFIX = "--ru"


def _is_vllm_runner_option(token: str) -> bool:
    """Match runner spellings accepted by both current and target vLLM parsers."""
    option_head = token.partition("=")[0].replace("_", "-")
    return (
        option_head.startswith("--")
        and len(option_head) >= len(_VLLM_RUNNER_MIN_PREFIX)
        and _VLLM_RUNNER_FLAG.startswith(option_head)
    )


def _normalize_platform_vllm_runner(extra_argv: list[str]) -> list[str]:
    """Render the Platform-owned runner last while preserving positional arguments."""
    try:
        separator_index = extra_argv.index("--")
    except ValueError:
        option_argv = extra_argv
        positional_argv: list[str] = []
    else:
        option_argv = extra_argv[:separator_index]
        positional_argv = extra_argv[separator_index:]

    normalized: list[str] = []
    index = 0
    while index < len(option_argv):
        token = option_argv[index]
        if not _is_vllm_runner_option(token):
            normalized.append(token)
            index += 1
            continue

        # Remove the ignored legacy override. argparse runner values never start
        # with '-', so leave a following option untouched when the value is missing.
        has_inline_value = "=" in token
        index += 1
        if not has_inline_value and index < len(option_argv) and not option_argv[index].startswith("-"):
            index += 1

    normalized.extend((_VLLM_RUNNER_FLAG, "generate"))
    return [*normalized, *positional_argv]


# Worker topology rendered into the DynamoGraphDeployment.
class DynamoServingMode(StrEnum):
    AGGREGATED = "aggregated"
    DISAGGREGATED = "disaggregated"


class DynamoWorkerRole(StrEnum):
    WORKER = "worker"
    PREFILL = "prefill"
    DECODE = "decode"


class DynamoWorkerPoolValues(DevShm, ResourceRequirements):
    """Resources and placement owned by one Dynamo worker pool."""

    node_affinity_labels: dict[str, str] = Field(default_factory=dict)


class DynamoStaticWorkerPoolValues(DynamoWorkerPoolValues):
    """Fixed replica topology for one disaggregated Prefill or Decode pool."""

    replicas: int = Field(default=1, ge=1)
    tensor_parallel_size: int = Field(ge=1)

    @model_validator(mode="after")
    def validate_tensor_parallel_size(self) -> "DynamoStaticWorkerPoolValues":
        """Each static worker must use every GPU reserved for its pod."""
        accelerator_counts = (
            self.resources.requests.accelerator_count,
            self.resources.limits.accelerator_count,
        )
        if any(count != self.tensor_parallel_size for count in accelerator_counts):
            raise ValueError("tensor_parallel_size must match the worker pool's requested and limited accelerators")
        return self


class DynamoDeploymentValues(DeploymentSharedHelmValues):
    model_config = ConfigDict(extra="forbid")
    user_id: int
    deployment_id: int
    name: str

    # NGC Dynamo runtime image, e.g.
    # `nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.0`
    image_url: str
    domain: str
    deployment_url: Optional[str] = None

    # Internal port the Dynamo Frontend process listens on. The operator-owned
    # Service always exposes port 8000 and forwards it to the container's named
    # `http` port, which the chart overrides to this value.
    container_port: int = Field(default=8000, ge=1024, le=65535)

    # Rendered into the chart's `--grpc-metrics-port` argument (kserve mode).
    # Not exposed through the API; exists so the Python constant is the single
    # source of truth for the chart template.
    kserve_grpc_metrics_port: int = KSERVE_GRPC_METRICS_PORT

    # Pin the KV event plane to NATS so the platform does not inherit upstream
    # default changes while moving between Dynamo releases. Per
    # https://docs.nvidia.com/dynamo/latest/design-docs/communication-planes/request-plane
    # the event and request planes are independent.
    event_plane: Literal["nats"] = "nats"

    # Request plane backend for Frontend ↔ Worker traffic.
    #
    # NATS for the *request* plane is not viable here: `NatsRequestClient`
    # (`lib/runtime/src/pipeline/network/egress/nats_client.rs`) implements
    # unary NATS request-reply only, returning a single `response.payload`,
    # whereas inference responses are streamed token-by-token. Worker
    # responses therefore always go back over raw TCP via
    # `tcp::client::TcpClient::create_response_stream` regardless of the
    # configured request plane (see the hard-coded `// for now, we only
    # support tcp here` in `push_handler.rs`).
    #
    # Because that response stream uses an OS-assigned ephemeral port that
    # can't be pre-declared as a `containerPort`, Istio's auto-mTLS cannot
    # enroll it under STRICT `PeerAuthentication`. The Dynamo pods are
    # therefore opted out of sidecar injection (see `podTemplate.metadata` in
    # `templates/dgd.yaml`) and the chart's NetworkPolicy explicitly
    # allows the intra-namespace Frontend ↔ Worker TCP traffic that the
    # base `namespace-default` policy would otherwise block.
    request_plane: str = "tcp"

    # HuggingFace model identifier passed to `python3 -m dynamo.vllm --model ...`,
    # e.g. `Qwen/Qwen3-0.6B`.
    model: str

    # Optional override for the served-model name returned by the OpenAI API.
    served_model_name: Optional[str] = None

    # Worker autoscaling config (min/max replicas, per-worker concurrency target,
    # cooldown). Reuses the inference_v3 HPA model; the DGDSA/KEDA holds the worker
    # count at hpa.min_replicas..hpa.max_replicas (fixed when min == max).
    # See templates/scaled-object.yaml.
    hpa: HPA = HPA()

    # Aggregated uses one HPA-scaled worker_pool; disaggregated requires fixed
    # prefill_pool and decode_pool values and has no scalingAdapter.
    serving_mode: DynamoServingMode = DynamoServingMode.AGGREGATED
    worker_pool: DynamoWorkerPoolValues | None = None
    prefill_pool: DynamoStaticWorkerPoolValues | None = None
    decode_pool: DynamoStaticWorkerPoolValues | None = None

    # Literal CLI arguments appended to `python3 -m dynamo.vllm`. The public API
    # accepts a shell-style string for text-area ergonomics and converts it to
    # this argv representation before constructing Helm values. This is the sole
    # input representation of the worker arguments (extra="forbid" rejects any
    # attempt to set the derived `extra_args` string directly).
    extra_argv: list[str] = Field(default_factory=list)

    @computed_field  # type: ignore[prop-decorator]
    @property
    def extra_args(self) -> str:
        """Shell-quoted rejoin of `extra_argv`, emitted only for pre-1.0.3 chart pins.

        The 1.0.3+ chart consumes `extra_argv` as individually quoted list items and
        ignores this field. Older pinned revisions render `{{ .Values.extra_args }}`
        into a `/bin/sh -c` command, so a resume against them must still carry the
        worker flags or they are silently dropped. Deriving the value with
        `shlex.join` (never the raw request string) keeps it a single shell-safe line:
        `extra_argv` tokens cannot contain the newlines/quoting that let the original
        raw field escape the YAML document (CCL-306), so old pins render both correctly
        and safely.
        The Helm serializer replaces this compatibility value with the normalized
        effective argv, including Platform-owned options, before rendering.
        """
        return shlex.join(self.extra_argv)

    # HF token. The chart renders a `{name}-hf-token` Secret (secret.hf.yaml) and both
    # the Frontend and worker consume it as an HF_TOKEN env var via `envFrom`.
    hf_token: Optional[str] = None

    env_vars: Dict[str, str] = {}

    revision_number: int

    enable_logging: bool = True

    # Frontend router mode is selected via the DYN_ROUTER_MODE env var (Dynamo
    # supports round-robin, random, kv, least-loaded, power-of-two-choices).
    # The chart defaults it to `kv` (the aggregated + KV-router pattern) and
    # honours a caller-supplied env_vars["DYN_ROUTER_MODE"] override, emitting
    # the var exactly once so there is no glibc getenv first-match ambiguity.
    # See https://docs.nvidia.com/dynamo/latest/architecture/kv_cache_routing

    # Request-plane protocol for the OpenAI-compatible Frontend, mirroring
    # inference_deployment_v3's `backend_protocol`. `GRPC` starts Dynamo's
    # KServe gRPC Frontend server (`python3 -m dynamo.frontend
    # --kserve-grpc-server`); `HTTP` (the default) keeps the OpenAI HTTP
    # frontend. This is separate from worker `extra_argv`, which is appended
    # to `python3 -m dynamo.vllm`.
    backend_protocol: BackendProtocol = BackendProtocol.HTTP

    # Operator-managed rolling-update surge/unavailable, rendered as the
    # `nvidia.com/deployment-rolling-update-*` annotations on the DGD. Reuses
    # inference_deployment_v3's `RolloutStrategy` model so the API contract and
    # bounds match the v3 types (the Dynamo operator owns the roll itself; only
    # the surge/unavailable knobs are exposed — see dynamo-rollouts-design.md).
    rollout_strategy: RolloutStrategy = RolloutStrategy()

    # When true, mount the namespace's pre-provisioned `node-model-cache-pvc`
    # (created by the `namespace_base` chart, hostPath `/tmp` on the node) at
    # `/tmp` inside every worker, and set `HF_HUB_CACHE=/tmp` so the Dynamo
    # ModelExpress downloader stores model files there.
    #
    # Why this matters: pulling a 100GB+ model fans out into hundreds of
    # parallel resolver requests via `hf-hub`'s `.high()` mode. A single full
    # pull can exhaust the per-token Hugging Face resolver bucket (5,000 req
    # per 5-minute window on a Free token; see
    # https://huggingface.co/docs/hub/rate-limits#rate-limit-tiers), which
    # then 429s any sibling worker that started a fresh download in the same
    # window. With this flag set, only the first worker on a given node
    # downloads the weights; subsequent workers (and replicas added by a
    # later scale-up) hot-load from the shared host directory and never hit
    # Hugging Face. Mirrors `inference_deployment_v3`'s
    # `enable_node_model_cache` behaviour.
    #
    # The PVC is `ReadWriteOnce`, but RWO is per-node — multiple pods on the
    # same node can mount it concurrently, which is the only configuration
    # we currently produce for Dynamo (each worker pool lands on the
    # `dedicated_compute` GPU nodes selected by its node affinity labels).
    enable_node_model_cache: bool = False

    image_pull_secret: Optional[ImagePullSecret] = None
    endpoint_certificate_authority: Optional[str] = None
    endpoint_bearer_token: Optional[str] = None

    @field_validator("model")
    @classmethod
    def model_must_be_non_empty(cls, v: str) -> str:
        if not v.strip():
            raise ValueError("model must be a non-empty HuggingFace model identifier")
        return v

    @model_validator(mode="before")
    @classmethod
    def lift_legacy_aggregated_worker_pool(cls, raw_values: Any) -> Any:
        """Lift persisted root-level worker fields into worker_pool."""
        if not isinstance(raw_values, dict):
            return raw_values
        if raw_values.get("serving_mode", DynamoServingMode.AGGREGATED) != DynamoServingMode.AGGREGATED:
            return raw_values
        if raw_values.get("worker_pool") is not None or "resources" not in raw_values:
            return raw_values

        normalized = dict(raw_values)
        worker_pool = {
            "resources": normalized.pop("resources"),
            "node_affinity_labels": normalized.pop("node_affinity_labels", {}),
        }
        for field in ("dev_shm_size", "supplemental_group_ids"):
            if field in normalized:
                worker_pool[field] = normalized.pop(field)
        normalized["worker_pool"] = worker_pool
        return normalized

    def helm_values_yaml(self) -> str:
        """Add compatibility values consumed by current and pinned legacy charts."""
        serialized = json.loads(super().helm_values_yaml())
        if self.serving_mode == DynamoServingMode.AGGREGATED and self.worker_pool is not None:
            worker_pool = serialized["worker_pool"]
            for field in (
                "resources",
                "dev_shm_size",
                "node_affinity_labels",
                "supplemental_group_ids",
            ):
                if field in worker_pool:
                    serialized[field] = worker_pool[field]

        effective_extra_argv = _normalize_platform_vllm_runner(self.extra_argv)
        # 1.0.3+ consumes literal argv; older chart pins consume the shell-safe
        # compatibility string. Current templates use the marker to avoid adding
        # the same Platform-owned options twice.
        serialized["extra_argv"] = effective_extra_argv
        serialized["extra_args"] = shlex.join(effective_extra_argv)
        serialized[_PLATFORM_VLLM_RUNNER_NORMALIZED_KEY] = True
        return json.dumps(serialized, separators=(",", ":"))

    @model_validator(mode="after")
    def validate_frontend_ports(self) -> "DynamoDeploymentValues":
        # KServe starts a second HTTP metrics/health listener. Binding the
        # inference server to the same port would make the pod crash.
        if self.backend_protocol == BackendProtocol.GRPC and self.container_port == KSERVE_GRPC_METRICS_PORT:
            raise ValueError(f"container_port {KSERVE_GRPC_METRICS_PORT} conflicts with the KServe gRPC metrics port")
        return self

    @model_validator(mode="after")
    def validate_worker_pools(self) -> "DynamoDeploymentValues":
        if self.serving_mode == DynamoServingMode.AGGREGATED:
            if self.worker_pool is None:
                raise ValueError("aggregated serving requires worker_pool")
            if self.prefill_pool is not None or self.decode_pool is not None:
                raise ValueError("prefill_pool and decode_pool are only supported for disaggregated serving")
            return self

        if self.worker_pool is not None:
            raise ValueError("worker_pool is only supported for aggregated serving")
        pools = (
            ("prefill_pool", self.prefill_pool),
            ("decode_pool", self.decode_pool),
        )
        missing = [name for name, pool in pools if pool is None]
        if missing:
            raise ValueError(f"disaggregated serving requires {', '.join(missing)}")
        return self


class DynamoDeploymentConfig(BaseAppConfig):
    values: DynamoDeploymentValues
    app_path: str = "catalog/src/catalog/dynamo_deployment"

    @model_validator(mode="before")
    @classmethod
    def normalize_stored_extra_args(cls, raw_config: Any) -> Any:
        """Normalize persisted extra_args into literal extra_argv."""
        if not isinstance(raw_config, dict) or not isinstance(raw_config.get("values"), dict):
            return raw_config
        values = raw_config["values"]
        if "extra_args" not in values:
            return raw_config

        normalized_values = dict(values)
        extra_args = normalized_values.pop("extra_args")
        if not isinstance(extra_args, str):
            raise ValueError("stored Dynamo extra_args must be a string")

        if "extra_argv" in normalized_values:
            extra_argv = normalized_values["extra_argv"]
            if not isinstance(extra_argv, list) or not all(isinstance(arg, str) for arg in extra_argv):
                raise ValueError("stored Dynamo extra_argv must be a list of strings")
            if extra_args != shlex.join(extra_argv):
                raise ValueError("stored Dynamo extra_args does not match extra_argv")
        else:
            try:
                normalized_values["extra_argv"] = shlex.split(extra_args.translate(_YAML_LINE_BREAK_TRANSLATION))
            except ValueError as exc:
                raise ValueError("stored Dynamo extra_args must use valid shell-style quoting") from exc

        normalized_config = dict(raw_config)
        normalized_config["values"] = normalized_values
        return normalized_config
