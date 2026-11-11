# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Deploy a basic inference service onto a registered infra-plane cluster.

The service is Hugging Face's text-embeddings-inference serving BAAI/bge-small-en-v1.5 on
CPU, so no GPU is needed. It is deployed in two component sets, each rolled out in order:

1. "platform": the platform components the service needs, in auto-mode: Prometheus,
   Istio, ingress-nginx, KEDA and Argo Rollouts.
2. "embeddings": the service itself.

Prerequisites: the cluster is registered, with register.py or `ccluster register`, it
supports LoadBalancer Services, and `pip install ccluster-py`. Then, with the control
plane's kubeconfig context:

    python deploy.py --context <control-plane context> --namespace team-a --infra-plane edge-1

Rollout takes several minutes. When it finishes, the script prints a request to try.
Add --delete to remove the service, then the platform components.
"""

import argparse
import time

from kubernetes.config import new_client_from_config
from kubernetes.dynamic.exceptions import NotFoundError

from ccluster import catalogs
from ccluster.catalogs.cluster_base import (
    argo_rollouts,
    ingress_nginx,
    istio,
    keda,
    kube_prometheus_stack,
)
from ccluster.catalogs.deployments import inference_deployment as inference
from ccluster.client import CCLClient, component_set_current_conditions

SERVICE = "embeddings"
TIMEOUT_SECONDS = 30 * 60


def wait_until(predicate, what: str) -> None:
    deadline = time.monotonic() + TIMEOUT_SECONDS
    while not predicate():
        if time.monotonic() > deadline:
            raise TimeoutError(f"timed out waiting for {what}")
        time.sleep(10)


def wait_finished(client: CCLClient, name: str) -> None:
    print(f"waiting for {name} to roll out")

    def finished() -> bool:
        if client.is_component_set_finished(name=name):
            return True
        conditions = component_set_current_conditions(client.get_component_set(name=name))
        progressing = conditions.get("Progressing")
        if progressing is not None:
            print(f"  {progressing.reason}: {progressing.message}")
        return False

    wait_until(finished, f"component set {name}")


def wait_deleted(client: CCLClient, name: str) -> None:
    def deleted() -> bool:
        try:
            client.get_component_set(name=name)
        except NotFoundError:
            return True
        return False

    print(f"waiting for {name} to be removed")
    wait_until(deleted, f"component set {name} to be removed")


def deploy(client: CCLClient, plane: str) -> None:
    # Platform components, in the order and namespaces the catalog expects. The
    # inference chart needs every one of them: Prometheus's CRDs and server, Istio's
    # traffic rules, the ingress controller, KEDA for autoscaling and Argo Rollouts to
    # run the pods.
    client.apply_component_set(
        name="platform",
        components=[
            catalogs.component(
                "kube-prometheus-stack", plane, kube_prometheus_stack.CHART, "prometheus"
            ),
            catalogs.component("istio", plane, istio.CHART, "istio-system"),
            catalogs.component("ingress-nginx", plane, ingress_nginx.CHART, "ingress-nginx"),
            catalogs.component("keda", plane, keda.CHART, "keda"),
            catalogs.component("argo-rollouts", plane, argo_rollouts.CHART, "argo-rollouts"),
        ],
    )
    wait_finished(client, "platform")

    # The cluster's domain, <plane>.<namespace>.<base domain>; the service is served at
    # embeddings.nginx.<domain>.
    domain = client.get_infraplane(name=plane).status.domain
    resources = inference.Resources(cpu="2", memory="4Gi")
    client.apply_component_set(
        name=SERVICE,
        components=[
            catalogs.component(
                SERVICE,
                plane,
                inference.CHART,
                SERVICE,
                inference.Values(
                    name=SERVICE,
                    deployment_id=SERVICE,
                    image_url="ghcr.io/huggingface/text-embeddings-inference:cpu-1.8",
                    args=["--model-id", "BAAI/bge-small-en-v1.5", "--port", "8080"],
                    container_port=8080,
                    healthcheck="/health",
                    domain=domain,
                    resources=inference.ResourceRequirements(
                        limits=resources, requests=resources
                    ),
                    # The default scheduler: this example installs no kai-scheduler.
                    scheduler_name="",
                ),
            ),
        ],
    )
    wait_finished(client, SERVICE)

    status = client.get_infraplane(name=plane).status
    ingress = next(
        (
            lb
            for lb in (status.cluster_config.load_balancers or [])
            if lb.name == "nginx"
        ),
        None,
    ) if status.cluster_config else None
    address = (ingress.ip or ingress.hostname) if ingress else "<ingress address>"
    host = f"{SERVICE}.nginx.{domain}"
    print(f"\n{SERVICE} is running at https://{host}. Try:\n")
    print(
        f"  curl -k https://{host}/embed --resolve {host}:443:{address} \\\n"
        "    -H 'Content-Type: application/json' -d '{\"inputs\": \"Hello, CCluster\"}'"
    )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--context", help="control-plane kubeconfig context; default: current")
    parser.add_argument("--namespace", required=True, help="namespace the cluster is registered in")
    parser.add_argument("--infra-plane", required=True, help="name the cluster is registered as")
    parser.add_argument(
        "--delete", action="store_true", help="remove the service and platform components"
    )
    args = parser.parse_args()

    client = CCLClient(args.namespace, api_client=new_client_from_config(context=args.context))
    if args.delete:
        # The service first: removing a set uninstalls its charts, and the service
        # depends on the platform components.
        client.delete_component_set(name=SERVICE)
        wait_deleted(client, SERVICE)
        client.delete_component_set(name="platform")
        wait_deleted(client, "platform")
        return
    deploy(client, args.infra_plane)


if __name__ == "__main__":
    main()
