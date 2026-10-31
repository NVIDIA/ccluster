# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The dashboard's Kubernetes reads, returned as the generated models.

The group, version and plurals are fixed, so reads go through CustomObjectsApi and need no
API discovery: DynamicClient would query the API server's /version and discovery
documents whenever it is constructed.

A namespace of None reads every namespace. Kubernetes answers such a list with every object
or with 403: it does not narrow a list to the namespaces the caller may read.
"""

from __future__ import annotations

from kubernetes import client as k8s
from urllib3.exceptions import HTTPError

from ccluster.v1alpha1 import API_VERSION, CCLComponentSet, CCLInfraPlane

GROUP, VERSION = API_VERSION.split("/")
INFRA_PLANES = "cclinfraplanes"
COMPONENT_SETS = "cclcomponentsets"


def list_infraplanes(api_client: k8s.ApiClient, namespace: str | None) -> list[CCLInfraPlane]:
    return [
        CCLInfraPlane.model_validate(item)
        for item in _list(api_client, namespace, INFRA_PLANES)
    ]


def list_component_sets(
    api_client: k8s.ApiClient, namespace: str | None
) -> list[CCLComponentSet]:
    return [
        CCLComponentSet.model_validate(item)
        for item in _list(api_client, namespace, COMPONENT_SETS)
    ]


def whoami(api_client: k8s.ApiClient) -> str | None:
    """The user name the API server authenticates the credentials as, if it says.

    None when the server does not serve SelfSubjectReview or the request fails: the name is
    shown for information only.
    """
    try:
        review = k8s.AuthenticationV1Api(api_client).create_self_subject_review(
            k8s.V1SelfSubjectReview()
        )
    except (k8s.ApiException, HTTPError):
        return None
    user_info = review.status.user_info if review.status else None
    return user_info.username if user_info else None


def _list(api_client: k8s.ApiClient, namespace: str | None, plural: str) -> list[dict]:
    api = k8s.CustomObjectsApi(api_client)
    if namespace is None:
        return api.list_cluster_custom_object(GROUP, VERSION, plural)["items"]
    return api.list_namespaced_custom_object(GROUP, VERSION, namespace, plural)["items"]
