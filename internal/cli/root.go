/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package cli is the ccluster command line, built on cobra and pkg/cclclient.
//
// NewRootCommand is the command cmd/ccluster runs; without a subcommand, it prints its
// usage. init saves the kubeconfig context of the one control plane the user works with,
// after checking it serves the ccluster API, to a small config file; the other commands
// read it from there. register registers an infra-plane cluster with that control plane:
// it creates a cluster-admin ServiceAccount token on the infra plane and hands it to
// cclclient.Client.ApplyInfraPlane on the control plane. Both clusters are reached
// through kubeconfig contexts, so no credentials are stored by ccluster itself.
// unregister reverses register on the control plane and leaves the infra plane alone.
package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the release version, stamped by the Makefile with
// -ldflags "-X github.com/NVIDIA/ccluster/internal/cli.version=...". A binary built
// without it, such as one from go install, reports its module version instead.
var version string

// Version returns the version the binary reports.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// NewRootCommand returns the ccluster command with its subcommands. Each call returns a
// new tree, so tests can run it repeatedly.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:     "ccluster",
		Short:   "Manage ccluster infra planes and component sets.",
		Version: Version(),
		// Without a subcommand, print the usage rather than fail.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		Args: cobra.NoArgs,
	}
	root.AddCommand(newInitCommand(), newRegisterCommand(), newUnregisterCommand())
	return root
}
