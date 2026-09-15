"""Render the KAI GPU queues chart through `helm template`."""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

from catalog.kai_gpu_queues import (
    CCLUSTER_ROOT_QUEUE,
    UNLIMITED_GPU_RESOURCE,
    KaiGpuQueuesValues,
    OrgGpuQueueValues,
    org_queue_name,
)

_HELM_BIN = shutil.which("helm")
_CHART_DIR = Path(__file__).resolve().parent


def _helm_template(values_yaml: str) -> list[dict]:
    assert _HELM_BIN is not None
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
        f.write(values_yaml)
        values_path = f.name
    try:
        result = subprocess.run(
            [_HELM_BIN, "template", "kai-gpu-queues", str(_CHART_DIR), "-f", values_path],
            check=True,
            capture_output=True,
            text=True,
        )
    finally:
        os.unlink(values_path)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


class TestKaiGpuQueuesChart(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if _HELM_BIN:
            return
        if os.environ.get("CI"):
            raise AssertionError("helm binary is required to render kai_gpu_queues in CI")
        raise unittest.SkipTest("helm binary not available in this environment")

    def test_renders_ccluster_root_and_org_leaves(self):
        values = KaiGpuQueuesValues(
            org_queues=[
                OrgGpuQueueValues(organization_id=42, gpu_quota=8),
                OrgGpuQueueValues(organization_id=7, gpu_quota=-1),
            ]
        )
        docs = _helm_template(values.helm_values_yaml())
        by_name = {doc["metadata"]["name"]: doc for doc in docs}

        self.assertEqual(set(by_name), {CCLUSTER_ROOT_QUEUE, "org-42", "org-7"})
        root = by_name[CCLUSTER_ROOT_QUEUE]
        self.assertEqual(root["kind"], "Queue")
        self.assertEqual(root["apiVersion"], "scheduling.run.ai/v2")
        self.assertEqual(root["metadata"]["labels"]["platform.centml.ai/gpu-quota"], "true")
        self.assertNotIn("parentQueue", root["spec"])
        self.assertEqual(root["spec"]["resources"]["gpu"]["quota"], UNLIMITED_GPU_RESOURCE)

        leaf = by_name["org-42"]
        self.assertEqual(leaf["spec"]["parentQueue"], CCLUSTER_ROOT_QUEUE)
        self.assertEqual(leaf["spec"]["resources"]["gpu"]["quota"], 8)
        self.assertEqual(leaf["spec"]["resources"]["gpu"]["limit"], 8)
        self.assertEqual(leaf["spec"]["resources"]["cpu"]["quota"], UNLIMITED_GPU_RESOURCE)

        unlimited = by_name["org-7"]
        self.assertEqual(unlimited["spec"]["resources"]["gpu"]["quota"], UNLIMITED_GPU_RESOURCE)
        self.assertEqual(unlimited["spec"]["resources"]["gpu"]["limit"], UNLIMITED_GPU_RESOURCE)

    def test_zero_quota_is_a_hard_cap(self):
        values = KaiGpuQueuesValues(org_queues=[OrgGpuQueueValues(organization_id=9, gpu_quota=0)])
        docs = _helm_template(values.helm_values_yaml())
        leaf = next(doc for doc in docs if doc["metadata"]["name"] == "org-9")
        self.assertEqual(leaf["spec"]["resources"]["gpu"]["quota"], 0)
        self.assertEqual(leaf["spec"]["resources"]["gpu"]["limit"], 0)

    def test_chart_defaults_render_ccluster_root(self):
        assert _HELM_BIN is not None
        result = subprocess.run(
            [_HELM_BIN, "template", "kai-gpu-queues", str(_CHART_DIR)],
            check=True,
            capture_output=True,
            text=True,
        )
        docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
        self.assertEqual([doc["metadata"]["name"] for doc in docs], [CCLUSTER_ROOT_QUEUE])

    def test_empty_org_queues_still_renders_ccluster_root(self):
        values = KaiGpuQueuesValues(org_queues=[])
        docs = _helm_template(values.helm_values_yaml())
        self.assertEqual([doc["metadata"]["name"] for doc in docs], [CCLUSTER_ROOT_QUEUE])


class TestOrgQueueValues(unittest.TestCase):
    def test_org_queue_name(self):
        self.assertEqual(org_queue_name(42), "org-42")

    def test_values_serialize_derived_helm_fields(self):
        dumped = yaml.safe_load(
            KaiGpuQueuesValues(org_queues=[OrgGpuQueueValues(organization_id=9, gpu_quota=0)]).helm_values_yaml()
        )
        queue = dumped["org_queues"][0]
        self.assertEqual(queue["queue_name"], "org-9")
        self.assertEqual(queue["gpu_quota"], 0)
        self.assertNotIn("gpu_limit", queue)
        self.assertEqual(dumped["ccluster_root_queue"], CCLUSTER_ROOT_QUEUE)
        self.assertEqual(dumped["unlimited_quota"], UNLIMITED_GPU_RESOURCE)
