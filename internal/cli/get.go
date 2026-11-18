/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	var namespace, output string
	cmd := &cobra.Command{
		Use:   "get (infraplane|componentset) NAME --namespace NAMESPACE",
		Short: "Show an infra plane or component set.",
		Long: `Show an infra plane or component set.

Reads the CCLInfraPlane or CCLComponentSet NAME in NAMESPACE on the control plane saved by
ccluster init and prints what it reports. infraplane can be written infraplanes or ip,
componentset componentsets or cs.

For an infra plane: its domain and credentials Secret, its conditions, and the status of
the infra-plane cluster's CCLClusterConfig with its load balancers. For a component set:
its conditions, and each component in rollout order with Argo CD's sync and message
for it and its health as its chart judges it; a component not yet released shows -. A condition computed from an
earlier generation of the object is marked stale.

With --output yaml or json, prints the object instead.`,
		Example: `  ccluster get infraplane edge-1 -n tenant-a
  ccluster get cs base -n tenant-a -o yaml`,
		Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return completeResources(func(r resource) string { return r.singular }), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return completeNames(cmd, args[0]), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := parseResource(args[0])
			if err != nil {
				return err
			}
			if err := checkNamespace(namespace); err != nil {
				return err
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			cmd.SilenceUsage = true
			name := args[1]

			client, err := controlPlaneClient(namespace)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if r.singular == infraPlanes.singular {
				plane, err := client.GetInfraPlane(ctx, name)
				if err != nil {
					return apiError(err)
				}
				if output != outputHuman {
					return printInfraPlane(out, plane, output)
				}
				return describeInfraPlane(out, plane)
			}

			set, err := client.GetComponentSet(ctx, name)
			if err != nil {
				return apiError(err)
			}
			if output != outputHuman {
				return printComponentSet(out, set, output)
			}
			return describeComponentSet(out, set)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Control-plane namespace of the object.")
	_ = cmd.MarkFlagRequired("namespace")
	addOutputFlag(cmd, &output)
	return cmd
}

// completeNames offers the names of the objects of resource type arg in the namespace
// given with --namespace. Without one, or on any error, it offers nothing.
func completeNames(cmd *cobra.Command, arg string) []string {
	r, err := parseResource(arg)
	if err != nil {
		return nil
	}
	namespace, _ := cmd.Flags().GetString("namespace")
	if namespace == "" {
		return nil
	}
	client, err := controlPlaneClient(namespace)
	if err != nil {
		return nil
	}
	var names []string
	if r.singular == infraPlanes.singular {
		list, err := client.ListInfraPlanes(cmd.Context())
		if err != nil {
			return nil
		}
		for _, item := range list.Items {
			names = append(names, item.Name)
		}
		return names
	}
	list, err := client.ListComponentSets(cmd.Context())
	if err != nil {
		return nil
	}
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	return names
}
