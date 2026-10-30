# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""View models: what each page shows, as plain dataclasses built from the API objects.

Templates only format these, so the rules about what to show - which conditions are
current, the order components roll out in - live here and are tested without HTML.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone

from ccluster.client import component_set_finished, current_conditions
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane


@dataclass(frozen=True)
class ConditionRow:
    type: str
    status: str
    reason: str
    message: str
    age: str
    observed_generation: int | None


@dataclass(frozen=True)
class InfraPlaneRow:
    """One row of the overview, after the CRD's printer columns."""

    name: str
    domain: str
    available: str
    cluster_config: str
    verified: str
    age: str
    terminating: bool


@dataclass(frozen=True)
class ComponentSetRow:
    """One row of the overview, after the CRD's printer columns, plus whether it finished."""

    name: str
    applied: str
    progress: str
    finished: bool
    age: str
    terminating: bool


@dataclass(frozen=True)
class Overview:
    infra_planes: list[InfraPlaneRow]
    component_sets: list[ComponentSetRow]


@dataclass(frozen=True)
class InfraPlaneDetail:
    name: str
    namespace: str
    domain: str
    credentials_secret: str
    age: str
    terminating: bool
    generation: int | None
    observed_generation: int | None
    conditions: list[ConditionRow]
    """Conditions computed from the current generation."""
    stale_conditions: list[ConditionRow]
    """Conditions left from an earlier generation."""
    cluster_config_conditions: list[ConditionRow]
    """The infra-plane operator's conditions on the cluster's CCLClusterConfig."""


@dataclass(frozen=True)
class ComponentRow:
    position: int | None
    """1-based place in spec.components, the rollout order; None once removed from it."""
    name: str
    chart: str
    namespace: str
    infra_plane: str
    released: bool
    """Whether the operator has released it, so that status.componentSyncs reports it."""
    health: str
    sync: str
    compared_to_matches_spec: bool | None
    message: str


@dataclass(frozen=True)
class ComponentSetDetail:
    name: str
    namespace: str
    age: str
    terminating: bool
    finished: bool
    generation: int | None
    observed_generation: int | None
    conditions: list[ConditionRow]
    """Conditions computed from the current generation."""
    stale_conditions: list[ConditionRow]
    """Conditions left from an earlier generation."""
    components: list[ComponentRow]
    """Components in rollout order, then any that status still reports but spec dropped."""


def overview(
    infra_planes: list[CCLInfraPlane],
    component_sets: list[CCLComponentSet],
    now: datetime | None = None,
) -> Overview:
    now = now or _now()
    return Overview(
        infra_planes=[_infra_plane_row(plane, now) for plane in infra_planes],
        component_sets=[_component_set_row(component_set, now) for component_set in component_sets],
    )


def infra_plane_detail(plane: CCLInfraPlane, now: datetime | None = None) -> InfraPlaneDetail:
    now = now or _now()
    current, stale = _split_conditions(plane, now)
    cluster_config = plane.status.cluster_config if plane.status else None
    return InfraPlaneDetail(
        name=plane.metadata.name or "",
        namespace=plane.metadata.namespace or "",
        domain=plane.spec.domain,
        credentials_secret=plane.spec.cluster_credentials_ref.name,
        age=_age(plane, now),
        terminating=_terminating(plane),
        generation=_meta(plane, "generation"),
        observed_generation=plane.status.observed_generation if plane.status else None,
        conditions=current,
        stale_conditions=stale,
        cluster_config_conditions=[
            _condition_row(condition, now)
            for condition in (cluster_config.conditions if cluster_config else None) or []
        ],
    )


def component_set_detail(
    component_set: CCLComponentSet, now: datetime | None = None
) -> ComponentSetDetail:
    now = now or _now()
    current, stale = _split_conditions(component_set, now)
    status = component_set.status
    syncs = {sync.name: sync for sync in (status.component_syncs if status else None) or []}

    components = []
    for position, component in enumerate(component_set.spec.components, start=1):
        sync = syncs.pop(component.name, None)
        components.append(
            ComponentRow(
                position=position,
                name=component.name,
                chart=component.chart,
                namespace=component.namespace,
                infra_plane=component.infra_plane_ref.name,
                released=sync is not None,
                health=(sync.health or "") if sync else "",
                sync=(sync.sync or "") if sync else "",
                compared_to_matches_spec=sync.compared_to_matches_spec if sync else None,
                message=(sync.message or "") if sync else "",
            )
        )
    for sync in syncs.values():
        components.append(
            ComponentRow(
                position=None,
                name=sync.name,
                chart="",
                namespace="",
                infra_plane="",
                released=True,
                health=sync.health or "",
                sync=sync.sync or "",
                compared_to_matches_spec=sync.compared_to_matches_spec,
                message=sync.message or "",
            )
        )

    return ComponentSetDetail(
        name=component_set.metadata.name or "",
        namespace=component_set.metadata.namespace or "",
        age=_age(component_set, now),
        terminating=_terminating(component_set),
        finished=component_set_finished(component_set),
        generation=_meta(component_set, "generation"),
        observed_generation=status.observed_generation if status else None,
        conditions=current,
        stale_conditions=stale,
        components=components,
    )


def human_duration(seconds: float) -> str:
    """A short age in kubectl's style: 45s, 12m, 5h, 3d, 2y."""
    seconds = max(int(seconds), 0)
    if seconds < 120:
        return f"{seconds}s"
    minutes = seconds // 60
    if minutes < 120:
        return f"{minutes}m"
    hours = minutes // 60
    if hours < 48:
        return f"{hours}h"
    days = hours // 24
    if days < 730:
        return f"{days}d"
    return f"{days // 365}y"


def _infra_plane_row(plane: CCLInfraPlane, now: datetime) -> InfraPlaneRow:
    conditions = current_conditions(plane)
    cluster_config = plane.status.cluster_config if plane.status else None
    # The CCLClusterConfig's conditions carry its own generation on the infra plane, which
    # this object cannot check, so they are shown as reported.
    verified = {
        condition.type: condition
        for condition in (cluster_config.conditions if cluster_config else None) or []
    }
    return InfraPlaneRow(
        name=plane.metadata.name or "",
        domain=plane.spec.domain,
        available=_status(conditions, "Available"),
        cluster_config=_status(conditions, "ClusterConfigApplied"),
        verified=_status(verified, "Verified"),
        age=_age(plane, now),
        terminating=_terminating(plane),
    )


def _component_set_row(component_set: CCLComponentSet, now: datetime) -> ComponentSetRow:
    conditions = current_conditions(component_set)
    progressing = conditions.get("Progressing")
    return ComponentSetRow(
        name=component_set.metadata.name or "",
        applied=_status(conditions, "ComponentsApplied"),
        progress=progressing.reason if progressing else "",
        finished=component_set_finished(component_set),
        age=_age(component_set, now),
        terminating=_terminating(component_set),
    )


def _split_conditions(
    obj: CCLInfraPlane | CCLComponentSet, now: datetime
) -> tuple[list[ConditionRow], list[ConditionRow]]:
    current = current_conditions(obj)
    all_conditions = (obj.status.conditions if obj.status else None) or []
    return (
        [_condition_row(condition, now) for condition in current.values()],
        [
            _condition_row(condition, now)
            for condition in all_conditions
            if current.get(condition.type) is not condition
        ],
    )


def _condition_row(condition, now: datetime) -> ConditionRow:
    return ConditionRow(
        type=condition.type,
        status=condition.status.value,
        reason=condition.reason,
        message=condition.message,
        age=human_duration((now - condition.last_transition_time).total_seconds()),
        observed_generation=condition.observed_generation,
    )


def _status(conditions: dict, condition_type: str) -> str:
    condition = conditions.get(condition_type)
    return condition.status.value if condition else ""


def _meta(obj: CCLInfraPlane | CCLComponentSet, key: str):
    """A server-managed metadata field, kept by ObjectMeta's extra="allow" as sent."""
    return getattr(obj.metadata, key, None)


def _terminating(obj: CCLInfraPlane | CCLComponentSet) -> bool:
    return _meta(obj, "deletionTimestamp") is not None


def _age(obj: CCLInfraPlane | CCLComponentSet, now: datetime) -> str:
    created = _meta(obj, "creationTimestamp")
    if not created:
        return ""
    # Python 3.10's fromisoformat does not accept the API server's trailing Z.
    created_at = datetime.fromisoformat(str(created).replace("Z", "+00:00"))
    return human_duration((now - created_at).total_seconds())


def _now() -> datetime:
    return datetime.now(timezone.utc)
