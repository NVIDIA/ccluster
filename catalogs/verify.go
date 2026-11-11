/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package catalogs

import (
	"context"
	"errors"
	"fmt"
	"sort"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyCRDExists is a building block for a chart's Verify function: it returns nil when
// the CustomResourceDefinition name, such as "rollouts.argoproj.io", exists, and
// otherwise an error saying it is missing. Use it for a component whose consumers in the
// catalog render one of its custom resources. Existence is all it checks: not that the
// CRD is Established, nor that a controller reconciles its resources.
//
// It reads the CRD's metadata only, so the ip operator's cache holds the metadata of
// every CRD in the cluster rather than their schemas, which run to hundreds of kilobytes
// each. r must be able to read apiextensions.k8s.io CustomResourceDefinitions; the ip
// operator's ClusterRole grants that. A read that fails is returned wrapped, not
// reported as missing.
func VerifyCRDExists(ctx context.Context, r client.Reader, name string) error {
	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	if err := r.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("CustomResourceDefinition %s not found", name)
		}
		return fmt.Errorf("reading CustomResourceDefinition %s: %w", name, err)
	}
	return nil
}

// VerifyServiceReady is a building block for a chart's Verify function: it returns nil
// when the Service namespace/name exists and has at least one ready endpoint, and
// otherwise an error saying which of the two is missing. Use it for a component that
// other catalog charts reach at a fixed address.
//
// r must be able to read Services and discovery.k8s.io EndpointSlices; the ip operator's
// ClusterRole grants both. A read that fails is returned wrapped, not reported as missing.
func VerifyServiceReady(ctx context.Context, r client.Reader, namespace, name string) error {
	var svc corev1.Service
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no Service %s/%s found", namespace, name)
		}
		return fmt.Errorf("reading Service %s/%s: %w", namespace, name, err)
	}

	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: name}); err != nil {
		return fmt.Errorf("listing EndpointSlices of Service %s/%s: %w", namespace, name, err)
	}
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			// A nil Ready means unknown, which the API defines as ready.
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				return nil
			}
		}
	}
	return fmt.Errorf("no ready endpoints for Service %s/%s", namespace, name)
}

// VerifyValidatingWebhookServed is a building block for a chart's Verify function: it
// returns nil when an admission webhook named webhook, such as "webhook.cert-manager.io",
// is registered in a ValidatingWebhookConfiguration, has a CA bundle, and is served. It
// finds the webhook by its entry name, which upstream charts fix, rather than by the
// configuration's name, which follows the release name; the Service it checks is the
// one the entry points at, wherever it is. Use it for a component the API server calls
// on writes the catalog makes, which fail while the webhook is down.
//
// Served means the entry's Service has a ready endpoint, by VerifyServiceReady. An entry
// that reaches its webhook by URL rather than through a Service has no endpoints to
// read and passes on its CA bundle alone. With more than one entry of that name, one
// that passes is enough; otherwise the error names the first, in configuration name
// order.
//
// r must be able to read admissionregistration.k8s.io ValidatingWebhookConfigurations,
// Services and discovery.k8s.io EndpointSlices; the ip operator's ClusterRole grants all
// three. A read that fails is returned wrapped, not reported as a missing webhook.
func VerifyValidatingWebhookServed(ctx context.Context, r client.Reader, webhook string) error {
	var configs admissionregistrationv1.ValidatingWebhookConfigurationList
	if err := r.List(ctx, &configs); err != nil {
		return fmt.Errorf("listing ValidatingWebhookConfigurations: %w", err)
	}
	sort.Slice(configs.Items, func(i, j int) bool {
		return configs.Items[i].Name < configs.Items[j].Name
	})

	var errs []error
	for _, config := range configs.Items {
		for _, w := range config.Webhooks {
			if w.Name != webhook {
				continue
			}
			err := verifyWebhookClient(ctx, r, w.ClientConfig)
			if err == nil {
				return nil
			}
			errs = append(errs, fmt.Errorf("webhook %s in ValidatingWebhookConfiguration %s: %w",
				webhook, config.Name, err))
		}
	}
	if len(errs) == 0 {
		return fmt.Errorf("no ValidatingWebhookConfiguration has webhook %s", webhook)
	}
	return errs[0]
}

// verifyWebhookClient checks that the API server can call a webhook: it trusts the
// webhook's certificate through the CA bundle, and the Service behind it, if any, has a
// ready endpoint.
func verifyWebhookClient(ctx context.Context, r client.Reader, cc admissionregistrationv1.WebhookClientConfig) error {
	if len(cc.CABundle) == 0 {
		return errors.New("no CA bundle injected")
	}
	if cc.Service == nil {
		return nil
	}
	return VerifyServiceReady(ctx, r, cc.Service.Namespace, cc.Service.Name)
}
