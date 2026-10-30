# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The dashboard renders a namespace's infra planes and component sets from the API, with
the source's credentials, and only for the hosts it serves, without a cluster."""

import json
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from kubernetes import client as k8s
from starlette.testclient import TestClient
from urllib3.exceptions import MaxRetryError

from ccluster import dashboard
from ccluster.dashboard import reads, views
from ccluster.dashboard.routes import FRAGMENT_HEADER
from ccluster.v1alpha1 import CCLComponentSet, CCLInfraPlane

NS = "tenant-a"
CREATED = "2026-01-01T00:00:00Z"
NOW = datetime(2026, 1, 5, tzinfo=timezone.utc)


def condition(condition_type, status, reason, generation=1, message=""):
    return {
        "type": condition_type,
        "status": status,
        "reason": reason,
        "message": message or f"message for {reason}",
        "lastTransitionTime": CREATED,
        "observedGeneration": generation,
    }


def infra_plane_body(name="edge-1", generation=1, conditions=None, verified="True", **meta):
    return {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLInfraPlane",
        "metadata": {
            "name": name, "namespace": NS, "generation": generation,
            "creationTimestamp": CREATED, **meta,
        },
        "spec": {
            "clusterCredentialsRef": {"name": f"{name}-credentials"},
            "domain": f"{name}.example.com",
        },
        "status": {
            "observedGeneration": generation,
            "conditions": conditions if conditions is not None else [
                condition("Available", "True", "ClusterRegistered", generation),
                condition("ClusterConfigApplied", "True", "DomainApplied", generation),
            ],
            "clusterConfig": {
                "observedGeneration": 7,
                "conditions": [condition("Verified", verified, "AllVerified", 7)],
            },
        },
    }


def component_set_body(name="base", generation=2, **meta):
    """Three components: the first synced, the second syncing, the third not released."""
    return {
        "apiVersion": "ccluster.nvidia.com/v1alpha1",
        "kind": "CCLComponentSet",
        "metadata": {
            "name": name, "namespace": NS, "generation": generation,
            "creationTimestamp": CREATED, **meta,
        },
        "spec": {
            "components": [
                {
                    "name": component,
                    "infraPlaneRef": {"name": "edge-1"},
                    "chart": f"catalogs/cluster_base/{component}",
                    "namespace": component,
                }
                for component in ("cert-manager", "istio", "keda")
            ]
        },
        "status": {
            "observedGeneration": generation,
            # Reported in another order than the spec's, and with one the spec dropped.
            "componentSyncs": [
                {"name": "istio", "health": "Progressing", "sync": "OutOfSync",
                 "comparedToMatchesSpec": True, "message": "waiting for istiod"},
                {"name": "old", "health": "Healthy", "sync": "Synced",
                 "comparedToMatchesSpec": True},
                {"name": "cert-manager", "health": "Healthy", "sync": "Synced",
                 "comparedToMatchesSpec": True},
            ],
            "conditions": [
                condition("Validated", "True", "ComponentsAccepted", generation),
                condition("ComponentsApplied", "True", "ApplicationsCreated", generation),
                condition("Progressing", "True", "WaveSyncing", generation),
                # Not rewritten since the previous generation.
                condition("Degraded", "False", "OldNews", generation - 1),
            ],
        },
    }


class FakeKubernetes:
    """The API server behind the fake CustomObjectsApi and AuthenticationV1Api.

    objects maps (plural, name) to a body; error, when set, is raised by every read.
    """

    def __init__(self):
        self.objects = {}
        self.error = None
        self.calls = []
        self.whoami_calls = 0

    def add(self, plural, body):
        self.objects[(plural, body["metadata"]["name"])] = body

    def custom_objects(self, api_client):
        def list_namespaced_custom_object(group, version, namespace, plural):
            self._read(api_client, "list", namespace, plural)
            return {"items": [b for (p, _), b in sorted(self.objects.items()) if p == plural]}

        def get_namespaced_custom_object(group, version, namespace, plural, name):
            self._read(api_client, "get", namespace, plural, name)
            if (plural, name) not in self.objects:
                raise ApiError(404, f'{plural}.ccluster.nvidia.com "{name}" not found')
            return self.objects[(plural, name)]

        return SimpleNamespace(
            list_namespaced_custom_object=list_namespaced_custom_object,
            get_namespaced_custom_object=get_namespaced_custom_object,
        )

    def authentication(self, api_client):
        def create_self_subject_review(body):
            self.whoami_calls += 1
            return SimpleNamespace(
                status=SimpleNamespace(user_info=SimpleNamespace(username="bob"))
            )

        return SimpleNamespace(create_self_subject_review=create_self_subject_review)

    def _read(self, api_client, *call):
        self.calls.append((api_client, *call))
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


@pytest.fixture
def client(kube):
    app = dashboard.create_app(
        dashboard.LocalSource(API_CLIENT), namespace=NS, allowed_hosts=["testserver"]
    )
    return TestClient(app)


def test_home_redirects_to_the_namespace(client):
    response = client.get("/", follow_redirects=False)
    assert response.status_code == 302
    assert response.headers["location"] == f"/namespaces/{NS}"


def test_namespace_page_lists_planes_and_sets_with_the_sources_credentials(client, kube):
    kube.add("cclinfraplanes", infra_plane_body())
    kube.add("cclcomponentsets", component_set_body())

    response = client.get(f"/namespaces/{NS}")

    assert response.status_code == 200
    html = response.text
    assert "Infra planes (1)" in html
    assert f'href="/namespaces/{NS}/infraplanes/edge-1"' in html
    assert "edge-1.example.com" in html
    assert "Component sets (1)" in html
    assert f'href="/namespaces/{NS}/componentsets/base"' in html
    assert "WaveSyncing" in html
    assert "Signed in as bob" in html
    assert 'data-refresh-seconds="5"' in html
    assert {call[:4] for call in kube.calls} == {
        (API_CLIENT, "list", NS, "cclinfraplanes"),
        (API_CLIENT, "list", NS, "cclcomponentsets"),
    }


def test_other_namespaces_are_one_url_away(client, kube):
    client.get("/namespaces/tenant-b")
    assert {call[2] for call in kube.calls} == {"tenant-b"}


def test_an_empty_namespace_says_so(client):
    html = client.get(f"/namespaces/{NS}").text
    assert f"No infra planes in {NS}." in html
    assert f"No component sets in {NS}." in html


def test_refresh_requests_get_the_content_alone(client, kube):
    kube.add("cclcomponentsets", component_set_body())

    response = client.get(f"/namespaces/{NS}", headers={FRAGMENT_HEADER: "1"})

    assert response.status_code == 200
    assert "<html" not in response.text
    assert "Component sets (1)" in response.text
    assert kube.whoami_calls == 0


def test_component_set_page_shows_components_in_rollout_order(client, kube):
    kube.add("cclcomponentsets", component_set_body())

    html = client.get(f"/namespaces/{NS}/componentsets/base").text

    positions = [html.index(f">{name}") for name in ("cert-manager", "istio", "keda", "old")]
    assert positions == sorted(positions)
    assert "waiting for istiod" in html
    assert "Not released yet" in html
    assert "Removed from spec" in html


def test_stale_conditions_are_not_shown_as_current(client, kube):
    kube.add("cclcomponentsets", component_set_body())

    html = client.get(f"/namespaces/{NS}/componentsets/base").text

    current, _, earlier = html.partition("Conditions from earlier generations (1)")
    assert "WaveSyncing" in current
    assert "OldNews" not in current
    assert "OldNews" in earlier


def test_infra_plane_page_shows_the_cluster_config_report(client, kube):
    kube.add("cclinfraplanes", infra_plane_body(verified="False"))

    html = client.get(f"/namespaces/{NS}/infraplanes/edge-1").text

    assert "CCLInfraPlane edge-1" in html
    assert "edge-1-credentials" in html
    assert "ClusterRegistered" in html
    assert "AllVerified" in html
    assert 'class="status status-false"' in html


def test_terminating_objects_are_marked(client, kube):
    kube.add("cclcomponentsets", component_set_body(deletionTimestamp=CREATED))

    html = client.get(f"/namespaces/{NS}").text

    assert "Terminating" in html


def test_forbidden_namespace_renders_a_panel_naming_the_viewer_roles(client, kube):
    kube.error = ApiError(403, 'cclinfraplanes is forbidden: User "bob" cannot list')

    response = client.get(f"/namespaces/{NS}")

    assert response.status_code == 403
    assert f"No access to namespace {NS}" in response.text
    assert "User &#34;bob&#34; cannot list" in response.text
    assert "cclcomponentset-viewer-role" in response.text
    assert "data-refresh-seconds" not in response.text


def test_missing_object_renders_not_found(client):
    response = client.get(f"/namespaces/{NS}/componentsets/nope")
    assert response.status_code == 404
    assert "Not found" in response.text
    assert "&#34;nope&#34; not found" in response.text


def test_unreachable_api_server_renders_a_panel(client, kube):
    kube.error = MaxRetryError(None, "/apis", "connection refused")

    response = client.get(f"/namespaces/{NS}")

    assert response.status_code == 502
    assert "Could not reach the API server" in response.text


def test_failed_refresh_keeps_its_error_status(client, kube):
    kube.error = ApiError(401, "Unauthorized")

    response = client.get(f"/namespaces/{NS}", headers={FRAGMENT_HEADER: "1"})

    assert response.status_code == 401
    assert "<html" not in response.text


def test_api_data_is_escaped(client, kube):
    body = component_set_body()
    body["status"]["componentSyncs"][0]["message"] = "<script>alert(1)</script>"
    kube.add("cclcomponentsets", body)

    html = client.get(f"/namespaces/{NS}/componentsets/base").text

    assert "<script>alert(1)</script>" not in html
    assert "&lt;script&gt;" in html


def test_responses_forbid_caching_framing_and_foreign_content(client):
    headers = client.get(f"/namespaces/{NS}").headers
    assert headers["content-security-policy"].startswith("default-src 'self'")
    assert "frame-ancestors 'none'" in headers["content-security-policy"]
    assert headers["cache-control"] == "no-store"
    assert headers["referrer-policy"] == "no-referrer"


def test_requests_for_other_hosts_are_refused(client, kube):
    response = client.get(f"/namespaces/{NS}", headers={"host": "attacker.example"})
    assert response.status_code == 400
    assert kube.calls == []


@pytest.mark.parametrize("method", ["POST", "PUT", "PATCH", "DELETE"])
def test_only_get_is_served(client, kube, method):
    response = client.request(method, f"/namespaces/{NS}")
    assert response.status_code == 405
    assert kube.calls == []


def test_static_files_are_served(client):
    assert client.get("/static/dashboard.js").status_code == 200
    assert client.get("/static/dashboard.css").status_code == 200


def test_overview_rows_follow_the_printer_columns():
    plane = CCLInfraPlane.model_validate(
        infra_plane_body(
            generation=2,
            conditions=[
                condition("Available", "True", "ClusterRegistered", 2),
                condition("ClusterConfigApplied", "False", "Pending", 1),
            ],
        )
    )
    component_set = CCLComponentSet.model_validate(component_set_body())

    overview = views.overview([plane], [component_set], now=NOW)

    (plane_row,) = overview.infra_planes
    assert (plane_row.available, plane_row.cluster_config, plane_row.verified) == (
        "True", "", "True",
    )
    assert plane_row.age == "4d"
    (set_row,) = overview.component_sets
    assert (set_row.applied, set_row.progress, set_row.finished) == (
        "True", "WaveSyncing", False,
    )


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
