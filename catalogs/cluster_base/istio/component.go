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

// Package istio describes the values of catalogs/cluster_base/istio.
package istio

import "github.com/NVIDIA/ccluster/catalogs"

const (
	// Name is the component's name in a CCLDeployment.
	Name = "istio"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/istio"
	// Namespace is where Istio is installed.
	Namespace = "istio-system"
)

// Well-known cniBinDir values. The chart's own default targets k3s/k3d; anything else
// has to be set explicitly, and getting it wrong breaks sidecar networking in ways that
// only show up when a workload is scheduled.
const (
	// CNIBinDirK3s is where k3s embeds its CNI plugins. containerd looks for them
	// there, so istio-cni must install its binary alongside them. This is the chart's
	// default.
	CNIBinDirK3s = "/var/lib/rancher/k3s/data/cni"

	// CNIBinDirGKE is the writable CNI binary directory on GKE's COS images.
	// /opt/cni/bin is mounted read-only there, and using it fails during the binary
	// copy with "read-only file system".
	CNIBinDirGKE = "/home/kubernetes/bin"

	// CNIBinDirDefault is the standard location, correct for EKS, kind and kubeadm.
	CNIBinDirDefault = "/opt/cni/bin"
)

// CNIConfDirDefault is the standard CNI configuration directory, correct for every
// distro except k3s, whose default the chart already carries.
const CNIConfDirDefault = "/etc/cni/net.d"

// Values is the top-level values document for catalogs/cluster_base/istio.
//
// Unlike the other components in this catalog, this chart wraps four upstream charts
// rather than one, so its values have five sibling top-level keys rather than a single
// chart-named one.
type Values struct {
	IstioBase    *IstioBase    `json:"istio-base,omitempty"`
	Istiod       *Istiod       `json:"istiod,omitempty"`
	IstioGateway *IstioGateway `json:"istio-gateway,omitempty"`
	IstioCNI     *IstioCNI     `json:"istio-cni,omitempty"`

	// CiliumIstioPatch enables the in-chart DaemonSet patch applied to
	// istio-cni-node when Cilium is the cluster CNI. Rendered by
	// templates/cilium-patch.yaml. The chart ships it off.
	CiliumIstioPatch *CiliumIstioPatch `json:"ciliumIstioPatch,omitempty"`
}

// IstioBase configures the istio-base subchart.
type IstioBase struct {
	// DefaultRevision is the revision tag installed as the default. The chart ships
	// "default".
	DefaultRevision *string `json:"defaultRevision,omitempty"`
}

// Istiod configures the istiod subchart.
type Istiod struct {
	Pilot *Pilot `json:"pilot,omitempty"`
}

// Pilot configures istiod's pilot.
type Pilot struct {
	// AutoscaleMin is the minimum pilot replica count. The chart ships 2.
	AutoscaleMin *int32 `json:"autoscaleMin,omitempty"`
}

// IstioGateway configures the istio-gateway subchart.
type IstioGateway struct {
	// Enabled turns the ingress gateway on. The chart ships it off, to avoid the
	// istiod-webhook race on first install; enable it in a second pass once istiod
	// is Ready.
	Enabled *bool `json:"enabled,omitempty"`
}

// IstioCNI configures the istio-cni subchart.
type IstioCNI struct {
	CNI *CNI `json:"cni,omitempty"`
}

// CNI is where istio-cni writes its plugin binary and configuration. These are the only
// values in this chart that must be tuned per cluster: the chart's defaults target
// k3s/k3d, and the correct paths depend on the node image. See the CNIBinDir constants.
type CNI struct {
	// CNIConfDir is the CNI configuration directory. Use CNIConfDirDefault on
	// anything but k3s.
	CNIConfDir *string `json:"cniConfDir,omitempty"`

	// CNIBinDir is the CNI binary directory. Use CNIBinDirGKE on GKE COS and
	// CNIBinDirDefault on EKS, kind and kubeadm.
	CNIBinDir *string `json:"cniBinDir,omitempty"`
}

// CiliumIstioPatch configures the Cilium DaemonSet patch.
type CiliumIstioPatch struct {
	// Enabled applies the patch. The chart ships it off.
	Enabled *bool `json:"enabled,omitempty"`
}

// Component is the catalog entry for istio.
type Component struct {
	catalogs.ComponentEntry

	// Values overrides the chart's defaults. The zero value means "chart defaults",
	// which target k3s/k3d — see CNI.
	Values Values
}

// New returns the istio component with its chart metadata filled in and no value
// overrides.
func New() *Component {
	return &Component{ComponentEntry: catalogs.ComponentEntry{
		Name:      Name,
		Chart:     Chart,
		Namespace: Namespace,
	}}
}

// ValuesObject returns the chart's typed values.
func (c *Component) ValuesObject() any { return c.Values }
