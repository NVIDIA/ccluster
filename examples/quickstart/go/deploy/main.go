/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Command deploy serves a small LLM on a registered infra-plane cluster through
// pkg/cclclient, or removes it.
//
// The model is Qwen2.5-0.5B-Instruct, served by llama.cpp on CPU, so no GPU is needed. It
// is deployed in two component sets, each rolled out in order:
//
//  1. "dependencies": the infra-plane dependencies the service needs, installed by the
//     control plane (auto-mode).
//  2. "qwen": the model server.
//
// The cluster must be registered, the control plane must publish DNS for it, its ingress
// load balancer must be reachable from the internet, for the Let's Encrypt certificate,
// and it must serve the resource metrics API, which the autoscalers of istiod and
// ingress-nginx read. From the repository root, with the control plane's kubeconfig
// context:
//
//	go run ./examples/quickstart/go/deploy -context <control-plane context> \
//	    -namespace team-a -infra-plane edge-1
//
// Rollout takes several minutes. When it finishes, the command prints a request to try.
// Add -delete to remove the model server, then the dependencies.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argorollouts "github.com/NVIDIA/ccluster/catalogs/cluster_base/argo_rollouts"
	certmanager "github.com/NVIDIA/ccluster/catalogs/cluster_base/cert_manager"
	finalstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/final_stack"
	ingressnginx "github.com/NVIDIA/ccluster/catalogs/cluster_base/ingress_nginx"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/istio"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/keda"
	kubeprometheusstack "github.com/NVIDIA/ccluster/catalogs/cluster_base/kube_prometheus_stack"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
	inferencedeployment "github.com/NVIDIA/ccluster/catalogs/deployments/inference_deployment"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

const (
	dependencies = "dependencies"
	service      = "qwen"
	timeout      = 30 * time.Minute
)

func main() {
	kubeContext := flag.String("context", "", "control-plane kubeconfig context; default: current")
	namespace := flag.String("namespace", "", "namespace the cluster is registered in")
	plane := flag.String("infra-plane", "", "name the cluster is registered as")
	remove := flag.Bool("delete", false, "remove the model server and the dependencies")
	flag.Parse()
	if *namespace == "" || (*plane == "" && !*remove) {
		log.Fatal("-namespace and -infra-plane are required")
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: *kubeContext},
	).ClientConfig()
	if err != nil {
		log.Fatalf("loading kubeconfig: %v", err)
	}
	client, err := cclclient.NewForConfig(*namespace, config)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	if *remove {
		// The model server first: removing a set uninstalls its charts, and the server
		// depends on the dependencies.
		for _, name := range []string{service, dependencies} {
			if err := client.DeleteComponentSet(ctx, name); err != nil {
				log.Fatal(err)
			}
			waitDeleted(ctx, client, name)
		}
		return
	}
	if err := deploy(ctx, client, *plane); err != nil {
		log.Fatal(err)
	}
}

func deploy(ctx context.Context, client *cclclient.Client, plane string) error {
	// The dependencies, in the order and namespaces the catalog expects: Prometheus's
	// CRDs and server, Istio's traffic rules, cert-manager and the Let's Encrypt issuer in
	// final-stack for TLS, the ingress controller, KEDA for autoscaling and Argo Rollouts
	// to run the pods.
	dependencyComponents, err := components(plane, []named{
		{"kube-prometheus-stack", entry(kubeprometheusstack.New("prometheus", kubeprometheusstack.Values{}))},
		// On GKE: istio.Values{IstioCNI: &istio.IstioCNI{CNI: &istio.CNI{CNIBinDir: new(istio.CNIBinDirGKE)}}}
		{"istio", entry(istio.New("istio-system", istio.Values{}))},
		{"cert-manager", entry(certmanager.New("cert-manager", certmanager.Values{}))},
		{"ingress-nginx", entry(ingressnginx.New("ingress-nginx", ingressnginx.Values{}))},
		{"final-stack", entry(finalstack.New("final-stack", finalstack.Values{}))},
		{"keda", entry(keda.New("keda", keda.Values{}))},
		{"argo-rollouts", entry(argorollouts.New("argo-rollouts", argorollouts.Values{}))},
	})
	if err != nil {
		return err
	}
	if _, err := client.ApplyComponentSet(ctx, dependencies, dependencyComponents); err != nil {
		return err
	}
	if err := waitFinished(ctx, client, dependencies); err != nil {
		return err
	}

	// The cluster's domain, <plane>.<namespace>.<base domain>; the model is served at
	// qwen.nginx.<domain>.
	infraPlane, err := client.GetInfraPlane(ctx, plane)
	if err != nil {
		return err
	}
	domain := infraPlane.Status.Domain
	size := deployments.Resources{CPU: "2", Memory: "2Gi"}
	serviceComponents, err := components(plane, []named{
		{service, entry(inferencedeployment.New(service, inferencedeployment.Values{
			Workload: deployments.Workload{
				Name:         service,
				DeploymentID: service,
				ImageURL:     "ghcr.io/ggml-org/llama.cpp:server-b11706",
				Resources:    deployments.ResourceRequirements{Limits: size, Requests: size},
				// The default scheduler: this example installs no kai-scheduler.
				SchedulerName: new(""),
			},
			Args: []string{
				"--hf-repo", "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
				"--hf-file", "qwen2.5-0.5b-instruct-q4_k_m.gguf",
				"--host", "0.0.0.0",
				"--port", "8080",
			},
			ContainerPort: 8080,
			Healthcheck:   new("/health"),
			Domain:        domain,
		}))},
	})
	if err != nil {
		return err
	}
	if _, err := client.ApplyComponentSet(ctx, service, serviceComponents); err != nil {
		return err
	}
	if err := waitFinished(ctx, client, service); err != nil {
		return err
	}

	host := fmt.Sprintf("%s.nginx.%s", service, domain)
	fmt.Printf("\n%s is running at https://%s. Try:\n\n", service, host)
	fmt.Printf("  curl https://%s/v1/chat/completions -H 'Content-Type: application/json' \\\n", host)
	fmt.Printf("    -d '{\"messages\": [{\"role\": \"user\", \"content\": \"What is Kubernetes, in one sentence?\"}]}'\n")
	return nil
}

// named is a component's name and its chart entry, or the error building the entry.
type named struct {
	name  string
	entry entryResult
}

type entryResult struct {
	entry cclusternvidiacomv1alpha1.ComponentEntry
	err   error
}

func entry(e cclusternvidiacomv1alpha1.ComponentEntry, err error) entryResult {
	return entryResult{e, err}
}

// components builds the component list, in order, all deployed to plane.
func components(plane string, list []named) ([]cclusternvidiacomv1alpha1.ComponentSpec, error) {
	specs := make([]cclusternvidiacomv1alpha1.ComponentSpec, 0, len(list))
	for _, c := range list {
		if c.entry.err != nil {
			return nil, fmt.Errorf("component %s: %w", c.name, c.entry.err)
		}
		specs = append(specs, catalogs.Component(c.name, plane, c.entry.entry))
	}
	return specs, nil
}

func waitFinished(ctx context.Context, client *cclclient.Client, name string) error {
	fmt.Printf("waiting for %s to roll out\n", name)
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			componentSet, err := client.GetComponentSet(ctx, name)
			if err != nil {
				return false, err
			}
			if cclclient.ComponentSetFinished(componentSet) {
				return true, nil
			}
			conditions := cclclient.ComponentSetCurrentConditions(componentSet)
			if progressing, ok := conditions[cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing]; ok {
				fmt.Printf("  %s: %s\n", progressing.Reason, progressing.Message)
			}
			return false, nil
		})
}

func waitDeleted(ctx context.Context, client *cclclient.Client, name string) {
	fmt.Printf("waiting for %s to be removed\n", name)
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			_, err := client.GetComponentSet(ctx, name)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
	if err != nil {
		log.Fatal(err)
	}
}
