/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package teleportkubeagent describes the values of
// catalogs/cluster_base/teleport_kube_agent.
package teleportkubeagent

import (
	"errors"
	"fmt"
	"strings"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/cluster_base/teleport_kube_agent"

// ErrMissingValue reports a value the chart cannot be installed without.
var ErrMissingValue = errors.New("required value not set")

// Values is the top-level values document for
// catalogs/cluster_base/teleport_kube_agent.
type Values struct {
	TeleportKubeAgent *ChartValues `json:"teleport-kube-agent,omitempty"`
}

// ChartValues carries the four values the chart cannot be installed without, plus the
// optional overrides worth setting per cluster.
//
// The four required fields ship in catalogs/cluster_base/teleport_kube_agent/values.yaml
// as the literal placeholders "<PROXY_ADDR>", "<CLUSTER_NAME>", "<KUBE_CLUSTER>" and
// "<TOKEN>". A CCLComponentSet that omits them is accepted by the API server and installs
// an agent that cannot connect, so they are non-pointer and carry no omitempty: an unset
// one reaches Helm as the empty string and fails there rather than silently inheriting
// the placeholder.
type ChartValues struct {
	// ProxyAddr is the Teleport proxy's address, as host:port.
	ProxyAddr string `json:"proxyAddr"`

	// TeleportClusterName is the Teleport cluster this agent joins.
	TeleportClusterName string `json:"teleportClusterName"`

	// KubeClusterName is the name this Kubernetes cluster is registered under.
	KubeClusterName string `json:"kubeClusterName"`

	// JoinParams carries the join method and token.
	JoinParams JoinParams `json:"joinParams"`

	// Roles are the agent's roles. The chart ships "kube".
	Roles *string `json:"roles,omitempty"`

	// Enterprise selects the enterprise image. The chart ships true.
	Enterprise *bool `json:"enterprise,omitempty"`

	// HighAvailability overrides the chart's two-replica, anti-affinity default.
	HighAvailability *HighAvailability `json:"highAvailability,omitempty"`
}

// JoinParams is how the agent authenticates to the Teleport cluster.
type JoinParams struct {
	// Method is the join method. The chart ships "kubernetes".
	Method string `json:"method,omitempty"`

	// TokenName is the name of the join token.
	TokenName string `json:"tokenName"`
}

// HighAvailability configures the agent's replica count and scheduling.
type HighAvailability struct {
	// ReplicaCount is the number of agent replicas. The chart ships 2.
	ReplicaCount *int32 `json:"replicaCount,omitempty"`

	// RequireAntiAffinity forces replicas onto distinct nodes. The chart ships true.
	RequireAntiAffinity *bool `json:"requireAntiAffinity,omitempty"`
}

// New returns the teleport-kube-agent chart entry, installed into namespace with
// values. Unlike every other component in this catalog, some of these values are
// mandatory: New reports the ones left unset rather than building an entry that
// installs an agent which cannot connect. See Values.Validate.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Validate reports the required values that are unset. It implements catalogs.Validator,
// so catalogs.Entry - and therefore New - refuses to build an entry missing any of them.
func (v Values) Validate() error {
	values := v.TeleportKubeAgent
	if values == nil {
		return fmt.Errorf("%w: %s", ErrMissingValue, strings.Join([]string{
			"proxyAddr", "teleportClusterName", "kubeClusterName", "joinParams.tokenName",
		}, ", "))
	}

	var missing []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{"proxyAddr", values.ProxyAddr},
		{"teleportClusterName", values.TeleportClusterName},
		{"kubeClusterName", values.KubeClusterName},
		{"joinParams.tokenName", values.JoinParams.TokenName},
	} {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingValue, strings.Join(missing, ", "))
	}
	return nil
}
