/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

func newListCommand() *cobra.Command {
	var namespace, output string
	var allNamespaces bool
	cmd := &cobra.Command{
		Use:   "list (infraplanes|componentsets) (--namespace NAMESPACE | --all-namespaces)",
		Short: "List the infra planes or component sets in a namespace, or in all of them.",
		Long: `List the infra planes or component sets in a namespace, or in all of them.

Reads the CCLInfraPlanes or CCLComponentSets in NAMESPACE on the control plane saved by
ccluster init and prints a table with the same columns as kubectl get, as the objects
report them. infraplanes can be written infraplane or ip, componentsets componentset or
cs.

With --all-namespaces, reads them across the control plane and adds a NAMESPACE column.
This needs list permission on them at cluster scope.

With --output yaml or json, prints the CCLInfraPlaneList or CCLComponentSetList instead.`,
		Example: `  ccluster list infraplanes -n tenant-a
  ccluster list ip -A
  ccluster list cs -n tenant-a -o yaml`,
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeResources(func(r resource) string { return r.plural }), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := parseResource(args[0])
			if err != nil {
				return err
			}
			if !allNamespaces {
				if err := checkNamespace(namespace); err != nil {
					return err
				}
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			cmd.SilenceUsage = true

			cfg, err := controlPlaneRESTConfig()
			if err != nil {
				return err
			}
			if allNamespaces {
				namespace = metav1.NamespaceAll
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if r.singular == infraPlanes.singular {
				list, err := listInfraPlanes(ctx, cfg, namespace)
				if err != nil {
					return apiError(err)
				}
				if output != outputHuman {
					return printInfraPlaneList(out, list, output)
				}
				if len(list.Items) == 0 {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), noneFound("CCLInfraPlanes", namespace))
					return nil
				}
				return printInfraPlaneTable(out, list.Items, allNamespaces)
			}

			list, err := listComponentSets(ctx, cfg, namespace)
			if err != nil {
				return apiError(err)
			}
			if output != outputHuman {
				return printComponentSetList(out, list, output)
			}
			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), noneFound("CCLComponentSets", namespace))
				return nil
			}
			return printComponentSetTable(out, list.Items, allNamespaces)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Control-plane namespace to list.")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List across all control-plane namespaces.")
	cmd.MarkFlagsOneRequired("namespace", "all-namespaces")
	cmd.MarkFlagsMutuallyExclusive("namespace", "all-namespaces")
	addOutputFlag(cmd, &output)
	return cmd
}

// listInfraPlanes lists the CCLInfraPlanes in namespace, or across the control plane for
// metav1.NamespaceAll. cclclient.Client serves one namespace only, so listing across them
// uses the clientset directly.
func listInfraPlanes(
	ctx context.Context,
	cfg *rest.Config,
	namespace string,
) (*cclusternvidiacomv1alpha1.CCLInfraPlaneList, error) {
	if namespace != metav1.NamespaceAll {
		client, err := cclclient.NewForConfig(namespace, cfg)
		if err != nil {
			return nil, err
		}
		return client.ListInfraPlanes(ctx)
	}
	ccl, err := versioned.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return ccl.CclusterV1alpha1().CCLInfraPlanes(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
}

// listComponentSets is listInfraPlanes for CCLComponentSets.
func listComponentSets(
	ctx context.Context,
	cfg *rest.Config,
	namespace string,
) (*cclusternvidiacomv1alpha1.CCLComponentSetList, error) {
	if namespace != metav1.NamespaceAll {
		client, err := cclclient.NewForConfig(namespace, cfg)
		if err != nil {
			return nil, err
		}
		return client.ListComponentSets(ctx)
	}
	ccl, err := versioned.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return ccl.CclusterV1alpha1().CCLComponentSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
}

// noneFound is what list prints to stderr for an empty list in namespace, or across the
// control plane for metav1.NamespaceAll.
func noneFound(kinds, namespace string) string {
	if namespace == metav1.NamespaceAll {
		return "No " + kinds + " found"
	}
	return "No " + kinds + " found in namespace " + namespace
}
