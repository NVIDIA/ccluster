"""Smoke test for the dynamo_deployment Helm chart.

Exercises `helm template` against the canonical `test_values.yaml` and
asserts the invariants the API and operator depend on:

- exactly one v1beta1 DynamoGraphDeployment is rendered with KV-router enabled
  on the Frontend component
- worker `replicas` and accelerator limits flow through unchanged
- the Frontend listener and named pod port stay aligned for HTTP and gRPC
- the platform's standard ingress + ExternalName service are produced
  alongside the DGD so the operator-created Frontend is reachable through
  the existing nginx ingress path

Skipped automatically if `helm` is not on PATH.
"""

import json
import shlex
import shutil
import subprocess
import unittest
from pathlib import Path
from typing import Any, ClassVar

import yaml
from pydantic import ValidationError

from catalog.dynamo_deployment import (
    KSERVE_GRPC_METRICS_PORT,
    DynamoDeploymentConfig,
    DynamoDeploymentValues,
    DynamoServingMode,
)

_CHART_DIR = Path(__file__).resolve().parent
_VALUES_FILE = _CHART_DIR / "test_values.yaml"
COMPONENT_TYPE_LABEL = "ccluster.nvidia.com/component-type"
_COMMON_RUNTIME_ENV = {
    "DYN_EVENT_PLANE": "nats",
    "DYN_REQUEST_PLANE": "tcp",
    "PYTHONHASHSEED": "0",
}
_FRONTEND_RUNTIME_ENV = {
    "DYN_ROUTER_QUEUE_THRESHOLD": "4.0",
    "DYN_DISABLE_FRONTEND_ADMIN_API": "1",
    "DYN_DISABLE_FRONTEND_NVEXT": "1",
    "DYN_VLLM_ENABLE_INFERENCE_V1_GENERATE": "0",
}
_WORKER_RUNTIME_ENV = {"DYN_SELF_HOST_METADATA": "0"}


def _helm_template(extra_set: list[str] | None = None, values: dict | None = None) -> list[dict]:
    values_source = str(_VALUES_FILE) if values is None else "-"
    cmd = ["helm", "template", "test-dynamo", str(_CHART_DIR), "-f", values_source]
    for kv in extra_set or []:
        cmd.extend(["--set", kv])
    out = subprocess.run(
        cmd,
        check=True,
        capture_output=True,
        text=True,
        input=None if values is None else yaml.safe_dump(values),
    )
    return [doc for doc in yaml.safe_load_all(out.stdout) if doc]


def _disaggregated_set(*, prefill_replicas: int = 1, decode_replicas: int = 1) -> list[str]:
    """Complete static xPyD values with intentionally heterogeneous pools."""
    return [
        "serving_mode=disaggregated",
        f"prefill_pool.replicas={prefill_replicas}",
        "prefill_pool.tensor_parallel_size=1",
        "prefill_pool.dev_shm_size=600Mi",
        "prefill_pool.resources.limits.cpu=4",
        "prefill_pool.resources.limits.memory=2000Mi",
        "prefill_pool.resources.limits.accelerator_key=nvidia.com/gpu",
        "prefill_pool.resources.limits.accelerator_count=1",
        "prefill_pool.resources.requests.cpu=3",
        "prefill_pool.resources.requests.memory=1500Mi",
        "prefill_pool.resources.requests.accelerator_key=nvidia.com/gpu",
        "prefill_pool.resources.requests.accelerator_count=1",
        "prefill_pool.node_affinity_labels.pool-role=prefill",
        f"decode_pool.replicas={decode_replicas}",
        "decode_pool.tensor_parallel_size=2",
        "decode_pool.dev_shm_size=1200Mi",
        "decode_pool.resources.limits.cpu=8",
        "decode_pool.resources.limits.memory=4000Mi",
        "decode_pool.resources.limits.accelerator_key=nvidia.com/gpu",
        "decode_pool.resources.limits.accelerator_count=2",
        "decode_pool.resources.requests.cpu=6",
        "decode_pool.resources.requests.memory=3000Mi",
        "decode_pool.resources.requests.accelerator_key=nvidia.com/gpu",
        "decode_pool.resources.requests.accelerator_count=2",
        "decode_pool.node_affinity_labels.pool-role=decode",
    ]


def _dgd(docs: list[dict]) -> dict:
    return next(doc for doc in docs if doc.get("kind") == "DynamoGraphDeployment")


def _components(dgd: dict) -> dict[str, dict]:
    return {component["name"]: component for component in dgd["spec"]["components"]}


def _component(dgd: dict, name: str) -> dict:
    return _components(dgd)[name]


def _pod_spec(component: dict) -> dict:
    return component["podTemplate"]["spec"]


def _main_container(component: dict) -> dict:
    return next(container for container in _pod_spec(component)["containers"] if container["name"] == "main")


def _env(component: dict) -> dict[str, str]:
    return {item["name"]: item["value"] for item in _main_container(component).get("env", [])}


def _env_values(component: dict, name: str) -> list[str]:
    return [item["value"] for item in _main_container(component).get("env", []) if item["name"] == name]


def _assert_runtime_env(
    test_case: unittest.TestCase,
    component: dict,
    expected: dict[str, str],
    *,
    absent: tuple[str, ...] = (),
) -> None:
    for name, value in expected.items():
        test_case.assertEqual(_env_values(component, name), [value], f"{name} must be emitted exactly once")
    for name in absent:
        test_case.assertEqual(_env_values(component, name), [], f"{name} must not be emitted for this role")


def _without_aggregated_worker(values: dict) -> dict:
    result = dict(values)
    result.pop("worker_pool", None)
    return result


def _canonical_values() -> dict:
    return yaml.safe_load(_VALUES_FILE.read_text())


def _static_pool(values: dict, role: str) -> dict:
    resources = values["worker_pool"]["resources"]
    return {
        "replicas": 1,
        "tensor_parallel_size": resources["requests"]["accelerator_count"],
        "resources": resources,
        "node_affinity_labels": {"pool-role": role},
    }


def _aggregated_worker(*, gpu_count: int, extra_argv: list[str] | None = None) -> dict:
    values = _canonical_values()
    resources = values["worker_pool"]["resources"]
    for resource_type in ("requests", "limits"):
        resources[resource_type]["accelerator_count"] = gpu_count
    values["extra_argv"] = extra_argv or []
    return _component(_dgd(_helm_template(values=values)), "VllmDecodeWorker")


def _disaggregated_components(*, extra_argv: list[str] | None = None) -> dict[str, dict]:
    values = _canonical_values()
    prefill_pool = _static_pool(values, "prefill")
    decode_pool = _static_pool(values, "decode")
    values = _without_aggregated_worker(values)
    values.update(
        serving_mode="disaggregated",
        prefill_pool=prefill_pool,
        decode_pool=decode_pool,
        extra_argv=extra_argv or [],
    )
    return _components(_dgd(_helm_template(values=values)))


class DynamoValuesValidationTest(unittest.TestCase):
    def test_event_plane_is_pinned_to_nats(self):
        values = DynamoDeploymentValues.model_validate(_canonical_values())

        self.assertEqual(values.event_plane, "nats")
        with self.assertRaises(ValidationError):
            DynamoDeploymentValues.model_validate({**_canonical_values(), "event_plane": "etcd"})

    def test_aggregated_mode_remains_the_default_with_worker_pool(self):
        values = DynamoDeploymentValues.model_validate(_canonical_values())
        self.assertEqual(values.serving_mode, DynamoServingMode.AGGREGATED)
        self.assertIsNotNone(values.worker_pool)
        self.assertIsNone(values.prefill_pool)
        self.assertIsNone(values.decode_pool)

    def test_canonical_dump_owns_resources_only_under_worker_pool(self):
        values = DynamoDeploymentValues.model_validate(_canonical_values())

        dumped = values.model_dump(mode="json")

        self.assertIn("worker_pool", dumped)
        for legacy_field in ("resources", "dev_shm_size", "node_affinity_labels", "supplemental_group_ids"):
            self.assertNotIn(legacy_field, dumped)

    def test_helm_wire_derives_legacy_aliases_for_pinned_aggregated_charts(self):
        values = DynamoDeploymentValues.model_validate(_canonical_values())

        serialized = json.loads(values.helm_values_yaml())

        worker_pool = serialized["worker_pool"]
        for legacy_field in ("resources", "dev_shm_size", "node_affinity_labels"):
            self.assertEqual(serialized[legacy_field], worker_pool[legacy_field])

    def test_helm_wire_normalizes_aggregated_runner_for_pinned_charts(self):
        extra_argv = [
            "--r",
            "ambiguous-value",
            "--runner-extra",
            "preserved",
            "--ru=pooling",
            "--max-model-len",
            "4096",
            "--",
            "--runner",
            "pooling",
        ]
        values = DynamoDeploymentValues.model_validate({**_canonical_values(), "extra_argv": extra_argv})

        serialized = json.loads(values.helm_values_yaml())

        expected = [
            "--r",
            "ambiguous-value",
            "--runner-extra",
            "preserved",
            "--max-model-len",
            "4096",
            "--runner",
            "generate",
            "--",
            "--runner",
            "pooling",
        ]
        self.assertEqual(values.extra_argv, extra_argv)
        self.assertTrue(serialized["platform_vllm_runner_normalized"])
        self.assertEqual(serialized["extra_argv"], expected)
        # Charts 1.0.5 and 1.0.6 already append TP themselves. Keep the
        # cross-version compatibility wire runner-only to avoid duplicate TP.
        self.assertNotIn("--tensor-parallel-size", serialized["extra_argv"])
        # Pre-1.0.3 pins consume extra_args; 1.0.3+ pins consume extra_argv.
        self.assertEqual(shlex.split(serialized["extra_args"]), expected)

        replayed_values = DynamoDeploymentValues.model_validate({**_canonical_values(), "extra_argv": expected})
        replayed = json.loads(replayed_values.helm_values_yaml())
        self.assertEqual(replayed["extra_argv"], expected)

    def test_disaggregated_helm_wire_does_not_emit_legacy_aliases(self):
        canonical = _canonical_values()
        values = DynamoDeploymentValues.model_validate(
            {
                **_without_aggregated_worker(canonical),
                "serving_mode": "disaggregated",
                "prefill_pool": _static_pool(canonical, "prefill"),
                "decode_pool": _static_pool(canonical, "decode"),
            }
        )

        serialized = json.loads(values.helm_values_yaml())

        for legacy_field in ("resources", "dev_shm_size", "node_affinity_labels", "supplemental_group_ids"):
            self.assertNotIn(legacy_field, serialized)

    def test_disaggregated_helm_wire_normalizes_runner_without_aggregated_tp(self):
        canonical = _canonical_values()
        values = DynamoDeploymentValues.model_validate(
            {
                **_without_aggregated_worker(canonical),
                "serving_mode": "disaggregated",
                "prefill_pool": _static_pool(canonical, "prefill"),
                "decode_pool": _static_pool(canonical, "decode"),
                "extra_argv": ["--runner", "pooling", "--max-model-len", "4096"],
            }
        )

        serialized = json.loads(values.helm_values_yaml())

        expected = ["--max-model-len", "4096", "--runner", "generate"]
        self.assertEqual(serialized["extra_argv"], expected)
        self.assertEqual(shlex.split(serialized["extra_args"]), expected)
        self.assertNotIn("--tensor-parallel-size", serialized["extra_argv"])

    def test_legacy_top_level_aggregated_values_lift_without_mutating_input(self):
        canonical = _canonical_values()
        worker_pool = canonical.pop("worker_pool")
        legacy = {**canonical, **worker_pool}
        original = json.loads(json.dumps(legacy))
        expected = DynamoDeploymentValues.model_validate({**canonical, "worker_pool": worker_pool})

        values = DynamoDeploymentValues.model_validate(legacy)

        self.assertEqual(legacy, original)
        self.assertIsNotNone(values.worker_pool)
        self.assertIsNotNone(expected.worker_pool)
        self.assertEqual(values.worker_pool.resources, expected.worker_pool.resources)
        self.assertEqual(values.worker_pool.node_affinity_labels, worker_pool["node_affinity_labels"])
        for legacy_field in ("resources", "dev_shm_size", "node_affinity_labels", "supplemental_group_ids"):
            self.assertNotIn(legacy_field, values.model_dump(mode="json"))

    def test_serialized_config_replays_computed_extra_args(self):
        values = DynamoDeploymentValues.model_validate(
            {**_canonical_values(), "extra_argv": ["--tokenizer", "model with spaces"]}
        )
        stored = DynamoDeploymentConfig(values=values).model_dump(mode="json")

        replayed = DynamoDeploymentConfig.model_validate(stored)

        self.assertEqual(replayed.values.extra_argv, ["--tokenizer", "model with spaces"])

    def test_legacy_extra_args_only_config_replays_as_literal_argv(self):
        for separator in ("\n", "\r", "\u0085", "\u2028", "\u2029"):
            with self.subTest(separator=repr(separator)):
                stored_values = _canonical_values()
                stored_values.pop("extra_argv")
                stored_values["extra_args"] = f"--tokenizer{separator}'model with spaces'"

                replayed = DynamoDeploymentConfig.model_validate({"values": stored_values})

                self.assertEqual(replayed.values.extra_argv, ["--tokenizer", "model with spaces"])

    def test_serialized_config_rejects_mismatched_extra_arg_representations(self):
        stored_values = {
            **_canonical_values(),
            "extra_argv": ["--tokenizer", "expected"],
            "extra_args": "--tokenizer different",
        }

        with self.assertRaisesRegex(ValidationError, "does not match extra_argv"):
            DynamoDeploymentConfig.model_validate({"values": stored_values})

    def test_disaggregated_mode_requires_both_worker_pools(self):
        values = _without_aggregated_worker(_canonical_values())
        with self.assertRaisesRegex(ValidationError, "prefill_pool, decode_pool"):
            DynamoDeploymentValues.model_validate({**values, "serving_mode": "disaggregated"})

    def test_aggregated_mode_rejects_disaggregated_pools(self):
        canonical = _canonical_values()

        with self.assertRaisesRegex(ValidationError, "only supported for disaggregated"):
            DynamoDeploymentValues.model_validate(
                {
                    **canonical,
                    "prefill_pool": _static_pool(canonical, "prefill"),
                    "decode_pool": _static_pool(canonical, "decode"),
                }
            )

    def test_disaggregated_mode_rejects_aggregated_worker_pool(self):
        canonical = _canonical_values()

        with self.assertRaisesRegex(ValidationError, "worker_pool is only supported for aggregated"):
            DynamoDeploymentValues.model_validate(
                {
                    **canonical,
                    "serving_mode": "disaggregated",
                    "prefill_pool": _static_pool(canonical, "prefill"),
                    "decode_pool": _static_pool(canonical, "decode"),
                }
            )

    def test_disaggregated_worker_pool_replicas_must_be_positive(self):
        canonical = _canonical_values()
        values = _without_aggregated_worker(canonical)
        pool = {
            "replicas": 1,
            "tensor_parallel_size": 1,
            "resources": canonical["worker_pool"]["resources"],
            "node_affinity_labels": {"pool-role": "prefill"},
        }
        with self.assertRaises(ValidationError):
            DynamoDeploymentValues.model_validate(
                {
                    **values,
                    "serving_mode": "disaggregated",
                    "prefill_pool": {**pool, "replicas": 0},
                    "decode_pool": {**pool, "node_affinity_labels": {"pool-role": "decode"}},
                }
            )

    def test_disaggregated_worker_pool_tensor_parallel_size_must_match_resources(self):
        canonical = _canonical_values()
        values = _without_aggregated_worker(canonical)
        pool = {
            "replicas": 1,
            "tensor_parallel_size": 2,
            "resources": canonical["worker_pool"]["resources"],
            "node_affinity_labels": {"pool-role": "prefill"},
        }
        with self.assertRaisesRegex(ValidationError, "tensor_parallel_size must match"):
            DynamoDeploymentValues.model_validate(
                {
                    **values,
                    "serving_mode": "disaggregated",
                    "prefill_pool": pool,
                    "decode_pool": {**pool, "node_affinity_labels": {"pool-role": "decode"}},
                }
            )

    def test_rejects_unsupported_frontend_ports(self):
        values = _canonical_values()
        invalid_overrides = [
            {"container_port": 1023},
            {"container_port": KSERVE_GRPC_METRICS_PORT, "backend_protocol": "GRPC"},
        ]

        for overrides in invalid_overrides:
            with self.subTest(overrides=overrides), self.assertRaises(ValidationError):
                DynamoDeploymentValues.model_validate({**values, **overrides})

    def test_worker_arguments_have_one_literal_values_representation(self):
        values = yaml.safe_load(_VALUES_FILE.read_text())
        rendered = DynamoDeploymentValues.model_validate(
            {
                **values,
                "model": "Qwen/model; $(id)",
                "served_model_name": "served; $(id)",
                "extra_argv": ["--label", "two words", "$(id)"],
            }
        )

        self.assertEqual(rendered.model, "Qwen/model; $(id)")
        self.assertEqual(rendered.served_model_name, "served; $(id)")
        self.assertEqual(rendered.extra_argv, ["--label", "two words", "$(id)"])
        # The retired worker_* fields stay gone; extra_args is a derived, shell-quoted
        # rejoin of extra_argv emitted only so pre-1.0.3 chart pins keep the flags.
        self.assertTrue({"worker_model_arg", "worker_served_model_name_arg"}.isdisjoint(rendered.model_dump()))
        self.assertEqual(rendered.model_dump()["extra_args"], "--label 'two words' '$(id)'")

    def test_rejects_retired_worker_argument_fields(self):
        values = yaml.safe_load(_VALUES_FILE.read_text())
        for field, value in (
            ("worker_model_arg", "Qwen/Qwen3-0.6B"),
            ("worker_served_model_name_arg", "served"),
            ("extra_args", "--max-model-len 8192"),
        ):
            with self.subTest(field=field), self.assertRaisesRegex(ValidationError, "Extra inputs are not permitted"):
                DynamoDeploymentValues.model_validate({**values, field: value})

    def test_derived_extra_args_stays_single_line_and_shell_safe(self):
        # Tokens carrying YAML document markers / shell metacharacters (the CCL-306
        # payload, already split into argv) must rejoin into one shell-safe line so a
        # pre-1.0.3 chart cannot be tricked into starting a new YAML document or
        # expanding the value in its /bin/sh -c command.
        values = yaml.safe_load(_VALUES_FILE.read_text())
        hostile = ["---", "securityContext:", "privileged: true", "$(id)", ";", "`id`"]
        rendered = DynamoDeploymentValues.model_validate({**values, "extra_argv": hostile})
        extra_args = rendered.model_dump()["extra_args"]
        self.assertNotIn("\n", extra_args)
        # Re-splitting the emitted string recovers exactly the original tokens.
        self.assertEqual(shlex.split(extra_args), hostile)


@unittest.skipIf(shutil.which("helm") is None, "helm CLI not installed")
class DynamoChartRenderTest(unittest.TestCase):
    docs: ClassVar[list[dict[str, Any]]]
    by_kind: ClassVar[dict[str, list[dict[str, Any]]]]

    @classmethod
    def setUpClass(cls) -> None:
        cls.docs = _helm_template()
        cls.by_kind = {}
        for d in cls.docs:
            cls.by_kind.setdefault(d.get("kind", "?"), []).append(d)

    def test_legacy_and_nested_aggregated_values_render_identically(self):
        canonical = _canonical_values()
        worker_pool = canonical.pop("worker_pool")
        legacy = {**canonical, **worker_pool}

        nested = {**canonical, "worker_pool": worker_pool}
        self.assertEqual(_helm_template(values=legacy), _helm_template(values=nested))

    def test_renders_exactly_one_dgd(self):
        self.assertEqual(len(self.by_kind.get("DynamoGraphDeployment", [])), 1)

    def test_explicit_aggregated_mode_preserves_the_legacy_render(self):
        self.assertEqual(_helm_template(extra_set=["serving_mode=aggregated"]), self.docs)

    def test_dgd_uses_v1beta1_schema(self):
        dgd = self.by_kind["DynamoGraphDeployment"][0]
        component_list = dgd["spec"]["components"]
        components = _components(dgd)

        self.assertEqual(dgd["apiVersion"], "nvidia.com/v1beta1")
        self.assertNotIn("services", dgd["spec"])
        self.assertEqual(len(component_list), 2)
        self.assertEqual(set(components), {"Frontend", "VllmDecodeWorker"})
        self.assertEqual(components["Frontend"]["type"], "frontend")
        self.assertEqual(components["VllmDecodeWorker"]["type"], "worker")
        for component in components.values():
            self.assertNotIn("componentType", component)
            self.assertNotIn("dynamoNamespace", component)
            self.assertNotIn("extraPodMetadata", component)
            self.assertNotIn("extraPodSpec", component)
            self.assertNotIn("envs", component)

    def test_dgd_uses_aggregated_router_pattern(self):
        dgd = self.by_kind["DynamoGraphDeployment"][0]
        components = _components(dgd)
        self.assertIn("Frontend", components)
        self.assertIn("VllmDecodeWorker", components)

        self.assertEqual(
            _env(components["Frontend"]).get("DYN_ROUTER_MODE"),
            "kv",
            "Frontend must enable KV routing for the aggregated+router pattern",
        )

    def test_http_frontend_uses_configured_listener_port(self):
        frontend = _component(self.by_kind["DynamoGraphDeployment"][0], "Frontend")
        main_container = _main_container(frontend)

        self.assertNotIn("livenessProbe", main_container)
        self.assertNotIn("readinessProbe", main_container)
        self.assertEqual(main_container["command"], ["python3"])
        self.assertEqual(
            main_container["args"],
            ["-m", "dynamo.frontend", "--http-port", "8000", "--no-kserve-grpc-server"],
        )
        self.assertEqual(
            main_container["ports"],
            [{"name": "http", "containerPort": 8000, "protocol": "TCP"}],
        )

    def test_kserve_grpc_frontend_flag_renders_when_enabled(self):
        docs = _helm_template(extra_set=["backend_protocol=GRPC"])
        main_container = _main_container(_component(_dgd(docs), "Frontend"))

        self.assertEqual(main_container["command"], ["python3"])
        self.assertEqual(
            main_container["args"],
            [
                "-m",
                "dynamo.frontend",
                "--http-port",
                "8000",
                "--kserve-grpc-server",
                "--grpc-metrics-port",
                # Pins the hardcoded chart argument to the shared constant.
                str(KSERVE_GRPC_METRICS_PORT),
            ],
        )
        self.assertEqual(main_container["livenessProbe"], {"tcpSocket": {"port": "http"}})
        self.assertEqual(
            main_container["readinessProbe"],
            {"successThreshold": 1, "tcpSocket": {"port": "http"}},
        )

    def test_non_default_frontend_port_stays_aligned_for_http_and_grpc(self):
        for backend_protocol in ("HTTP", "GRPC"):
            is_grpc = backend_protocol == "GRPC"
            with self.subTest(backend_protocol=backend_protocol):
                docs = _helm_template(
                    extra_set=[
                        "container_port=9000",
                        f"backend_protocol={backend_protocol}",
                    ]
                )
                main_container = _main_container(_component(_dgd(docs), "Frontend"))
                ingress = next(doc for doc in docs if doc.get("kind") == "Ingress")
                ingress_port = ingress["spec"]["rules"][0]["http"]["paths"][0]["backend"]["service"]["port"]

                self.assertEqual(main_container["command"], ["python3"])
                self.assertEqual(
                    main_container["args"][:4],
                    ["-m", "dynamo.frontend", "--http-port", "9000"],
                )
                self.assertEqual(
                    main_container["ports"],
                    [{"name": "http", "containerPort": 9000, "protocol": "TCP"}],
                )
                # The operator-owned Service exposes a stable 8000 and
                # forwards to the custom named container port.
                self.assertEqual(ingress_port, {"number": 8000})
                if is_grpc:
                    self.assertIn("--kserve-grpc-server", main_container["args"])
                    self.assertEqual(
                        main_container["args"][-2:],
                        ["--grpc-metrics-port", str(KSERVE_GRPC_METRICS_PORT)],
                    )
                    self.assertEqual(main_container["livenessProbe"], {"tcpSocket": {"port": "http"}})
                    self.assertEqual(
                        main_container["readinessProbe"],
                        {"successThreshold": 1, "tcpSocket": {"port": "http"}},
                    )
                else:
                    self.assertNotIn("--kserve-grpc-server", main_container["args"])
                    self.assertIn("--no-kserve-grpc-server", main_container["args"])
                    self.assertNotIn("livenessProbe", main_container)
                    self.assertNotIn("readinessProbe", main_container)

    def test_request_plane_is_tcp_on_both_pods(self):
        """Both Frontend and Worker must agree on the request plane backend or
        they cannot exchange requests; TCP is required because NATS request
        plane is unary-only and inference responses are streamed (see
        `request_plane` field comment in __init__.py)."""
        components = _components(self.by_kind["DynamoGraphDeployment"][0])
        for component_name in ("Frontend", "VllmDecodeWorker"):
            self.assertEqual(
                _env(components[component_name]).get("DYN_REQUEST_PLANE"),
                "tcp",
                f"{component_name} must use TCP request plane",
            )

    def test_aggregated_runtime_compatibility_env_contract(self):
        components = _components(self.by_kind["DynamoGraphDeployment"][0])
        frontend = components["Frontend"]
        worker = components["VllmDecodeWorker"]

        _assert_runtime_env(
            self,
            frontend,
            {**_COMMON_RUNTIME_ENV, **_FRONTEND_RUNTIME_ENV},
            absent=tuple(_WORKER_RUNTIME_ENV),
        )
        _assert_runtime_env(
            self,
            worker,
            {**_COMMON_RUNTIME_ENV, **_WORKER_RUNTIME_ENV},
            absent=tuple(_FRONTEND_RUNTIME_ENV),
        )

    def test_round_robin_preserves_chart_owned_runtime_env(self):
        values = _canonical_values()
        values["env_vars"] = {
            "DYN_ROUTER_MODE": "round-robin",
            "DYN_EVENT_PLANE": "etcd",
            "DYN_REQUEST_PLANE": "nats",
            "PYTHONHASHSEED": "random",
            "DYN_SELF_HOST_METADATA": "1",
            "DYN_ROUTER_QUEUE_THRESHOLD": "99",
            "DYN_DISABLE_FRONTEND_ADMIN_API": "0",
            "DYN_DISABLE_FRONTEND_NVEXT": "0",
            "DYN_VLLM_ENABLE_INFERENCE_V1_GENERATE": "1",
            "USER_SETTING": "preserved",
        }
        components = _components(_dgd(_helm_template(values=values)))
        frontend = components["Frontend"]
        worker = components["VllmDecodeWorker"]

        self.assertEqual(_env_values(frontend, "DYN_ROUTER_MODE"), ["round-robin"])
        self.assertEqual(_env(frontend)["USER_SETTING"], "preserved")
        self.assertEqual(_env(worker)["USER_SETTING"], "preserved")
        _assert_runtime_env(
            self,
            frontend,
            {**_COMMON_RUNTIME_ENV, **_FRONTEND_RUNTIME_ENV},
            absent=tuple(_WORKER_RUNTIME_ENV),
        )
        _assert_runtime_env(
            self,
            worker,
            {**_COMMON_RUNTIME_ENV, **_WORKER_RUNTIME_ENV},
            absent=tuple(_FRONTEND_RUNTIME_ENV),
        )

    def test_pods_opt_out_of_istio_sidecar(self):
        """The TCP response stream uses an OS-assigned ephemeral port that
        cannot be enrolled into Istio's auto-mTLS, so both pods must be
        opted out of sidecar injection — otherwise STRICT PeerAuthentication
        in the user namespace rejects the Worker → Frontend response stream."""
        components = _components(self.by_kind["DynamoGraphDeployment"][0])
        for component_name in ("Frontend", "VllmDecodeWorker"):
            annotations = components[component_name]["podTemplate"].get("metadata", {}).get("annotations", {})
            self.assertEqual(
                annotations.get("sidecar.istio.io/inject"),
                "false",
                f"{component_name} must opt out of Istio sidecar injection",
            )

    def test_operator_pods_receive_platform_metadata(self):
        """Status grouping and Fluent Bit log streams depend on the
        operator-created pods carrying the platform deployment/revision
        metadata, not just the top-level DGD CR."""
        components = _components(self.by_kind["DynamoGraphDeployment"][0])
        expected_component_types = {
            "Frontend": "frontend",
            "VllmDecodeWorker": "worker",
        }
        for component_name, expected_component_type in expected_component_types.items():
            metadata = components[component_name]["podTemplate"].get("metadata", {})
            annotations = metadata.get("annotations", {})
            labels = metadata.get("labels", {})
            self.assertEqual(annotations.get("platform.centml.ai/deployment-id"), "1732")
            self.assertEqual(annotations.get("platform.centml.ai/revision-number"), "1")
            self.assertEqual(labels.get("platform.centml.ai/deployment-id"), "1732")
            self.assertEqual(labels.get("platform.centml.ai/revision-number"), "1")
            self.assertEqual(labels.get(COMPONENT_TYPE_LABEL), expected_component_type)

    def test_worker_gpu_limits_flow_through_without_replicas(self):
        # v1beta1 embeds native Kubernetes ResourceRequirements in the main
        # container, so accelerator names are resource-map keys rather than
        # the v1alpha1 operator-specific `gpu`/`gpuType` fields.
        worker = _component(self.by_kind["DynamoGraphDeployment"][0], "VllmDecodeWorker")
        resources = _main_container(worker)["resources"]
        # The DGDSA/KEDA owns the count, so the worker renders no `replicas`.
        self.assertNotIn("replicas", worker)
        self.assertEqual(resources["limits"]["nvidia.com/gpu"], "1")
        self.assertEqual(resources["requests"]["nvidia.com/gpu"], "1")
        self.assertNotIn("gpu", resources["limits"])
        self.assertNotIn("gpu", resources["requests"])
        self.assertNotIn("gpuType", resources["limits"])
        self.assertNotIn("gpuType", resources["requests"])
        joined_args = " ".join(_main_container(worker)["args"])
        self.assertIn("--model Qwen/Qwen3-0.6B", joined_args)

    def test_aggregated_worker_tensor_parallel_size_matches_requested_gpus(self):
        container = _main_container(_aggregated_worker(gpu_count=2))
        args = container["args"]

        self.assertEqual(args.count("--tensor-parallel-size"), 1)
        self.assertEqual(args[args.index("--tensor-parallel-size") + 1], "2")
        self.assertEqual(container["resources"]["requests"]["nvidia.com/gpu"], "2")
        self.assertEqual(container["resources"]["limits"]["nvidia.com/gpu"], "2")

    def test_aggregated_worker_pins_text_generation_runner(self):
        args = _main_container(_aggregated_worker(gpu_count=2))["args"]

        self.assertEqual(args.count("--runner"), 1)
        self.assertEqual(args[args.index("--runner") + 1], "generate")

    def test_aggregated_platform_runner_supersedes_historical_override(self):
        args = _main_container(_aggregated_worker(gpu_count=2, extra_argv=["--ru=pooling"]))["args"]

        self.assertEqual(args[-4:], ["--tensor-parallel-size", "2", "--runner", "generate"])

    def test_serialized_aggregated_runner_wire_does_not_duplicate_platform_args(self):
        values = DynamoDeploymentValues.model_validate(
            {**_canonical_values(), "extra_argv": ["--ru=pooling", "--", "--runner", "pooling"]}
        )
        serialized = json.loads(values.helm_values_yaml())

        component = _component(_dgd(_helm_template(values=serialized)), "VllmDecodeWorker")
        args = _main_container(component)["args"]
        separator = args.index("--")
        self.assertEqual(args[:separator].count("--runner"), 1)
        self.assertEqual(args[:separator].count("--tensor-parallel-size"), 1)
        self.assertEqual(args[args.index("--tensor-parallel-size") + 1], "1")
        self.assertEqual(args[separator + 1 :], ["--runner", "pooling"])

    def test_aggregated_platform_tensor_parallel_size_supersedes_historical_override(self):
        args = _main_container(_aggregated_worker(gpu_count=2, extra_argv=["--te=1"]))["args"]

        self.assertEqual(args[-4:-2], ["--tensor-parallel-size", "2"])

    def test_aggregated_platform_tensor_parallel_size_precedes_positional_separator(self):
        extra_argv = ["--te=1", "--ru=pooling", "--", "--tensor-parallel-size", "3", "--runner", "pooling"]
        args = _main_container(_aggregated_worker(gpu_count=2, extra_argv=extra_argv))["args"]
        separator = args.index("--")

        self.assertEqual(
            args[separator - 6 : separator],
            ["--te=1", "--ru=pooling", "--tensor-parallel-size", "2", "--runner", "generate"],
        )
        self.assertEqual(args[separator + 1 :], ["--tensor-parallel-size", "3", "--runner", "pooling"])

    def test_worker_enables_kv_event_publishing(self):
        """The KV router needs real vLLM cache events; router mode alone only
        enables the Frontend subscriber."""
        worker = _component(self.by_kind["DynamoGraphDeployment"][0], "VllmDecodeWorker")
        command = _main_container(worker)["args"]

        self.assertEqual(command.count("--kv-events-config"), 1)
        config_index = command.index("--kv-events-config")
        self.assertEqual(
            json.loads(command[config_index + 1]),
            {
                "publisher": "zmq",
                "topic": "kv-events",
                "endpoint": "tcp://*:20080",
                "enable_kv_cache_events": True,
            },
        )

    def test_non_default_accelerator_uses_native_resource_key(self):
        docs = _helm_template(
            extra_set=[
                "worker_pool.resources.limits.accelerator_key=amd.com/gpu",
                "worker_pool.resources.requests.accelerator_key=amd.com/gpu",
            ]
        )
        resources = _main_container(_component(_dgd(docs), "VllmDecodeWorker"))["resources"]
        self.assertEqual(resources["limits"]["amd.com/gpu"], "1")
        self.assertEqual(resources["requests"]["amd.com/gpu"], "1")
        self.assertNotIn("nvidia.com/gpu", resources["limits"])

    def test_hf_token_renders_chart_owned_secret_and_envfrom(self):
        # hf_token => the chart renders a {name}-hf-token Secret and both components consume it
        # as an HF_TOKEN env var via envFrom: the worker to load weights, the Frontend to fetch
        # model/tokenizer metadata at registration (gated models 401 on the Frontend otherwise).
        docs = _helm_template(extra_set=["hf_token=hf_secret123"])
        secret = next(
            (d for d in docs if d.get("kind") == "Secret" and d["metadata"]["name"] == "test-dynamo-hf-token"), None
        )
        self.assertIsNotNone(secret, "chart must render the {name}-hf-token Secret")
        self.assertIn("HF_TOKEN", secret["data"])
        for name in ("VllmDecodeWorker", "Frontend"):
            self.assertEqual(
                _main_container(_component(_dgd(docs), name))["envFrom"],
                [{"secretRef": {"name": "test-dynamo-hf-token"}}],
                f"{name} must envFrom the HF token secret",
            )

    def test_hf_token_secret_and_envfrom_absent_when_unset(self):
        self.assertFalse(
            any(d.get("kind") == "Secret" and d["metadata"]["name"].endswith("-hf-token") for d in self.docs)
        )
        for name in ("VllmDecodeWorker", "Frontend"):
            self.assertNotIn("envFrom", _main_container(_component(self.by_kind["DynamoGraphDeployment"][0], name)))

    def test_pods_tolerate_dedicated_compute_taint(self):
        """All pod templates must tolerate the GPU-node taint or they won't schedule."""
        components = _components(self.by_kind["DynamoGraphDeployment"][0])
        for component_name, component in components.items():
            tolerations = _pod_spec(component).get("tolerations", [])
            self.assertTrue(
                any(
                    t.get("key") == "dedicated_compute" and t.get("value") == "true" and t.get("effect") == "NoSchedule"
                    for t in tolerations
                ),
                f"{component_name} pod template missing dedicated_compute toleration",
            )

    def test_renders_standard_ingress_and_service(self):
        self.assertIn("Ingress", self.by_kind, "ingress must be rendered for external traffic")
        ingress = self.by_kind["Ingress"][0]
        annotations = ingress["metadata"]["annotations"]
        self.assertEqual(annotations["nginx.ingress.kubernetes.io/backend-protocol"], "HTTP")
        self.assertEqual(annotations["nginx.ingress.kubernetes.io/rewrite-target"], "/")
        services = self.by_kind.get("Service", [])
        # ExternalName service that aliases to the operator-managed Frontend
        externalname = [s for s in services if s["spec"].get("type") == "ExternalName"]
        self.assertEqual(
            len(externalname),
            1,
            "exactly one ExternalName service must alias the operator-created Frontend",
        )

    def test_grpc_frontend_uses_grpc_ingress_backend_protocol(self):
        docs = _helm_template(extra_set=["backend_protocol=GRPC"])
        ingress = next(d for d in docs if d.get("kind") == "Ingress")
        annotations = ingress["metadata"]["annotations"]

        self.assertEqual(annotations["nginx.ingress.kubernetes.io/backend-protocol"], "GRPC")
        self.assertNotIn("nginx.ingress.kubernetes.io/rewrite-target", annotations)

    def test_no_istio_policies_rendered(self):
        """With Dynamo pods out of the mesh, mesh-only resources
        (PeerAuthentication, AuthorizationPolicy) would have no effect and
        should not be emitted — a stray PeerAuthentication after re-enabling
        the sidecar would silently break mTLS for unrelated workloads."""
        self.assertNotIn("PeerAuthentication", self.by_kind)
        self.assertNotIn("AuthorizationPolicy", self.by_kind)

    def test_networkpolicy_adds_required_cross_namespace_egress(self):
        """The base `namespace-default` policy already allows same-namespace
        pod ↔ pod traffic via `to: [{podSelector: {}}]`, so the chart only
        needs to *add* the two cross-namespace destinations Dynamo workloads
        require: NATS in `dynamo-system` (request plane / KV events) and the
        Kubernetes API server (the operator runs Dynamo with
        `DYN_DISCOVERY_BACKEND=kubernetes`)."""
        policies = self.by_kind.get("NetworkPolicy", [])
        self.assertEqual(
            len(policies),
            1,
            "exactly one NetworkPolicy must be rendered for the Dynamo egress allowlist",
        )
        np = policies[0]
        self.assertEqual(
            np["spec"]["podSelector"]["matchLabels"].get("nvidia.com/dynamo-graph-deployment-name"),
            "test-dynamo",
        )

        egress = np["spec"].get("egress", [])
        self.assertTrue(
            any(
                any(
                    peer.get("namespaceSelector", {}).get("matchLabels", {}).get("kubernetes.io/metadata.name")
                    == "dynamo-system"
                    for peer in rule.get("to", [])
                )
                and any(p.get("port") == 4222 for p in rule.get("ports", []))
                for rule in egress
            ),
            "NetworkPolicy must allow egress to NATS in dynamo-system on TCP/4222",
        )
        self.assertTrue(
            any("to" not in rule and any(p.get("port") == 443 for p in rule.get("ports", [])) for rule in egress),
            "NetworkPolicy must allow egress to TCP/443 for the Kubernetes API server",
        )

    def test_no_image_pull_secret_when_credentials_unset(self):
        self.assertNotIn(
            "Secret",
            self.by_kind,
            "image pull secret should only be rendered when credentials are provided",
        )

    def test_router_mode_defaults_to_kv(self):
        """With no override the Frontend defaults to KV-aware routing (the
        aggregated + KV-router pattern)."""
        self.assertEqual(
            _env(_component(self.by_kind["DynamoGraphDeployment"][0], "Frontend")).get("DYN_ROUTER_MODE"),
            "kv",
        )

    def test_router_mode_env_override_wins_and_is_not_duplicated(self):
        """A caller-supplied env_vars[DYN_ROUTER_MODE] overrides the default and
        is emitted exactly once — a duplicate would let glibc's getenv return
        the first match and silently ignore the override."""
        docs = _helm_template(extra_set=["env_vars.DYN_ROUTER_MODE=round-robin"])
        frontend = _component(_dgd(docs), "Frontend")
        self.assertEqual(_env(frontend).get("DYN_ROUTER_MODE"), "round-robin")
        names = [item["name"] for item in _main_container(frontend).get("env", [])]
        self.assertEqual(names.count("DYN_ROUTER_MODE"), 1)

    def test_empty_env_vars_renders_and_router_mode_defaults(self):
        """An omitted/empty env_vars must not crash the template. `index`/`range` over a
        nil (or empty-string) env_vars would raise; the `| default dict` guard renders it as
        the default DYN_ROUTER_MODE=kv. (Regression: a payload without env_vars 500'd.)"""
        for env_vars in ("", "null"):
            with self.subTest(env_vars=env_vars):
                docs = _helm_template(extra_set=[f"env_vars={env_vars}"])
                self.assertEqual(_env(_component(_dgd(docs), "Frontend")).get("DYN_ROUTER_MODE"), "kv")

    def _worker_pod_annotations(self, docs):
        return _component(_dgd(docs), "VllmDecodeWorker")["podTemplate"]["metadata"]["annotations"]

    def test_rollout_strategy_defaults_render_on_worker_pod_annotations(self):
        """The surge/unavailable annotations must live on the WORKER component's
        podTemplate.metadata.annotations: the DGD rolling-update reconciler reads
        them off the raw component (GetPodTemplateAnnotations on dgd.Spec.Components),
        NOT off DGD spec.annotations (which are only propagated on the DCD-generation
        path). They carry the platform defaults (max_surge=0, max_unavailable=1) so
        the worker roll doesn't fall back to the operator's 25%/25%."""
        worker_annotations = self._worker_pod_annotations(self.docs)
        self.assertEqual(worker_annotations["nvidia.com/deployment-rolling-update-max-surge"], "0")
        self.assertEqual(worker_annotations["nvidia.com/deployment-rolling-update-max-unavailable"], "1")
        # Must NOT be on spec.annotations or metadata.annotations — the roll reconciler ignores those.
        dgd = self.by_kind["DynamoGraphDeployment"][0]
        self.assertNotIn("nvidia.com/deployment-rolling-update-max-surge", dgd.get("spec", {}).get("annotations", {}))
        self.assertNotIn("nvidia.com/deployment-rolling-update-max-surge", dgd["metadata"]["annotations"])

    def test_rollout_strategy_overrides_flow_into_worker_annotations(self):
        docs = _helm_template(
            extra_set=[
                "rollout_strategy.max_surge=2",
                "rollout_strategy.max_unavailable=0",
            ]
        )
        worker_annotations = self._worker_pod_annotations(docs)
        self.assertEqual(worker_annotations["nvidia.com/deployment-rolling-update-max-surge"], "2")
        self.assertEqual(worker_annotations["nvidia.com/deployment-rolling-update-max-unavailable"], "0")

    def test_kai_queue_and_scheduler_on_frontend_and_worker(self):
        dgd = _dgd(self.docs)
        frontend = _component(dgd, "Frontend")
        worker = _component(dgd, "VllmDecodeWorker")
        self.assertEqual(frontend["podTemplate"]["metadata"]["labels"]["kai.scheduler/queue"], "org-7")
        self.assertEqual(worker["podTemplate"]["metadata"]["labels"]["kai.scheduler/queue"], "org-7")
        self.assertEqual(_pod_spec(frontend).get("schedulerName"), "kai-scheduler")
        self.assertEqual(_pod_spec(worker).get("schedulerName"), "kai-scheduler")

    def test_priority_class_on_frontend_and_worker(self):
        values = _canonical_values()
        values["priority_class_name"] = "experiment"
        dgd = _dgd(_helm_template(values=values))
        frontend = _component(dgd, "Frontend")
        worker = _component(dgd, "VllmDecodeWorker")
        self.assertEqual(_pod_spec(frontend).get("priorityClassName"), "experiment")
        self.assertEqual(_pod_spec(worker).get("priorityClassName"), "experiment")

    def test_priority_class_omitted_when_unset(self):
        worker = _component(_dgd(_helm_template()), "VllmDecodeWorker")
        self.assertNotIn("priorityClassName", _pod_spec(worker))

    def test_kai_queue_omitted_when_unset(self):
        values = _canonical_values()
        values.pop("kai_queue", None)
        worker = _component(_dgd(_helm_template(values=values)), "VllmDecodeWorker")
        self.assertNotIn("kai.scheduler/queue", worker["podTemplate"]["metadata"]["labels"])

    def test_worker_always_mounts_dev_shm(self):
        """vLLM/PyTorch IPC needs a tmpfs-backed /dev/shm regardless of the
        node-cache opt-in; the k8s default 64Mi otherwise causes bus errors."""
        worker = _component(self.by_kind["DynamoGraphDeployment"][0], "VllmDecodeWorker")
        volumes = {v["name"]: v for v in _pod_spec(worker).get("volumes", [])}
        self.assertEqual(volumes["dshm"]["emptyDir"]["medium"], "Memory")
        mounts = {m["name"]: m for m in _main_container(worker).get("volumeMounts", [])}
        self.assertEqual(mounts["dshm"]["mountPath"], "/dev/shm")

    def test_node_model_cache_off_by_default(self):
        """Without the opt-in flag the worker pod must not reference the
        node cache PVC — older deployments and clusters that haven't yet
        provisioned `node-model-cache-pvc` would fail to schedule otherwise.
        (The always-present /dev/shm volume is covered separately.)"""
        worker = _component(self.by_kind["DynamoGraphDeployment"][0], "VllmDecodeWorker")
        pod_spec = _pod_spec(worker)
        volume_names = {v["name"] for v in pod_spec.get("volumes", [])}
        self.assertNotIn("model-cache", volume_names)
        self.assertNotIn("initContainers", pod_spec)
        mount_names = {m["name"] for m in _main_container(worker).get("volumeMounts", [])}
        self.assertNotIn("model-cache", mount_names)
        self.assertNotIn("HF_HUB_CACHE", _env(worker))


@unittest.skipIf(shutil.which("helm") is None, "helm CLI not installed")
class DynamoDisaggregatedChartRenderTest(unittest.TestCase):
    """Static xPyD uses first-class pools without the aggregated scaler."""

    one_p_one_d: ClassVar[list[dict[str, Any]]]
    one_p_two_d: ClassVar[list[dict[str, Any]]]

    @classmethod
    def setUpClass(cls) -> None:
        cls.one_p_one_d = _helm_template(extra_set=_disaggregated_set())
        cls.one_p_two_d = _helm_template(extra_set=_disaggregated_set(decode_replicas=2))

    @staticmethod
    def _required_affinity(component: dict) -> dict[str, str]:
        terms = _pod_spec(component)["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]
        expressions = terms["nodeSelectorTerms"][0]["matchExpressions"]
        return {expression["key"]: expression["values"][0] for expression in expressions}

    def test_one_p_one_d_renders_first_class_fixed_components(self):
        components = _components(_dgd(self.one_p_one_d))
        self.assertEqual(set(components), {"Frontend", "VllmDecodeWorker", "VllmPrefillWorker"})
        self.assertEqual(components["Frontend"]["type"], "frontend")
        self.assertEqual(components["VllmDecodeWorker"]["type"], "decode")
        self.assertEqual(components["VllmPrefillWorker"]["type"], "prefill")

        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            self.assertEqual(components[name]["replicas"], 1)
            self.assertNotIn("scalingAdapter", components[name])
            self.assertEqual(_env(components[name])["DYN_REQUEST_PLANE"], "tcp")

        self.assertFalse(any(doc.get("kind") == "ScaledObject" for doc in self.one_p_one_d))
        self.assertTrue(any(doc.get("kind") == "ServiceMonitor" for doc in self.one_p_one_d))

    def test_one_p_one_d_runtime_compatibility_env_contract(self):
        components = _components(_dgd(self.one_p_one_d))
        _assert_runtime_env(
            self,
            components["Frontend"],
            {**_COMMON_RUNTIME_ENV, **_FRONTEND_RUNTIME_ENV},
            absent=tuple(_WORKER_RUNTIME_ENV),
        )
        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            _assert_runtime_env(
                self,
                components[name],
                {**_COMMON_RUNTIME_ENV, **_WORKER_RUNTIME_ENV},
                absent=tuple(_FRONTEND_RUNTIME_ENV),
            )

    def test_role_specific_vllm_disaggregation_and_nixl_arguments(self):
        components = _components(_dgd(self.one_p_one_d))
        decode = _main_container(components["VllmDecodeWorker"])["args"]
        prefill = _main_container(components["VllmPrefillWorker"])["args"]

        self.assertEqual(_main_container(components["VllmDecodeWorker"])["command"], ["python3"])
        self.assertEqual(_main_container(components["VllmPrefillWorker"])["command"], ["python3"])

        self.assertEqual(decode[decode.index("--disaggregation-mode") + 1], "decode")
        self.assertEqual(decode[decode.index("--tensor-parallel-size") + 1], "2")
        self.assertEqual(prefill[prefill.index("--disaggregation-mode") + 1], "prefill")
        self.assertEqual(prefill[prefill.index("--tensor-parallel-size") + 1], "1")
        for args in (decode, prefill):
            self.assertEqual(args.count("--kv-transfer-config"), 1)
            transfer_config = json.loads(args[args.index("--kv-transfer-config") + 1])
            self.assertEqual(transfer_config, {"kv_connector": "NixlConnector", "kv_role": "kv_both"})
            self.assertEqual(args.count("--runner"), 1)
            self.assertEqual(args[args.index("--runner") + 1], "generate")

    def test_disaggregated_platform_runner_precedes_positional_separator(self):
        components = _disaggregated_components(
            extra_argv=["--ru=pooling", "--", "--runner", "pooling", "positional-value"]
        )

        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            args = _main_container(components[name])["args"]
            separator = args.index("--")
            self.assertEqual(args[separator - 3 : separator], ["--ru=pooling", "--runner", "generate"])
            self.assertEqual(args[separator + 1 :], ["--runner", "pooling", "positional-value"])

    def test_serialized_disaggregated_runner_wire_renders_once_per_worker(self):
        canonical = _canonical_values()
        values = DynamoDeploymentValues.model_validate(
            {
                **_without_aggregated_worker(canonical),
                "serving_mode": "disaggregated",
                "prefill_pool": _static_pool(canonical, "prefill"),
                "decode_pool": _static_pool(canonical, "decode"),
                "extra_argv": ["--runner", "pooling", "--max-model-len", "4096"],
            }
        )
        components = _components(_dgd(_helm_template(values=json.loads(values.helm_values_yaml()))))

        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            args = _main_container(components[name])["args"]
            self.assertEqual(args.count("--runner"), 1)
            self.assertEqual(args[args.index("--runner") + 1], "generate")

    def test_disaggregated_workers_do_not_advertise_cross_pod_cuda_ipc(self):
        """Separate pod IPC namespaces and per-pod /dev/shm mean that an
        NVLink-capable node alone does not enable cross-pod CUDA IPC."""
        components = _components(_dgd(self.one_p_one_d))

        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            pod_spec = _pod_spec(components[name])
            self.assertNotIn("hostIPC", pod_spec)
            self.assertNotIn("shareProcessNamespace", pod_spec)

            volumes = {volume["name"]: volume for volume in pod_spec["volumes"]}
            self.assertEqual(volumes["dshm"]["emptyDir"]["medium"], "Memory")

    def test_kv_router_events_are_published_by_prefill_only(self):
        components = _components(_dgd(self.one_p_one_d))
        decode = _main_container(components["VllmDecodeWorker"])["args"]
        prefill = _main_container(components["VllmPrefillWorker"])["args"]

        self.assertEqual(_env(components["Frontend"])["DYN_ROUTER_MODE"], "kv")
        self.assertNotIn("--kv-events-config", decode)
        self.assertEqual(prefill.count("--kv-events-config"), 1)
        event_config = json.loads(prefill[prefill.index("--kv-events-config") + 1])
        self.assertEqual(
            event_config,
            {
                "publisher": "zmq",
                "topic": "kv-events",
                "endpoint": "tcp://*:20080",
                "enable_kv_cache_events": True,
            },
        )

    def test_one_p_two_d_keeps_pool_replica_resources_placement_and_shm_independent(self):
        components = _components(_dgd(self.one_p_two_d))
        prefill = components["VllmPrefillWorker"]
        decode = components["VllmDecodeWorker"]

        self.assertEqual((prefill["replicas"], decode["replicas"]), (1, 2))
        self.assertEqual(
            _main_container(prefill)["resources"]["limits"],
            {
                "cpu": "4",
                "memory": "2000Mi",
                "nvidia.com/gpu": "1",
            },
        )
        self.assertEqual(
            _main_container(decode)["resources"]["limits"],
            {
                "cpu": "8",
                "memory": "4000Mi",
                "nvidia.com/gpu": "2",
            },
        )
        self.assertEqual(self._required_affinity(prefill), {"pool-role": "prefill"})
        self.assertEqual(self._required_affinity(decode), {"pool-role": "decode"})
        self.assertEqual(_pod_spec(prefill)["volumes"][0]["emptyDir"]["sizeLimit"], "600Mi")
        self.assertEqual(_pod_spec(decode)["volumes"][0]["emptyDir"]["sizeLimit"], "1200Mi")

    def test_disaggregated_workers_do_not_guess_network_extended_resources(self):
        docs = _helm_template(
            extra_set=[
                *_disaggregated_set(),
                "prefill_pool.resources.limits.infiniband_count=1",
                "prefill_pool.resources.limits.infiniband_key=rdma/ib",
                "prefill_pool.resources.requests.infiniband_count=1",
                "prefill_pool.resources.requests.infiniband_key=rdma/ib",
            ]
        )
        components = _components(_dgd(docs))

        # The API has no cluster/profile-level RDMA resource mapping yet. A
        # shared catalog model carrying legacy IB fields must not make this
        # chart guess a resource name that may not exist on the target cluster.
        for name in ("VllmDecodeWorker", "VllmPrefillWorker"):
            resources = _main_container(components[name])["resources"]
            self.assertNotIn("rdma/ib", resources["limits"])
            self.assertNotIn("rdma/ib", resources["requests"])

    def test_both_pools_inherit_platform_metadata_secrets_cache_logging_and_env(self):
        docs = _helm_template(
            extra_set=[
                *_disaggregated_set(),
                "hf_token=hf_secret123",
                "enable_node_model_cache=true",
                "enable_logging=false",
                "env_vars.SHARED_SETTING=enabled",
                "user_annotations.benchmark=xpyd",
            ]
        )
        components = _components(_dgd(docs))

        for name, role in (("VllmDecodeWorker", "decode"), ("VllmPrefillWorker", "prefill")):
            component = components[name]
            metadata = component["podTemplate"]["metadata"]
            self.assertEqual(metadata["labels"][COMPONENT_TYPE_LABEL], role)
            self.assertEqual(metadata["labels"]["platform.centml.ai/deployment-id"], "1732")
            self.assertEqual(metadata["annotations"]["platform.centml.ai/log-exclude"], "true")
            self.assertEqual(metadata["annotations"]["ccluster.nvidia.com/user-benchmark"], "xpyd")
            self.assertEqual(_env(component)["SHARED_SETTING"], "enabled")
            self.assertEqual(
                _main_container(component)["envFrom"],
                [{"secretRef": {"name": "test-dynamo-hf-token"}}],
            )
            self.assertEqual(_env(component)["HF_HUB_CACHE"], "/tmp/dynamo-hf-cache")
            self.assertTrue(any("persistentVolumeClaim" in volume for volume in _pod_spec(component)["volumes"]))


@unittest.skipIf(shutil.which("helm") is None, "helm CLI not installed")
class DynamoChartNodeModelCacheTest(unittest.TestCase):
    """When `enable_node_model_cache=true` the worker pods must mount the
    namespace-provisioned `node-model-cache-pvc` at /tmp and steer the
    Hugging Face downloader at it via `HF_HUB_CACHE`. This is the
    configuration that lets multiple worker replicas on the same GPU node
    share weights instead of each re-pulling them and tripping the HF
    resolver rate limit."""

    @classmethod
    def setUpClass(cls):
        cls.docs = _helm_template(extra_set=["enable_node_model_cache=true"])
        cls.dgd = _dgd(cls.docs)

    def test_worker_mounts_dynamo_subpath_of_shared_pvc(self):
        """A single volume entry references `node-model-cache-pvc` and is
        mounted twice within the pod: unscoped at `/cache` in the init
        container so it can mkdir + chown the dynamo-private subdir, then
        with subPath `dynamo-hf` at `/tmp/dynamo-hf-cache` in the main
        container so it can't even see foreign sibling files left by
        other tenants on the same node (e.g. cserve_v3 at UID 1024).

        Declaring two volume entries against the same RWO PVC makes
        kubelet hang in PodInitializing on dev-12 with no Pulling event —
        do NOT regress to that shape."""
        worker = _component(self.dgd, "VllmDecodeWorker")
        pod_spec = _pod_spec(worker)

        volumes = pod_spec.get("volumes", [])
        pvc_volumes = [v for v in volumes if "persistentVolumeClaim" in v]
        self.assertEqual(len(pvc_volumes), 1, "must declare exactly one PVC volume — see test docstring")
        self.assertEqual(pvc_volumes[0]["name"], "model-cache")
        self.assertEqual(
            pvc_volumes[0]["persistentVolumeClaim"]["claimName"],
            "node-model-cache-pvc",
        )

        mounts = {m["name"]: m for m in _main_container(worker).get("volumeMounts", [])}
        self.assertIn("model-cache", mounts)
        self.assertEqual(mounts["model-cache"]["mountPath"], "/tmp/dynamo-hf-cache")
        self.assertEqual(
            mounts["model-cache"].get("subPath"),
            "dynamo-hf",
            "main container must mount the dynamo-private subPath, not the shared /tmp root",
        )

    def test_init_container_chowns_dynamo_subpath(self):
        """The initContainer must run as root, mount the same volume as
        the main container but unscoped at /cache, and idempotently
        mkdir+chown the dynamo-hf subdir before exiting."""
        worker = _component(self.dgd, "VllmDecodeWorker")
        init = _pod_spec(worker).get("initContainers", [])
        self.assertEqual(len(init), 1, "exactly one initContainer must prepare the cache")
        ic = init[0]
        self.assertEqual(ic.get("securityContext", {}).get("runAsUser"), 0)
        joined = " ".join(ic.get("command", []))
        self.assertIn("/cache/dynamo-hf", joined)
        self.assertIn("chown -R 1000:1000", joined)

        ic_mounts = {m["name"]: m for m in ic.get("volumeMounts", [])}
        self.assertIn(
            "model-cache",
            ic_mounts,
            "initContainer must reuse the same volume entry as the main container",
        )
        self.assertEqual(ic_mounts["model-cache"]["mountPath"], "/cache")
        self.assertNotIn(
            "subPath",
            ic_mounts["model-cache"],
            "initContainer must NOT use subPath, otherwise kubelet auto-creates "
            "dynamo-hf as root 0755 and the chown is moot",
        )

    def test_worker_hf_hub_cache_points_at_subpath(self):
        worker = _component(self.dgd, "VllmDecodeWorker")
        self.assertEqual(
            _env(worker).get("HF_HUB_CACHE"),
            "/tmp/dynamo-hf-cache",
            "HF_HUB_CACHE must point at the chowned dynamo-hf subPath, not the shared root",
        )

    def test_frontend_does_not_mount_cache(self):
        """The Frontend doesn't load weights, so it doesn't need (and must
        not contend for) the cache PVC."""
        frontend = _component(self.dgd, "Frontend")
        pod_spec = _pod_spec(frontend)
        self.assertNotIn("volumes", pod_spec)
        self.assertNotIn("initContainers", pod_spec)
        self.assertNotIn("volumeMounts", _main_container(frontend))


@unittest.skipIf(shutil.which("helm") is None, "helm CLI not installed")
class DynamoAutoscalingRenderTest(unittest.TestCase):
    """Verify DGDSA/KEDA autoscaling for the aggregated worker."""

    autoscaled: ClassVar[list[dict[str, Any]]]
    fixed: ClassVar[list[dict[str, Any]]]

    @classmethod
    def setUpClass(cls) -> None:
        cls.autoscaled = _helm_template(
            extra_set=[
                "hpa.min_replicas=1",
                "hpa.max_replicas=5",
                "hpa.average_value=4",
                "hpa.cooldown_period=120",
            ]
        )
        cls.fixed = _helm_template()

    @staticmethod
    def _kinds(docs: list[dict]) -> list[str | None]:
        return [d.get("kind") for d in docs]

    def _scaledobject(self) -> dict:
        return next(d for d in self.autoscaled if d.get("kind") == "ScaledObject")

    def test_fixed_replicas_still_render_autoscaling_objects_pinned(self):
        # Aggregated fixed replicas still use DGDSA/KEDA; min == max pins the worker count.
        self.assertIn("ScaledObject", self._kinds(self.fixed))
        self.assertIn("ServiceMonitor", self._kinds(self.fixed))
        self.assertEqual(_component(_dgd(self.fixed), "VllmDecodeWorker").get("scalingAdapter"), {})
        so = next(d for d in self.fixed if d.get("kind") == "ScaledObject")
        self.assertEqual(so["spec"]["minReplicaCount"], so["spec"]["maxReplicaCount"])

    def test_worker_opts_into_scaling_adapter_frontend_does_not(self):
        worker = _component(_dgd(self.autoscaled), "VllmDecodeWorker")
        self.assertEqual(worker.get("scalingAdapter"), {})
        # The Frontend is pinned at 1 replica and must not get an adapter.
        self.assertNotIn("scalingAdapter", _component(_dgd(self.autoscaled), "Frontend"))

    def test_scaledobject_targets_the_worker_dgdsa(self):
        ref = self._scaledobject()["spec"]["scaleTargetRef"]
        self.assertEqual(ref["apiVersion"], "nvidia.com/v1beta1")
        self.assertEqual(ref["kind"], "DynamoGraphDeploymentScalingAdapter")
        # The operator names the DGDSA {dgd-name}-{component-name-lowercased}.
        dgd_name = _dgd(self.autoscaled)["metadata"]["name"]
        self.assertEqual(ref["name"], f"{dgd_name}-vllmdecodeworker")
        spec = self._scaledobject()["spec"]
        self.assertEqual((spec["minReplicaCount"], spec["maxReplicaCount"]), (1, 5))
        self.assertEqual(spec["cooldownPeriod"], 120)

    def test_scaledobject_uses_mode_independent_active_request_trigger(self):
        so = self._scaledobject()
        dep_id = str(_dgd(self.autoscaled)["metadata"]["labels"]["platform.centml.ai/deployment-id"])
        triggers = so["spec"]["triggers"]
        self.assertEqual(len(triggers), 1)

        trigger = triggers[0]
        query = trigger["metadata"]["query"]
        dgd_name = _dgd(self.autoscaled)["metadata"]["name"]
        self.assertEqual(trigger["type"], "prometheus")
        self.assertEqual(trigger["metadata"]["metricName"], f"{dgd_name}-frontend-active")
        self.assertIn(f'deployment_id="{dep_id}"', query)
        self.assertIn("dynamo_frontend_active_requests", query)
        self.assertIn("or on() vector(0)", query)
        self.assertNotIn("dynamo_frontend_inflight_requests", query)
        self.assertNotIn("dynamo_frontend_router_queue_pending_requests", query)
        # concurrency (average_value) becomes the per-worker active-request threshold.
        self.assertEqual(trigger["metadata"]["threshold"].strip(), "4")

    def test_frontend_metrics_scrape_rendered_with_deployment_id_relabel(self):
        sm = next((d for d in self.autoscaled if d.get("kind") == "ServiceMonitor"), None)
        self.assertIsNotNone(sm, "ServiceMonitor must render so dynamo_frontend_* reaches Prometheus")
        endpoint = sm["spec"]["endpoints"][0]
        self.assertEqual(endpoint["path"], "/metrics")
        relabels = endpoint.get("metricRelabelings", [])
        self.assertTrue(any(r.get("targetLabel") == "deployment_id" for r in relabels))

    def test_frontend_metrics_service_uses_standardized_component_selector(self):
        selector = self._metrics_service(self.autoscaled)["spec"]["selector"]
        self.assertEqual(selector[COMPONENT_TYPE_LABEL], "frontend")
        self.assertNotIn("nvidia.com/dynamo-component-type", selector)

    def _metrics_service(self, docs: list[dict]) -> dict:
        return next(
            d for d in docs if d.get("kind") == "Service" and d["metadata"]["name"].endswith("-frontend-metrics")
        )

    def test_http_mode_scrapes_the_container_port(self):
        # HTTP mode: /metrics is on the http listener (container_port), not the gRPC metrics port.
        port = self._metrics_service(self.autoscaled)["spec"]["ports"][0]
        self.assertEqual(port["targetPort"], port["port"])
        self.assertNotEqual(port["port"], KSERVE_GRPC_METRICS_PORT)

    def test_cooldown_period_zero_is_preserved(self):
        # Helm's `default` treats 0 as empty; a key-presence check must keep an
        # explicit 0 (scale down with no wait) rather than silently using 1800.
        docs = _helm_template(
            extra_set=["hpa.min_replicas=1", "hpa.max_replicas=5", "hpa.average_value=4", "hpa.cooldown_period=0"]
        )
        so = next(d for d in docs if d.get("kind") == "ScaledObject")
        assert so["spec"]["cooldownPeriod"] == 0
        assert so["spec"]["initialCooldownPeriod"] == 0

    def test_cooldown_period_defaults_when_omitted(self):
        docs = _helm_template(extra_set=["hpa.min_replicas=1", "hpa.max_replicas=5", "hpa.average_value=4"])
        so = next(d for d in docs if d.get("kind") == "ScaledObject")
        assert so["spec"]["cooldownPeriod"] == 1800

    def test_grpc_mode_scrapes_the_metrics_port_not_the_grpc_port(self):
        # In gRPC mode the http port serves gRPC and HTTP /metrics moves to the
        # kserve grpc-metrics-port; the scrape Service must follow it, else every
        # KEDA query resolves to zero and the worker never scales past min.
        docs = _helm_template(extra_set=["hpa.min_replicas=1", "hpa.max_replicas=5", "backend_protocol=GRPC"])
        port = self._metrics_service(docs)["spec"]["ports"][0]
        self.assertEqual(port["port"], KSERVE_GRPC_METRICS_PORT)
        self.assertEqual(port["targetPort"], KSERVE_GRPC_METRICS_PORT)


if __name__ == "__main__":
    unittest.main()
