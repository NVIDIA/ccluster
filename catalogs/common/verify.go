/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package common holds helpers shared by catalog chart packages, such as checks a
// chart's Verify builds on. It imports no chart package, so every chart package can
// import it.
package common

import (
	"context"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CRDsEstablished checks that every named CustomResourceDefinition exists and is
// Established, meaning the API server serves its kind, and is not being deleted: a CRD
// keeps Established=True while its deletion waits on remaining custom resources, yet
// the API server already refuses new ones. It returns nil when every CRD passes, and
// otherwise one error naming each CRD that is missing, being deleted or not yet
// established, so the condition message lists them all rather than the first. A read
// that fails for any other reason is returned as it is.
//
// r must be able to read apiextensions.k8s.io/v1 CustomResourceDefinitions: the
// operator's scheme registers the kind and its ClusterRole grants get, list and watch.
func CRDsEstablished(ctx context.Context, r client.Reader, names ...string) error {
	var problems []string
	for _, name := range names {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			if apierrors.IsNotFound(err) {
				problems = append(problems, fmt.Sprintf("CRD %s not found", name))
				continue
			}
			return fmt.Errorf("reading CRD %s: %w", name, err)
		}
		switch {
		case !crd.DeletionTimestamp.IsZero():
			problems = append(problems, fmt.Sprintf("CRD %s being deleted", name))
		case !established(&crd):
			problems = append(problems, fmt.Sprintf("CRD %s not established", name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, ", "))
	}
	return nil
}

// established reports whether crd carries Established=True.
func established(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established {
			return c.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}

// APIServiceAvailable checks that the named APIService, such as v1beta1.metrics.k8s.io,
// exists and is Available, meaning the aggregator reached the service behind it and the
// group-version is served. It looks only at the APIService, not at what serves it, so it
// holds whoever installed that: a cluster_base chart, or the cluster's provider. It
// returns nil when the APIService passes, and otherwise an error saying which of the
// two it is not, with the aggregator's reason. A read that fails for any other reason is
// returned as it is.
//
// r must be able to read apiregistration.k8s.io/v1 APIServices: the operator's scheme
// registers the kind and its ClusterRole grants get, list and watch.
func APIServiceAvailable(ctx context.Context, r client.Reader, name string) error {
	var service apiregistrationv1.APIService
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &service); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("APIService %s not found", name)
		}
		return fmt.Errorf("reading APIService %s: %w", name, err)
	}
	for _, c := range service.Status.Conditions {
		if c.Type != apiregistrationv1.Available {
			continue
		}
		if c.Status == apiregistrationv1.ConditionTrue {
			return nil
		}
		return fmt.Errorf("APIService %s not available: %s", name, c.Reason)
	}
	return fmt.Errorf("APIService %s not available", name)
}
