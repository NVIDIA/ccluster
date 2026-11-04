# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Register a cluster with the control plane.

The bearer token and CA certificate are those of a ServiceAccount on the cluster being
registered, with the rights Argo CD needs there. Inside the control-plane cluster:

    python create_infraplane.py --namespace tenant-a \\
        edge-1 https://10.0.0.1:6443 --token-file token --ca-file ca.crt

Omit server, --token-file and --ca-file to register the cluster the operator itself runs
in instead, credential-free:

    python create_infraplane.py --namespace tenant-a ccluster-local

The operator derives the cluster's DNS domain, <name>.<namespace>.<its base domain>, so the
name must be a DNS label.

From a workstation, add --kubeconfig to connect with the current kubeconfig context
instead of the in-cluster configuration.
"""

import argparse
from pathlib import Path

from kubernetes import client as k8s
from kubernetes import config

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
        "--namespace",
        required=True,
        help="namespace to register it in; created if it does not exist",
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

    if args.kubeconfig:
        api_client = config.new_client_from_config()
    else:
        config.load_incluster_config()
        api_client = k8s.ApiClient()
    client = CCLClient(args.namespace, api_client=api_client)
    client.ensure_namespace()
    plane = client.apply_infraplane(
        name=args.name,
        server=args.server,
        bearer_token=args.token_file.read_text().strip() if args.token_file else None,
        ca_cert=args.ca_file.read_bytes() if args.ca_file else None,
    )
    print(f"created {plane.metadata.namespace}/{plane.metadata.name}")


if __name__ == "__main__":
    main()
