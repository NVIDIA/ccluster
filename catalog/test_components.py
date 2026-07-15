# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml
from pydantic import ValidationError

from catalog.components import (
    DeploymentBaseHelmValues,
    HelmValues,
    ResourceLimitsRequests,
    Resources,
    cpu_resource_str,
    memory_resource_str,
)
from catalog.compute_deployment import ComputeDeploymentValues
from catalog.cserve_deployment_v3 import CServeV3InferenceDeploymentValues
from catalog.inference_deployment_v3 import (
    CONFIG_FILE_CONTENT_MAX_BYTES,
    CONFIGMAP_KEY_MAX_LENGTH,
    HPA,
    LINUX_PATH_MAX,
    BackendProtocol,
    ConfigFileMount,
    ConfigFileMountValues,
    InferenceDeploymentValues,
    MetricsConfig,
    RolloutStrategy,
)
from catalog.job_deployment import JobDeploymentValues

_HELM_BIN = shutil.which("helm")


class TestHelperFunctions(unittest.TestCase):
    def test_cpu_resource_str(self):
        # Test case where cpu is a full unit
        self.assertEqual(cpu_resource_str(1000), "1")
        self.assertEqual(cpu_resource_str(2000), "2")
        self.assertEqual(cpu_resource_str(3000), "3")

        # Test case where cpu is in millicpu
        self.assertEqual(cpu_resource_str(500), "500m")
        self.assertEqual(cpu_resource_str(2500), "2500m")
        self.assertEqual(cpu_resource_str(1250), "1250m")

    def test_memory_resource_str(self):
        # Test memory conversion
        self.assertEqual(memory_resource_str(512), "512Mi")
        self.assertEqual(memory_resource_str(1024), "1024Mi")
        self.assertEqual(memory_resource_str(2048), "2048Mi")


class ResourcesTest(unittest.TestCase):
    def test_cpu_serializer(self):
        resource = Resources(cpu=1000, accelerator_key="nvidia.com/gpu")
        self.assertEqual(resource.cpu_serializer(1000), "1")
        self.assertEqual(resource.cpu_serializer(999), "999m")

    def test_memory_serializer(self):
        resource = Resources(memory=512, accelerator_key="nvidia.com/gpu")
        self.assertEqual(resource.memory_serializer(512), "512Mi")

    def test_model_dump_json(self):
        resource = Resources(
            cpu=1000,
            memory=2048,
            accelerator_count=2,
            accelerator_key="nvidia.com/gpu",
            infiniband_count=2,
        )
        self.assertEqual(
            resource.model_dump_json(by_alias=True),
            '{"accelerator_count":2,"accelerator_key":"nvidia.com/gpu","gpus":1,"cpu":"1","memory":"2048Mi",'
            '"infiniband_count":2,"infiniband_key":"nvidia.com/ib"}',
        )


class ResourceLimitsRequestsTest(unittest.TestCase):
    def test_limits_and_requests(self):
        limits = Resources(cpu=1000, memory=2048, accelerator_count=2, accelerator_key="nvidia.com/gpu")
        requests = Resources(cpu=500, memory=1024, accelerator_count=1, accelerator_key="nvidia.com/gpu")
        resource_limits_requests = ResourceLimitsRequests(limits=limits, requests=requests)

        self.assertEqual(resource_limits_requests.limits.cpu, 1000)
        self.assertEqual(resource_limits_requests.requests.memory, 1024)
        self.assertEqual(resource_limits_requests.limits.accelerator_count, 2)


class HelmValuesTest(unittest.TestCase):
    def setUp(self):
        self.helm_values = HelmValues()

    def test_flatten_yaml(self):
        nested_dict = {"parent": {"child1": "value1", "child2": "value2"}}
        expected_flat_dict = {"parent.child1": "value1", "parent.child2": "value2"}
        flat_dict = self.helm_values._flatten_yaml(nested_dict)
        self.assertEqual(flat_dict, expected_flat_dict)

    def test_flatten_yaml_does_not_escape_dots(self):
        """
        _flatten_yaml_dict must NOT touch the characters inside keys. Callers
        such as `get_hardware_specifications_v2` pre-escape node_affinity_labels
        keys at the API boundary (`re.escape(...)`); if flatten added a second
        layer of escaping the result would be `\\\\.` which Helm --set parses as
        literal `\\` then nesting `.`, breaking the rendered key. Regression
        guard for the bug found while debugging PR #3646.
        """
        # Already-escaped key from a re.escape caller upstream
        pre_escaped = {"node_affinity_labels": {r"platform\.centml\.ai/instance\-type": "small"}}
        flat = self.helm_values._flatten_yaml(pre_escaped)
        self.assertEqual(
            flat,
            {r"node_affinity_labels.platform\.centml\.ai/instance\-type": "small"},
        )
        # Plain key with dot - flatten still leaves it literal (no auto-escape)
        plain = {"a.b": "v"}
        self.assertEqual(self.helm_values._flatten_yaml(plain), {"a.b": "v"})

    @patch("yaml.safe_load")
    def test_helm_params(self, mock_safe_load):
        # Mocking the JSON-to-YAML conversion
        mock_safe_load.return_value = {
            "key1": "value1",
            "key2": {"key3": "value3"},
            "key4": None,
            "key5": "123",
            "key6": 123,
            "key7": [{"key8": 4}, {"key9": "5"}],
        }
        expected_params = [
            {"name": "key1", "value": "value1", "forceString": True},
            {"name": "key2.key3", "value": "value3", "forceString": True},
            {"name": "key4", "value": None, "forceString": False},
            {"name": "key5", "value": "123", "forceString": True},
            {"name": "key6", "value": "123", "forceString": False},
            {"name": "key7[0].key8", "value": "4", "forceString": False},
            {"name": "key7[1].key9", "value": "5", "forceString": True},
        ]

        params = self.helm_values.helm_params()
        self.assertEqual(params, expected_params)

    @patch("yaml.dump")
    def test_yaml(self, mock_yaml_dump):
        # Mocking the Pydantic model's YAML serialization
        mock_yaml_dump.return_value = "key1: value1\nkey2:\n  key3: value3\n"
        yaml_output = self.helm_values.yaml()
        self.assertEqual(yaml_output, "key1: value1\nkey2:\n  key3: value3\n")


class TestDeploymentBaseHelmValues(unittest.TestCase):
    def test_default_dev_shm_size(self):
        """Test that dev_shm_size defaults to 30% of resources.requests.memory when not provided."""
        resources = ResourceLimitsRequests(
            limits=Resources(
                cpu=2000,
                memory=4000,
                accelerator_count=1,
                accelerator_key="nvidia.com/gpu",
            ),
            requests=Resources(
                cpu=1000,
                memory=1000,
                accelerator_count=0,
                accelerator_key="nvidia.com/gpu",
            ),
        )
        deployment_values = DeploymentBaseHelmValues(dev_shm_size=None, resources=resources)
        self.assertEqual(deployment_values.dev_shm_size, 300)  # 30% of 1000 is 300

    def test_dev_shm_size_deserialization(self):
        """Test that dev_shm_size is correctly deserialized from a string."""
        resources = ResourceLimitsRequests(
            limits=Resources(
                cpu=2000,
                memory=4000,
                accelerator_count=1,
                accelerator_key="nvidia.com/gpu",
            ),
            requests=Resources(
                cpu=1000,
                memory=1000,
                accelerator_count=0,
                accelerator_key="nvidia.com/gpu",
            ),
        )
        deployment_values = DeploymentBaseHelmValues(
            dev_shm_size="500Mi",  # type: ignore
            resources=resources,
        )
        self.assertEqual(deployment_values.dev_shm_size, 500)

    def test_dev_shm_size_serialization(self):
        """Test that dev_shm_size is correctly serialized to a string."""
        resources = ResourceLimitsRequests(
            limits=Resources(
                cpu=2000,
                memory=4000,
                accelerator_count=1,
                accelerator_key="nvidia.com/gpu",
            ),
            requests=Resources(
                cpu=1000,
                memory=1000,
                accelerator_count=0,
                accelerator_key="nvidia.com/gpu",
            ),
        )
        deployment_values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources)
        serialized = deployment_values.model_dump()
        self.assertEqual(serialized["dev_shm_size"], "500Mi")

    def test_supplemental_group_ids_serialization(self):
        """Test that supplemental_group_ids is correctly serialized."""
        resources = ResourceLimitsRequests(
            limits=Resources(
                cpu=2000,
                memory=4000,
                accelerator_count=1,
                accelerator_key="nvidia.com/gpu",
            ),
            requests=Resources(
                cpu=1000,
                memory=1000,
                accelerator_count=0,
                accelerator_key="nvidia.com/gpu",
            ),
        )
        deployment_values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources)
        serialized = deployment_values.model_dump()
        self.assertIsNone(serialized["supplemental_group_ids"])

    def test_helm_params(self):
        """Test that helm_params method returns the correct list of parameters."""
        resources = ResourceLimitsRequests(
            limits=Resources(
                cpu=2000,
                memory=4000,
                accelerator_key="nvidia.com/gpu",
                gpus=1,
                accelerator_count=2,
            ),
            requests=Resources(
                cpu=1000,
                memory=1000,
                gpus=2,
                accelerator_key="nvidia.com/gpu",
                infiniband_count=2,
            ),
        )
        deployment_values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources)
        params = deployment_values.helm_params()
        expected_params = [
            {"name": "dev_shm_size", "value": "500Mi", "forceString": True},
            {"name": "resources.limits.cpu", "value": "2", "forceString": True},
            {"name": "resources.limits.gpus", "value": "1", "forceString": False},
            {"name": "resources.limits.memory", "value": "4000Mi", "forceString": True},
            {
                "name": "resources.limits.accelerator_key",
                "value": "nvidia.com/gpu",
                "forceString": True,
            },
            {
                "name": "resources.limits.accelerator_count",
                "value": "2",
                "forceString": False,
            },
            {
                "name": "resources.limits.infiniband_key",
                "value": "nvidia.com/ib",
                "forceString": True,
            },
            {
                "name": "resources.limits.infiniband_count",
                "value": "0",
                "forceString": False,
            },
            {"name": "resources.requests.cpu", "value": "1", "forceString": True},
            {"name": "resources.requests.gpus", "value": "2", "forceString": False},
            {
                "name": "resources.requests.memory",
                "value": "1000Mi",
                "forceString": True,
            },
            {
                "name": "resources.requests.accelerator_key",
                "value": "nvidia.com/gpu",
                "forceString": True,
            },
            {
                "name": "resources.requests.accelerator_count",
                "value": "2",
                "forceString": False,
            },
            {
                "name": "resources.requests.infiniband_key",
                "value": "nvidia.com/ib",
                "forceString": True,
            },
            {
                "name": "resources.requests.infiniband_count",
                "value": "2",
                "forceString": False,
            },
            {"name": "supplemental_group_ids", "value": None, "forceString": False},
            {"name": "user_annotations", "value": None, "forceString": False},
            {"name": "scheduler_name", "value": "kai-scheduler", "forceString": True},
        ]
        self.assertEqual(len(params), len(expected_params))
        for param in expected_params:
            self.assertIn(param, params)

    def test_user_annotations_escaped_in_helm_params_only(self):
        """
        Dotted keys in user_annotations must be escaped for Helm --set, but the
        model's own field must keep the raw keys so DB round-trips (clone, GET/PUT)
        return the user's original input.
        """
        resources = ResourceLimitsRequests(
            limits=Resources(cpu=2000, memory=4000, accelerator_count=1),
            requests=Resources(cpu=1000, memory=1000, accelerator_count=0),
        )
        values = DeploymentBaseHelmValues(
            dev_shm_size=500,
            resources=resources,
            user_annotations={"balsfa.dfan": "v", "team": "infra"},
        )

        # Field unchanged after helm_params()
        params = values.helm_params()
        self.assertEqual(values.user_annotations, {"balsfa.dfan": "v", "team": "infra"})

        names = {p["name"]: p["value"] for p in params}
        self.assertEqual(names.get("user_annotations.balsfa\\.dfan"), "v")
        self.assertEqual(names.get("user_annotations.team"), "infra")
        # Pre-escape regression guard: no double-backslash
        self.assertNotIn("user_annotations.balsfa\\\\.dfan", names)

    def test_user_annotations_none_or_empty_no_escape_call(self):
        """None / empty user_annotations should not break helm_params()."""
        resources = ResourceLimitsRequests(
            limits=Resources(cpu=2000, memory=4000, accelerator_count=1),
            requests=Resources(cpu=1000, memory=1000, accelerator_count=0),
        )
        for ua in (None, {}):
            values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources, user_annotations=ua)
            # Should not raise; downstream Helm/ArgoCD handles the empty/None case.
            values.helm_params()


class TestJobDeploymentTemplate(unittest.TestCase):
    def test_indexed_completion_and_total_completions_env_are_always_rendered(self):
        template_path = Path(__file__).parent / "job_deployment/templates/job.yaml"
        template = template_path.read_text()

        completion_mode_block = "  completionMode: Indexed"
        env_block = """          env:
            - name: "JOB_COMPLETIONS"
              value: "{{ .Values.completions }}"
            {{- range $key, $value := .Values.env_vars }}"""

        self.assertIn(completion_mode_block, template)
        self.assertIn(env_block, template)

    def test_job_lifecycle_options_are_rendered(self):
        template_path = Path(__file__).parent / "job_deployment/templates/job.yaml"
        template = template_path.read_text()

        self.assertIn("  backoffLimit: {{ .Values.backoff_limit }}", template)
        self.assertIn("  ttlSecondsAfterFinished: 7200", template)
        self.assertIn("  {{- if .Values.active_deadline_seconds }}", template)
        self.assertIn("  activeDeadlineSeconds: {{ .Values.active_deadline_seconds }}", template)

    def test_user_annotations_render_as_pod_annotations_and_labels(self):
        template_path = Path(__file__).parent / "job_deployment/templates/job.yaml"
        template = template_path.read_text()

        annotation_block = """      annotations:
        sidecar.istio.io/inject: "false"
        platform.centml.ai/deployment-id: "{{ .Values.deployment_id }}"
        platform.centml.ai/revision-number: "1"
        platform.centml.ai/user-id: "{{ .Values.user_id }}"
        {{- if not .Values.enable_logging }}
        platform.centml.ai/log-exclude: "true"
        {{- end }}
        {{- if .Values.user_annotations }}
        {{- range $key, $value := .Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}"""
        label_block = """      labels:
        platform.centml.ai/deployment-id: "{{ .Values.deployment_id }}"
        platform.centml.ai/revision-number: "1"
        platform.centml.ai/workload_type: job
        {{- if .Values.user_annotations }}
        {{- range $key, $value := .Values.user_annotations }}
        ccluster.nvidia.com/user-{{ $key }}: {{ $value | quote }}
        {{- end }}
        {{- end }}"""

        self.assertIn(annotation_block, template)
        self.assertIn(label_block, template)

    def test_config_file_volume_wiring_is_rendered(self):
        template_path = Path(__file__).parent / "job_deployment/templates/job.yaml"
        template = template_path.read_text()

        self.assertIn('{{- template "setExtraConfigFiles" . }}', template)

        volume_mount_block = """            {{- range .ExtraConfigFiles }}
            - name: {{ .configMapName }}
              readOnly: {{ .volumeMountReadOnly }}
              subPath: {{ .volumeMountSubPath | quote }}
              mountPath: {{ .volumeMountMountPath | quote }}
            {{- end }}"""
        volume_block = """        {{- range .ExtraConfigFiles }}
        - name: {{ .configMapName }}
          configMap:
            name: {{ .configMapName }}
        {{- end }}"""

        self.assertIn(volume_mount_block, template)
        self.assertIn(volume_block, template)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestComputeDeploymentHelmTemplate(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parent /"compute_deployment"

    def _values(self, image_pull_secret=None) -> dict:
        resources = {
            "cpu": "1",
            "memory": "1Gi",
            "accelerator_count": 0,
            "accelerator_key": "nvidia.com/gpu",
        }
        return {
            "user_id": 42,
            "deployment_id": 100,
            "name": "compute-probe",
            "image_url": "ubuntu:24.04",
            "ssh_injector_image": "registry.local/compute-ssh-launcher:test",
            "image_pull_secret": image_pull_secret,
            "domain": "centml.ai",
            "container_port": 2222,
            "exposed_port": 22,
            "node_affinity_labels": {"zone": "a"},
            "resources": {"limits": resources, "requests": resources},
            "storage_class_name": "standard",
            "ssh_public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMockKey",
            "enable_logging": True,
        }

    def _render_dict(self, values_dict: dict) -> list[dict]:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(values_dict, f)
            values_path = f.name
        try:
            result = subprocess.run(
                [helm_bin, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return [d for d in yaml.safe_load_all(result.stdout.replace("\t", " ")) if d]

    def _find(self, docs: list[dict], kind: str, name: str) -> dict:
        matches = [d for d in docs if d.get("kind") == kind and d.get("metadata", {}).get("name") == name]
        self.assertEqual(len(matches), 1, f"expected exactly one {kind}/{name}, found {len(matches)}")
        return matches[0]

    def test_compute_deployment_renders_ssh_launcher_injection(self):
        docs = self._render_dict(self._values())
        deployment = self._find(docs, "Deployment", "compute-probe")
        pod_spec = deployment["spec"]["template"]["spec"]

        init_container = pod_spec["initContainers"][0]
        self.assertEqual(init_container["name"], "ssh-launcher-init")
        self.assertEqual(init_container["image"], "registry.local/compute-ssh-launcher:test")
        self.assertEqual(init_container["command"], ["/compute-ssh", "install", "/ssh-bin/compute-ssh"])

        user_container = pod_spec["containers"][0]
        self.assertEqual(user_container["name"], "user-container")
        self.assertEqual(user_container["command"], ["/ccluster/compute-ssh/compute-ssh"])
        self.assertIn({"containerPort": 2222, "protocol": "TCP"}, user_container["ports"])

        env = {entry["name"]: entry["value"] for entry in user_container["env"] if "value" in entry}
        self.assertEqual(env["CCLUSTER_SSH_ADDR"], ":2222")
        self.assertEqual(env["CCLUSTER_SSH_HOST_KEY_DIR"], "/home/.ccluster/ssh")
        self.assertEqual(env["CCLUSTER_SSH_WORKDIR"], "/home")
        self.assertEqual(env["SSH_PUBLIC_KEY_FILE"], "/authorized_keys")

        mount_names = {mount["name"]: mount["mountPath"] for mount in user_container["volumeMounts"]}
        self.assertEqual(mount_names["ssh-launcher"], "/ccluster/compute-ssh")
        self.assertEqual(mount_names["compute-volume"], "/home")
        self.assertEqual(mount_names["authorized-keys"], "/authorized_keys")

        volume_names = {volume["name"] for volume in pod_spec["volumes"]}
        self.assertIn("ssh-launcher", volume_names)
        self.assertIn("compute-volume", volume_names)

    def test_compute_image_pull_secret_renders_when_configured(self):
        docs = self._render_dict(
            self._values(
                image_pull_secret={
                    "server": "registry.example.com",
                    "username": "user",
                    "password": "pass",
                    "username_base64": "dXNlcg==",
                    "password_base64": "cGFzcw==",
                }
            )
        )
        deployment = self._find(docs, "Deployment", "compute-probe")
        image_pull_secret_names = [item["name"] for item in deployment["spec"]["template"]["spec"]["imagePullSecrets"]]
        self.assertIn("ecr-image-pull", image_pull_secret_names)
        self.assertIn("compute-probe-image-pull-secret", image_pull_secret_names)

        secret = self._find(docs, "Secret", "compute-probe-image-pull-secret")
        self.assertEqual(secret["type"], "kubernetes.io/dockerconfigjson")


class TestRolloutStrategy(unittest.TestCase):
    def test_rollout_strategy_rejects_both_zero(self):
        with self.assertRaises(ValidationError):
            RolloutStrategy(max_surge=0, max_unavailable=0)

    def test_rollout_strategy_rejects_negative_max_surge(self):
        with self.assertRaises(ValidationError):
            RolloutStrategy(max_surge=-1)

    def test_rollout_strategy_rejects_negative_max_unavailable(self):
        with self.assertRaises(ValidationError):
            RolloutStrategy(max_unavailable=-1)

    def test_rollout_strategy_accepts_valid_sums(self):
        RolloutStrategy(max_surge=1, max_unavailable=0)
        RolloutStrategy(max_surge=0, max_unavailable=1)
        RolloutStrategy(max_surge=2, max_unavailable=3)

    def test_rollout_strategy_defaults_are_valid(self):
        rs = RolloutStrategy()
        self.assertEqual(rs.max_surge, 0)
        self.assertEqual(rs.max_unavailable, 1)


class TestHPA(unittest.TestCase):
    def test_valid_defaults(self):
        hpa = HPA()
        self.assertEqual(hpa.min_replicas, 1)
        self.assertEqual(hpa.max_replicas, 1)

    def test_valid_custom_values(self):
        hpa = HPA(min_replicas=2, max_replicas=5)
        self.assertEqual(hpa.min_replicas, 2)
        self.assertEqual(hpa.max_replicas, 5)

    def test_min_replicas_zero_is_valid(self):
        hpa = HPA(min_replicas=0)
        self.assertEqual(hpa.min_replicas, 0)

    def test_min_replicas_negative_is_invalid(self):
        with self.assertRaises(ValidationError):
            HPA(min_replicas=-1)

    def test_max_replicas_less_than_min_is_invalid(self):
        with self.assertRaises(ValidationError) as ctx:
            HPA(min_replicas=3, max_replicas=2)
        self.assertIn("max_replicas", str(ctx.exception))

    def test_equal_min_max_replicas_is_valid(self):
        hpa = HPA(min_replicas=3, max_replicas=3)
        self.assertEqual(hpa.min_replicas, 3)
        self.assertEqual(hpa.max_replicas, 3)

    def test_cooldown_period_default(self):
        hpa = HPA()
        self.assertEqual(hpa.cooldown_period, 1800)

    def test_cooldown_period_custom(self):
        hpa = HPA(cooldown_period=600)
        self.assertEqual(hpa.cooldown_period, 600)

    def test_cooldown_period_negative_is_invalid(self):
        with self.assertRaises(ValidationError):
            HPA(cooldown_period=-1)


def _make_resources():
    return ResourceLimitsRequests(
        limits=Resources(cpu=2000, memory=4000, accelerator_count=1, accelerator_key="nvidia.com/gpu"),
        requests=Resources(cpu=1000, memory=1000, accelerator_count=1, accelerator_key="nvidia.com/gpu"),
    )


def _make_inference_values(**overrides):
    defaults = dict(
        user_id=42,
        deployment_id=100,
        name="test-deploy",
        image_url="nginx:latest",
        domain="centml.ai",
        container_port=8000,
        hpa=HPA(min_replicas=1, max_replicas=3),
        node_affinity_labels={"zone": "a"},
        revision_number=1,
        resources=_make_resources(),
    )
    defaults.update(overrides)
    return InferenceDeploymentValues(**defaults)


class TestInferenceDeploymentValuesSessionAffinity(unittest.TestCase):
    def test_default_session_affinity_is_false(self):
        vals = _make_inference_values()
        self.assertFalse(vals.session_affinity)

    def test_session_affinity_false_in_helm_params(self):
        # False value is required so Helm's {{- if .Values.session_affinity }}
        # evaluates to false and skips the DestinationRule template entirely.
        vals = _make_inference_values()
        params = vals.helm_params()
        sa = [p for p in params if p["name"] == "session_affinity"]
        self.assertEqual(len(sa), 1)
        self.assertEqual(sa[0]["value"], "False")

    def test_session_affinity_true_in_helm_params(self):
        vals = _make_inference_values(session_affinity=True)
        params = vals.helm_params()
        sa = [p for p in params if p["name"] == "session_affinity"]
        self.assertEqual(len(sa), 1)
        self.assertEqual(sa[0]["value"], "True")

    def test_backward_compat_missing_session_affinity_in_json(self):
        # Old app_config JSONB records (written before session_affinity existed)
        # must still deserialize without raising, defaulting to False.
        vals = _make_inference_values()
        json_data = vals.model_dump(mode="json")
        del json_data["session_affinity"]
        restored = InferenceDeploymentValues(**json_data)
        self.assertFalse(restored.session_affinity)

    def test_roundtrip_with_session_affinity(self):
        vals = _make_inference_values(session_affinity=True)
        json_data = vals.model_dump(mode="json")
        restored = InferenceDeploymentValues(**json_data)
        self.assertTrue(restored.session_affinity)


class TestInferenceDeploymentValuesContainerPort(unittest.TestCase):
    def test_reserved_container_port_rejected(self):
        for port in (15000, 15001, 15004, 15006, 15020, 15021, 15090):
            with self.subTest(port=port):
                with self.assertRaises(ValidationError) as ctx:
                    _make_inference_values(container_port=port)
                # Error must name the field the user set, not metrics.port.
                self.assertIn("container_port", str(ctx.exception))

    def test_non_reserved_container_port_accepted(self):
        vals = _make_inference_values(container_port=8080)
        self.assertEqual(vals.container_port, 8080)

    def test_container_port_out_of_range_rejected(self):
        for port in (0, 70000):
            with self.subTest(port=port), self.assertRaises(ValidationError):
                _make_inference_values(container_port=port)


class TestCServeV3ContainerPortGuardInherited(unittest.TestCase):
    """cserve_v3 has no request-level mirror, so it relies entirely on
    inheriting the catalog-layer container_port guard from
    InferenceDeploymentValues."""

    def _make_cserve_values(self, **overrides):
        defaults = dict(
            user_id=42,
            deployment_id=100,
            name="test-deploy",
            image_url="nginx:latest",
            domain="centml.ai",
            container_port=8000,
            hpa=HPA(min_replicas=1, max_replicas=3),
            node_affinity_labels={"zone": "a"},
            revision_number=1,
            resources=_make_resources(),
            # DeploymentInputV3 + cserve-specific required fields.
            model="tiny",
            ngpus=1,
            plan_yaml="deployment_input:\n  model: tiny\nrecipe_options: {}\n",
        )
        defaults.update(overrides)
        return CServeV3InferenceDeploymentValues(**defaults)

    def test_reserved_container_port_rejected(self):
        for port in (15000, 15001, 15004, 15006, 15020, 15021, 15090):
            with self.subTest(port=port):
                with self.assertRaises(ValidationError) as ctx:
                    self._make_cserve_values(container_port=port)
                self.assertIn("container_port", str(ctx.exception))

    def test_non_reserved_container_port_accepted(self):
        vals = self._make_cserve_values(container_port=8080)
        self.assertEqual(vals.container_port, 8080)


class TestMetricsConfigClass(unittest.TestCase):
    def test_both_fields_required(self):
        with self.assertRaises(ValidationError):
            MetricsConfig()  # type: ignore[call-arg]
        with self.assertRaises(ValidationError):
            MetricsConfig(port=9090)  # type: ignore[call-arg]
        with self.assertRaises(ValidationError):
            MetricsConfig(path="/m")  # type: ignore[call-arg]

    def test_explicit_values_accepted(self):
        cfg = MetricsConfig(port=9090, path="/m")
        self.assertEqual(cfg.port, 9090)
        self.assertEqual(cfg.path, "/m")

    def test_explicit_reserved_port_rejected(self):
        with self.assertRaises(ValidationError) as ctx:
            MetricsConfig(port=15090, path="/m")
        self.assertIn("reserved", str(ctx.exception))

    def test_path_must_start_with_slash(self):
        with self.assertRaises(ValidationError):
            MetricsConfig(port=9090, path="metrics")

    def test_path_rejects_query_string(self):
        with self.assertRaises(ValidationError):
            MetricsConfig(port=9090, path="/m?foo=bar")


class TestInferenceValuesMetricsPassthrough(unittest.TestCase):
    def test_metrics_omitted_stays_none(self):
        # Catalog does NOT auto-fill — the request layer is the only fill
        # site. Reconstruction from JSONB preserves whatever was persisted.
        vals = _make_inference_values(container_port=9000)
        self.assertIsNone(vals.metrics)

    def test_explicit_metrics_preserved_verbatim(self):
        vals = _make_inference_values(metrics=MetricsConfig(port=9090, path="/custom"), container_port=8000)
        self.assertEqual(vals.metrics.port, 9090)
        self.assertEqual(vals.metrics.path, "/custom")


class TestConfigFileMountValues(unittest.TestCase):
    VALID = {
        "filename": "config.yaml",
        "mount_path": "/etc/engine",
        "content": "engine: vllm\n",
    }

    def test_valid_entry(self):
        cf = ConfigFileMountValues(**self.VALID)
        self.assertEqual(cf.filename, "config.yaml")
        self.assertEqual(cf.mount_path, "/etc/engine")
        self.assertEqual(cf.content, "engine: vllm\n")

    def test_content_empty_raises(self):
        # Empty content is almost always user error — they forgot to paste.
        with self.assertRaises(ValidationError):
            ConfigFileMountValues(**{**self.VALID, "content": ""})

    def test_filename_empty_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMountValues(**{**self.VALID, "filename": ""})

    def test_filename_with_slash_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMountValues(**{**self.VALID, "filename": "dir/config.yaml"})

    def test_filename_non_ascii_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMountValues(**{**self.VALID, "filename": "配置.yaml"})

    def test_filename_too_long_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMountValues(**{**self.VALID, "filename": "a" * (CONFIGMAP_KEY_MAX_LENGTH + 1)})

    def test_filename_at_max_length_ok(self):
        ConfigFileMountValues(**{**self.VALID, "filename": "a" * CONFIGMAP_KEY_MAX_LENGTH})

    def test_filename_dotdir_rejected(self):
        # K8s hasChDirPrefix rejects ".", "..", and anything starting with "..".
        for bad in (".", "..", "..config", "..hidden.yaml"):
            with self.subTest(bad=bad), self.assertRaises(ValidationError):
                ConfigFileMountValues(**{**self.VALID, "filename": bad})

    def test_filename_single_dot_prefix_allowed(self):
        ConfigFileMountValues(**{**self.VALID, "filename": ".env"})

    def test_mount_path_relative_raises(self):
        with self.assertRaisesRegex(ValidationError, "absolute path"):
            ConfigFileMountValues(**{**self.VALID, "mount_path": "etc/engine"})

    def test_mount_path_root_raises(self):
        with self.assertRaisesRegex(ValidationError, "root"):
            ConfigFileMountValues(**{**self.VALID, "mount_path": "/"})

    def test_mount_path_forbidden_roots_raise(self):
        for bad in ("/proc", "/proc/1", "/sys/fs", "/dev", "/dev/null"):
            with (
                self.subTest(bad=bad),
                self.assertRaisesRegex(ValidationError, "must not be under"),
            ):
                ConfigFileMountValues(**{**self.VALID, "mount_path": bad})

    def test_mount_path_prefix_not_component_is_ok(self):
        # "/devtmp" must not be rejected as if it were under "/dev".
        cf = ConfigFileMountValues(**{**self.VALID, "mount_path": "/devtmp"})
        self.assertEqual(cf.mount_path, "/devtmp")

    def test_mount_path_is_normalized(self):
        cf = ConfigFileMountValues(**{**self.VALID, "mount_path": "/etc//engine/"})
        self.assertEqual(cf.mount_path, "/etc/engine")

    def test_content_at_max_bytes_ok(self):
        content = "x" * CONFIG_FILE_CONTENT_MAX_BYTES
        cf = ConfigFileMountValues(**{**self.VALID, "content": content})
        self.assertEqual(len(cf.content.encode("utf-8")), CONFIG_FILE_CONTENT_MAX_BYTES)

    def test_content_over_max_bytes_raises(self):
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMountValues(**{**self.VALID, "content": "x" * (CONFIG_FILE_CONTENT_MAX_BYTES + 1)})

    def test_content_utf8_byte_counting(self):
        # Each '中' is 3 UTF-8 bytes; the cap is bytes, not characters.
        max_chars = CONFIG_FILE_CONTENT_MAX_BYTES // 3
        ConfigFileMountValues(**{**self.VALID, "content": "中" * max_chars})  # OK
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMountValues(**{**self.VALID, "content": "中" * (max_chars + 1)})

    def test_mount_path_with_dotdot_rejected(self):
        # '..' segments would let "/etc/../proc/foo" slip past forbidden-roots check.
        for bad in ("/etc/../proc/kmsg", "/var/../../etc", "/foo/.."):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValidationError, "'..'"):
                ConfigFileMountValues(**{**self.VALID, "mount_path": bad})

    def test_mount_path_with_dotdot_segment_substring_allowed(self):
        # '/foo/..bar' contains '..' as part of a name, not as a path segment.
        cf = ConfigFileMountValues(**{**self.VALID, "mount_path": "/foo/..bar"})
        self.assertEqual(cf.mount_path, "/foo/..bar")

    def test_combined_path_within_limit_ok(self):
        # mount_path + '/' + filename must fit Linux PATH_MAX.
        filename = "a" * 100
        mount_path = "/" + "x" * (LINUX_PATH_MAX - len(filename) - 2)
        ConfigFileMountValues(**{**self.VALID, "filename": filename, "mount_path": mount_path})

    def test_combined_path_over_limit_raises(self):
        filename = "a" * 100
        mount_path = "/" + "x" * (LINUX_PATH_MAX - len(filename))  # 1 over the limit after concat
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMountValues(**{**self.VALID, "filename": filename, "mount_path": mount_path})

    def test_mount_path_multibyte_over_path_max_raises(self):
        # PATH_MAX is bytes; each '中' is 3 UTF-8 bytes, so 2000 of them = 6000 bytes > 4096.
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMountValues(**{**self.VALID, "mount_path": "/" + "中" * 2000})

    def test_combined_path_multibyte_byte_counting(self):
        # mount_path uses 1365 multibyte chars = 4095 bytes; filename 1 char = 1 byte;
        # combined = 4095 + 1 + 1 = 4097 bytes -> 1 over PATH_MAX.
        # Without byte-aware counting, len("/" + "中"*1364) = 1365 chars would pass.
        mount_path = "/" + "中" * 1364  # 1+1364 = 1365 chars; bytes = 1+1364*3 = 4093
        # adjust so combined byte count = 4097 (just over)
        # bytes = len(mount_path.encode()) = 4093; need filename to push total to 4097
        # combined_bytes = 4093 + 1 + len(filename); we want 4097 -> filename = 3
        filename = "abc"
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMountValues(**{**self.VALID, "filename": filename, "mount_path": mount_path})

    def test_content_base64_populated_and_decodable(self):
        cf = ConfigFileMountValues(**self.VALID)
        self.assertEqual(base64.b64decode(cf.content_base64).decode("utf-8"), cf.content)
        self.assertIn("content_base64", cf.model_dump())

    def test_api_schema_strips_content_base64(self):
        # The API response_model is the parent ConfigFileMount; constructing it from
        # the subclass' dump must drop content_base64 (FastAPI's serialization path).
        cf = ConfigFileMountValues(**self.VALID)
        api_view = ConfigFileMount.model_validate(cf.model_dump())
        self.assertNotIn("content_base64", api_view.model_dump())


class TestInferenceValuesConfigFile(unittest.TestCase):
    SAMPLE = 'engine: vllm\nargs: ["--model","foo"]\nstop: {a: ["\n\n"]}\n'

    def test_config_file_defaults_to_none(self):
        vals = _make_inference_values()
        self.assertIsNone(vals.config_file)

    def test_config_file_value_round_trips(self):
        cf = ConfigFileMountValues(filename="config.yaml", mount_path="/etc/engine", content=self.SAMPLE)
        vals = _make_inference_values(config_file=cf)
        self.assertEqual(vals.config_file.content, self.SAMPLE)

    def test_helm_params_emits_base64_and_empties_raw_content(self):
        vals = _make_inference_values(
            config_file=ConfigFileMountValues(filename="config.yaml", mount_path="/etc/engine", content=self.SAMPLE)
        )
        params = {p["name"]: p["value"] for p in vals.helm_params()}
        self.assertEqual(params["config_file.filename"], "config.yaml")
        self.assertEqual(params["config_file.mount_path"], "/etc/engine")
        # raw content is blanked on the helm wire (strvals can't parse { } [ ] in --set-string);
        # chart consumes content_base64 instead.
        self.assertEqual(params["config_file.content"], "")
        self.assertEqual(
            base64.b64decode(params["config_file.content_base64"]).decode("utf-8"),
            self.SAMPLE,
        )

    def test_helm_params_idempotent_across_round_trips(self):
        # DB load -> instance rebuild -> validator re-runs -> content_base64 re-derived.
        original = _make_inference_values(
            config_file=ConfigFileMountValues(filename="a.yaml", mount_path="/etc/x", content=self.SAMPLE)
        )
        rebuilt = InferenceDeploymentValues(**original.model_dump(mode="json"))
        params = {p["name"]: p["value"] for p in rebuilt.helm_params()}
        self.assertEqual(
            base64.b64decode(params["config_file.content_base64"]).decode("utf-8"),
            self.SAMPLE,
        )

    def test_no_config_file_emits_no_content_param(self):
        vals = _make_inference_values()
        params = {p["name"]: p["value"] for p in vals.helm_params()}
        self.assertNotIn("config_file.content_base64", params)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateConfigFile(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parent /"inference_deployment_v3"

    def _values(self, config_file):
        resources_one = {
            "cpu": "1",
            "memory": "1Gi",
            "accelerator_count": 1,
            "accelerator_key": "nvidia.com/gpu",
        }
        return {
            "user_id": 42,
            "deployment_id": 100,
            "name": "probe",
            "image_url": "nginx:latest",
            "domain": "centml.ai",
            "container_port": 8000,
            "hpa": {"min_replicas": 1, "max_replicas": 1},
            "node_affinity_labels": {"zone": "a"},
            "revision_number": 1,
            "rollout_strategy": {"max_surge": 0, "max_unavailable": 1},
            "resources": {"limits": resources_one, "requests": resources_one},
            "config_file": config_file,
            # When metrics is omitted it persists as null and the chart applies
            # the container_port + /metrics defaults at render time. Supply the
            # bundle explicitly here to exercise the chart's metrics-set branch.
            "metrics": {"port": 8000, "path": "/metrics"},
        }

    def _render_raw(self, values: dict) -> str:
        # @skipUnless guarantees _HELM_BIN is set; local copy narrows for mypy.
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(values, f)
            values_path = f.name
        try:
            result = subprocess.run(
                [
                    helm_bin,
                    "template",
                    "probe",
                    str(self.CHART_PATH),
                    "-f",
                    values_path,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return result.stdout

    def _extract_configmaps(self, raw: str):
        # Fail fast on YAMLError — silent skip would mask a broken render as an empty list.
        cms = []
        for doc in raw.split("\n---\n"):
            if "\nkind: ConfigMap" not in doc:
                continue
            try:
                parsed = yaml.safe_load(doc)
            except yaml.YAMLError as exc:
                self.fail(f"Failed to parse rendered ConfigMap YAML: {exc}\n---\n{doc}")
            if parsed and parsed.get("kind") == "ConfigMap":
                cms.append(parsed)
        return cms

    def test_no_config_file_emits_no_configmap(self):
        raw = self._render_raw(self._values(config_file=None))
        self.assertEqual(self._extract_configmaps(raw), [])

    def _config_file_fixture(self, content: str) -> dict:
        return ConfigFileMountValues(filename="config.yaml", mount_path="/etc/engine", content=content).model_dump(
            mode="json"
        )

    def test_config_file_renders_single_configmap(self):
        # The `|-` block chomp strips the final newline; rstrip the expected to match.
        content = "engine: vllm\nport: 8000\n"
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        cm = cms[0]
        self.assertEqual(cm["metadata"]["name"], "inference-config-100-r1")
        self.assertEqual(cm["metadata"]["namespace"], "user-42")
        self.assertEqual(cm["data"]["config.yaml"], content.rstrip("\n"))

    def test_special_chars_roundtrip_via_base64(self):
        content = 'engine: vllm\nhf_overrides: {"architectures":["X"]}\nstop: ["\n\n", "###"]\n'
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        self.assertEqual(cms[0]["data"]["config.yaml"], content.rstrip("\n"))

    def test_scalar_looking_filename_stays_a_string(self):
        # "true" passes the filename regex but must not render as a YAML bool:
        # k8s requires subPath and ConfigMap data keys to be strings.
        cf = ConfigFileMountValues(filename="true", mount_path="/etc/engine", content="x: 1\n").model_dump(mode="json")
        raw = self._render_raw(self._values(config_file=cf))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        self.assertEqual(list(cms[0]["data"].keys()), ["true"])
        self.assertIn('subPath: "true"', raw)


def _make_job_values(**overrides):
    defaults = dict(
        user_id=42,
        deployment_id=100,
        name="test-job",
        image_url="ubuntu:latest",
        node_affinity_labels={"zone": "a"},
        resources=_make_resources(),
    )
    defaults.update(overrides)
    return JobDeploymentValues(**defaults)


class TestJobValuesConfigFile(unittest.TestCase):
    SAMPLE = 'batch: 32\nargs: ["--epochs","10"]\nstop: {a: ["\n\n"]}\n'

    def test_config_file_defaults_to_none(self):
        vals = _make_job_values()
        self.assertIsNone(vals.config_file)

    def test_config_file_value_round_trips(self):
        cf = ConfigFileMountValues(filename="config.yaml", mount_path="/etc/job", content=self.SAMPLE)
        vals = _make_job_values(config_file=cf)
        self.assertEqual(vals.config_file.content, self.SAMPLE)

    def test_helm_params_emits_base64_and_empties_raw_content(self):
        vals = _make_job_values(
            config_file=ConfigFileMountValues(filename="config.yaml", mount_path="/etc/job", content=self.SAMPLE)
        )
        params = {p["name"]: p["value"] for p in vals.helm_params()}
        self.assertEqual(params["config_file.filename"], "config.yaml")
        self.assertEqual(params["config_file.mount_path"], "/etc/job")
        # raw content is blanked on the helm wire (strvals can't parse { } [ ] in --set-string);
        # chart consumes content_base64 instead.
        self.assertEqual(params["config_file.content"], "")
        self.assertEqual(
            base64.b64decode(params["config_file.content_base64"]).decode("utf-8"),
            self.SAMPLE,
        )

    def test_helm_params_idempotent_across_round_trips(self):
        # DB load -> instance rebuild -> validator re-runs -> content_base64 re-derived.
        original = _make_job_values(
            config_file=ConfigFileMountValues(filename="a.yaml", mount_path="/etc/x", content=self.SAMPLE)
        )
        rebuilt = JobDeploymentValues(**original.model_dump(mode="json"))
        params = {p["name"]: p["value"] for p in rebuilt.helm_params()}
        self.assertEqual(
            base64.b64decode(params["config_file.content_base64"]).decode("utf-8"),
            self.SAMPLE,
        )

    def test_no_config_file_emits_no_content_param(self):
        vals = _make_job_values()
        params = {p["name"]: p["value"] for p in vals.helm_params()}
        self.assertNotIn("config_file.content_base64", params)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestJobDeploymentHelmTemplateConfigFile(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parent /"job_deployment"

    def _values(self, config_file) -> dict:
        # Render from the real values-model dump so the test exercises the exact
        # JobDeploymentValues -> chart contract (defaults included).
        return _make_job_values(config_file=config_file).model_dump(mode="json")

    def _render_raw(self, values: dict) -> str:
        # @skipUnless guarantees _HELM_BIN is set; local copy narrows for mypy.
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(values, f)
            values_path = f.name
        try:
            result = subprocess.run(
                [
                    helm_bin,
                    "template",
                    "probe",
                    str(self.CHART_PATH),
                    "-f",
                    values_path,
                ],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return result.stdout

    def _extract_kind(self, raw: str, kind: str):
        # Fail fast on YAMLError — silent skip would mask a broken render as an empty list.
        docs = []
        for doc in raw.split("\n---\n"):
            if f"\nkind: {kind}" not in doc:
                continue
            try:
                parsed = yaml.safe_load(doc)
            except yaml.YAMLError as exc:
                self.fail(f"Failed to parse rendered {kind} YAML: {exc}\n---\n{doc}")
            if parsed and parsed.get("kind") == kind:
                docs.append(parsed)
        return docs

    def _config_file_fixture(self, content: str) -> ConfigFileMountValues:
        return ConfigFileMountValues(filename="config.yaml", mount_path="/etc/job", content=content)

    def test_no_config_file_emits_no_configmap(self):
        raw = self._render_raw(self._values(config_file=None))
        self.assertEqual(self._extract_kind(raw, "ConfigMap"), [])

    def test_config_file_renders_single_configmap(self):
        # The `|-` block chomp strips the final newline; rstrip the expected to match.
        content = "batch: 32\nepochs: 10\n"
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_kind(raw, "ConfigMap")
        self.assertEqual(len(cms), 1)
        cm = cms[0]
        self.assertEqual(cm["metadata"]["name"], "job-config-100")
        self.assertEqual(cm["metadata"]["namespace"], "user-42")
        self.assertEqual(cm["data"]["config.yaml"], content.rstrip("\n"))

    def test_special_chars_roundtrip_via_base64(self):
        content = 'batch: 32\nhf_overrides: {"architectures":["X"]}\nstop: ["\n\n", "###"]\n'
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_kind(raw, "ConfigMap")
        self.assertEqual(len(cms), 1)
        self.assertEqual(cms[0]["data"]["config.yaml"], content.rstrip("\n"))

    def test_pod_spec_mounts_config_file(self):
        raw = self._render_raw(self._values(config_file=self._config_file_fixture("batch: 32\n")))
        jobs = self._extract_kind(raw, "Job")
        self.assertEqual(len(jobs), 1)
        pod_spec = jobs[0]["spec"]["template"]["spec"]

        container = pod_spec["containers"][0]
        config_mounts = [m for m in container["volumeMounts"] if m["name"] == "job-config-100"]
        self.assertEqual(len(config_mounts), 1)
        mount = config_mounts[0]
        self.assertEqual(mount["mountPath"], "/etc/job/config.yaml")
        self.assertEqual(mount["subPath"], "config.yaml")
        self.assertTrue(mount["readOnly"])

        config_volumes = [v for v in pod_spec["volumes"] if v["name"] == "job-config-100"]
        self.assertEqual(len(config_volumes), 1)
        self.assertEqual(config_volumes[0]["configMap"]["name"], "job-config-100")

    def test_no_config_file_pod_spec_has_no_config_volume(self):
        raw = self._render_raw(self._values(config_file=None))
        jobs = self._extract_kind(raw, "Job")
        self.assertEqual(len(jobs), 1)
        pod_spec = jobs[0]["spec"]["template"]["spec"]
        self.assertEqual([m["name"] for m in pod_spec["containers"][0]["volumeMounts"]], ["dshm"])
        self.assertEqual([v["name"] for v in pod_spec["volumes"]], ["dshm"])

    def test_scalar_looking_filename_stays_a_string(self):
        # "true" passes the filename regex but must not render as a YAML bool:
        # k8s requires subPath and ConfigMap data keys to be strings.
        cf = ConfigFileMountValues(filename="true", mount_path="/etc/job", content="x: 1\n")
        raw = self._render_raw(self._values(config_file=cf))

        cms = self._extract_kind(raw, "ConfigMap")
        self.assertEqual(len(cms), 1)
        self.assertEqual(list(cms[0]["data"].keys()), ["true"])

        jobs = self._extract_kind(raw, "Job")
        mounts = jobs[0]["spec"]["template"]["spec"]["containers"][0]["volumeMounts"]
        mount = [m for m in mounts if m["name"] == "job-config-100"][0]
        self.assertEqual(mount["subPath"], "true")
        self.assertIsInstance(mount["subPath"], str)
        self.assertEqual(mount["mountPath"], "/etc/job/true")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateAppMetrics(unittest.TestCase):
    """app_metrics.yaml renders Service + ServiceMonitor when metrics is set,
    and is gracefully skipped when absent (legacy revisions persisted before
    the field existed)."""

    CHART_PATH = Path(__file__).resolve().parent /"inference_deployment_v3"

    def _render_dict(self, values_dict: dict) -> list[dict]:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(values_dict, f)
            values_path = f.name
        try:
            result = subprocess.run(
                [helm_bin, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return [d for d in yaml.safe_load_all(result.stdout.replace("\t", " ")) if d]

    def _render(self, vals: InferenceDeploymentValues) -> list[dict]:
        return self._render_dict(vals.model_dump(mode="json"))

    def _find(self, docs, kind: str, name: str):
        matches = [d for d in docs if d.get("kind") == kind and d.get("metadata", {}).get("name") == name]
        if not matches:
            return None
        self.assertEqual(len(matches), 1, f"expected exactly one {kind}/{name}, found {len(matches)}")
        return matches[0]

    def _vals_with_metrics(self, port: int = 8000, path: str = "/metrics", **overrides) -> InferenceDeploymentValues:
        return _make_inference_values(metrics=MetricsConfig(port=port, path=path), **overrides)

    def test_service_and_servicemonitor_always_rendered(self):
        # Chart now defaults: every inference_v3 deployment gets scrape resources.
        rendered = self._render(_make_inference_values())  # no metrics field
        svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        self.assertIsNotNone(svc)
        self.assertIsNotNone(sm)

    def test_chart_defaults_targetPort_to_container_port_when_metrics_none(self):
        rendered = self._render(_make_inference_values(container_port=8000))
        svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
        self.assertEqual(svc["spec"]["ports"][0]["targetPort"], 8000)

    def test_chart_defaults_path_to_slash_metrics_when_metrics_none(self):
        rendered = self._render(_make_inference_values())
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        self.assertEqual(sm["spec"]["endpoints"][0]["path"], "/metrics")

    def test_service_selector_uses_only_deployment_id(self):
        rendered = self._render(self._vals_with_metrics())
        svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
        self.assertEqual(svc["spec"]["selector"], {"platform.centml.ai/deployment-id": "100"})

    def test_service_port_is_fixed_18000(self):
        # Service-side port is platform convention (mirrors cserve scraper).
        # Pod-side targetPort follows the user's metrics.port.
        for user_port in (8000, 9090, 15432):
            rendered = self._render(self._vals_with_metrics(port=user_port, container_port=user_port))
            svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
            self.assertEqual(svc["spec"]["ports"][0]["port"], 18000)
            self.assertEqual(svc["spec"]["ports"][0]["targetPort"], user_port)

    def test_servicemonitor_path_matches_metrics_path(self):
        rendered = self._render(self._vals_with_metrics())
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        self.assertEqual(sm["spec"]["endpoints"][0]["path"], "/metrics")

    def test_custom_metrics_path_propagates_targetPort_follows_user(self):
        rendered = self._render(self._vals_with_metrics(port=9090, path="/custom"))
        svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        self.assertEqual(svc["spec"]["ports"][0]["targetPort"], 9090)
        self.assertEqual(sm["spec"]["endpoints"][0]["path"], "/custom")

    def test_enable_http2_false(self):
        rendered = self._render(self._vals_with_metrics())
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        self.assertEqual(sm["spec"]["endpoints"][0]["enableHttp2"], False)

    def test_deployment_id_injected_via_metric_relabeling(self):
        rendered = self._render(self._vals_with_metrics())
        sm = self._find(rendered, kind="ServiceMonitor", name="test-deploy-app-metrics")
        relabel = sm["spec"]["endpoints"][0]["metricRelabelings"][0]
        self.assertEqual(relabel["targetLabel"], "deployment_id")
        self.assertEqual(relabel["replacement"], "100")

    def test_app_metrics_service_declares_no_appprotocol(self):
        # The app-metrics Service shares the serving workload port (targetPort
        # defaults to container_port). Declaring appProtocol:http on it poisons
        # Istio's protocol resolution for that port: on affected clusters the
        # gRPC serving port gets treated as HTTP and gRPC traffic is reset
        # ("protocol error" / "connection termination"). Omitting appProtocol
        # lets Istio sniff (http/1.1,h2c), so a metrics misconfiguration can at
        # worst fail the scrape silently instead of breaking serving.
        for vals in (_make_inference_values(), self._vals_with_metrics(port=8000, path="/metrics")):
            rendered = self._render(vals)
            svc = self._find(rendered, kind="Service", name="test-deploy-app-metrics")
            port = svc["spec"]["ports"][0]
            self.assertNotIn("appProtocol", port)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestCServeV3DoesNotRenderAppMetrics(unittest.TestCase):
    """app_metrics.yaml is intentionally NOT symlinked into cserve_v3 — CServe
    metrics continue via the namespace-shared custom-metrics-scraper Service
    (which knows to scrape `CSERVE_METRICS_PORT=18000`). The per-deployment
    app-metrics Service+ServiceMonitor added by this PR are for inference_v3
    (user-supplied containers) only.

    This test guards against an accidental re-introduction of the symlink, which
    would create a permanent down-target on every CServe deployment (because
    CServe's metrics server binds 18000, but the chart would scrape
    .Values.metrics.port = container_port).
    """

    CHART_PATH = Path(__file__).resolve().parent /"cserve_deployment_v3"

    def _values(self, **overrides):
        resources_one = {
            "cpu": "1",
            "memory": "1Gi",
            "accelerator_count": 1,
            "accelerator_key": "nvidia.com/gpu",
        }
        base = {
            "user_id": 42,
            "deployment_id": 100,
            "name": "test-cserve",
            "image_url": "img",
            "domain": "centml.ai",
            "container_port": 8080,
            "hpa": {"min_replicas": 1, "max_replicas": 1},
            "node_affinity_labels": {"zone": "a"},
            "revision_number": 1,
            "rollout_strategy": {"max_surge": 0, "max_unavailable": 1},
            "resources": {"limits": resources_one, "requests": resources_one},
            "plan_yaml": "deployment_input:\n  model: tiny\nrecipe_options: {}\n",
            # Adversarial fixture: real cserve revisions persist metrics: null
            # (the cserve request schema does not expose this field). Forcing
            # a populated value here proves the chart never renders app_metrics
            # for cserve even when given concrete values — a guard against an
            # accidental app_metrics.yaml symlink in `cserve_deployment_v3/`.
            "metrics": {"port": 8080, "path": "/metrics"},
        }
        base.update(overrides)
        return base

    def _render_cserve_v3(self) -> list[dict]:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(self._values(), f)
            values_path = f.name
        try:
            result = subprocess.run(
                [helm_bin, "template", "test-cserve", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return [d for d in yaml.safe_load_all(result.stdout.replace("\t", " ")) if d]

    def _find(self, docs, kind: str, name: str):
        matches = [d for d in docs if d.get("kind") == kind and d.get("metadata", {}).get("name") == name]
        if not matches:
            return None
        return matches[0]

    def test_no_app_metrics_service_rendered(self):
        rendered = self._render_cserve_v3()
        svc = self._find(rendered, kind="Service", name="test-cserve-app-metrics")
        self.assertIsNone(svc, "cserve_v3 must not render the per-deployment app-metrics Service")

    def test_no_app_metrics_servicemonitor_rendered(self):
        rendered = self._render_cserve_v3()
        sm = self._find(rendered, kind="ServiceMonitor", name="test-cserve-app-metrics")
        self.assertIsNone(sm, "cserve_v3 must not render the per-deployment app-metrics ServiceMonitor")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateIngress(unittest.TestCase):
    """ingress.yaml must carry upstream-vhost so nginx sets the correct Host
    header when forwarding to the Istio sidecar.  Without it the sidecar cannot
    match any VirtualService and resets the connection — fatal for gRPC/HTTP2
    which relies on :authority for routing, and a latent risk for HTTP."""

    CHART_PATH = Path(__file__).resolve().parent /"inference_deployment_v3"

    def _render(self, vals: InferenceDeploymentValues) -> list[dict]:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(vals.model_dump(mode="json"), f)
            values_path = f.name
        try:
            result = subprocess.run(
                [helm_bin, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return [d for d in yaml.safe_load_all(result.stdout.replace("\t", " ")) if d]

    def _ingress(self, vals: InferenceDeploymentValues) -> dict:
        docs = self._render(vals)
        matches = [d for d in docs if d.get("kind") == "Ingress"]
        self.assertEqual(len(matches), 1, "expected exactly one Ingress")
        return matches[0]

    def _expected_vhost(self, vals: InferenceDeploymentValues) -> str:
        return f"{vals.name}.user-{vals.user_id}.svc.cluster.local"

    def test_upstream_vhost_present_for_http_deployment(self):
        vals = _make_inference_values()
        ingress = self._ingress(vals)
        annotations = ingress["metadata"]["annotations"]
        self.assertIn("nginx.ingress.kubernetes.io/upstream-vhost", annotations)
        self.assertEqual(
            annotations["nginx.ingress.kubernetes.io/upstream-vhost"],
            self._expected_vhost(vals),
        )

    def test_upstream_vhost_present_for_grpc_deployment(self):
        vals = _make_inference_values(backend_protocol=BackendProtocol.GRPC)
        ingress = self._ingress(vals)
        annotations = ingress["metadata"]["annotations"]
        self.assertIn("nginx.ingress.kubernetes.io/upstream-vhost", annotations)
        self.assertEqual(
            annotations["nginx.ingress.kubernetes.io/upstream-vhost"],
            self._expected_vhost(vals),
        )

    def test_upstream_vhost_fqdn_uses_deployment_name_and_user_id(self):
        vals = _make_inference_values(name="my-model", user_id=99)
        ingress = self._ingress(vals)
        vhost = ingress["metadata"]["annotations"]["nginx.ingress.kubernetes.io/upstream-vhost"]
        self.assertEqual(vhost, "my-model.user-99.svc.cluster.local")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateSchedulerName(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parent /"inference_deployment_v3"

    def _render(self, vals: InferenceDeploymentValues) -> list[dict]:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(vals.model_dump(mode="json"), f)
            values_path = f.name
        try:
            result = subprocess.run(
                [helm_bin, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return [d for d in yaml.safe_load_all(result.stdout.replace("\t", " ")) if d]

    def _rollout(self, docs: list[dict]) -> dict:
        matches = [d for d in docs if d.get("kind") == "Rollout"]
        self.assertEqual(len(matches), 1, "expected exactly one Rollout")
        return matches[0]

    def test_scheduler_name_injected_into_pod_spec(self):
        vals = _make_inference_values(scheduler_name="kai-scheduler")
        rollout = self._rollout(self._render(vals))
        scheduler = rollout["spec"]["template"]["spec"].get("schedulerName")
        self.assertEqual(scheduler, "kai-scheduler")

    def test_scheduler_name_absent_when_none(self):
        vals = _make_inference_values(scheduler_name=None)
        rollout = self._rollout(self._render(vals))
        self.assertNotIn("schedulerName", rollout["spec"]["template"]["spec"])


class ComputeDeploymentValuesBackwardCompatTest(unittest.TestCase):
    def _stored_values(self, **overrides) -> dict:
        # Mirrors a compute app_config["values"] dict as persisted on a revision.
        values = {
            "user_id": 1,
            "deployment_id": 2,
            "name": "legacy",
            "image_url": "ubuntu:24.04",
            "domain": "centml.ai",
            "container_port": 2222,
            "exposed_port": 22,
            "node_affinity_labels": {},
            "storage_class_name": "standard",
            "resources": {
                "limits": {"cpu": 1000, "memory": 2048, "accelerator_count": 1, "accelerator_key": "nvidia.com/gpu"},
                "requests": {"cpu": 1000, "memory": 2048, "accelerator_count": 1, "accelerator_key": "nvidia.com/gpu"},
            },
            "ssh_public_key": "ssh-ed25519 AAAA",
        }
        values.update(overrides)
        return values

    def test_legacy_jupyter_fields_are_ignored(self):
        # Compute deployments created before managed Jupyter was dropped persist
        # jupyter_token/jupyter_port in their app_config. Resuming or updating
        # such a deployment re-parses the stored values, so the model must accept
        # and discard the legacy fields rather than raise, and must not render
        # them back out.
        stored = self._stored_values(jupyter_token="legacy-token", jupyter_port=8888)

        values = ComputeDeploymentValues.model_validate(stored)

        dumped = values.model_dump()
        self.assertNotIn("jupyter_token", dumped)
        self.assertNotIn("jupyter_port", dumped)


if __name__ == "__main__":
    unittest.main()
