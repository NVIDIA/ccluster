/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package externalsecrets describes the values of catalogs/cluster_base/external_secrets.
package externalsecrets

import (
	"context"
	"errors"
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
)

const (
	// Name is the component's name in a CCLComponentSet, and the key its values
	// nest under.
	Name = "external-secrets"
	// Chart is the chart directory, relative to the repository root.
	Chart = "catalogs/cluster_base/external_secrets"
)

// Values is the top-level values document for catalogs/cluster_base/external_secrets.
type Values struct {
	ExternalSecrets *ChartValues `json:"external-secrets,omitempty"`
}

// ChartValues are the upstream external-secrets values worth setting per cluster.
type ChartValues struct {
	// ReplicaCount is the controller replica count. The chart ships 2, with leader
	// election on.
	ReplicaCount *int32 `json:"replicaCount,omitempty"`

	// Webhook validates every ExternalSecret and SecretStore write. The chart ships 2
	// replicas.
	Webhook *Replicas `json:"webhook,omitempty"`

	// ServiceMonitor is shipped off, because the ServiceMonitor CRD only exists once
	// kube-prometheus-stack is deployed. Enable it after that.
	ServiceMonitor *Toggle `json:"serviceMonitor,omitempty"`
}

// Replicas is a subchart's replica count.
type Replicas struct {
	ReplicaCount *int32 `json:"replicaCount,omitempty"`
}

// Toggle is a subchart's enable flag.
type Toggle struct {
	// Enabled turns the feature on.
	Enabled *bool `json:"enabled,omitempty"`
}

// New returns the external-secrets chart entry, installed into namespace with values.
// The zero Values means "chart defaults".
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// WebhookConfiguration is the ValidatingWebhookConfiguration through which the API server
// sends ExternalSecret writes to ESO's webhook. The upstream chart gives it this name
// whatever the release is called.
const WebhookConfiguration = "externalsecret-validate"

// webhook is the entry of WebhookConfiguration that validates ExternalSecrets.
const webhook = "validate.externalsecret.external-secrets.io"

// externalSecrets is the API the catalog's ExternalSecrets are written in, which ESO
// serves from v0.16.0.
var externalSecrets = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecretList"}

// Verify checks that ESO, when installed, serves what the catalog's ExternalSecrets need:
// the external-secrets.io/v1 API, and a webhook with a ready endpoint, because the API
// server rejects an ExternalSecret write the webhook does not answer.
//
// ESO is optional, so a cluster without WebhookConfiguration passes. That configuration,
// not the API, marks ESO as installed: the cache keeps answering for a kind after its
// CRD is deleted, but sees a deleted configuration. An ESO run without its webhook
// therefore passes unchecked.
//
// Like metrics_server's check, it checks what ESO provides rather than this chart's
// objects, so an ESO installed by other means passes too. Nothing in the chart depends
// on the cluster's domain, so the CCLClusterConfig spec is not consulted.
//
// r must be able to read ValidatingWebhookConfigurations, the metadata of
// ExternalSecrets, Services and EndpointSlices; the ip operator's ClusterRole grants all
// four.
func Verify(ctx context.Context, r client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	var cfg admissionregistrationv1.ValidatingWebhookConfiguration
	if err := r.Get(ctx, client.ObjectKey{Name: WebhookConfiguration}, &cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading ValidatingWebhookConfiguration %s: %w", WebhookConfiguration, err)
	}

	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(externalSecrets)
	if err := r.List(ctx, list, client.Limit(1)); err != nil {
		if meta.IsNoMatchError(err) {
			return errors.New("external-secrets.io/v1 ExternalSecret not served; ESO before v0.16.0 does not serve v1")
		}
		return fmt.Errorf("listing external-secrets.io/v1 ExternalSecrets: %w", err)
	}

	for _, w := range cfg.Webhooks {
		if w.Name == webhook && w.ClientConfig.Service != nil {
			return catalogs.VerifyServiceReady(ctx, r, w.ClientConfig.Service.Namespace, w.ClientConfig.Service.Name)
		}
	}
	return fmt.Errorf("ValidatingWebhookConfiguration %s has no webhook %s calling a Service",
		WebhookConfiguration, webhook)
}
