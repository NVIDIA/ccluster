/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package remote builds clients for infra-plane clusters from the credentials Secrets
// that register them with Argo CD.
//
// The Secret is read the way Argo CD reads it, so a cluster the control-plane operator can
// reach is exactly a cluster Argo CD can deploy to. See
// https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
)

// RequestTimeout bounds every request to an infra-plane cluster, so an unreachable
// cluster fails a reconcile instead of holding a worker until the TCP timeout.
const RequestTimeout = 30 * time.Second

// ErrUnsupportedCredentials reports a Secret that authenticates with an exec plugin or
// AWS IAM. Argo CD runs those inside its own Pods; the control-plane operator has neither
// the binaries nor the cloud identity to do the same.
var ErrUnsupportedCredentials = errors.New("unsupported cluster credentials")

// ErrInvalidCredentials reports a Secret whose "config" is not valid JSON.
var ErrInvalidCredentials = errors.New("invalid cluster credentials")

// clusterConfig is the part of Argo CD's "config" key the operator understands. []byte
// fields are base64 in JSON, as Argo CD writes them.
type clusterConfig struct {
	Username        string          `json:"username,omitempty"`
	Password        string          `json:"password,omitempty"`
	BearerToken     string          `json:"bearerToken,omitempty"`
	TLSClientConfig tlsClientConfig `json:"tlsClientConfig,omitzero"`

	// Present only to be refused.
	ExecProviderConfig json.RawMessage `json:"execProviderConfig,omitempty"`
	AWSAuthConfig      json.RawMessage `json:"awsAuthConfig,omitempty"`
}

type tlsClientConfig struct {
	Insecure   bool   `json:"insecure,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	CAData     []byte `json:"caData,omitempty"`
	CertData   []byte `json:"certData,omitempty"`
	KeyData    []byte `json:"keyData,omitempty"`
}

// hasCredentials reports whether the config names any way to authenticate.
func (c clusterConfig) hasCredentials() bool {
	return c.BearerToken != "" || c.Username != "" ||
		len(c.TLSClientConfig.CertData) > 0 || len(c.ExecProviderConfig) > 0 || len(c.AWSAuthConfig) > 0
}

// RESTConfig returns the rest.Config for the cluster secret describes. A Secret for the
// operator's own cluster - no "server", or argocdv1alpha1.InClusterServer with no
// credentials - returns a copy of local. The result carries RequestTimeout.
func RESTConfig(secret *corev1.Secret, local *rest.Config) (*rest.Config, error) {
	var cfg clusterConfig
	if raw := secret.Data[argocdv1alpha1.ClusterSecretConfigKey]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			// The JSON error is not wrapped: it can quote the Secret's content.
			return nil, fmt.Errorf("%w: %q of Secret %s/%s is not valid JSON",
				ErrInvalidCredentials, argocdv1alpha1.ClusterSecretConfigKey,
				secret.Namespace, secret.Name)
		}
	}
	if len(cfg.ExecProviderConfig) > 0 {
		return nil, fmt.Errorf("%w: execProviderConfig", ErrUnsupportedCredentials)
	}
	if len(cfg.AWSAuthConfig) > 0 {
		return nil, fmt.Errorf("%w: awsAuthConfig", ErrUnsupportedCredentials)
	}

	server := string(secret.Data[argocdv1alpha1.ClusterSecretServerKey])
	if server == "" || (server == argocdv1alpha1.InClusterServer && !cfg.hasCredentials()) {
		restConfig := rest.CopyConfig(local)
		restConfig.Timeout = RequestTimeout
		return restConfig, nil
	}

	return &rest.Config{
		Host:        server,
		Username:    cfg.Username,
		Password:    cfg.Password,
		BearerToken: cfg.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   cfg.TLSClientConfig.Insecure,
			ServerName: cfg.TLSClientConfig.ServerName,
			CAData:     cfg.TLSClientConfig.CAData,
			CertData:   cfg.TLSClientConfig.CertData,
			KeyData:    cfg.TLSClientConfig.KeyData,
		},
		Timeout: RequestTimeout,
	}, nil
}

// ClientFactory returns a client for the infra-plane cluster a credentials Secret
// describes. It is an interface so tests can point the control-plane operator at a
// cluster of their choosing.
type ClientFactory interface {
	ClientFor(secret *corev1.Secret) (client.Client, error)
}

// SecretClientFactory is the ClientFactory the operator runs with.
type SecretClientFactory struct {
	// Local is the operator's own rest.Config, used for a Secret that registers the
	// operator's own cluster.
	Local *rest.Config

	// Scheme maps the types the returned clients read and write.
	Scheme *runtime.Scheme
}

// ClientFor builds a new client from secret. It contacts nothing: the client discovers
// the cluster's API lazily, on its first request.
func (f SecretClientFactory) ClientFor(secret *corev1.Secret) (client.Client, error) {
	restConfig, err := RESTConfig(secret, f.Local)
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: f.Scheme})
}

// ClientFactoryFunc adapts a function to a ClientFactory.
type ClientFactoryFunc func(secret *corev1.Secret) (client.Client, error)

// ClientFor calls f.
func (f ClientFactoryFunc) ClientFor(secret *corev1.Secret) (client.Client, error) {
	return f(secret)
}
