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

// Package fluentbit describes the values of catalogs/cluster_base/fluent_bit.
package fluentbit

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "fluent-bit"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/fluent_bit"
)

// Values is the top-level values document for catalogs/cluster_base/fluent_bit.
type Values struct {
	FluentBit *ChartValues `json:"fluent-bit,omitempty"`
}

// ChartValues are the upstream fluent-bit values worth setting per cluster.
type ChartValues struct {
	ServiceAccount *ServiceAccount `json:"serviceAccount,omitempty"`
	Config         *Config         `json:"config,omitempty"`
}

// ServiceAccount configures the identity Fluent Bit runs as.
type ServiceAccount struct {
	// Create is shipped true for standalone testing. Set it false and
	// pre-create the account, with IRSA for instance, in production.
	Create *bool   `json:"create,omitempty"`
	Name   *string `json:"name,omitempty"`
}

// Config holds Fluent Bit's classic INI configuration sections, each a literal
// block passed through to the chart verbatim.
type Config struct {
	Service *string `json:"service,omitempty"`
	Inputs  *string `json:"inputs,omitempty"`
	Filters *string `json:"filters,omitempty"`

	// Outputs ships fanning logs back to Fluent Bit's own stdout. Replace it
	// with cloudwatch_logs, es, loki or kafka for production.
	Outputs *string `json:"outputs,omitempty"`
}

// New returns the fluent-bit chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
