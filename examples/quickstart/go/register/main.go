/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Command register registers an infra-plane cluster with the control plane through
// pkg/cclclient, or unregisters it.
//
// The token and CA certificate are those of a ServiceAccount on the infra-plane cluster
// with the rights Argo CD needs there; ../../README.md shows how to create them. From the
// repository root, with the control plane's kubeconfig context:
//
//	go run ./examples/quickstart/go/register -context <control-plane context> \
//	    -namespace team-a -name edge-1 -server https://<infra-plane API server> \
//	    -token-file token -ca-file ca.crt
//
// Omit -server, -token-file and -ca-file to register the control-plane cluster itself,
// credential-free. Add -delete to unregister the cluster; the infra plane is not touched.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

func main() {
	kubeContext := flag.String("context", "", "control-plane kubeconfig context; default: current")
	namespace := flag.String("namespace", "", "namespace to register the cluster in; created if missing")
	name := flag.String("name", "", "name to register the cluster as; a DNS label")
	server := flag.String("server", "", "API server URL of the cluster; empty registers the control-plane cluster")
	tokenFile := flag.String("token-file", "", "file holding the ServiceAccount's token")
	caFile := flag.String("ca-file", "", "file holding the API server's CA certificate, PEM")
	unregister := flag.Bool("delete", false, "unregister the cluster")
	flag.Parse()
	if *namespace == "" || *name == "" {
		log.Fatal("-namespace and -name are required")
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

	if *unregister {
		if err := client.DeleteInfraPlane(ctx, *name); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("unregistered %s/%s\n", *namespace, *name)
		return
	}

	opts := cclclient.InfraPlaneOptions{Name: *name, Server: *server}
	if *tokenFile != "" {
		token, err := os.ReadFile(*tokenFile)
		if err != nil {
			log.Fatal(err)
		}
		opts.BearerToken = strings.TrimSpace(string(token))
	}
	if *caFile != "" {
		if opts.CACert, err = os.ReadFile(*caFile); err != nil {
			log.Fatal(err)
		}
	}
	if err := client.EnsureNamespace(ctx); err != nil {
		log.Fatal(err)
	}
	if _, err := client.ApplyInfraPlane(ctx, opts); err != nil {
		log.Fatal(err)
	}

	// ClusterConfigApplied turns True once the control plane has reached the cluster and
	// written its domain there. Available only means the credentials were handed to Argo CD.
	// A False is not final: after a change of server it can still describe the old one.
	fmt.Printf("registered %s/%s; waiting for the control plane to reach it\n", *namespace, *name)
	var plane *cclusternvidiacomv1alpha1.CCLInfraPlane
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			plane, err = client.GetInfraPlane(ctx, *name)
			if err != nil {
				return false, err
			}
			return apimeta.IsStatusConditionTrue(plane.Status.Conditions,
				cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied), nil
		})
	if wait.Interrupted(err) {
		applied := apimeta.FindStatusCondition(plane.Status.Conditions,
			cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied)
		if applied == nil {
			log.Fatalf("%s has no ClusterConfigApplied condition; is the operator running?", *name)
		}
		log.Fatalf("%s: ClusterConfigApplied is %s (%s): %s; if the control plane cannot reach the "+
			"server, register the cluster again with one it can reach",
			*name, applied.Status, applied.Reason, applied.Message)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s is registered; its domain is %s\n", *name, plane.Status.Domain)
}
