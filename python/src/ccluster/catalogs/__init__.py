# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The Helm chart catalog, as typed values.

Each chart under ccluster.catalogs.cluster_base, ccluster.catalogs.deployments and
ccluster.catalogs.resources, and ccluster.catalogs.namespace_base itself, has a Values
model and a CHART path, generated from the Go package beside the chart. entry()
turns one of those into the ComponentEntry fields of a CCLComponentSet component, as
catalogs.Entry does in Go.
"""

from typing import Any

from pydantic import BaseModel

from ccluster.v1alpha1.cclcomponentset import ComponentSpec, LocalObjectReference

__all__ = ["component", "entry"]


def entry(chart: str, namespace: str, values: BaseModel | None) -> dict[str, Any]:
    """Return the chart, namespace and values fields of a component.

    values is dumped as JSON by alias with None fields left out, so an enum becomes
    its string and a Values with one still serializes. That is the shape Helm
    expects and the shape the Go catalog produces. An empty result is dropped rather
    than stored: the operator omits the Application's helm block when values is unset,
    and an empty object would defeat that. Keys the Values does not model are kept and
    dumped as given, since a chart accepts more than its Go type describes. A chart's
    required values are checked when its Values is constructed; the finer rules the Go
    Validate methods apply (port ranges, mount paths, replica bounds) are not mirrored
    here.
    """
    out: dict[str, Any] = {"chart": chart, "namespace": namespace}
    if values is not None:
        dumped = values.model_dump(mode="json", by_alias=True, exclude_none=True)
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
