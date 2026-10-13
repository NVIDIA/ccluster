# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
import shutil
import subprocess

import pytest
import yaml
from conftest import ROOT, render
from test_dynamo_metrics import CHART, platform_values, serving_mode


@pytest.mark.parametrize("bounds,hold_count", [((1, 1), 0), ((2, 2), 0), ((1, 3), 1)])
def test_readiness_hold_contract(dynamo_values, bounds, hold_count):
    dynamo_values["hpa"].update(minReplicas=bounds[0], maxReplicas=bounds[1])
    spec = next(
        d["spec"] for d in render(CHART, dynamo_values) if d["kind"] == "ScaledObject"
    )
    assert len(spec["triggers"]) == 1 + hold_count
    if hold_count:
        hold = spec["triggers"][1]
        assert hold["metricType"] == "Value"
        assert (
            hold["metadata"]["threshold"]
            == hold["metadata"]["activationThreshold"]
            == "1"
        )
        assert (
            hold["metadata"]["serverAddress"]
            == spec["triggers"][0]["metadata"]["serverAddress"]
        )
        if chart := os.environ.get("PLATFORM_DYNAMO_CHART"):
            values = platform_values(dynamo_values)
            values.update(user_id=1340, deployment_id=1732)
            reference = next(
                d["spec"] for d in render(chart, values) if d["kind"] == "ScaledObject"
            )
            assert hold == reference["triggers"][1]
    serving_mode(dynamo_values, "disaggregated")
    assert not any(d["kind"] == "ScaledObject" for d in render(CHART, dynamo_values))


@pytest.mark.parametrize("chart", ["dynamo_deployment", "inference_deployment"])
@pytest.mark.parametrize("cooldown", [None, -1, 0, 1800, 3600, 3601, 3600.5, "invalid"])
def test_cooldown_rendering(dynamo_values, chart, cooldown):
    if chart == "inference_deployment":
        dynamo_values["resources"] = dynamo_values.pop("workerPool")["resources"]
    if cooldown is not None:
        dynamo_values["hpa"]["cooldownPeriod"] = cooldown
    path = ROOT / "catalogs/deployments" / chart
    if cooldown in [-1, 3601, 3600.5, "invalid"]:
        with pytest.raises(subprocess.CalledProcessError) as error:
            render(path, dynamo_values)
        # Helm 3 uses dotted paths; Helm 4 reports JSON pointers.
        assert "cooldownPeriod" in error.value.stderr and "hpa" in error.value.stderr
        return
    spec = next(
        d["spec"] for d in render(path, dynamo_values) if d["kind"] == "ScaledObject"
    )
    expected = (
        1800
        if cooldown is None or (chart == "inference_deployment" and cooldown == 0)
        else cooldown
    )
    assert spec["cooldownPeriod"] == spec["initialCooldownPeriod"] == expected
    if chart == "dynamo_deployment":
        assert (
            spec["advanced"]["horizontalPodAutoscalerConfig"]["behavior"]["scaleDown"][
                "stabilizationWindowSeconds"
            ]
            == expected
        )


def pod(
    name="worker",
    *,
    ready=False,
    age=60,
    queued=0,
    scheduled=True,
    restarts=0,
    deleting=False,
    namespace="user-1340",
    deployment="1732",
    component="VllmDecodeWorker",
):
    labels = f'namespace="{namespace}",pod="{name}"'
    samples = [
        (
            f'kube_pod_labels{{{labels},label_platform_centml_ai_deployment_id="{deployment}",label_nvidia_com_dynamo_component="{component}"}}',
            1,
        ),
        (f'kube_pod_status_ready{{{labels},condition="false"}}', int(not ready)),
        (f"kube_pod_created{{{labels}}}", -(age + queued)),
        (
            f'kube_pod_container_status_restarts_total{{{labels},container="main"}}',
            restarts,
        ),
    ]
    if scheduled:
        samples.append((f"kube_pod_start_time{{{labels}}}", -age))
    if deleting:
        samples.append((f"kube_pod_deletion_timestamp{{{labels}}}", 1))
    return [{"series": series, "values": str(value)} for series, value in samples]


def test_loading_query_with_real_promql(dynamo_values, prometheus_tools, tmp_path):
    dynamo_values["hpa"].update(maxReplicas=3)
    spec = next(
        d["spec"] for d in render(CHART, dynamo_values) if d["kind"] == "ScaledObject"
    )
    query = spec["triggers"][1]["metadata"]["query"]
    duplicate = [
        {**s, "series": s["series"].replace("}", ',instance="second-ksm"}')}
        for s in pod()
    ]
    scenarios = [
        ("all ready", pod(ready=True), 0),
        ("one loading", pod(), 1),
        ("two loading", [*pod("a"), *pod("b")], 1),
        ("frontend loading", pod(component="Frontend"), 0),
        ("other deployment", pod(deployment="other"), 0),
        ("other namespace", pod(namespace="other"), 0),
        ("terminating", pod(deleting=True), 0),
        ("restarted", pod(restarts=1), 0),
        ("age just below cap", pod(age=1799), 1),
        ("age at cap", pod(age=1800), 0),
        ("age above cap", pod(age=1801), 0),
        ("long node queue before start", pod(queued=7200), 1),
        ("never scheduled", pod(scheduled=False, queued=7200), 0),
        ("duplicate KSM scrapes", [*pod(), *duplicate], 1),
        (
            "missing Pod labels",
            [s for s in pod() if not s["series"].startswith("kube_pod_labels")],
            0,
        ),
        ("no metrics", [], 0),
    ]
    fixture = {
        "evaluation_interval": "1m",
        "tests": [
            {
                "name": name,
                "interval": "1m",
                "input_series": inputs,
                "promql_expr_test": [
                    {
                        "expr": query,
                        "eval_time": "0m",
                        "exp_samples": [{"labels": "{}", "value": expected}],
                    }
                ],
            }
            for name, inputs, expected in scenarios
        ],
    }
    path = tmp_path / "hold.yaml"
    path.write_text(yaml.safe_dump(fixture))
    result = subprocess.run(
        [str(prometheus_tools / "promtool"), "test", "rules", str(path)],
        text=True,
        capture_output=True,
    )
    assert result.returncode == 0, result.stdout + result.stderr


def test_ksm_allowlist_reaches_the_deployment(tmp_path):
    source = ROOT / "catalogs/cluster_base/kube_prometheus_stack"
    chart = tmp_path / "chart"
    shutil.copytree(source, chart)
    dependency = yaml.safe_load((chart / "Chart.yaml").read_text())["dependencies"][0]
    # Test the pinned upstream dependency as well as our values, so a misspelled or
    # incorrectly nested allowlist cannot pass just by appearing in values.yaml.
    (chart / "charts").mkdir(exist_ok=True)
    subprocess.run(
        [
            os.environ.get("HELM", "helm"),
            "pull",
            dependency["name"],
            "--repo",
            dependency["repository"],
            "--version",
            dependency["version"],
            "--destination",
            str(chart / "charts"),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    documents = render(chart, {}, "prometheus")
    deployment = next(
        d
        for d in documents
        if d["kind"] == "Deployment"
        and d["metadata"]["name"].endswith("kube-state-metrics")
    )
    args = deployment["spec"]["template"]["spec"]["containers"][0]["args"]
    flag = next(a for a in args if a.startswith("--metric-labels-allowlist="))
    expected = {
        "platform.centml.ai/deployment-id",
        "platform.centml.ai/revision-number",
        "ccluster.nvidia.com/component-type",
        "nvidia.com/dynamo-component",
    }
    assert (
        set(
            flag.removeprefix("--metric-labels-allowlist=pods=[")
            .removesuffix("]")
            .split(",")
        )
        == expected
    )
    prom = next(d["spec"] for d in documents if d["kind"] == "Prometheus")
    assert (
        prom["serviceMonitorSelector"] == prom["serviceMonitorNamespaceSelector"] == {}
    )
