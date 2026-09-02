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

// Package ingressnginx describes the values of catalogs/cluster_base/ingress_nginx.
package ingressnginx

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "ingress-nginx"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/ingress_nginx"
)

// Values is the top-level values document for catalogs/cluster_base/ingress_nginx.
type Values struct {
	IngressNginx *ChartValues `json:"ingress-nginx,omitempty"`
}

// ChartValues are the upstream ingress-nginx values worth setting per cluster.
type ChartValues struct {
	Controller *Controller `json:"controller,omitempty"`
}

// Controller configures the ingress controller.
type Controller struct {
	// Service is shipped disabled; the gateway in front of the cluster is
	// expected to be provisioned out of band.
	Service *Toggle `json:"service,omitempty"`

	// Metrics requires the kube-prometheus-stack CRDs. Disable it on a
	// cluster that does not deploy them.
	Metrics *Metrics `json:"metrics,omitempty"`

	Autoscaling *Autoscaling `json:"autoscaling,omitempty"`
}

// Metrics configures the controller's metrics endpoint and ServiceMonitor.
type Metrics struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	ServiceMonitor *Toggle `json:"serviceMonitor,omitempty"`
}

// Autoscaling configures the controller's HorizontalPodAutoscaler.
type Autoscaling struct {
	Enabled *bool `json:"enabled,omitempty"`
	// MinReplicas is the floor. The chart ships 4.
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	// MaxReplicas is the ceiling. The chart ships 180.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the ingress-nginx chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
