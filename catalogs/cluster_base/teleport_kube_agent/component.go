/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package teleportkubeagent describes the values of
// catalogs/cluster_base/teleport_kube_agent.
package teleportkubeagent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLDeployment, and the key its values nest
	// under.
	Name = "teleport-kube-agent"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/teleport_kube_agent"
	// Namespace is where the agent is installed.
	Namespace = "teleport"
)

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
// "<TOKEN>". A CCLDeployment that omits them is accepted by the API server and installs
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

// Config carries the values the chart requires.
type Config struct {
	// ProxyAddr is the Teleport proxy's address, as host:port.
	ProxyAddr string
	// TeleportClusterName is the Teleport cluster this agent joins.
	TeleportClusterName string
	// KubeClusterName is the name this Kubernetes cluster is registered under.
	KubeClusterName string
	// TokenName is the name of the join token.
	TokenName string
}

// Component is the catalog entry for teleport-kube-agent.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. Unlike every other component in this
	// catalog, some of these are mandatory — see ChartValues and Validate.
	Values Values
}

// New returns the teleport-kube-agent component configured with the values the chart
// requires. Since nothing validates a CCLDeployment's values at admission, taking them
// here is the only way to make them hard to forget; Validate is the check for callers
// that build a Component as a literal instead.
func New(config Config) *Component {
	return &Component{
		ComponentEntry: catalogs.ComponentEntry{
			Name:      Name,
			Chart:     Chart,
			Namespace: Namespace,
		},
		Values: Values{TeleportKubeAgent: &ChartValues{
			ProxyAddr:           config.ProxyAddr,
			TeleportClusterName: config.TeleportClusterName,
			KubeClusterName:     config.KubeClusterName,
			JoinParams:          JoinParams{TokenName: config.TokenName},
		}},
	}
}

// Entry returns the component's chart metadata.
func (c *Component) Entry() catalogs.ComponentEntry { return c.ComponentEntry }

// ValuesObject returns the chart's typed values.
func (c *Component) ValuesObject() any { return c.Values }

// Validate reports the required values that are unset. It implements catalogs.Validator,
// so catalogs.ToComponentSpec refuses to convert a component that is missing any of them.
func (c *Component) Validate() error {
	values := c.Values.TeleportKubeAgent
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
