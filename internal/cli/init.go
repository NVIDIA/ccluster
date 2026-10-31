/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"

	"github.com/spf13/cobra"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

const kindCCLInfraPlane = "CCLInfraPlane"

func newInitCommand() *cobra.Command {
	var kubeContext string
	cmd := &cobra.Command{
		Use:   "init --context CONTEXT",
		Short: "Set the control plane the other commands work with.",
		Long: `Set the control plane the other commands work with.

Checks that the kubeconfig context exists and that its cluster serves the ccluster API,
then saves the context name to the ccluster config file. Running it again replaces the
saved control plane. Only the context name is saved; credentials stay in the kubeconfig.

The config file is $CCLUSTER_CONFIG, else ccluster/config.yaml under $XDG_CONFIG_HOME
or ~/.config.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			cfg, err := restConfig(kubeContext)
			if err != nil {
				return err
			}
			client, err := discovery.NewDiscoveryClientForConfig(cfg)
			if err != nil {
				return err
			}
			if err := servesInfraPlanes(client, kubeContext); err != nil {
				return err
			}

			path, err := saveControlPlane(kubeContext)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Initialized ccluster with control plane %s (saved to %s)\n", kubeContext, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "Kubeconfig context of the control-plane cluster.")
	_ = cmd.MarkFlagRequired("context")
	return cmd
}

// servesInfraPlanes returns an error unless the cluster serves CCLInfraPlane, which only
// a cluster with the ccluster-cp operator's CRDs does.
func servesInfraPlanes(client discovery.DiscoveryInterface, kubeContext string) error {
	notServed := fmt.Errorf(
		"control plane %s does not serve %s; is the ccluster-cp operator installed?",
		kubeContext, kindCCLInfraPlane)

	resources, err := client.ServerResourcesForGroupVersion(
		cclusternvidiacomv1alpha1.GroupVersion.String())
	if apierrors.IsNotFound(err) {
		return notServed
	}
	if err != nil {
		return apiError(err)
	}
	for _, resource := range resources.APIResources {
		if resource.Kind == kindCCLInfraPlane {
			return nil
		}
	}
	return notServed
}
