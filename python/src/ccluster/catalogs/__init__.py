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

"""The Helm chart catalog, as typed values.

Each chart under ccluster.catalogs.cluster_base has a Values model and a CHART path,
generated from the Go package beside the chart. entry() turns one of those into the
ComponentEntry fields of a CCLComponentSet component, as catalogs.Entry does in Go.
"""

from typing import Any

from pydantic import BaseModel

from ccluster.v1alpha1 import ComponentSpec, LocalObjectReference

__all__ = ["component", "entry"]


def entry(chart: str, namespace: str, values: BaseModel | None) -> dict[str, Any]:
    """Return the chart, namespace and values fields of a component.

    values is dumped by alias with None fields left out, which is the shape Helm
    expects and the shape the Go catalog produces. An empty result is dropped rather
    than stored: the operator omits the Application's helm block when values is unset,
    and an empty object would defeat that. The required values of a chart (only
    teleport-kube-agent has any) are checked when its Values is constructed.
    """
    out: dict[str, Any] = {"chart": chart, "namespace": namespace}
    if values is not None:
        dumped = values.model_dump(by_alias=True, exclude_none=True)
        if dumped:
            out["values"] = dumped
    return out


def component(
    name: str,
    infra_plane: str,
    chart: str,
    namespace: str,
    values: BaseModel | None = None,
) -> ComponentSpec:
    """Build one CCLComponentSet component from a chart's typed values.

    chart is the module's CHART constant. The component's name and the infra plane it
    is deployed to are the caller's, as is the namespace it installs into: none of
    those are facts about the chart.
    """
    return ComponentSpec(
        name=name,
        infra_plane_ref=LocalObjectReference(name=infra_plane),
        **entry(chart, namespace, values),
    )
