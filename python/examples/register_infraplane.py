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

"""Register a cluster with the control plane.

The bearer token and CA certificate are those of a ServiceAccount on the cluster being
registered, with the rights Argo CD needs there. Inside the control-plane cluster:

    python register_infraplane.py --namespace tenant-a edge-1 https://10.0.0.1:6443 \\
        --token-file token --ca-file ca.crt

Omit server, --token-file and --ca-file to register the cluster the operator itself runs
in instead, credential-free:

    python register_infraplane.py --namespace tenant-a ccluster-local

From a workstation, add --kubeconfig to connect with the current kubeconfig context
instead of the in-cluster configuration.
"""

import argparse
from pathlib import Path

from kubernetes.config import new_client_from_config

from ccluster.client import CCLClient


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("name", help="name of the CCLInfraPlane to create")
    parser.add_argument(
        "server",
        nargs="?",
        default=None,
        help="API server URL of the cluster to register; omit to register the "
        "in-cluster server",
    )
    parser.add_argument(
        "--namespace", required=True, help="namespace to register it in"
    )
    parser.add_argument("--token-file", type=Path, default=None)
    parser.add_argument(
        "--ca-file", type=Path, default=None, help="CA certificate, PEM"
    )
    parser.add_argument(
        "--kubeconfig",
        action="store_true",
        help="connect with the local kubeconfig instead of the in-cluster configuration",
    )
    args = parser.parse_args()

    api_client = new_client_from_config() if args.kubeconfig else None
    client = CCLClient(args.namespace, api_client=api_client)
    plane = client.register_infraplane(
        args.name,
        args.server,
        bearer_token=args.token_file.read_text().strip() if args.token_file else None,
        ca_cert=args.ca_file.read_bytes() if args.ca_file else None,
    )
    print(f"registered {plane['metadata']['namespace']}/{plane['metadata']['name']}")


if __name__ == "__main__":
    main()
