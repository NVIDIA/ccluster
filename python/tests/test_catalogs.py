# Copyright 2026 NVIDIA CORPORATION & AFFILIATES.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""The generated catalog values behave like their Go counterparts."""

import importlib

import pytest
from pydantic import ValidationError

from ccluster import catalogs
from ccluster.catalogs import cluster_base
from ccluster.catalogs.cluster_base import gpu_operator, teleport_kube_agent


@pytest.mark.parametrize("name", cluster_base.__all__)
def test_component_package_shape(name):
    pkg = importlib.import_module(f"ccluster.catalogs.cluster_base.{name}")
    assert pkg.CHART == f"catalogs/cluster_base/{name}"
    for exported in pkg.__all__:
        assert hasattr(pkg, exported)
    # Zero values mean "chart defaults": nothing is stored, as catalogs.Entry does in Go.
    assert "values" not in catalogs.entry(pkg.CHART, "ns", pkg.Values())


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


def test_teleport_required_values():
    with pytest.raises(ValidationError):
        teleport_kube_agent.ChartValues(proxy_addr="teleport.example.com:443")

    # The sample from catalogs/cluster_base/cluster_base.go, dumped as Go dumps it.
    values = teleport_kube_agent.Values(
        teleport_kube_agent=teleport_kube_agent.ChartValues(
            proxy_addr="teleport.example.com:443",
            teleport_cluster_name="example",
            kube_cluster_name="example-infra-plane",
            join_params=teleport_kube_agent.JoinParams(token_name="example-kube-token"),
        )
    )
    assert catalogs.entry(teleport_kube_agent.CHART, "teleport", values) == {
        "chart": "catalogs/cluster_base/teleport_kube_agent",
        "namespace": "teleport",
        "values": {
            "teleport-kube-agent": {
                "proxyAddr": "teleport.example.com:443",
                "teleportClusterName": "example",
                "kubeClusterName": "example-infra-plane",
                "joinParams": {"tokenName": "example-kube-token"},
            }
        },
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
