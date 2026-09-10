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

"""A client for the control-plane operator's API.

CCLClient registers infra-plane clusters with the control plane and removes them again.
It writes into one namespace, fixed at construction, and by default runs inside the
control-plane cluster using the pod's ServiceAccount.
"""

from __future__ import annotations

import base64
import json
from pathlib import Path

from kubernetes import client as k8s
from kubernetes import config
from kubernetes.dynamic import DynamicClient
from kubernetes.dynamic.exceptions import NotFoundError

from ccluster.v1alpha1 import CCLInfraPlane, CCLInfraPlaneSpec

# The package-level LocalObjectReference is the CCLComponentSet module's; the generator
# emits a second, identical class in the CCLInfraPlane module and the spec wants that one.
from ccluster.v1alpha1.cclinfraplane import LocalObjectReference, ObjectMeta

__all__ = ["FIELD_MANAGER", "CCLClient"]

FIELD_MANAGER = "ccluster-py"
API_VERSION = "ccluster.nvidia.com/v1alpha1"

SERVICE_ACCOUNT_NAMESPACE = Path(
    "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)


def credentials_secret_name(cluster: str) -> str:
    return f"{cluster}-credentials"


class CCLClient:
    """Register and unregister infra-plane clusters in one namespace.

    With no arguments the client loads the in-cluster configuration and uses the pod's
    own namespace. Outside a cluster pass both: an ApiClient (for example from
    kubernetes.config.new_client_from_config()) and the namespace to write into.
    """

    def __init__(
        self,
        namespace: str | None = None,
        api_client: k8s.ApiClient | None = None,
    ) -> None:
        if api_client is None:
            config.load_incluster_config()
            api_client = k8s.ApiClient()
        if namespace is None:
            try:
                namespace = SERVICE_ACCOUNT_NAMESPACE.read_text().strip()
            except OSError as e:
                raise ValueError(
                    "namespace is required when not running inside a cluster"
                ) from e
        self.namespace = namespace
        self._dynamic = DynamicClient(api_client)
        self._secrets = self._dynamic.resources.get(api_version="v1", kind="Secret")
        self._infra_planes = self._dynamic.resources.get(
            api_version=API_VERSION, kind="CCLInfraPlane"
        )

    def register_cluster(
        self,
        name: str,
        server: str,
        bearer_token: str,
        ca_cert: str | bytes,
    ) -> CCLInfraPlane:
        """Register the cluster at server as the CCLInfraPlane called name.

        Creates (or updates) a Secret named "<name>-credentials" in Argo CD's
        cluster-secret shape, then a CCLInfraPlane pointing at it. ca_cert is the API
        server's CA certificate in PEM. The call returns as soon as both objects are
        applied; it does not wait for the operator to report Available.
        """
        if isinstance(ca_cert, str):
            ca_cert = ca_cert.encode()
        secret_name = credentials_secret_name(name)
        secret = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {"name": secret_name, "namespace": self.namespace},
            "type": "Opaque",
            "stringData": {
                "server": server,
                "config": json.dumps(
                    {
                        "bearerToken": bearer_token,
                        "tlsClientConfig": {
                            "insecure": False,
                            "caData": base64.b64encode(ca_cert).decode(),
                        },
                    }
                ),
            },
        }
        self._apply(self._secrets, secret)

        plane = CCLInfraPlane(
            metadata=ObjectMeta(name=name, namespace=self.namespace),
            spec=CCLInfraPlaneSpec(
                cluster_credentials_ref=LocalObjectReference(name=secret_name),
            ),
        )
        applied = self._apply(
            self._infra_planes, plane.model_dump(by_alias=True, exclude_none=True)
        )
        return CCLInfraPlane.model_validate(applied.to_dict())

    def unregister_cluster(self, name: str) -> None:
        """Remove the CCLInfraPlane called name and its credentials Secret.

        The CCLInfraPlane goes first so its finalizer can remove the mirrored Argo CD
        cluster Secret. Either object already being gone is not an error.
        """
        self._delete(self._infra_planes, name)
        self._delete(self._secrets, credentials_secret_name(name))

    def _apply(self, resource, body: dict):
        return self._dynamic.server_side_apply(
            resource,
            body=body,
            namespace=self.namespace,
            field_manager=FIELD_MANAGER,
            force_conflicts=True,
        )

    def _delete(self, resource, name: str) -> None:
        try:
            self._dynamic.delete(resource, name=name, namespace=self.namespace)
        except NotFoundError:
            pass
