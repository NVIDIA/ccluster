# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Create or delete a component set on a registered cluster.

Deploys the quickstart pair, argo-rollouts then keda, onto the CCLInfraPlane named by
--infra-plane, which must be in the same namespace and already Available. keda's typed
Values pin the operator to one replica, as examples/quickstart does; argo-rollouts uses
the chart's defaults. Inside the control-plane cluster:

    python create_component_set.py --namespace tenant-a --infra-plane ccluster-local local-base

Add --delete to remove the set instead. The command returns as soon as the delete is
accepted; the operator then uninstalls the charts in reverse, one at a time, and
`kubectl get cclcomponentset -n tenant-a` shows when the set is gone:

    python create_component_set.py --namespace tenant-a --delete local-base

From a workstation, add --kubeconfig to connect with the current kubeconfig context
instead of the in-cluster configuration.
"""

import argparse

from kubernetes.config import new_client_from_config

from ccluster import catalogs
from ccluster.catalogs.cluster_base import argo_rollouts, keda
from ccluster.client import CCLClient


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("name", help="name of the CCLComponentSet")
    parser.add_argument(
        "--namespace", required=True, help="namespace the CCLComponentSet lives in"
    )
    parser.add_argument(
        "--infra-plane",
        default=None,
        help="name of the CCLInfraPlane to deploy onto; required unless --delete",
    )
    parser.add_argument(
        "--delete", action="store_true", help="delete the set instead of creating it"
    )
    parser.add_argument(
        "--kubeconfig",
        action="store_true",
        help="connect with the local kubeconfig instead of the in-cluster configuration",
    )
    args = parser.parse_args()
    if not args.delete and args.infra_plane is None:
        parser.error("--infra-plane is required unless --delete is given")

    api_client = new_client_from_config() if args.kubeconfig else None
    client = CCLClient(api_client=api_client)
    if args.delete:
        client.delete_component_set(args.namespace, args.name)
        print(f"deleted {args.namespace}/{args.name}")
        return

    component_set = client.apply_component_set(
        args.namespace,
        args.name,
        [
            catalogs.component(
                "argo-rollouts", args.infra_plane, argo_rollouts.CHART, "argo-rollouts"
            ),
            catalogs.component(
                "keda",
                args.infra_plane,
                keda.CHART,
                "keda",
                keda.Values(
                    keda=keda.ChartValues(operator=keda.Replicas(replica_count=1)),
                ),
            ),
        ],
    )
    print(f"created {component_set.metadata.namespace}/{component_set.metadata.name}")


if __name__ == "__main__":
    main()
