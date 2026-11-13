/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"

	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

// resource is a resource type list and get take as their first argument.
type resource struct {
	// singular and plural are the names help and completion show for get and for list.
	singular, plural string
	// aliases are every spelling accepted with either command.
	aliases []string
}

var (
	infraPlanes = resource{
		singular: "infraplane", plural: "infraplanes",
		aliases: []string{"infraplane", "infraplanes", "ip"},
	}
	componentSets = resource{
		singular: "componentset", plural: "componentsets",
		aliases: []string{"componentset", "componentsets", "cs"},
	}
	resources = []resource{infraPlanes, componentSets}
)

// parseResource returns the resource a type argument names, in any of its spellings.
func parseResource(arg string) (resource, error) {
	for _, r := range resources {
		if slices.Contains(r.aliases, arg) {
			return r, nil
		}
	}
	return resource{}, fmt.Errorf("unknown resource type %q; use infraplane (ip) or componentset (cs)", arg)
}

// The values of -o/--output. The empty string is the human-readable default.
const (
	outputHuman = ""
	outputYAML  = "yaml"
	outputJSON  = "json"
)

func addOutputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", outputHuman,
		"Output format: yaml or json. Unset, prints a human-readable table or summary.")
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(
		[]string{outputYAML, outputJSON}, cobra.ShellCompDirectiveNoFileComp))
}

func checkOutput(output string) error {
	switch output {
	case outputHuman, outputYAML, outputJSON:
		return nil
	}
	return fmt.Errorf("unknown output format %q; use yaml or json", output)
}

// checkNamespace refuses an empty --namespace, such as one from an unset shell variable,
// which the Kubernetes clients would read as every namespace.
func checkNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("--namespace must not be empty")
	}
	return nil
}

// controlPlaneRESTConfig returns the client config of the control plane saved by init.
func controlPlaneRESTConfig() (*rest.Config, error) {
	controlPlane, err := loadControlPlane()
	if err != nil {
		return nil, err
	}
	return restConfig(controlPlane)
}

// controlPlaneClient returns a client for namespace on the control plane saved by init.
func controlPlaneClient(namespace string) (*cclclient.Client, error) {
	cfg, err := controlPlaneRESTConfig()
	if err != nil {
		return nil, err
	}
	return cclclient.NewForConfig(namespace, cfg)
}

// completeResources offers the resource types, each under name.
func completeResources(name func(resource) string) []string {
	names := make([]string, 0, len(resources))
	for _, r := range resources {
		names = append(names, name(r))
	}
	return names
}
