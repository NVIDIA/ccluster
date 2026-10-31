# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""View models: what the page shows, as plain dataclasses built from the API objects.

Templates only format these, so the rules about what to show - which conditions are
current, what state a component set is in, what needs attention - live here and are tested
without HTML.
"""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from datetime import datetime, timezone

from ccluster.client import component_set_finished, current_conditions
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane

# A component set's state, as the Status column and the summary show it.
TERMINATING = "Terminating"
FINISHED = "Finished"
FAILED = "Failed"
ROLLING_OUT = "Rolling out"
SET_STATUSES = (FINISHED, ROLLING_OUT, FAILED, TERMINATING)

# Argo CD's Application health values, in the order the summary lists them, then the
# summary's own label for a component the operator has not released yet.
HEALTHS = ("Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown")
NOT_RELEASED = "Not released"

# The Progressing reason the operator reports when a component's sync failed.
_SYNC_FAILED = "WaveSyncFailed"


@dataclass(frozen=True)
class InfraPlaneRow:
    """One row of the infra planes table, after the CRD's printer columns."""

    namespace: str
    name: str
    domain: str
    available: str
    cluster_config: str
    verified: str
    age: str


@dataclass(frozen=True)
class ComponentSetRow:
    """One row of the component sets table."""

    namespace: str
    name: str
    status: str
    """One of SET_STATUSES."""
    healthy: int
    """Components in spec whose Argo CD health is Healthy."""
    total: int
    """Components in spec."""
    progress: str
    """The current Progressing condition's reason, the CRD's Progress column."""
    applied: str
    age: str


@dataclass(frozen=True)
class Count:
    label: str
    count: int


@dataclass(frozen=True)
class Attention:
    """An object that needs a look, and why."""

    kind: str
    namespace: str
    name: str
    problem: str


@dataclass(frozen=True)
class Summary:
    infra_planes: int
    available: int
    verified: int
    unavailable: int
    component_sets: int
    set_statuses: list[Count]
    """Component sets per status, in SET_STATUSES order, leaving out zeros."""
    components: int
    component_health: list[Count]
    """Components per Argo CD health, then not released, leaving out zeros."""
    attention: list[Attention]


@dataclass(frozen=True)
class Overview:
    summary: Summary
    infra_planes: list[InfraPlaneRow]
    component_sets: list[ComponentSetRow]


def overview(
    infra_planes: list[CCLInfraPlane],
    component_sets: list[CCLComponentSet],
    now: datetime | None = None,
) -> Overview:
    now = now or _now()
    return Overview(
        summary=summary(infra_planes, component_sets),
        infra_planes=[_infra_plane_row(plane, now) for plane in infra_planes],
        component_sets=[_component_set_row(component_set, now) for component_set in component_sets],
    )


def summary(
    infra_planes: list[CCLInfraPlane], component_sets: list[CCLComponentSet]
) -> Summary:
    attention = []

    available = verified = 0
    for plane in infra_planes:
        condition = current_conditions(plane).get("Available")
        if condition is not None and condition.status.value == "True":
            available += 1
        else:
            reason = condition.reason if condition is not None else "not reported yet"
            attention.append(_attention("CCLInfraPlane", plane, f"not available: {reason}"))
        verified_condition = _verified(plane)
        if verified_condition is not None and verified_condition.status.value == "True":
            verified += 1
        elif verified_condition is not None and verified_condition.status.value == "False":
            attention.append(
                _attention("CCLInfraPlane", plane, f"not verified: {verified_condition.reason}")
            )

    statuses: Counter[str] = Counter()
    healths: Counter[str] = Counter()
    for component_set in component_sets:
        status = component_set_status(component_set)
        statuses[status] += 1
        if status == FAILED:
            attention.append(
                _attention("CCLComponentSet", component_set, f"failed: {_failure(component_set)}")
            )
        healths.update(_component_healths(component_set))

    return Summary(
        infra_planes=len(infra_planes),
        available=available,
        verified=verified,
        unavailable=len(infra_planes) - available,
        component_sets=len(component_sets),
        set_statuses=[Count(s, statuses[s]) for s in SET_STATUSES if statuses[s]],
        components=sum(healths.values()),
        component_health=[
            Count(h, healths[h]) for h in (*HEALTHS, NOT_RELEASED) if healths[h]
        ],
        attention=attention,
    )


def component_set_status(component_set: CCLComponentSet) -> str:
    """The set's state: Terminating, else Finished, else Failed, else Rolling out."""
    if _terminating(component_set):
        return TERMINATING
    if component_set_finished(component_set):
        return FINISHED
    if _failure(component_set):
        return FAILED
    return ROLLING_OUT


def namespaces(
    infra_planes: list[CCLInfraPlane], component_sets: list[CCLComponentSet]
) -> list[str]:
    """The namespaces holding any of the objects, sorted."""
    return sorted(
        {obj.metadata.namespace for obj in (*infra_planes, *component_sets) if obj.metadata.namespace}
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
    verified = _verified(plane)
    return InfraPlaneRow(
        namespace=plane.metadata.namespace or "",
        name=plane.metadata.name or "",
        domain=plane.spec.domain,
        available=_status(conditions, "Available"),
        cluster_config=_status(conditions, "ClusterConfigApplied"),
        verified=verified.status.value if verified is not None else "",
        age=_age(plane, now),
    )


def _component_set_row(component_set: CCLComponentSet, now: datetime) -> ComponentSetRow:
    conditions = current_conditions(component_set)
    progressing = conditions.get("Progressing")
    healths = _component_healths(component_set)
    return ComponentSetRow(
        namespace=component_set.metadata.namespace or "",
        name=component_set.metadata.name or "",
        status=component_set_status(component_set),
        healthy=healths.count("Healthy"),
        total=len(healths),
        progress=progressing.reason if progressing else "",
        applied=_status(conditions, "ComponentsApplied"),
        age=_age(component_set, now),
    )


def _component_healths(component_set: CCLComponentSet) -> list[str]:
    """Each spec component's Argo CD health, in spec order; NOT_RELEASED without a report.

    status.componentSyncs reports only the components the operator has released, by name.
    One it still reports after the spec dropped it is being removed and is not counted.
    """
    status = component_set.status
    syncs = {sync.name: sync for sync in (status.component_syncs if status else None) or []}
    healths = []
    for component in component_set.spec.components:
        sync = syncs.get(component.name)
        if sync is None:
            healths.append(NOT_RELEASED)
        else:
            healths.append(sync.health if sync.health in HEALTHS else "Unknown")
    return healths


def _failure(component_set: CCLComponentSet) -> str:
    """Why the current generation failed, as a condition reason, or "" if it has not.

    It has failed when validation fails (an infraPlaneRef does not resolve to an Available
    CCLInfraPlane) or a component's sync failed.
    """
    conditions = current_conditions(component_set)
    validated = conditions.get("Validated")
    if validated is not None and validated.status.value == "False":
        return validated.reason
    progressing = conditions.get("Progressing")
    if progressing is not None and progressing.reason == _SYNC_FAILED:
        return progressing.reason
    return ""


def _verified(plane: CCLInfraPlane):
    """The infra-plane operator's Verified condition on the cluster's CCLClusterConfig.

    It carries the CCLClusterConfig's own generation on the infra plane, which this object
    cannot check, so it is taken as reported.
    """
    cluster_config = plane.status.cluster_config if plane.status else None
    for condition in (cluster_config.conditions if cluster_config else None) or []:
        if condition.type == "Verified":
            return condition
    return None


def _attention(kind: str, obj: CCLInfraPlane | CCLComponentSet, problem: str) -> Attention:
    return Attention(
        kind=kind,
        namespace=obj.metadata.namespace or "",
        name=obj.metadata.name or "",
        problem=problem,
    )


def _status(conditions: dict, condition_type: str) -> str:
    condition = conditions.get(condition_type)
    return condition.status.value if condition else ""


def _terminating(obj: CCLInfraPlane | CCLComponentSet) -> bool:
    """Whether deletion has begun: deletionTimestamp is kept by ObjectMeta's extra="allow"."""
    return getattr(obj.metadata, "deletionTimestamp", None) is not None


def _age(obj: CCLInfraPlane | CCLComponentSet, now: datetime) -> str:
    created = getattr(obj.metadata, "creationTimestamp", None)
    if not created:
        return ""
    # Python 3.10's fromisoformat does not accept the API server's trailing Z.
    created_at = datetime.fromisoformat(str(created).replace("Z", "+00:00"))
    return human_duration((now - created_at).total_seconds())


def _now() -> datetime:
    return datetime.now(timezone.utc)
