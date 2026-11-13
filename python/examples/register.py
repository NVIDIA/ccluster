# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Register an infra-plane cluster with the control plane, or unregister it.

The token and CA certificate are those of a ServiceAccount on the infra-plane cluster
with the rights Argo CD needs there; examples/quickstart/README.md at the repository
root shows how to create them. With the control plane's kubeconfig context:

    python register.py --context <control-plane context> --namespace team-a \\
        edge-1 https://<infra-plane API server> --token-file token --ca-file ca.crt

Omit the server, --token-file and --ca-file to register the control-plane cluster itself,
credential-free. Add --delete to unregister the cluster; the infra plane is not touched.
"""

import argparse
import time
from pathlib import Path

from kubernetes.config import new_client_from_config

from ccluster.client import CCLClient


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("name", help="name to register the cluster as; a DNS label")
    parser.add_argument(
        "server",
        nargs="?",
        default=None,
        help="API server URL of the cluster; omit to register the control-plane cluster",
    )
    parser.add_argument("--context", help="control-plane kubeconfig context; default: current")
    parser.add_argument(
        "--namespace", required=True, help="namespace to register it in; created if missing"
    )
    parser.add_argument("--token-file", type=Path, help="the ServiceAccount's token")
    parser.add_argument("--ca-file", type=Path, help="the API server's CA certificate, PEM")
    parser.add_argument("--delete", action="store_true", help="unregister the cluster")
    args = parser.parse_args()

    client = CCLClient(args.namespace, api_client=new_client_from_config(context=args.context))
    if args.delete:
        client.delete_infraplane(name=args.name)
        print(f"unregistered {args.namespace}/{args.name}")
        return

    client.ensure_namespace()
    client.apply_infraplane(
        name=args.name,
        server=args.server,
        bearer_token=args.token_file.read_text().strip() if args.token_file else None,
        ca_cert=args.ca_file.read_bytes() if args.ca_file else None,
    )

    # ClusterConfigApplied turns True once the control plane has reached the cluster and
    # written its domain there. Available only means the credentials were handed to Argo CD.
    # A False is not final: after a change of server it can still describe the old one.
    print(f"registered {args.namespace}/{args.name}; waiting for the control plane to reach it")
    deadline = time.monotonic() + 5 * 60
    while True:
        plane = client.get_infraplane(name=args.name)
        conditions = {c.type: c for c in (plane.status.conditions or [])} if plane.status else {}
        applied = conditions.get("ClusterConfigApplied")
        if applied is not None and applied.status.value == "True":
            print(f"{args.name} is registered; its domain is {plane.status.domain}")
            return
        if time.monotonic() >= deadline:
            break
        time.sleep(5)
    if applied is None:
        raise TimeoutError(
            f"{args.name} has no ClusterConfigApplied condition; is the operator running?"
        )
    raise TimeoutError(
        f"{args.name}: ClusterConfigApplied is {applied.status.value} ({applied.reason}): "
        f"{applied.message}; if the control plane cannot reach the server, register the "
        "cluster again with one it can reach"
    )


if __name__ == "__main__":
    main()
