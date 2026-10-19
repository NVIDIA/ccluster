/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package istio describes the values of catalogs/cluster_base/istio.
package istio

import (
	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

const (
	// Name is the component's name in a CCLComponentSet.
	Name = "istio"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/istio"
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

// New returns the istio chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// IgnoreDifferences returns the fields of this chart's objects that istiod owns once it
// runs. The cp operator registers it with the IgnoreDifferencesFunc signature.
//
// The istiod and istio-base subcharts render their validating webhooks with
// failurePolicy Ignore, so that the API server fails open until istiod serves, and
// istiod's webhook controller then patches it to Fail. Diffed, that is permanent drift
// which self-heal reverts and istiod restores, and the Application never reports
// Synced. The rules name the two webhooks rather than the kind, so drift on any other
// webhook still shows. istiod's name depends on its revision and the global Istio
// namespace, which Values does not set; istio-base's is fixed.
func IgnoreDifferences() []argocdv1alpha1.ResourceIgnoreDifferences {
	return []argocdv1alpha1.ResourceIgnoreDifferences{
		webhookFailurePolicy("istio-validator-istio-system"), // istiod
		webhookFailurePolicy("istiod-default-validator"),     // istio-base
	}
}

func webhookFailurePolicy(name string) argocdv1alpha1.ResourceIgnoreDifferences {
	return argocdv1alpha1.ResourceIgnoreDifferences{
		Group:             "admissionregistration.k8s.io",
		Kind:              "ValidatingWebhookConfiguration",
		Name:              name,
		JQPathExpressions: []string{".webhooks[]?.failurePolicy"},
	}
}
