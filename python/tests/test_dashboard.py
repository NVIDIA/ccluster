# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The dashboard summarises and lists infra planes and component sets from the API, across
the cluster or in the one namespace it serves, with the source's credentials and only for
the hosts it serves, without a cluster."""

import json
import re
from datetime import timedelta
from types import SimpleNamespace

import pytest
from kubernetes import client as k8s
from starlette.testclient import TestClient
from urllib3.exceptions import MaxRetryError

from ccluster import dashboard
from ccluster.dashboard import reads, views
from ccluster.dashboard.routes import FRAGMENT_HEADER
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane

CREATED = "2026-01-01T00:00:00Z"
PLANES = "cclinfraplanes"
SETS = "cclcomponentsets"


def condition(condition_type, status, reason, generation=1):
    return {
        "type": condition_type,
        "status": status,
        "reason": reason,
        "message": f"message for {reason}",
        "lastTransitionTime": CREATED,
        "observedGeneration": generation,
    }


def infra_plane_body(name="ip1", namespace="tenant-a", available="True", verified="True"):
    conditions = [condition("ClusterConfigApplied", "True", "ClusterConfigApplied")]
    if available is not None:
        reason = "ClusterSecretCreated" if available == "True" else "ClusterUnreachable"
        conditions.append(condition("Available", available, reason))
    status = {"observedGeneration": 1, "conditions": conditions}
    if verified is not None:
        reason = "ComponentsVerified" if verified == "True" else "ComponentsNotVerified"
        status["clusterConfig"] = {
            "observedGeneration": 7,
            "conditions": [condition("Verified", verified, reason, 7)],
        }
    return {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLInfraPlane",
        "metadata": {
            "name": name, "namespace": namespace, "generation": 1, "creationTimestamp": CREATED,
        },
        "spec": {
            "clusterCredentialsRef": {"name": f"{name}-credentials"},
            "domain": f"{name}.example.com",
        },
        "status": status,
    }


ROLLING_OUT = [
    condition("Validated", "True", "ComponentsAccepted", 2),
    condition("ComponentsApplied", "True", "ApplicationsCreated", 2),
    condition("Progressing", "True", "WaveSyncing", 2),
]
FINISHED = [
    condition("Validated", "True", "ComponentsAccepted", 2),
    condition("ComponentsApplied", "True", "ApplicationsCreated", 2),
    condition("Progressing", "False", "RolloutComplete", 2),
]


def component_set_body(name="base", namespace="tenant-a", conditions=ROLLING_OUT, syncs=None,
                       components=("cert-manager", "istio", "keda"), **meta):
    """By default: three components, the first healthy, the second progressing, the third
    not released, and one more reported that the spec dropped."""
    if syncs is None:
        syncs = [
            {"name": "istio", "health": "Progressing", "sync": "OutOfSync",
             "comparedToMatchesSpec": True},
            {"name": "old", "health": "Healthy", "sync": "Synced", "comparedToMatchesSpec": True},
            {"name": "cert-manager", "health": "Healthy", "sync": "Synced",
             "comparedToMatchesSpec": True},
        ]
    return {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLComponentSet",
        "metadata": {
            "name": name, "namespace": namespace, "generation": 2, "creationTimestamp": CREATED,
            **meta,
        },
        "spec": {
            "components": [
                {
                    "name": component,
                    "infraPlaneRef": {"name": "ip1"},
                    "chart": f"catalogs/cluster_base/{component}",
                    "namespace": component,
                }
                for component in components
            ]
        },
        "status": {"observedGeneration": 2, "componentSyncs": syncs, "conditions": list(conditions)},
    }


class FakeKubernetes:
    """The API server behind the fake CustomObjectsApi and AuthenticationV1Api.

    objects maps (plural, namespace, name) to a body. error is raised by every read;
    cluster_error only by reads across all namespaces, as RBAC that grants some namespaces
    but nothing cluster-wide answers.
    """

    def __init__(self):
        self.objects = {}
        self.error = None
        self.cluster_error = None
        self.calls = []
        self.whoami_calls = 0

    def add(self, plural, body):
        meta = body["metadata"]
        self.objects[(plural, meta["namespace"], meta["name"])] = body

    def custom_objects(self, api_client):
        def list_namespaced_custom_object(group, version, namespace, plural):
            self._read(api_client, plural, namespace)
            return {"items": [b for (p, ns, _), b in sorted(self.objects.items())
                              if p == plural and ns == namespace]}

        def list_cluster_custom_object(group, version, plural):
            self._read(api_client, plural, None)
            if self.cluster_error:
                raise self.cluster_error
            return {"items": [b for (p, _, _), b in sorted(self.objects.items()) if p == plural]}

        return SimpleNamespace(
            list_namespaced_custom_object=list_namespaced_custom_object,
            list_cluster_custom_object=list_cluster_custom_object,
        )

    def authentication(self, api_client):
        def create_self_subject_review(body):
            self.whoami_calls += 1
            return SimpleNamespace(
                status=SimpleNamespace(user_info=SimpleNamespace(username="bob"))
            )

        return SimpleNamespace(create_self_subject_review=create_self_subject_review)

    def _read(self, api_client, plural, namespace):
        self.calls.append((api_client, plural, namespace))
        if self.error:
            raise self.error


def ApiError(status, message):
    """An ApiException as the client raises it for a Status response from the server."""
    return k8s.ApiException(status=status, reason="Reason", body=json.dumps({"message": message}))


@pytest.fixture
def kube(monkeypatch):
    fake = FakeKubernetes()
    monkeypatch.setattr(reads.k8s, "CustomObjectsApi", fake.custom_objects)
    monkeypatch.setattr(reads.k8s, "AuthenticationV1Api", fake.authentication)
    return fake


API_CLIENT = object()


def make_client(namespace):
    app = dashboard.create_app(
        dashboard.LocalSource(API_CLIENT), namespace=namespace, allowed_hosts=["testserver"]
    )
    return TestClient(app)


@pytest.fixture
def admin(kube):
    """The dashboard started without -n."""
    return make_client(None)


@pytest.fixture
def user(kube):
    """The dashboard started with -n tenant-a."""
    return make_client("tenant-a")


@pytest.fixture
def two_namespaces(kube):
    kube.add(PLANES, infra_plane_body("ip1", "tenant-a"))
    kube.add(PLANES, infra_plane_body("ip2", "tenant-b", verified="False"))
    kube.add(SETS, component_set_body("base", "tenant-a"))
    kube.add(SETS, component_set_body("gpu", "tenant-c", conditions=FINISHED, components=("keda",),
                                      syncs=[{"name": "keda", "health": "Healthy",
                                              "sync": "Synced", "comparedToMatchesSpec": True}]))
    return kube


def options(html):
    """The namespace dropdown's options as (value, label, selected)."""
    return [
        (value, label, bool(selected))
        for value, selected, label in re.findall(
            r'<option value="([^"]*)"( selected)?>([^<]*)</option>', html
        )
    ]


# Admin mode: no -n.


def test_admin_landing_page_shows_all_namespaces(admin, two_namespaces):
    response = admin.get("/")

    assert response.status_code == 200
    html = response.text
    assert "Summary: all namespaces" in html
    assert "<th>Namespace</th>" in html
    for name in ("ip1", "ip2", "base", "gpu"):
        assert f"<td>{name}</td>" in html
    assert options(html) == [
        ("", "All namespaces", True),
        ("tenant-a", "tenant-a", False),
        ("tenant-b", "tenant-b", False),
        ("tenant-c", "tenant-c", False),
    ]
    assert "Signed in as bob" in html
    assert 'data-refresh-seconds="5"' in html
    assert {call[2] for call in two_namespaces.calls} == {None}
    assert {call[0] for call in two_namespaces.calls} == {API_CLIENT}


def test_admin_picks_a_namespace(admin, two_namespaces):
    html = admin.get("/?namespace=tenant-b").text

    assert "Summary: tenant-b" in html
    assert "<th>Namespace</th>" not in html
    assert "<td>ip2</td>" in html
    assert "<td>ip1</td>" not in html
    assert ("tenant-b", "tenant-b", True) in options(html)
    assert ("", "All namespaces", False) in options(html)


def test_admin_dropdown_keeps_a_namespace_without_objects(admin, two_namespaces):
    html = admin.get("/?namespace=empty").text
    assert ("empty", "empty", True) in options(html)
    assert "No infra planes in empty." in html


def test_refreshes_skip_the_dropdown_and_identity(admin, two_namespaces):
    response = admin.get("/?namespace=tenant-b", headers={FRAGMENT_HEADER: "1"})

    assert response.status_code == 200
    assert "<html" not in response.text
    assert "<td>ip2</td>" in response.text
    assert {call[2] for call in two_namespaces.calls} == {"tenant-b"}
    assert two_namespaces.whoami_calls == 0


def test_admin_without_cluster_wide_access_is_told_to_use_n(admin, kube):
    kube.cluster_error = ApiError(403, 'cclinfraplanes is forbidden: User "bob" cannot list')

    response = admin.get("/")

    assert response.status_code == 403
    assert "Cannot read all namespaces" in response.text
    assert "User &#34;bob&#34; cannot list" in response.text
    assert "ccluster dashboard -n NAMESPACE" in response.text
    assert "data-refresh-seconds" not in response.text


# User mode: -n tenant-a.


def test_user_landing_page_shows_only_its_namespace(user, two_namespaces):
    html = user.get("/").text

    assert "Summary: tenant-a" in html
    assert "<th>Namespace</th>" not in html
    assert "<td>ip1</td>" in html
    assert "<td>ip2</td>" not in html
    assert options(html) == [("tenant-a", "tenant-a", True)]
    assert {call[2] for call in two_namespaces.calls} == {"tenant-a"}


def test_user_mode_refuses_other_namespaces_without_reading(user, kube):
    response = user.get("/?namespace=tenant-b")

    assert response.status_code == 404
    assert "Namespace tenant-b is not served here" in response.text
    assert kube.calls == []


def test_user_mode_accepts_its_own_namespace(user, kube):
    assert user.get("/?namespace=tenant-a").status_code == 200


def test_user_without_access_sees_the_namespace_panel(user, kube):
    kube.error = ApiError(403, "cclinfraplanes is forbidden")

    response = user.get("/")

    assert response.status_code == 403
    assert "No access to namespace tenant-a" in response.text
    assert "cclcomponentset-viewer-role" in response.text


# Both modes.


def test_an_empty_cluster_says_so(admin):
    html = admin.get("/").text
    assert "No infra planes in any namespace." in html
    assert "No component sets in any namespace." in html


def test_summary_and_table_columns(admin, two_namespaces):
    html = admin.get("/?namespace=tenant-a").text
    assert "1 available · 1 verified" in html
    assert "1 rolling out" in html
    assert "1 healthy · 1 progressing · 1 not released" in html
    assert "1/3 healthy" in html
    assert "WaveSyncing" in html


def test_unreachable_api_server_renders_a_panel(admin, kube):
    kube.error = MaxRetryError(None, "/apis", "connection refused")

    response = admin.get("/")

    assert response.status_code == 502
    assert "Could not reach the API server" in response.text


def test_failed_refresh_keeps_its_error_status(admin, kube):
    kube.error = ApiError(401, "Unauthorized")

    response = admin.get("/", headers={FRAGMENT_HEADER: "1"})

    assert response.status_code == 401
    assert "<html" not in response.text


def test_request_data_is_escaped(user):
    html = user.get("/?namespace=<script>alert(1)</script>").text
    assert "<script>alert(1)</script>" not in html
    assert "&lt;script&gt;" in html


def test_only_the_landing_page_is_served(admin, kube):
    assert admin.get("/namespaces/tenant-a").status_code == 404
    assert kube.calls == []


def test_responses_forbid_caching_framing_and_foreign_content(admin):
    headers = admin.get("/").headers
    assert headers["content-security-policy"].startswith("default-src 'self'")
    assert "frame-ancestors 'none'" in headers["content-security-policy"]
    assert headers["cache-control"] == "no-store"
    assert headers["referrer-policy"] == "no-referrer"


def test_requests_for_other_hosts_are_refused(admin, kube):
    response = admin.get("/", headers={"host": "attacker.example"})
    assert response.status_code == 400
    assert kube.calls == []


@pytest.mark.parametrize("method", ["POST", "PUT", "PATCH", "DELETE"])
def test_only_get_is_served(admin, kube, method):
    response = admin.request(method, "/")
    assert response.status_code == 405
    assert kube.calls == []


def test_static_files_are_served(admin):
    assert admin.get("/static/dashboard.js").status_code == 200
    assert admin.get("/static/dashboard.css").status_code == 200


# View models.


def plane(**kwargs):
    return CCLInfraPlane.model_validate(infra_plane_body(**kwargs))


def component_set(**kwargs):
    return CCLComponentSet.model_validate(component_set_body(**kwargs))


def test_summary_counts_planes_and_what_needs_attention():
    summary = views.summary(
        [
            plane(name="ip1"),
            plane(name="ip2", verified="False"),
            plane(name="gpu-east", available="False", verified=None),
            plane(name="new", available=None, verified=None),
        ],
        [],
    )

    assert (summary.infra_planes, summary.available, summary.verified, summary.unavailable) == (
        4, 2, 1, 2,
    )
    assert [(a.name, a.problem) for a in summary.attention] == [
        ("ip2", "not verified: ComponentsNotVerified"),
        ("gpu-east", "not available: ClusterUnreachable"),
        ("new", "not available: not reported yet"),
    ]


@pytest.mark.parametrize(
    "conditions, meta, expected",
    [
        (FINISHED, {}, views.FINISHED),
        (ROLLING_OUT, {}, views.ROLLING_OUT),
        (FINISHED, {"deletionTimestamp": CREATED}, views.TERMINATING),
        ([*FINISHED[:2], condition("Progressing", "True", "WaveSyncFailed", 2)], {}, views.FAILED),
        ([condition("Validated", "False", "InfraPlaneNotReady", 2), *ROLLING_OUT[1:]], {},
         views.FAILED),
        # A failure from an earlier generation is not current.
        ([condition("Validated", "False", "InfraPlaneNotReady", 1), *ROLLING_OUT[1:]], {},
         views.ROLLING_OUT),
    ],
)
def test_component_set_status(conditions, meta, expected):
    assert views.component_set_status(component_set(conditions=conditions, **meta)) == expected


def test_summary_counts_sets_and_component_health():
    failed = [*FINISHED[:2], condition("Progressing", "True", "WaveSyncFailed", 2)]
    summary = views.summary(
        [],
        [
            component_set(name="a"),
            component_set(name="b", conditions=failed, syncs=[
                {"name": "cert-manager", "health": "Degraded", "sync": "Synced",
                 "comparedToMatchesSpec": True},
                {"name": "istio", "health": "", "sync": "Unknown", "comparedToMatchesSpec": False},
            ]),
        ],
    )

    assert [(c.label, c.count) for c in summary.set_statuses] == [
        ("Rolling out", 1), ("Failed", 1),
    ]
    assert summary.components == 6
    assert [(c.label, c.count) for c in summary.component_health] == [
        ("Healthy", 1), ("Progressing", 1), ("Degraded", 1), ("Unknown", 1),
        ("Not released", 2),
    ]
    assert [(a.kind, a.name, a.problem) for a in summary.attention] == [
        ("CCLComponentSet", "b", "failed: WaveSyncFailed"),
    ]


def test_overview_rows_follow_the_printer_columns():
    overview = views.overview([plane(name="ip2", verified="False")], [component_set()])

    (plane_row,) = overview.infra_planes
    assert (plane_row.namespace, plane_row.available, plane_row.verified) == (
        "tenant-a", "True", "False",
    )
    (set_row,) = overview.component_sets
    assert (set_row.status, set_row.healthy, set_row.total, set_row.progress) == (
        views.ROLLING_OUT, 1, 3, "WaveSyncing",
    )


def test_namespaces_come_from_both_kinds():
    assert views.namespaces(
        [plane(namespace="b"), plane(namespace="a")], [component_set(namespace="c")]
    ) == ["a", "b", "c"]


@pytest.mark.parametrize(
    "delta, expected",
    [
        (timedelta(seconds=-3), "0s"),
        (timedelta(seconds=45), "45s"),
        (timedelta(minutes=12), "12m"),
        (timedelta(hours=5), "5h"),
        (timedelta(days=3), "3d"),
        (timedelta(days=800), "2y"),
    ],
)
def test_human_duration(delta, expected):
    assert views.human_duration(delta.total_seconds()) == expected
