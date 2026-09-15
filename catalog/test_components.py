# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml
from pydantic import ValidationError

from catalog.components import (
    DeploymentBaseHelmValues,
    PersistentVolumeClaimToMount,
    ResourceLimitsRequests,
    Resources,
    cpu_resource_str,
    memory_resource_str,
)
from catalog.compute_deployment import ComputeDeploymentValues
from catalog.cserve_deployment_v3 import CServeV3InferenceDeploymentValues
from catalog.dynamo_deployment import DynamoDeploymentValues, DynamoWorkerPoolValues
from catalog.inference_deployment_v3 import (
    CONFIG_FILE_CONTENT_MAX_BYTES,
    CONFIGMAP_KEY_MAX_LENGTH,
    HPA,
    LINUX_PATH_MAX,
    BackendProtocol,
    ConfigFileMount,
    ConfigFileMountValues,
    ImagePullSecret,
    InferenceDeploymentValues,
    MetricsConfig,
    RolloutStrategy,
)
from catalog.job_deployment import JobDeploymentValues
from catalog.persistent_volume import (
    FilesystemPersistentVolumeValues,
    ObjectPersistentVolumeValues,
    PersistentVolumeConfig,
    PersistentVolumeValues,
)

_HELM_BIN = shutil.which("helm")
COMPONENT_TYPE_LABEL = "ccluster.nvidia.com/component-type"


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


class TestPersistentVolumeValues(unittest.TestCase):
    def _values(self) -> FilesystemPersistentVolumeValues:
        return FilesystemPersistentVolumeValues(
            user_id=42, volume_id=1001, name="my-volume", size_gb=250, storage_class_name="longhorn"
        )

    def test_helm_values_yaml_keeps_scalar_types(self):
        values = json.loads(self._values().helm_values_yaml())
        self.assertEqual(
            values,
            {
                "user_id": 42,
                "volume_id": 1001,
                "name": "my-volume",
                "backend": "filesystem",
                "size_gb": 250,
                "storage_class_name": "longhorn",
            },
        )

    def test_app_path(self):
        config = PersistentVolumeConfig(values=self._values())
        self.assertEqual(config.app_path, "catalog/src/catalog/persistent_volume")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestPersistentVolumeHelmTemplate(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "persistent_volume"

    def _render(self, values: PersistentVolumeValues) -> list[dict]:
        return _parse_rendered_docs(_helm_template(self.CHART_PATH, values.helm_values_yaml()))

    def _object_values(self, **overrides) -> ObjectPersistentVolumeValues:
        values = {
            "user_id": 42,
            "volume_id": 1001,
            "name": "my-volume",
            "bucket": "models-bucket",
            "region": "us-west-2",
        }
        values.update(overrides)
        return ObjectPersistentVolumeValues(**values)

    def _find(self, docs: list[dict], kind: str, name: str) -> dict:
        matches = [d for d in docs if d.get("kind") == kind and d.get("metadata", {}).get("name") == name]
        self.assertEqual(len(matches), 1, f"expected exactly one {kind}/{name}, found {len(matches)}")
        return matches[0]

    def test_renders_single_rwx_pvc_with_id_derived_name(self):
        values = FilesystemPersistentVolumeValues(
            user_id=42, volume_id=1001, name="my-volume", size_gb=250, storage_class_name="longhorn"
        )
        docs = self._render(values)

        # The chart renders exactly one object: the PVC, named from the volume id (never the
        # user-chosen name)
        self.assertEqual(len(docs), 1)
        pvc = self._find(docs, "PersistentVolumeClaim", "vol-1001")
        self.assertEqual(pvc["metadata"]["namespace"], "user-42")
        self.assertEqual(pvc["metadata"]["labels"]["platform.centml.ai/volume-id"], "1001")
        self.assertEqual(pvc["metadata"]["labels"]["platform.centml.ai/volume-name"], "my-volume")
        self.assertEqual(pvc["spec"]["accessModes"], ["ReadWriteMany"])
        self.assertEqual(pvc["spec"]["resources"]["requests"]["storage"], "250Gi")
        self.assertEqual(pvc["spec"]["storageClassName"], "longhorn")

    def test_storage_class_rendered_verbatim(self):
        values = FilesystemPersistentVolumeValues(
            user_id=42, volume_id=1001, name="my-volume", size_gb=100, storage_class_name="custom.class-01"
        )
        docs = self._render(values)
        pvc = self._find(docs, "PersistentVolumeClaim", "vol-1001")
        self.assertEqual(pvc["spec"]["storageClassName"], "custom.class-01")

    def test_large_ids_render_as_integers(self):
        # helm.values arrives as JSON, so ids land in Go as float64; without `| int`
        # anything >= 1e6 would render in scientific notation (vol-1.000002e+06).
        values = FilesystemPersistentVolumeValues(
            user_id=1000013, volume_id=1000002, name="my-volume", size_gb=250, storage_class_name="longhorn"
        )
        docs = self._render(values)
        pvc = self._find(docs, "PersistentVolumeClaim", "vol-1000002")
        self.assertEqual(pvc["metadata"]["namespace"], "user-1000013")
        self.assertEqual(pvc["metadata"]["labels"]["platform.centml.ai/volume-id"], "1000002")

    def test_renders_static_s3_pv_and_pvc(self):
        docs = self._render(self._object_values())

        self.assertEqual(len(docs), 2)
        pv = self._find(docs, "PersistentVolume", "vol-1001")
        pvc = self._find(docs, "PersistentVolumeClaim", "vol-1001")

        self.assertEqual(pv["metadata"]["labels"]["platform.centml.ai/volume-id"], "1001")
        self.assertEqual(pv["metadata"]["labels"]["platform.centml.ai/volume-name"], "my-volume")
        self.assertEqual(pv["spec"]["accessModes"], ["ReadWriteMany"])
        self.assertEqual(pv["spec"]["capacity"]["storage"], "1Gi")
        self.assertEqual(pv["spec"]["claimRef"], {"name": "vol-1001", "namespace": "user-42"})
        self.assertEqual(pv["spec"]["persistentVolumeReclaimPolicy"], "Retain")
        self.assertEqual(pv["spec"]["storageClassName"], "")
        self.assertEqual(pv["spec"]["volumeMode"], "Filesystem")
        self.assertEqual(pv["spec"]["mountOptions"], ["region us-west-2"])
        self.assertEqual(
            pv["spec"]["csi"],
            {
                "driver": "s3.csi.aws.com",
                "volumeAttributes": {
                    "authenticationSource": "driver",
                    "bucketName": "models-bucket",
                },
                "volumeHandle": "vol-1001",
            },
        )

        self.assertEqual(pvc["metadata"]["namespace"], "user-42")
        self.assertEqual(pvc["spec"]["accessModes"], ["ReadWriteMany"])
        self.assertEqual(pvc["spec"]["resources"]["requests"]["storage"], "1Gi")
        self.assertEqual(pvc["spec"]["storageClassName"], "")
        self.assertEqual(pvc["spec"]["volumeMode"], "Filesystem")
        self.assertEqual(pvc["spec"]["volumeName"], "vol-1001")
        self.assertFalse(any(doc.get("kind") == "StorageClass" for doc in docs))

    def test_renders_prefix_and_volume_read_only_as_mount_options(self):
        docs = self._render(self._object_values(prefix="models/v1/", read_only=True))

        pv = self._find(docs, "PersistentVolume", "vol-1001")
        self.assertEqual(
            pv["spec"]["mountOptions"],
            ["region us-west-2", "prefix models/v1/", "read-only"],
        )


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


class TestHelmValuesYaml(unittest.TestCase):
    def test_basic_serialization(self):
        """helm_values_yaml() produces the JSON document Helm parses, with correct values."""
        resources = ResourceLimitsRequests(
            limits=Resources(cpu=2000, memory=4000, accelerator_count=1, accelerator_key="nvidia.com/gpu"),
            requests=Resources(cpu=1000, memory=1000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
        )
        values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources)
        yaml_str = values.helm_values_yaml()
        parsed = json.loads(yaml_str)

        self.assertEqual(parsed["dev_shm_size"], "500Mi")
        self.assertEqual(parsed["resources"]["limits"]["cpu"], "2")
        self.assertEqual(parsed["resources"]["limits"]["memory"], "4000Mi")
        self.assertEqual(parsed["resources"]["requests"]["cpu"], "1")

    def test_none_values_rendered_as_null(self):
        """None values are rendered as null (Helm treats them identically to absent)."""
        resources = ResourceLimitsRequests(
            limits=Resources(cpu=1000, memory=1000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
            requests=Resources(cpu=500, memory=500, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
        )
        values = DeploymentBaseHelmValues(dev_shm_size=None, resources=resources)
        yaml_str = values.helm_values_yaml()
        parsed = json.loads(yaml_str)

        self.assertIsNone(parsed["supplemental_group_ids"])

    def test_string_types_preserved(self):
        """Strings that look like numbers stay as strings in the serialized document."""
        resources = ResourceLimitsRequests(
            limits=Resources(cpu=16000, memory=32000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
            requests=Resources(cpu=8000, memory=16000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
        )
        values = DeploymentBaseHelmValues(dev_shm_size=500, resources=resources)
        yaml_str = values.helm_values_yaml()
        parsed = json.loads(yaml_str)

        # CPU "16" should remain a string, not become int 16
        self.assertIsInstance(parsed["resources"]["limits"]["cpu"], str)
        self.assertEqual(parsed["resources"]["limits"]["cpu"], "16")

    def test_number_like_string_env_vars_survive_as_strings(self):
        """Number-like env values (zero-padded ids, "1e-5", octal modes) must stay strings.

        Helm parses helm.values through a Go YAML->JSON parser that coerces bare
        number-like scalars to numbers; the serializer must keep them quoted. Parsing
        the output as JSON mirrors exactly what Helm sees, so a PyYAML roundtrip (which
        re-reads its own quoting) would not catch the coercion this guards against.
        """
        from catalog.inference_deployment_v3 import InferenceDeploymentValues

        env_vars = {
            "MONTH": "08",
            "POSTAL_CODE": "01890",
            "ACCOUNT_ID": "012345678901",
            "FILE_MODE": "00640",
            "LEARNING_RATE": "1e-5",
            "CHECKSUM": "0e462097431906509019562988736854",
        }
        instance = InferenceDeploymentValues(
            user_id=1,
            deployment_id=1,
            name="test",
            image_url="test-image",
            domain="test.com",
            container_port=8080,
            hpa={"metric_name": "test_metric"},
            revision_number=1,
            node_affinity_labels={},
            env_vars=env_vars,
            resources=ResourceLimitsRequests(
                limits=Resources(cpu=1000, memory=1000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
                requests=Resources(cpu=500, memory=500, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
            ),
        )

        parsed = json.loads(instance.helm_values_yaml())

        self.assertEqual(parsed["env_vars"], env_vars)
        for key in env_vars:
            self.assertIsInstance(parsed["env_vars"][key], str)

    def test_image_pull_secret_plain_fields_in_yaml(self):
        """helm_values_yaml() includes plain image_pull_secret fields with $ preserved."""
        from catalog.inference_deployment_v3 import InferenceDeploymentValues

        instance = InferenceDeploymentValues(
            user_id=1,
            deployment_id=1,
            name="test",
            image_url="test-image",
            domain="test.com",
            container_port=8080,
            hpa={"metric_name": "test_metric"},
            revision_number=1,
            node_affinity_labels={},
            image_pull_secret={
                "server": "nvcr.io",
                "username": "$oauthtoken",
                "password": "secret-key-123",
            },
            resources=ResourceLimitsRequests(
                limits=Resources(cpu=1000, memory=1000, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
                requests=Resources(cpu=500, memory=500, accelerator_count=0, accelerator_key="nvidia.com/gpu"),
            ),
        )

        yaml_str = instance.helm_values_yaml()
        parsed = json.loads(yaml_str)

        secret = parsed["image_pull_secret"]
        self.assertEqual(secret["server"], "nvcr.io")
        self.assertEqual(secret["username"], "$oauthtoken")
        self.assertEqual(secret["password"], "secret-key-123")
        # The *_base64 shadow stays on the wire: charts pinned via chart_revision
        # still b64dec these fields (removal tracked in #3998).
        self.assertEqual(base64.b64decode(secret["username_base64"]).decode(), "$oauthtoken")
        self.assertEqual(base64.b64decode(secret["password_base64"]).decode(), "secret-key-123")


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

    def test_session_affinity_false_in_helm_values(self):
        # False value is required so Helm's {{- if .Values.session_affinity }}
        # evaluates to false and skips the DestinationRule template entirely.
        vals = _make_inference_values()
        values = json.loads(vals.helm_values_yaml())
        self.assertIs(values["session_affinity"], False)

    def test_session_affinity_true_in_helm_values(self):
        vals = _make_inference_values(session_affinity=True)
        values = json.loads(vals.helm_values_yaml())
        self.assertIs(values["session_affinity"], True)

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


class TestConfigFileMount(unittest.TestCase):
    VALID = {
        "filename": "config.yaml",
        "mount_path": "/etc/engine",
        "content": "engine: vllm\n",
    }

    def test_valid_entry(self):
        cf = ConfigFileMount(**self.VALID)
        self.assertEqual(cf.filename, "config.yaml")
        self.assertEqual(cf.mount_path, "/etc/engine")
        self.assertEqual(cf.content, "engine: vllm\n")

    def test_content_empty_raises(self):
        # Empty content is almost always user error — they forgot to paste.
        with self.assertRaises(ValidationError):
            ConfigFileMount(**{**self.VALID, "content": ""})

    def test_filename_empty_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMount(**{**self.VALID, "filename": ""})

    def test_filename_with_slash_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMount(**{**self.VALID, "filename": "dir/config.yaml"})

    def test_filename_non_ascii_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMount(**{**self.VALID, "filename": "配置.yaml"})

    def test_filename_too_long_raises(self):
        with self.assertRaises(ValidationError):
            ConfigFileMount(**{**self.VALID, "filename": "a" * (CONFIGMAP_KEY_MAX_LENGTH + 1)})

    def test_filename_at_max_length_ok(self):
        ConfigFileMount(**{**self.VALID, "filename": "a" * CONFIGMAP_KEY_MAX_LENGTH})

    def test_filename_dotdir_rejected(self):
        # K8s hasChDirPrefix rejects ".", "..", and anything starting with "..".
        for bad in (".", "..", "..config", "..hidden.yaml"):
            with self.subTest(bad=bad), self.assertRaises(ValidationError):
                ConfigFileMount(**{**self.VALID, "filename": bad})

    def test_filename_single_dot_prefix_allowed(self):
        ConfigFileMount(**{**self.VALID, "filename": ".env"})

    def test_mount_path_relative_raises(self):
        with self.assertRaisesRegex(ValidationError, "absolute path"):
            ConfigFileMount(**{**self.VALID, "mount_path": "etc/engine"})

    def test_mount_path_root_raises(self):
        with self.assertRaisesRegex(ValidationError, "root"):
            ConfigFileMount(**{**self.VALID, "mount_path": "/"})

    def test_mount_path_forbidden_roots_raise(self):
        for bad in ("/proc", "/proc/1", "/sys/fs", "/dev", "/dev/null"):
            with (
                self.subTest(bad=bad),
                self.assertRaisesRegex(ValidationError, "must not be under"),
            ):
                ConfigFileMount(**{**self.VALID, "mount_path": bad})

    def test_mount_path_prefix_not_component_is_ok(self):
        # "/devtmp" must not be rejected as if it were under "/dev".
        cf = ConfigFileMount(**{**self.VALID, "mount_path": "/devtmp"})
        self.assertEqual(cf.mount_path, "/devtmp")

    def test_mount_path_is_normalized(self):
        cf = ConfigFileMount(**{**self.VALID, "mount_path": "/etc//engine/"})
        self.assertEqual(cf.mount_path, "/etc/engine")

    def test_double_leading_slash_representation_is_preserved(self):
        cf = ConfigFileMount(**{**self.VALID, "mount_path": "//etc/engine"})
        self.assertEqual(cf.mount_path, "//etc/engine")

    def test_content_at_max_bytes_ok(self):
        content = "x" * CONFIG_FILE_CONTENT_MAX_BYTES
        cf = ConfigFileMount(**{**self.VALID, "content": content})
        self.assertEqual(len(cf.content.encode("utf-8")), CONFIG_FILE_CONTENT_MAX_BYTES)

    def test_content_over_max_bytes_raises(self):
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMount(**{**self.VALID, "content": "x" * (CONFIG_FILE_CONTENT_MAX_BYTES + 1)})

    def test_content_utf8_byte_counting(self):
        # Each '中' is 3 UTF-8 bytes; the cap is bytes, not characters.
        max_chars = CONFIG_FILE_CONTENT_MAX_BYTES // 3
        ConfigFileMount(**{**self.VALID, "content": "中" * max_chars})  # OK
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMount(**{**self.VALID, "content": "中" * (max_chars + 1)})

    def test_mount_path_with_dotdot_rejected(self):
        # '..' segments would let "/etc/../proc/foo" slip past forbidden-roots check.
        for bad in ("/etc/../proc/kmsg", "/var/../../etc", "/foo/.."):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValidationError, "'..'"):
                ConfigFileMount(**{**self.VALID, "mount_path": bad})

    def test_mount_path_with_dotdot_segment_substring_allowed(self):
        # '/foo/..bar' contains '..' as part of a name, not as a path segment.
        cf = ConfigFileMount(**{**self.VALID, "mount_path": "/foo/..bar"})
        self.assertEqual(cf.mount_path, "/foo/..bar")

    def test_combined_path_within_limit_ok(self):
        # mount_path + '/' + filename must fit Linux PATH_MAX.
        filename = "a" * 100
        mount_path = "/" + "x" * (LINUX_PATH_MAX - len(filename) - 2)
        ConfigFileMount(**{**self.VALID, "filename": filename, "mount_path": mount_path})

    def test_combined_path_over_limit_raises(self):
        filename = "a" * 100
        mount_path = "/" + "x" * (LINUX_PATH_MAX - len(filename))  # 1 over the limit after concat
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMount(**{**self.VALID, "filename": filename, "mount_path": mount_path})

    def test_mount_path_multibyte_over_path_max_raises(self):
        # PATH_MAX is bytes; each '中' is 3 UTF-8 bytes, so 2000 of them = 6000 bytes > 4096.
        with self.assertRaisesRegex(ValidationError, "at most"):
            ConfigFileMount(**{**self.VALID, "mount_path": "/" + "中" * 2000})

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
            ConfigFileMount(**{**self.VALID, "filename": filename, "mount_path": mount_path})


class TestInferenceValuesConfigFile(unittest.TestCase):
    SAMPLE = 'engine: vllm\nargs: ["--model","foo"]\nstop: {a: ["\n\n"]}\n'

    def test_config_file_defaults_to_none(self):
        vals = _make_inference_values()
        self.assertIsNone(vals.config_file)

    def test_config_file_value_round_trips(self):
        cf = ConfigFileMountValues(filename="config.yaml", mount_path="/etc/engine", content=self.SAMPLE)
        vals = _make_inference_values(config_file=cf)
        self.assertEqual(vals.config_file.content, self.SAMPLE)

    def test_helm_values_carry_content_base64_shadow(self):
        vals = _make_inference_values(
            config_file=ConfigFileMountValues(filename="config.yaml", mount_path="/etc/engine", content=self.SAMPLE)
        )
        config_file = json.loads(vals.helm_values_yaml())["config_file"]
        self.assertEqual(config_file["filename"], "config.yaml")
        self.assertEqual(config_file["mount_path"], "/etc/engine")
        # by_alias blanks raw content on the wire; charts (incl. chart_revision-pinned
        # ones) consume content_base64 — removal tracked in #3998.
        self.assertEqual(config_file["content"], "")
        self.assertEqual(base64.b64decode(config_file["content_base64"]).decode("utf-8"), self.SAMPLE)

    def test_helm_values_idempotent_across_round_trips(self):
        # DB load -> instance rebuild -> validator re-derives content_base64.
        original = _make_inference_values(
            config_file=ConfigFileMountValues(filename="a.yaml", mount_path="/etc/x", content=self.SAMPLE)
        )
        rebuilt = InferenceDeploymentValues(**original.model_dump(mode="json"))
        config_file = json.loads(rebuilt.helm_values_yaml())["config_file"]
        self.assertEqual(base64.b64decode(config_file["content_base64"]).decode("utf-8"), self.SAMPLE)

    def test_no_config_file_emits_null_config_file(self):
        vals = _make_inference_values()
        values = json.loads(vals.helm_values_yaml())
        self.assertIsNone(values["config_file"])


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateNumberLikeEnvVars(unittest.TestCase):
    """End-to-end guard: number-like env values survive helm_values_yaml() -> Helm render.

    Writes the production helm.values serialization (not a re-dumped dict) to the values
    file, so a Go-parser coercion of a bare number-like scalar would surface as a changed
    env value in the rendered Rollout.
    """

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

    ENV_VARS = {
        "MONTH": "08",
        "POSTAL_CODE": "01890",
        "ACCOUNT_ID": "012345678901",
        "FILE_MODE": "00640",
        "LEARNING_RATE": "1e-5",
        "CHECKSUM": "0e462097431906509019562988736854",
    }

    def _render(self, values_yaml: str) -> str:
        assert _HELM_BIN is not None
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            f.write(values_yaml)
            values_path = f.name
        try:
            result = subprocess.run(
                [_HELM_BIN, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        return result.stdout

    def test_number_like_env_values_render_verbatim(self):
        vals = _make_inference_values(env_vars=self.ENV_VARS)
        raw = self._render(vals.helm_values_yaml())

        # The rendered Rollout carries pre-existing trailing tabs (Go YAML tolerates them,
        # PyYAML does not); strip trailing whitespace per line before parsing. Env values are
        # quoted, so whitespace outside the quotes is insignificant.
        cleaned = "\n".join(line.rstrip() for line in raw.splitlines())
        rollout = next(doc for doc in yaml.safe_load_all(cleaned) if doc and doc.get("kind") == "Rollout")
        containers = rollout["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}

        for key, expected in self.ENV_VARS.items():
            self.assertEqual(env[key], expected)

    def test_pod_template_has_standardized_component_type_label(self):
        raw = self._render(_make_inference_values().helm_values_yaml())
        cleaned = "\n".join(line.rstrip() for line in raw.splitlines())
        rollout = next(doc for doc in yaml.safe_load_all(cleaned) if doc and doc.get("kind") == "Rollout")

        self.assertEqual(
            rollout["spec"]["template"]["metadata"]["labels"][COMPONENT_TYPE_LABEL],
            "user-container",
        )
        self.assertNotIn(COMPONENT_TYPE_LABEL, rollout["spec"]["selector"]["matchLabels"])


def _helm_template(chart_path: Path, values_yaml: str) -> str:
    """Render the production helm_values_yaml() string through `helm template`."""
    assert _HELM_BIN is not None
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
        f.write(values_yaml)
        values_path = f.name
    try:
        result = subprocess.run(
            [_HELM_BIN, "template", "probe", str(chart_path), "-f", values_path],
            check=True,
            capture_output=True,
            text=True,
        )
    finally:
        os.unlink(values_path)
    return result.stdout


def _parse_rendered_docs(raw: str) -> list[dict]:
    """Parse rendered manifests; strips the pre-existing trailing tabs PyYAML rejects."""
    cleaned = "\n".join(line.rstrip() for line in raw.splitlines())
    return [doc for doc in yaml.safe_load_all(cleaned) if doc]


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateSpecialCharValues(unittest.TestCase):
    """End-to-end guard for the chart's `| quote` hardening: hostile env values and
    command args reach the container verbatim through helm_values_yaml() ->
    `helm template`. The `| quote` on each value is what lets an embedded double
    quote render as valid YAML."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

    def _rollout_container(self, values_yaml: str) -> dict:
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, values_yaml))
        rollout = next(doc for doc in docs if doc.get("kind") == "Rollout")
        containers = rollout["spec"]["template"]["spec"]["containers"]
        return next(c for c in containers if c["name"] == "user-container")

    def test_hostile_env_values_render_verbatim(self):
        env_vars = {
            "QUOTED": 'value with "double quotes" inside',
            "MIXED": 'it\'s a "complex" value',
            "SINGLE_QUOTE": "it's",
            "CONFIG": '{"key": "value", "nested": {"a": 1}}',
            "BRACKETS": "[1, 2, 3]",
            "COMMAS": "a,b,c",
            "TOKEN": "$ecret*value&more|pipe",
            "GLOB": "*|?",
        }
        container = self._rollout_container(_make_inference_values(env_vars=env_vars).helm_values_yaml())
        env = {e["name"]: e["value"] for e in container["env"]}
        for key, expected in env_vars.items():
            with self.subTest(key=key):
                self.assertEqual(env[key], expected)

    def test_yaml_bool_env_values_render_verbatim(self):
        # go-yaml (Helm) resolves y/n/Y/N/yes/no/on/off/true/false to booleans; the JSON
        # serializer's quoting keeps them strings so they reach the container verbatim.
        bool_words = ["y", "n", "Y", "N", "yes", "no", "on", "off", "true", "false"]
        env_vars = {f"BOOLY_{i}": v for i, v in enumerate(bool_words)}
        container = self._rollout_container(_make_inference_values(env_vars=env_vars).helm_values_yaml())
        env = {e["name"]: e["value"] for e in container["env"]}
        for key, expected in env_vars.items():
            with self.subTest(key=key):
                self.assertEqual(env[key], expected)

    def test_bool_like_env_key_stays_string(self):
        # The env var NAME is also `| quote`d: a key like YES/true would otherwise render as a
        # YAML bool and the apiserver rejects a non-string EnvVar.name.
        env_vars = {"YES": "a", "true": "b", "NORMAL": "c"}
        container = self._rollout_container(_make_inference_values(env_vars=env_vars).helm_values_yaml())
        names = {e["name"] for e in container["env"]}
        for key in env_vars:
            with self.subTest(key=key):
                self.assertIn(key, names)
                self.assertIsInstance(key, str)

    def test_bool_like_name_and_image_render_as_strings(self):
        # metadata.name and the container image are `| quote`d, so a value like "true" stays a
        # string instead of a YAML bool the apiserver rejects.
        docs = _parse_rendered_docs(
            _helm_template(self.CHART_PATH, _make_inference_values(name="true", image_url="true").helm_values_yaml())
        )
        rollout = next(doc for doc in docs if doc.get("kind") == "Rollout")
        self.assertIsInstance(rollout["metadata"]["name"], str)
        self.assertEqual(rollout["metadata"]["name"], "true")
        image = rollout["spec"]["template"]["spec"]["containers"][0]["image"]
        self.assertIsInstance(image, str)
        self.assertEqual(image, "true")

    def test_node_affinity_coercion_prone_labels_render_as_strings(self):
        # node_affinity keys/values render as `| quote`d scalars, so coercion-prone labels
        # (bool/number-like) stay strings and a reserved indicator like `@` doesn't break the
        # render. These are platform-derived today, but the quoting keeps the manifest type-valid.
        labels = {"true": "false", "123": "456", "flag": "yes", "@indicator": "@val"}
        docs = _parse_rendered_docs(
            _helm_template(self.CHART_PATH, _make_inference_values(node_affinity_labels=labels).helm_values_yaml())
        )
        rollout = next(doc for doc in docs if doc.get("kind") == "Rollout")
        terms = rollout["spec"]["template"]["spec"]["affinity"]["nodeAffinity"][
            "requiredDuringSchedulingIgnoredDuringExecution"
        ]["nodeSelectorTerms"][0]["matchExpressions"]
        rendered = {expr["key"]: expr["values"][0] for expr in terms}
        for key, value in labels.items():
            with self.subTest(key=key):
                self.assertIn(key, rendered)
                self.assertIsInstance(key, str)
                self.assertEqual(rendered[key], value)

    def test_hostile_hf_token_renders_verbatim(self):
        # hf_token renders the dedicated HF_TOKEN env var; it must go through | quote like
        # env_vars do, or a token containing a quote breaks the render and a newline corrupts.
        for token in ['ab"cd', "line\nbreak", "hf_normalToken123"]:
            with self.subTest(token=token):
                container = self._rollout_container(_make_inference_values(hf_token=token).helm_values_yaml())
                env = {e["name"]: e["value"] for e in container["env"]}
                self.assertEqual(env["HF_TOKEN"], token)

    def test_hostile_command_args_render_verbatim(self):
        command_args = [
            "-m",
            "vllm.entrypoints.openai.api_server",
            "--hf-overrides",
            '{"torch_dtype": "float16", "generation_config": {"max_length": 512}}',
            'echo "already quoted"',
            "*",
            "|",
            "{}<>?:{}|()_+***",
            "find /data -name '*.txt' | grep foo && echo done",
        ]
        vals = _make_inference_values(command=["bash"], command_args=command_args)
        container = self._rollout_container(vals.helm_values_yaml())
        self.assertEqual(container["command"], ["bash"])
        self.assertEqual(container["args"], command_args)

    def test_tritonserver_args_render_verbatim(self):
        triton_args = [
            "--model-repository=/registry",
            "--load-model=*",
            "--model-control-mode=explicit",
            "--http-port=8000",
            "--http-thread-count=128",
            "--http-header-forward-pattern=.*",
            "--grpc-port=8001",
            "--pinned-memory-pool-byte-size=2147483648",
            "--cuda-memory-pool-byte-size=0:8589934592",
            "--backend-config=python,shm-default-byte-size=4194304",
            "--backend-config=python,shm-growth-byte-size=2097152",
            "--backend-config=python,default-max-batch-size=256",
            "--metrics-config=summary_latencies=true",
            "--metrics-config=histogram_latencies=false",
            "--metrics-port=8002",
        ]
        vals = _make_inference_values(command=["tritonserver"], command_args=triton_args)
        container = self._rollout_container(vals.helm_values_yaml())
        self.assertEqual(container["command"], ["tritonserver"])
        self.assertEqual(container["args"], triton_args)

    def test_bool_like_name_renders_string_labels_on_scaled_object_and_pdb(self):
        # ScaledObject and PDB carry an `app: <name>` label value; `| quote` keeps a
        # name like "true" a string instead of a YAML bool the apiserver rejects.
        docs = _parse_rendered_docs(
            _helm_template(self.CHART_PATH, _make_inference_values(name="true").helm_values_yaml())
        )
        for kind in ["ScaledObject", "PodDisruptionBudget"]:
            with self.subTest(kind=kind):
                doc = next(d for d in docs if d.get("kind") == kind)
                label = doc["metadata"]["labels"]["app"]
                self.assertIsInstance(label, str)
                self.assertEqual(label, "true")

    def test_hostile_healthcheck_path_renders_verbatim(self):
        # healthcheck is an unconstrained user string; `| quote` keeps a bool-like value a
        # string and a value containing `: ` from being parsed as a YAML mapping.
        for path in ["/health: bad", "true", "\t/v1/health/ready"]:
            with self.subTest(path=path):
                container = self._rollout_container(_make_inference_values(healthcheck=path).helm_values_yaml())
                self.assertEqual(container["readinessProbe"]["httpGet"]["path"], path)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateConfigFile(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

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
        # binaryData preserves the payload byte-for-byte, trailing newline included.
        content = "engine: vllm\nport: 8000\n"
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        cm = cms[0]
        self.assertEqual(cm["metadata"]["name"], "inference-config-100-r1")
        self.assertEqual(cm["metadata"]["namespace"], "user-42")
        self.assertEqual(base64.b64decode(cm["binaryData"]["config.yaml"]).decode(), content)

    def test_special_chars_roundtrip(self):
        content = 'engine: vllm\nhf_overrides: {"architectures":["X"]}\nstop: ["\n\n", "###"]\n'
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        self.assertEqual(base64.b64decode(cms[0]["binaryData"]["config.yaml"]).decode(), content)

    def test_trailing_whitespace_preserved_byte_for_byte(self):
        # binaryData decodes the payload verbatim, so any trailing-newline shape survives.
        for content in ["one trailing\n", "no trailing newline", "many\n\n\n", "trailing spaces   \n", "tab\tend\n"]:
            with self.subTest(content=content):
                raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
                cms = self._extract_configmaps(raw)
                self.assertEqual(base64.b64decode(cms[0]["binaryData"]["config.yaml"]).decode(), content)

    def test_numeric_base64_payload_stays_string(self):
        # Some content encodes to an all-digit base64 ("Ӎ4" -> "0400"); the binaryData value must
        # render as a quoted string, or the apiserver rejects it ("cannot unmarshal number into
        # ...binaryData of type []uint8").
        raw = self._render_raw(self._values(config_file=self._config_file_fixture("Ӎ4")))
        cms = self._extract_configmaps(raw)
        value = cms[0]["binaryData"]["config.yaml"]
        self.assertIsInstance(value, str)
        self.assertEqual(base64.b64decode(value).decode(), "Ӎ4")

    def test_scalar_looking_filename_stays_a_string(self):
        # "true" passes the filename regex but must not render as a YAML bool:
        # k8s requires subPath and ConfigMap data keys to be strings.
        cf = ConfigFileMountValues(filename="true", mount_path="/etc/engine", content="x: 1\n").model_dump(mode="json")
        raw = self._render_raw(self._values(config_file=cf))
        cms = self._extract_configmaps(raw)
        self.assertEqual(len(cms), 1)
        self.assertEqual(list(cms[0]["binaryData"].keys()), ["true"])
        self.assertIn('subPath: "true"', raw)

    def test_seven_digit_ids_render_as_decimal_integers(self):
        # Helm parses helm.values numbers into float64; a bare {{ .Values.user_id }}
        # renders integers with >= 7 significant digits in scientific notation
        # (30000001 -> "3.0000001e+07"), corrupting namespace/resource names.
        # The templates pin `| int` on id interpolations; this guards them.
        values = self._values(config_file=None)
        values["user_id"] = 30000001
        values["deployment_id"] = 10000002
        raw = self._render_raw(values)
        self.assertNotIn("e+0", raw)
        self.assertIn("user-30000001", raw)
        self.assertIn("10000002", raw)


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

    def test_helm_values_carry_content_base64_shadow(self):
        vals = _make_job_values(
            config_file=ConfigFileMountValues(filename="config.yaml", mount_path="/etc/job", content=self.SAMPLE)
        )
        config_file = json.loads(vals.helm_values_yaml())["config_file"]
        self.assertEqual(config_file["filename"], "config.yaml")
        self.assertEqual(config_file["mount_path"], "/etc/job")
        # by_alias blanks raw content on the wire; charts (incl. chart_revision-pinned
        # ones) consume content_base64 — removal tracked in #3998.
        self.assertEqual(config_file["content"], "")
        self.assertEqual(base64.b64decode(config_file["content_base64"]).decode("utf-8"), self.SAMPLE)

    def test_helm_values_idempotent_across_round_trips(self):
        # DB load -> instance rebuild -> validator re-derives content_base64.
        original = _make_job_values(
            config_file=ConfigFileMountValues(filename="a.yaml", mount_path="/etc/x", content=self.SAMPLE)
        )
        rebuilt = JobDeploymentValues(**original.model_dump(mode="json"))
        config_file = json.loads(rebuilt.helm_values_yaml())["config_file"]
        self.assertEqual(base64.b64decode(config_file["content_base64"]).decode("utf-8"), self.SAMPLE)

    def test_no_config_file_emits_null_config_file(self):
        vals = _make_job_values()
        values = json.loads(vals.helm_values_yaml())
        self.assertIsNone(values["config_file"])


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestJobDeploymentHelmTemplate(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "job_deployment"

    def _values(self, config_file=None, **overrides) -> dict:
        # Render from the real values-model dump so the test exercises the exact
        # JobDeploymentValues -> chart contract (defaults included).
        return _make_job_values(config_file=config_file, **overrides).model_dump(mode="json")

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

    def _config_file_fixture(self, content: str) -> ConfigFileMount:
        return ConfigFileMountValues(filename="config.yaml", mount_path="/etc/job", content=content)

    def test_no_config_file_emits_no_configmap(self):
        raw = self._render_raw(self._values(config_file=None))
        self.assertEqual(self._extract_kind(raw, "ConfigMap"), [])

    def test_pod_template_has_standardized_component_type_label(self):
        raw = self._render_raw(self._values(config_file=None))
        jobs = self._extract_kind(raw, "Job")
        self.assertEqual(len(jobs), 1)
        self.assertEqual(
            jobs[0]["spec"]["template"]["metadata"]["labels"][COMPONENT_TYPE_LABEL],
            "user-container",
        )

    def test_kai_queue_on_job_and_pod_template(self):
        raw = self._render_raw(_make_job_values(kai_queue="org-7").model_dump(mode="json"))
        job = self._extract_kind(raw, "Job")[0]
        self.assertEqual(job["metadata"]["labels"]["kai.scheduler/queue"], "org-7")
        self.assertEqual(job["spec"]["template"]["metadata"]["labels"]["kai.scheduler/queue"], "org-7")
        self.assertEqual(job["spec"]["template"]["spec"]["schedulerName"], "kai-scheduler")

    def test_priority_class_rendered_when_set(self):
        raw = self._render_raw(_make_job_values(priority_class_name="experiment").model_dump(mode="json"))
        job = self._extract_kind(raw, "Job")[0]
        self.assertEqual(job["spec"]["template"]["spec"]["priorityClassName"], "experiment")

    def test_priority_class_omitted_when_unset(self):
        raw = self._render_raw(_make_job_values().model_dump(mode="json"))
        job = self._extract_kind(raw, "Job")[0]
        self.assertNotIn("priorityClassName", job["spec"]["template"]["spec"])

    def test_kai_queue_omitted_when_unset(self):
        raw = self._render_raw(self._values(config_file=None))
        job = self._extract_kind(raw, "Job")[0]
        self.assertNotIn("kai.scheduler/queue", job["metadata"].get("labels", {}))
        self.assertNotIn("kai.scheduler/queue", job["spec"]["template"]["metadata"]["labels"])

    def test_parallel_job_pod_template_mounts_existing_pvcs(self):
        volume_mounts = [
            PersistentVolumeClaimToMount(name="vol-123", mount_path="/data", read_only=False),
            PersistentVolumeClaimToMount(name="vol-123", mount_path="/models", read_only=True),
        ]
        raw = self._render_raw(
            self._values(
                config_file=self._config_file_fixture("batch: 32\n"),
                volume_mounts=volume_mounts,
                completions=4,
                parallelism=2,
            )
        )
        jobs = self._extract_kind(raw, "Job")
        self.assertEqual(len(jobs), 1)
        job_spec = jobs[0]["spec"]
        self.assertEqual(job_spec["completions"], 4)
        self.assertEqual(job_spec["parallelism"], 2)

        pod_spec = job_spec["template"]["spec"]
        mounts = {mount["mountPath"]: mount for mount in pod_spec["containers"][0]["volumeMounts"]}
        volumes = {volume["name"]: volume for volume in pod_spec["volumes"]}

        self.assertEqual(set(mounts), {"/dev/shm", "/data", "/models", "/etc/job/config.yaml"})
        self.assertEqual(mounts["/dev/shm"]["name"], "dshm")
        self.assertEqual(mounts["/data"]["name"], "external-volume-0")
        self.assertFalse(mounts["/data"]["readOnly"])
        self.assertEqual(mounts["/models"]["name"], "external-volume-0")
        self.assertTrue(mounts["/models"]["readOnly"])
        self.assertEqual(mounts["/etc/job/config.yaml"]["name"], "job-config-100")
        self.assertEqual(mounts["/etc/job/config.yaml"]["subPath"], "config.yaml")
        self.assertTrue(mounts["/etc/job/config.yaml"]["readOnly"])
        self.assertEqual(set(volumes), {"dshm", "external-volume-0", "job-config-100"})
        self.assertEqual(volumes["dshm"]["emptyDir"]["medium"], "Memory")
        self.assertEqual(volumes["external-volume-0"]["persistentVolumeClaim"]["claimName"], "vol-123")
        self.assertEqual(volumes["job-config-100"]["configMap"]["name"], "job-config-100")

    def test_config_file_renders_single_configmap(self):
        # binaryData preserves the payload byte-for-byte, trailing newline included.
        content = "batch: 32\nepochs: 10\n"
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_kind(raw, "ConfigMap")
        self.assertEqual(len(cms), 1)
        cm = cms[0]
        self.assertEqual(cm["metadata"]["name"], "job-config-100")
        self.assertEqual(cm["metadata"]["namespace"], "user-42")
        self.assertEqual(base64.b64decode(cm["binaryData"]["config.yaml"]).decode(), content)

    def test_special_chars_roundtrip(self):
        content = 'batch: 32\nhf_overrides: {"architectures":["X"]}\nstop: ["\n\n", "###"]\n'
        raw = self._render_raw(self._values(config_file=self._config_file_fixture(content)))
        cms = self._extract_kind(raw, "ConfigMap")
        self.assertEqual(len(cms), 1)
        self.assertEqual(base64.b64decode(cms[0]["binaryData"]["config.yaml"]).decode(), content)

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
        self.assertEqual(list(cms[0]["binaryData"].keys()), ["true"])

        jobs = self._extract_kind(raw, "Job")
        mounts = jobs[0]["spec"]["template"]["spec"]["containers"][0]["volumeMounts"]
        mount = [m for m in mounts if m["name"] == "job-config-100"][0]
        self.assertEqual(mount["subPath"], "true")
        self.assertIsInstance(mount["subPath"], str)
        self.assertEqual(mount["mountPath"], "/etc/job/true")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestJobDeploymentHelmTemplateSpecialCharEnvVars(unittest.TestCase):
    """job.yaml renders env values as `value: {{ $value | quote }}`, so embedded double
    quotes survive helm_values_yaml() -> `helm template` verbatim in the rendered Job."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "job_deployment"

    def test_env_vars_with_embedded_double_quotes_render_verbatim(self):
        env_vars = {
            "QUOTED": 'value with "double quotes" inside',
            "MIXED": 'it\'s a "complex" value',
        }
        vals = _make_job_values(env_vars=env_vars)
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        job = next(doc for doc in docs if doc.get("kind") == "Job")
        containers = job["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}
        for key, expected in env_vars.items():
            with self.subTest(key=key):
                self.assertEqual(env[key], expected)

    def test_hostile_env_key_renders_verbatim(self):
        # EnvVar names are user-controlled; `| quote` escapes an embedded double quote.
        env_vars = {'A"B': "v"}
        vals = _make_job_values(env_vars=env_vars)
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        job = next(doc for doc in docs if doc.get("kind") == "Job")
        containers = job["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}
        self.assertEqual(env['A"B'], "v")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateAppMetrics(unittest.TestCase):
    """app_metrics.yaml renders Service + ServiceMonitor when metrics is set,
    and is gracefully skipped when absent (legacy revisions persisted before
    the field existed)."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

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

    def test_rollout_retains_one_kubernetes_revision(self):
        rendered = self._render(_make_inference_values())
        rollout = self._find(rendered, kind="Rollout", name="test-deploy")

        self.assertEqual(rollout["spec"]["revisionHistoryLimit"], 1)

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

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "cserve_deployment_v3"

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

    def test_pod_template_has_standardized_component_type_label(self):
        rendered = self._render_cserve_v3()
        rollout = self._find(rendered, kind="Rollout", name="test-cserve")
        self.assertIsNotNone(rollout)
        self.assertEqual(
            rollout["spec"]["template"]["metadata"]["labels"][COMPONENT_TYPE_LABEL],
            "user-container",
        )
        self.assertNotIn(COMPONENT_TYPE_LABEL, rollout["spec"]["selector"]["matchLabels"])


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestInferenceV3HelmTemplateIngress(unittest.TestCase):
    """ingress.yaml must carry upstream-vhost so nginx sets the correct Host
    header when forwarding to the Istio sidecar.  Without it the sidecar cannot
    match any VirtualService and resets the connection — fatal for gRPC/HTTP2
    which relies on :authority for routing, and a latent risk for HTTP."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

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
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"

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

    def test_kai_queue_on_pod_template(self):
        vals = _make_inference_values(kai_queue="org-7")
        template = self._rollout(self._render(vals))["spec"]["template"]
        self.assertEqual(template["metadata"]["labels"]["kai.scheduler/queue"], "org-7")

    def test_priority_class_rendered_when_set(self):
        vals = _make_inference_values(priority_class_name="experiment")
        template = self._rollout(self._render(vals))["spec"]["template"]
        self.assertEqual(template["spec"]["priorityClassName"], "experiment")

    def test_priority_class_omitted_when_unset(self):
        vals = _make_inference_values()
        template = self._rollout(self._render(vals))["spec"]["template"]
        self.assertNotIn("priorityClassName", template["spec"])

    def test_kai_queue_omitted_when_unset(self):
        vals = _make_inference_values()
        template = self._rollout(self._render(vals))["spec"]["template"]
        self.assertNotIn("kai.scheduler/queue", template["metadata"]["labels"])


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


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestComputeHelmTemplateImagePullSecret(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "compute_deployment"

    def _values(self) -> dict:
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
            "container_port": 2222,
            "exposed_port": 22,
            "node_affinity_labels": {"zone": "a"},
            "storage_class_name": "standard",
            "resources": {"limits": resources_one, "requests": resources_one},
            # Real model dump so the base64 shadow the chart b64decs is present.
            "image_pull_secret": ImagePullSecret(
                server="registry.example.com", username="robot$user", password="p@ss$word!"
            ).model_dump(mode="json"),
        }

    def test_image_pull_secret_renders_dockerconfigjson(self) -> None:
        assert _HELM_BIN is not None
        helm_bin: str = _HELM_BIN
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(self._values(), f)
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

        secrets = [
            yaml.safe_load(doc)
            for doc in result.stdout.split("\n---\n")
            if "\nkind: Secret" in doc and "dockerconfigjson" in doc
        ]
        self.assertEqual(len(secrets), 1)
        docker_config = json.loads(base64.b64decode(secrets[0]["data"][".dockerconfigjson"]))
        auth = docker_config["auths"]["registry.example.com"]
        self.assertEqual(auth["username"], "robot$user")
        self.assertEqual(auth["password"], "p@ss$word!")

    def test_kai_queue_on_pod_template(self) -> None:
        assert _HELM_BIN is not None
        values = self._values()
        values["kai_queue"] = "org-7"
        values["priority_class_name"] = "experiment"
        values["scheduler_name"] = "kai-scheduler"
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
            yaml.safe_dump(values, f)
            values_path = f.name
        try:
            result = subprocess.run(
                [_HELM_BIN, "template", "probe", str(self.CHART_PATH), "-f", values_path],
                check=True,
                capture_output=True,
                text=True,
            )
        finally:
            os.unlink(values_path)
        deployments = [yaml.safe_load(doc) for doc in result.stdout.split("\n---\n") if "\nkind: Deployment" in doc]
        self.assertEqual(len(deployments), 1)
        template = deployments[0]["spec"]["template"]
        self.assertEqual(template["metadata"]["labels"]["kai.scheduler/queue"], "org-7")
        self.assertEqual(template["spec"]["priorityClassName"], "experiment")
        self.assertEqual(template["spec"]["schedulerName"], "kai-scheduler")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestHelmTemplateImagePullSecretHostileCreds(unittest.TestCase):
    """Credentials containing `"` / `\\` must produce a Secret whose
    .dockerconfigjson is valid JSON with the exact username/password; the
    templates build the config via `dict | toJson`, which JSON-escapes
    both fields.
    """

    SERVER = "registry.example.com"
    # `"` and `\` break unescaped JSON; `$oauthtoken` is the common nvcr.io user
    # that motivated the base64 wire-decoding, exercised alongside the hostile pair.
    CREDS = [('rob"ot\\user', 'p@ss"wo\\rd'), ("$oauthtoken", "secret-key-123")]

    def _docker_config(self, chart_path: Path, values_yaml: str) -> dict:
        raw = _helm_template(chart_path, values_yaml)
        secrets = [
            yaml.safe_load(doc) for doc in raw.split("\n---\n") if "\nkind: Secret" in doc and "dockerconfigjson" in doc
        ]
        self.assertEqual(len(secrets), 1)
        # json.loads doubles as the valid-JSON assertion for the rendered payload.
        return json.loads(base64.b64decode(secrets[0]["data"][".dockerconfigjson"]))

    def _assert_roundtrip(self, docker_config: dict, username: str, password: str) -> None:
        auth = docker_config["auths"][self.SERVER]
        self.assertEqual(auth["username"], username)
        self.assertEqual(auth["password"], password)
        self.assertEqual(base64.b64decode(auth["auth"]).decode(), f"{username}:{password}")

    def test_inference_v3(self):
        for username, password in self.CREDS:
            with self.subTest(username=username):
                vals = _make_inference_values(
                    image_pull_secret=ImagePullSecret(
                        server=self.SERVER, username=username, password=password
                    ).model_dump(mode="json"),
                )
                chart = Path(__file__).resolve().parents[2] / "src" / "catalog" / "inference_deployment_v3"
                self._assert_roundtrip(self._docker_config(chart, vals.helm_values_yaml()), username, password)

    def test_compute(self):
        for username, password in self.CREDS:
            with self.subTest(username=username):
                vals = ComputeDeploymentValues(
                    user_id=42,
                    deployment_id=100,
                    name="probe",
                    image_url="nginx:latest",
                    domain="centml.ai",
                    container_port=2222,
                    exposed_port=22,
                    node_affinity_labels={"zone": "a"},
                    storage_class_name="standard",
                    ssh_public_key="ssh-ed25519 AAAA",
                    resources=_make_resources(),
                    image_pull_secret=ImagePullSecret(
                        server=self.SERVER, username=username, password=password
                    ).model_dump(mode="json"),
                )
                chart = Path(__file__).resolve().parents[2] / "src" / "catalog" / "compute_deployment"
                self._assert_roundtrip(self._docker_config(chart, vals.helm_values_yaml()), username, password)

    def test_dynamo(self):
        for username, password in self.CREDS:
            with self.subTest(username=username):
                vals = DynamoDeploymentValues(
                    user_id=42,
                    deployment_id=100,
                    name="test-dynamo",
                    image_url="nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1",
                    domain="centml.ai",
                    revision_number=1,
                    model="Qwen/Qwen3-0.6B",
                    worker_pool=DynamoWorkerPoolValues(
                        node_affinity_labels={"platform.centml.ai/instance-type": "small"},
                        resources=_make_resources(),
                    ),
                    image_pull_secret=ImagePullSecret(
                        server=self.SERVER, username=username, password=password
                    ).model_dump(mode="json"),
                )
                chart = Path(__file__).resolve().parents[2] / "src" / "catalog" / "dynamo_deployment"
                self._assert_roundtrip(self._docker_config(chart, vals.helm_values_yaml()), username, password)


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestComputeDeploymentHelmTemplate(unittest.TestCase):
    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "compute_deployment"

    def _values(self, image_pull_secret=None, revision_number: int | None = None) -> dict:
        resources = {
            "cpu": "1",
            "memory": "1Gi",
            "accelerator_count": 0,
            "accelerator_key": "nvidia.com/gpu",
        }
        values = {
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
        if revision_number is not None:
            values["revision_number"] = revision_number
        return values

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
        self.assertEqual(user_container["securityContext"], {"runAsUser": 0, "runAsGroup": 0})
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

        compute_volume = next(volume for volume in pod_spec["volumes"] if volume["name"] == "compute-volume")
        self.assertEqual(compute_volume["persistentVolumeClaim"]["claimName"], "compute-probe")
        self._find(docs, "PersistentVolumeClaim", "compute-probe")

    def test_existing_home_pvc_is_mounted_without_creating_pvc(self):
        values = self._values()
        values["home_pvc_name"] = "vol-123"

        docs = self._render_dict(values)
        deployment = self._find(docs, "Deployment", "compute-probe")
        pod_spec = deployment["spec"]["template"]["spec"]
        user_container = pod_spec["containers"][0]
        env = {entry["name"]: entry["value"] for entry in user_container["env"] if "value" in entry}
        compute_volume = next(volume for volume in pod_spec["volumes"] if volume["name"] == "compute-volume")
        home_mounts = [mount for mount in user_container["volumeMounts"] if mount["mountPath"] == "/home"]

        self.assertEqual(compute_volume["persistentVolumeClaim"]["claimName"], "vol-123")
        self.assertEqual(len(home_mounts), 1)
        self.assertEqual(env["CCLUSTER_SSH_HOST_KEY_DIR"], "/home/.ccluster/ssh/100")
        self.assertFalse(any(doc["kind"] == "PersistentVolumeClaim" for doc in docs))

    def test_existing_pvcs_mount_at_user_paths(self):
        values = self._values()
        values["volume_mounts"] = [
            {"name": "vol-123", "mount_path": "/data", "read_only": False},
            {"name": "vol-456", "mount_path": "/models", "read_only": True},
        ]

        docs = self._render_dict(values)
        deployment = self._find(docs, "Deployment", "compute-probe")
        pod_spec = deployment["spec"]["template"]["spec"]
        user_container = pod_spec["containers"][0]
        mounts = {mount["mountPath"]: mount for mount in user_container["volumeMounts"]}
        volumes = {volume["name"]: volume for volume in pod_spec["volumes"]}

        self.assertEqual(mounts["/data"]["readOnly"], False)
        self.assertEqual(mounts["/models"]["readOnly"], True)
        self.assertEqual(volumes[mounts["/data"]["name"]]["persistentVolumeClaim"]["claimName"], "vol-123")
        self.assertEqual(volumes[mounts["/models"]["name"]]["persistentVolumeClaim"]["claimName"], "vol-456")
        self._find(docs, "PersistentVolumeClaim", "compute-probe")

    def test_one_existing_pvc_mounts_at_multiple_paths(self):
        values = self._values()
        values["volume_mounts"] = [
            {"name": "vol-123", "mount_path": "/data", "read_only": False},
            {"name": "vol-123", "mount_path": "/models", "read_only": True},
        ]

        docs = self._render_dict(values)
        deployment = self._find(docs, "Deployment", "compute-probe")
        pod_spec = deployment["spec"]["template"]["spec"]
        user_container = pod_spec["containers"][0]
        mounts = {mount["mountPath"]: mount for mount in user_container["volumeMounts"]}
        external_volumes = [volume for volume in pod_spec["volumes"] if volume["name"].startswith("external-volume-")]

        self.assertEqual(mounts["/data"]["name"], "external-volume-0")
        self.assertFalse(mounts["/data"]["readOnly"])
        self.assertEqual(mounts["/models"]["name"], "external-volume-0")
        self.assertTrue(mounts["/models"]["readOnly"])
        self.assertEqual(
            external_volumes,
            [{"name": "external-volume-0", "persistentVolumeClaim": {"claimName": "vol-123"}}],
        )

    def test_home_pvc_reuses_compute_volume_at_another_path(self):
        values = self._values()
        values["home_pvc_name"] = "vol-123"
        values["volume_mounts"] = [
            {"name": "vol-123", "mount_path": "/data", "read_only": False},
        ]

        docs = self._render_dict(values)
        deployment = self._find(docs, "Deployment", "compute-probe")
        pod_spec = deployment["spec"]["template"]["spec"]
        mounts = {mount["mountPath"]: mount for mount in pod_spec["containers"][0]["volumeMounts"]}
        pvc_volumes = [volume for volume in pod_spec["volumes"] if "persistentVolumeClaim" in volume]

        self.assertEqual(mounts["/home"]["name"], "compute-volume")
        self.assertEqual(mounts["/data"]["name"], "compute-volume")
        self.assertEqual(
            pvc_volumes,
            [{"name": "compute-volume", "persistentVolumeClaim": {"claimName": "vol-123"}}],
        )

    def test_pod_template_has_standardized_component_type_label(self):
        docs = self._render_dict(self._values())
        deployment = self._find(docs, "Deployment", "compute-probe")

        self.assertEqual(
            deployment["spec"]["template"]["metadata"]["labels"][COMPONENT_TYPE_LABEL],
            "user-container",
        )
        self.assertNotIn(COMPONENT_TYPE_LABEL, deployment["spec"]["selector"]["matchLabels"])

    def test_legacy_compute_without_revision_number_has_no_revision_metadata(self):
        docs = self._render_dict(self._values())
        deployment = self._find(docs, "Deployment", "compute-probe")

        pod_metadata = deployment["spec"]["template"]["metadata"]
        annotations = pod_metadata["annotations"]
        labels = pod_metadata["labels"]
        self.assertNotIn("platform.centml.ai/revision-number", annotations)
        self.assertNotIn("platform.centml.ai/revision-number", labels)
        self.assertNotIn("platform.centml.ai/revision-number", deployment["spec"]["selector"]["matchLabels"])

    def test_compute_revision_number_is_added_to_pod_metadata(self):
        for revision_number in (1, 2):
            with self.subTest(revision_number=revision_number):
                docs = self._render_dict(self._values(revision_number=revision_number))
                deployment = self._find(docs, "Deployment", "compute-probe")

                pod_metadata = deployment["spec"]["template"]["metadata"]
                annotations = pod_metadata["annotations"]
                labels = pod_metadata["labels"]
                self.assertEqual(annotations["platform.centml.ai/revision-number"], str(revision_number))
                self.assertEqual(labels["platform.centml.ai/revision-number"], str(revision_number))
                self.assertNotIn("platform.centml.ai/revision-number", deployment["spec"]["selector"]["matchLabels"])

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


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestComputeHelmTemplateSpecialCharEnvVars(unittest.TestCase):
    """deployment.yaml renders env values as `value: {{ $value | quote }}`, so embedded
    double quotes survive helm_values_yaml() -> `helm template` verbatim in the
    rendered Deployment."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "compute_deployment"

    def test_env_vars_with_embedded_double_quotes_render_verbatim(self):
        env_vars = {
            "QUOTED": 'value with "double quotes" inside',
            "MIXED": 'it\'s a "complex" value',
        }
        vals = ComputeDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="probe",
            image_url="nginx:latest",
            domain="centml.ai",
            container_port=2222,
            exposed_port=22,
            node_affinity_labels={"zone": "a"},
            storage_class_name="standard",
            ssh_public_key="ssh-ed25519 AAAA",
            env_vars=env_vars,
            resources=_make_resources(),
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        deployment = next(doc for doc in docs if doc.get("kind") == "Deployment")
        containers = deployment["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}
        for key, expected in env_vars.items():
            with self.subTest(key=key):
                self.assertEqual(env[key], expected)

    def test_ssh_username_fields_render_verbatim(self):
        # ssh_username / ssh_github_username are user-supplied and render as dedicated env
        # vars with | quote; a value with a double quote must not break the render.
        vals = ComputeDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="probe",
            image_url="nginx:latest",
            domain="centml.ai",
            container_port=2222,
            exposed_port=22,
            node_affinity_labels={"zone": "a"},
            storage_class_name="standard",
            ssh_username='us"er',
            ssh_github_username='gh"user',
            resources=_make_resources(),
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        deployment = next(doc for doc in docs if doc.get("kind") == "Deployment")
        containers = deployment["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}
        self.assertEqual(env["SSH_USERNAME"], 'us"er')
        self.assertEqual(env["SSH_GITHUB_USERNAME"], 'gh"user')

    def test_hostile_env_key_renders_verbatim(self):
        # EnvVar names are user-controlled; `| quote` escapes an embedded double quote.
        env_vars = {'A"B': "v"}
        vals = ComputeDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="probe",
            image_url="nginx:latest",
            domain="centml.ai",
            container_port=2222,
            exposed_port=22,
            node_affinity_labels={"zone": "a"},
            storage_class_name="standard",
            ssh_public_key="ssh-ed25519 AAAA",
            env_vars=env_vars,
            resources=_make_resources(),
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        deployment = next(doc for doc in docs if doc.get("kind") == "Deployment")
        containers = deployment["spec"]["template"]["spec"]["containers"]
        env = {e["name"]: e["value"] for c in containers for e in c.get("env", [])}
        self.assertEqual(env['A"B'], "v")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestComputeHelmTemplateSshPasswordSecret(unittest.TestCase):
    """ssh-password.secret.yaml renders `ssh_password: {{ .Values.ssh_password | b64enc | quote }}`;
    the `| quote` keeps an all-digit base64 encoding a string instead of a YAML number
    the apiserver rejects in Secret.data."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "compute_deployment"

    def test_all_digit_base64_ssh_password_stays_string(self):
        # "Ӎ4" (U+04CD + "4") base64-encodes to "0400", which unquoted parses as a number.
        vals = ComputeDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="probe",
            image_url="nginx:latest",
            domain="centml.ai",
            container_port=2222,
            exposed_port=22,
            node_affinity_labels={"zone": "a"},
            storage_class_name="standard",
            ssh_password="Ӎ4",
            resources=_make_resources(),
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        secret = next(d for d in docs if d.get("kind") == "Secret" and d["metadata"]["name"] == "ssh-password-100")
        value = secret["data"]["ssh_password"]
        self.assertIsInstance(value, str)
        self.assertEqual(value, "0400")


@unittest.skipUnless(_HELM_BIN, "helm binary not available in this environment")
class TestDynamoHelmTemplateSpecialCharValues(unittest.TestCase):
    """Hostile worker arguments and env values render as literal strings."""

    CHART_PATH = Path(__file__).resolve().parents[2] / "src" / "catalog" / "dynamo_deployment"

    def test_hostile_worker_values_render_as_literal_argv(self):
        model = "Qwen/$(touch-not-executed)\n---\nkind: ConfigMap"
        served_model_name = "served $(touch /tmp/not-executed)\nsecurityContext:\n  privileged: true"
        hostile_argv = "line\n---\nkind: ConfigMap\nsecurityContext:\n  privileged: true"
        extra_argv = [
            "--max-model-len",
            "8192",
            "--override-generation-config",
            '{"temperature": 0.1}',
            "$DYN_MODEL",
            "*",
            ";",
            "$(id)",
            hostile_argv,
        ]
        vals = DynamoDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="test-dynamo",
            image_url="nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1",
            domain="centml.ai",
            revision_number=1,
            model=model,
            served_model_name=served_model_name,
            extra_argv=extra_argv,
            worker_pool=DynamoWorkerPoolValues(
                node_affinity_labels={"platform.centml.ai/instance-type": "small"},
                resources=_make_resources(),
            ),
        )

        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        dgd = next(doc for doc in docs if doc.get("kind") == "DynamoGraphDeployment")
        worker = next(component for component in dgd["spec"]["components"] if component["name"] == "VllmDecodeWorker")
        container = next(
            candidate for candidate in worker["podTemplate"]["spec"]["containers"] if candidate["name"] == "main"
        )

        self.assertEqual(container["command"], ["python3"])
        self.assertEqual(
            container["args"],
            [
                "-m",
                "dynamo.vllm",
                "--model",
                model,
                "--kv-events-config",
                '{"publisher":"zmq","topic":"kv-events","endpoint":"tcp://*:20080","enable_kv_cache_events":true}',
                "--served-model-name",
                served_model_name,
                "--max-model-len",
                "8192",
                "--override-generation-config",
                '{"temperature": 0.1}',
                "$DYN_MODEL",
                "*",
                ";",
                "$(id)",
                hostile_argv,
                "--runner",
                "generate",
                "--tensor-parallel-size",
                "1",
            ],
        )
        self.assertNotIn("securityContext", container)
        self.assertNotIn("ConfigMap", [doc["kind"] for doc in docs])

    def test_missing_model_fails_loudly(self):
        vals = DynamoDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="test-dynamo",
            image_url="nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1",
            domain="centml.ai",
            revision_number=1,
            model="Qwen/Qwen3-0.6B",
            worker_pool=DynamoWorkerPoolValues(
                node_affinity_labels={"platform.centml.ai/instance-type": "small"},
                resources=_make_resources(),
            ),
        )

        for model_state in ("missing", "null", "empty"):
            with self.subTest(model_state=model_state):
                values = json.loads(vals.helm_values_yaml())
                if model_state == "missing":
                    values.pop("model")
                else:
                    values["model"] = None if model_state == "null" else ""

                with self.assertRaises(subprocess.CalledProcessError) as raised:
                    _helm_template(self.CHART_PATH, json.dumps(values))

                self.assertIn("model is required", raised.exception.stderr)

    def test_env_vars_with_embedded_double_quotes_render_verbatim(self):
        env_vars = {
            "QUOTED": 'value with "double quotes" inside',
            "MIXED": 'it\'s a "complex" value',
            "MY-FLAG": "kubernetes-name-with-dash",
            "my.flag": "kubernetes-name-with-dot",
        }
        vals = DynamoDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="test-dynamo",
            image_url="nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1",
            domain="centml.ai",
            revision_number=1,
            model="Qwen/Qwen3-0.6B",
            worker_pool=DynamoWorkerPoolValues(
                node_affinity_labels={"platform.centml.ai/instance-type": "small"},
                resources=_make_resources(),
            ),
            env_vars=env_vars,
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        dgd = next(doc for doc in docs if doc.get("kind") == "DynamoGraphDeployment")
        components = {c["name"]: c for c in dgd["spec"]["components"]}
        self.assertEqual(set(components), {"Frontend", "VllmDecodeWorker"})
        for component_name, component in components.items():
            containers = component["podTemplate"]["spec"]["containers"]
            env = {e["name"]: e.get("value") for c in containers for e in c.get("env", [])}
            for key, expected in env_vars.items():
                with self.subTest(component=component_name, key=key):
                    self.assertEqual(env[key], expected)

    def test_bool_like_name_renders_string_label_on_scaled_object(self):
        # The ScaledObject carries an `app: <name>` label value; `| quote` keeps a
        # name like "true" a string instead of a YAML bool the apiserver rejects.
        vals = DynamoDeploymentValues(
            user_id=42,
            deployment_id=100,
            name="true",
            image_url="nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1",
            domain="centml.ai",
            revision_number=1,
            model="Qwen/Qwen3-0.6B",
            worker_pool=DynamoWorkerPoolValues(
                node_affinity_labels={"platform.centml.ai/instance-type": "small"},
                resources=_make_resources(),
            ),
        )
        docs = _parse_rendered_docs(_helm_template(self.CHART_PATH, vals.helm_values_yaml()))
        scaled_object = next(doc for doc in docs if doc.get("kind") == "ScaledObject")
        label = scaled_object["metadata"]["labels"]["app"]
        self.assertIsInstance(label, str)
        self.assertEqual(label, "true")


if __name__ == "__main__":
    unittest.main()
