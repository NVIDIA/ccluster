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

// Package spire describes the values of catalogs/cluster_base/spire.
package spire

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLDeployment, and the key its values
	// nest under.
	Name = "spire"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/spire"
)

// Values is the top-level values document for catalogs/cluster_base/spire.
type Values struct {
	Spire *ChartValues `json:"spire,omitempty"`
}

// ChartValues are the upstream spire values worth setting per cluster.
type ChartValues struct {
	Global      *Global      `json:"global,omitempty"`
	SpireServer *SpireServer `json:"spire-server,omitempty"`

	// OIDCDiscoveryProvider is only useful when SPIRE JWTs are wired into AWS
	// IAM through the AWS OIDC provider sync. Standalone, it fails to mount
	// looking for an externally provided TLS secret.
	OIDCDiscoveryProvider *OIDCDiscoveryProvider `json:"spiffe-oidc-discovery-provider,omitempty"`

	CSIDriver *CSIDriver `json:"spiffe-csi-driver,omitempty"`
}

// Global carries the umbrella chart's global values.
type Global struct {
	// InstallAndUpgradeHooks must stay disabled under Argo CD, which renders
	// with helm template and does not run Helm hooks the way helm install
	// does. With them enabled the chart's pre-upgrade hook tries to patch a
	// validating webhook that does not exist yet and blocks the sync forever.
	InstallAndUpgradeHooks *Toggle `json:"installAndUpgradeHooks,omitempty"`

	// DeleteHooks is disabled for the same reason.
	DeleteHooks *Toggle `json:"deleteHooks,omitempty"`
}

// SpireServer configures the spire-server subchart.
type SpireServer struct {
	Persistence *Persistence `json:"persistence,omitempty"`
}

// Persistence is where the spire-server CA and registration database live.
type Persistence struct {
	// Type is the volume type. The chart ships emptyDir: the StatefulSet's
	// volumeClaimTemplate exposes no retention policy, so a PVC would survive
	// teardown and poison the next registration with a stale trust-domain CA.
	Type *string `json:"type,omitempty"`
}

// OIDCDiscoveryProvider configures the SPIFFE OIDC discovery provider.
type OIDCDiscoveryProvider struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// CSIDriver configures the SPIFFE CSI driver.
type CSIDriver struct {
	Enabled *bool `json:"enabled,omitempty"`

	// KubeletPath is kubelet's data root, where plugins_registry/ and pods/
	// live. It is distro-dependent; k3s/k3d uses the upstream default the
	// chart ships even though its config file lives elsewhere.
	KubeletPath *string `json:"kubeletPath,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the spire chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}
