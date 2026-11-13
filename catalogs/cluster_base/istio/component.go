/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package istio describes the values of catalogs/cluster_base/istio.
package istio

import (
	corev1 "k8s.io/api/core/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "istio"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/istio"
)

// Well-known cniBinDir values. The chart's default is CNIBinDirDefault; other node
// images need theirs set explicitly. A wrong path keeps istio-cni-node unready or,
// for a directory the container runtime never scans, breaks pod networking only once
// a workload is scheduled.
const (
	// CNIBinDirK3s is where k3s embeds its CNI plugins. containerd looks for them
	// there, so istio-cni must install its binary alongside them.
	CNIBinDirK3s = "/var/lib/rancher/k3s/data/cni"

	// CNIBinDirGKE is the writable CNI binary directory on GKE's COS images.
	// /opt/cni/bin is mounted read-only there, and using it fails during the binary
	// copy with "read-only file system".
	CNIBinDirGKE = "/home/kubernetes/bin"

	// CNIBinDirDefault is the standard location, correct for EKS, kind and kubeadm.
	// This is the chart's default.
	CNIBinDirDefault = "/opt/cni/bin"
)

// Well-known cniConfDir values.
const (
	// CNIConfDirK3s is where k3s keeps its CNI configuration.
	CNIConfDirK3s = "/var/lib/rancher/k3s/agent/etc/cni/net.d"

	// CNIConfDirDefault is the standard CNI configuration directory, correct for every
	// distro except k3s. This is the chart's default.
	CNIConfDirDefault = "/etc/cni/net.d"
)

// Values is the top-level values document for catalogs/cluster_base/istio.
//
// Unlike the other components in this catalog, this chart wraps four upstream charts
// rather than one, so its values have four sibling top-level keys rather than a single
// chart-named one.
type Values struct {
	IstioBase    *IstioBase    `json:"istio-base,omitempty"`
	Istiod       *Istiod       `json:"istiod,omitempty"`
	IstioGateway *IstioGateway `json:"istio-gateway,omitempty"`
	IstioCNI     *IstioCNI     `json:"istio-cni,omitempty"`
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

	// Tolerations replaces the istio-cni-node DaemonSet's tolerations. The chart keeps
	// the upstream list, whose wildcards tolerate every NoSchedule and NoExecute taint.
	//
	// On Cilium, list only the taints istio-cni-node must run on, such as
	// dedicated_compute=true:NoSchedule. The wildcards also tolerate
	// node.cilium.io/agent-not-ready, so istio-cni can write its CNI configuration
	// before cilium-agent is Ready and break pod networking on that node until the
	// istio-cni pod is recreated. The DaemonSet controller still adds the node-condition
	// tolerations, such as not-ready and unreachable.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// CNI is where istio-cni writes its plugin binary and configuration. These are the only
// values in this chart that must be tuned per cluster: the chart's defaults are the
// standard paths, and the correct paths depend on the node image.
type CNI struct {
	// CNIConfDir is the CNI configuration directory. The chart ships /etc/cni/net.d
	// (CNIConfDirDefault); k3s needs /var/lib/rancher/k3s/agent/etc/cni/net.d
	// (CNIConfDirK3s).
	CNIConfDir *string `json:"cniConfDir,omitempty"`

	// CNIBinDir is the CNI binary directory. The chart ships /opt/cni/bin
	// (CNIBinDirDefault); GKE COS needs /home/kubernetes/bin (CNIBinDirGKE) and k3s
	// /var/lib/rancher/k3s/data/cni (CNIBinDirK3s).
	CNIBinDir *string `json:"cniBinDir,omitempty"`
}

// New returns the istio chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Drift returns the fields of this chart's objects that istiod owns once it runs. The cp
// operator registers it in its drift registry.
//
// The istiod and istio-base subcharts render their validating webhooks with
// failurePolicy Ignore, so that the API server fails open until istiod serves, and
// istiod's webhook controller then patches it to Fail. Diffed, that is permanent drift
// which self-heal reverts and istiod restores, and the Application never reports
// Synced. The rule selects the two webhooks by their entry names, which are fixed:
// istiod's configuration is named after its revision and the global Istio namespace,
// either of which a CCLComponentSet can set. Drift on any other webhook still shows.
func Drift() []catalogs.DriftSpec {
	return []catalogs.DriftSpec{{
		Group: "admissionregistration.k8s.io",
		Kind:  "ValidatingWebhookConfiguration",
		JQPaths: []string{
			// rev.validation.istio.io is istiod's; validation.istio.io is istio-base's.
			`.webhooks[]? | select(.name == "rev.validation.istio.io" or .name == "validation.istio.io") | .failurePolicy`,
		},
	}}
}
