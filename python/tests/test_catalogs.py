# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The generated catalog values behave like their Go counterparts."""

import importlib
from pathlib import Path

import pytest
from pydantic import BaseModel, ValidationError

from ccluster import catalogs
from ccluster.catalogs import namespace_base
from ccluster.catalogs.cluster_base import gpu_operator
from ccluster.catalogs.deployments import dynamo_deployment, inference_deployment
from ccluster.catalogs.resources import kai_gpu_queues

REPO = Path(__file__).resolve().parents[2]


@pytest.mark.parametrize("model", [dynamo_deployment.HPA, inference_deployment.HPA])
@pytest.mark.parametrize("cooldown", [-1, 0, 1800, 3600, 3601, None])
def test_hpa_cooldown_bounds(model, cooldown):
    if cooldown is not None and not 0 <= cooldown <= 3600:
        with pytest.raises(ValidationError):
            model(cooldownPeriod=cooldown)
    else:
        result = model(cooldownPeriod=cooldown).model_dump(by_alias=True, exclude_none=True)
        assert result.get("cooldownPeriod") == cooldown


def chart_dirs():
    """Every chart directory under catalogs/: the ones with a Go package beside the chart."""
    return sorted(
        str(chart.parent.relative_to(REPO)) for chart in (REPO / "catalogs").rglob("component.go")
    )


@pytest.mark.parametrize("chart", chart_dirs())
def test_every_chart_has_a_generated_package(chart):
    # catalogs/<catalog>/<chart> is generated as ccluster.catalogs.<catalog>.<chart>, a
    # package with the chart's Values model and its CHART path. The catalog directories in
    # between are namespace packages, so the packages are found by directory, not by
    # walking ccluster.catalogs.
    pkg = importlib.import_module("ccluster." + chart.replace("/", "."))
    assert pkg.CHART == chart
    assert issubclass(pkg.Values, BaseModel)


def test_explicit_false_survives():
    # The reason every optional scalar in the Go catalog is a pointer.
    values = gpu_operator.Values(
        gpu_operator=gpu_operator.ChartValues(driver=gpu_operator.Toggle(enabled=False))
    )
    assert catalogs.entry(gpu_operator.CHART, "gpu-operator", values)["values"] == {
        "gpu-operator": {"driver": {"enabled": False}}
    }


def test_wire_names_are_aliases():
    values = gpu_operator.Values.model_validate(
        {"gpu-operator": {"toolkit": {"enabled": True}}}
    )
    assert values.gpu_operator.toolkit.enabled is True
    with pytest.raises(ValidationError):
        gpu_operator.Values.model_validate({"gpu-operator": {"unknown": 1}})


def test_kai_gpu_queues_required_values():
    with pytest.raises(ValidationError):
        kai_gpu_queues.Queue(name="team-a")

    values = kai_gpu_queues.Values(queues=[kai_gpu_queues.Queue(name="team-a", gpu_quota=4)])
    assert catalogs.entry(kai_gpu_queues.CHART, "kai-scheduler", values) == {
        "chart": "catalogs/resources/kai_gpu_queues",
        "namespace": "kai-scheduler",
        "values": {"queues": [{"name": "team-a", "gpuQuota": 4}]},
    }


def test_component_builds_a_component_spec():
    spec = catalogs.component(
        "gpu-operator", "plane", gpu_operator.CHART, "gpu-operator",
        gpu_operator.Values(gpu_operator=gpu_operator.ChartValues(nfd=gpu_operator.Toggle(enabled=True))),
    )
    assert spec.model_dump(by_alias=True, exclude_none=True) == {
        "name": "gpu-operator",
        "infraPlaneRef": {"name": "plane"},
        "chart": "catalogs/cluster_base/gpu_operator",
        "namespace": "gpu-operator",
        "values": {"gpu-operator": {"nfd": {"enabled": True}}},
    }
