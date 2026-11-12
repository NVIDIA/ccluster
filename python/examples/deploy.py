# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Serve a small LLM on a registered infra-plane cluster, or remove it.

The model is Qwen2.5-0.5B-Instruct, served by llama.cpp on CPU, so no GPU is needed. It is
deployed in two component sets, each rolled out in order:

1. "dependencies": the infra-plane dependencies the service needs, installed by the
   control plane (auto-mode).
2. "qwen": the model server.

Prerequisites: the cluster is registered, with register.py or `ccluster register`; the
control plane publishes DNS for it; its ingress load balancer is reachable from the
internet, for the Let's Encrypt certificate; and `pip install ccluster-py`. Then, with the
control plane's kubeconfig context:

    python deploy.py --context <control-plane context> --namespace team-a --infra-plane edge-1

Rollout takes several minutes. When it finishes, the script prints a request to try.
Add --delete to remove the model server, then the dependencies.
"""

import argparse
import time

from kubernetes.config import new_client_from_config
from kubernetes.dynamic.exceptions import NotFoundError

from ccluster import catalogs
from ccluster.catalogs.cluster_base import (
    argo_rollouts,
    cert_manager,
    final_stack,
    ingress_nginx,
    istio,
    keda,
    kube_prometheus_stack,
)
from ccluster.catalogs.deployments import inference_deployment as inference
from ccluster.client import CCLClient, component_set_current_conditions

DEPENDENCIES = "dependencies"
SERVICE = "qwen"
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
    # The dependencies, in the order and namespaces the catalog expects: Prometheus's CRDs
    # and server, Istio's traffic rules, cert-manager and the Let's Encrypt issuer in
    # final-stack for TLS, the ingress controller, KEDA for autoscaling and Argo Rollouts
    # to run the pods.
    client.apply_component_set(
        name=DEPENDENCIES,
        components=[
            catalogs.component(name, plane, chart, namespace)
            for name, chart, namespace in [
                ("kube-prometheus-stack", kube_prometheus_stack.CHART, "prometheus"),
                ("istio", istio.CHART, "istio-system"),
                ("cert-manager", cert_manager.CHART, "cert-manager"),
                ("ingress-nginx", ingress_nginx.CHART, "ingress-nginx"),
                ("final-stack", final_stack.CHART, "final-stack"),
                ("keda", keda.CHART, "keda"),
                ("argo-rollouts", argo_rollouts.CHART, "argo-rollouts"),
            ]
        ],
    )
    wait_finished(client, DEPENDENCIES)

    # The cluster's domain, <plane>.<namespace>.<base domain>; the model is served at
    # qwen.nginx.<domain>.
    domain = client.get_infraplane(name=plane).status.domain
    size = inference.Resources(cpu="2", memory="2Gi")
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
                    image_url="ghcr.io/ggml-org/llama.cpp:server-b5350",
                    args=[
                        "--hf-repo", "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
                        "--hf-file", "qwen2.5-0.5b-instruct-q4_k_m.gguf",
                        "--host", "0.0.0.0",
                        "--port", "8080",
                    ],
                    container_port=8080,
                    healthcheck="/health",
                    domain=domain,
                    resources=inference.ResourceRequirements(limits=size, requests=size),
                    # The default scheduler: this example installs no kai-scheduler.
                    scheduler_name="",
                ),
            ),
        ],
    )
    wait_finished(client, SERVICE)

    print(f"\n{SERVICE} is running at https://{SERVICE}.nginx.{domain}. Try:\n")
    print(
        f"  curl https://{SERVICE}.nginx.{domain}/v1/chat/completions"
        " -H 'Content-Type: application/json' \\\n"
        "    -d '{\"messages\": [{\"role\": \"user\","
        " \"content\": \"What is Kubernetes, in one sentence?\"}]}'"
    )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--context", help="control-plane kubeconfig context; default: current")
    parser.add_argument("--namespace", required=True, help="namespace the cluster is registered in")
    parser.add_argument("--infra-plane", help="name the cluster is registered as")
    parser.add_argument(
        "--delete", action="store_true", help="remove the model server and the dependencies"
    )
    args = parser.parse_args()
    if not args.delete and args.infra_plane is None:
        parser.error("--infra-plane is required unless --delete is given")

    client = CCLClient(args.namespace, api_client=new_client_from_config(context=args.context))
    if args.delete:
        # The model server first: removing a set uninstalls its charts, and the server
        # depends on the dependencies.
        for name in (SERVICE, DEPENDENCIES):
            client.delete_component_set(name=name)
            wait_deleted(client, name)
        return
    deploy(client, args.infra_plane)


if __name__ == "__main__":
    main()
