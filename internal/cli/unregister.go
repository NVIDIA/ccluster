/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

func newUnregisterCommand() *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "unregister NAME --namespace NAMESPACE",
		Short: "Unregister a cluster from the control plane.",
		Long: `Unregister a cluster from the control plane.

Deletes the CCLInfraPlane NAME and then its Secret NAME-credentials from the control plane
saved by ccluster init; the operator's finalizer removes the copy it made for Argo CD. The
namespace is left in place, and objects already gone are not an error.

The infra plane is not touched: its ccluster-manager ServiceAccount and token stay, so
other registrations of the same cluster keep working and registering it again reuses the
token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			name := args[0]

			controlPlane, err := loadControlPlane()
			if err != nil {
				return err
			}
			cfg, err := restConfig(controlPlane)
			if err != nil {
				return err
			}
			client, err := cclclient.NewForConfig(namespace, cfg)
			if err != nil {
				return err
			}
			if err := client.DeleteInfraPlane(cmd.Context(), name); err != nil {
				return apiError(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Unregistered CCLInfraPlane %s/%s\n", namespace, name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Control-plane namespace of the CCLInfraPlane.")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}
