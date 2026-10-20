/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package apiclient reads an Application's deployed objects through argocd-server's
// REST API: its resource tree, the live manifest of an object in it, and that object's
// events.
//
// None of these is in the Application object, which records only the objects the chart
// rendered and their aggregate health. argocd-server answers them on the operator's
// behalf with Argo CD's own credentials for the destination cluster, so the operator
// never holds those itself, and what it can read is bounded by the Argo CD account its
// token belongs to: a read-only account the cp chart declares.
//
// Like internal/argocd/v1alpha1, the client mirrors only the parts of the API it uses
// rather than importing Argo CD's Go client, whose dependency tree is most of Argo CD.
package apiclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The requests the client makes, each the last element of its path under
// /api/v1/applications/{name}/.
const (
	opResourceTree = "resource-tree"
	opResource     = "resource"
	opEvents       = "events"
)

// maxResponseBytes bounds how much of a response the client reads. A manifest is at most
// a Kubernetes object, which etcd limits to 1.5MiB; the tree and event lists of one
// Application are far smaller in practice.
const maxResponseBytes = 16 << 20

// SecretKey names one key of a Secret.
type SecretKey struct {
	types.NamespacedName
	Key string
}

func (s SecretKey) String() string {
	return fmt.Sprintf("%s[%s]", s.NamespacedName, s.Key)
}

// read returns the key's value, or an error if the Secret or the key is missing.
func (s SecretKey) read(ctx context.Context, r client.Reader) ([]byte, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, s.NamespacedName, &secret); err != nil {
		return nil, fmt.Errorf("reading Secret %s: %w", s.NamespacedName, err)
	}
	value, ok := secret.Data[s.Key]
	if !ok || len(value) == 0 {
		return nil, fmt.Errorf("secret %s has no key %q", s.NamespacedName, s.Key)
	}
	return value, nil
}

// Options configures a Client.
type Options struct {
	// ServerURL is argocd-server's base URL, such as https://argocd-server.argocd.svc.
	ServerURL string

	// Token is the Secret key holding the bearer token of the Argo CD account the
	// client reads as. It is read on every request, so a rotated token is used as soon
	// as the Secret changes, without a restart.
	Token SecretKey

	// CA, if set, is the Secret key holding the PEM bundle argocd-server's certificate
	// is verified against, read on every new connection. If unset, the system roots
	// are used.
	CA *SecretKey

	// InsecureSkipTLSVerify accepts any certificate argocd-server presents. It exists
	// for the Argo CD the cp chart bundles, whose server generates a self-signed
	// certificate no CA bundle can describe; the connection stays inside the cluster.
	InsecureSkipTLSVerify bool

	// Reader reads the Token and CA Secrets. The manager's cached client serves the
	// reads from memory.
	Reader client.Reader

	// Timeout bounds each request. Zero means 30 seconds.
	Timeout time.Duration
}

// Client is a read-only client of argocd-server. It is safe for concurrent use.
type Client struct {
	server *url.URL
	token  SecretKey
	reader client.Reader
	http   *http.Client
}

// New returns a Client for opts. It does not contact argocd-server or read the Secrets,
// so it succeeds before the manager's cache has started.
func New(opts Options) (*Client, error) {
	server, err := url.Parse(opts.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("parsing argocd-server URL: %w", err)
	}
	if server.Scheme != "https" && server.Scheme != "http" {
		return nil, fmt.Errorf("argocd-server URL %q: scheme must be https or http", opts.ServerURL)
	}
	if server.Host == "" {
		return nil, fmt.Errorf("argocd-server URL %q has no host", opts.ServerURL)
	}
	if opts.Reader == nil {
		return nil, errors.New("argocd-server client needs a Reader for its Secrets")
	}
	if opts.Token.Name == "" || opts.Token.Key == "" {
		return nil, errors.New("argocd-server client needs a token Secret name and key")
	}
	if opts.CA != nil && opts.InsecureSkipTLSVerify {
		return nil, errors.New("argocd-server client: a CA and InsecureSkipTLSVerify are mutually exclusive")
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig(opts)

	return &Client{
		server: server,
		token:  opts.Token,
		reader: opts.Reader,
		http:   &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// tlsConfig returns the TLS settings for opts.
//
// A CA from a Secret cannot go into RootCAs, which is fixed when the transport is
// built, before the cache that serves the Secret has started. Instead the standard
// verification is switched off and VerifyConnection repeats it against the bundle read
// at handshake time, so a rotated CA also takes effect without a restart.
func tlsConfig(opts Options) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case opts.InsecureSkipTLSVerify:
		cfg.InsecureSkipVerify = true
	case opts.CA != nil:
		ca, reader := *opts.CA, opts.Reader
		cfg.InsecureSkipVerify = true // VerifyConnection verifies instead.
		cfg.VerifyConnection = func(state tls.ConnectionState) error {
			return verifyAgainst(ca, reader, state)
		}
	}
	return cfg
}

// verifyAgainst verifies the server's certificate chain and name against the PEM bundle
// in ca, as the standard verification would against RootCAs.
func verifyAgainst(ca SecretKey, reader client.Reader, state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("argocd-server presented no certificate")
	}
	// The handshake has no context to inherit; the read is from the cache.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bundle, err := ca.read(ctx, reader)
	if err != nil {
		return fmt.Errorf("reading argocd-server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		return fmt.Errorf("argocd-server CA %s holds no PEM certificate", ca)
	}
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	_, err = state.PeerCertificates[0].Verify(x509.VerifyOptions{
		DNSName:       state.ServerName,
		Roots:         roots,
		Intermediates: intermediates,
	})
	return err
}

// ResourceTree returns every object app deployed, and the objects those created.
func (c *Client) ResourceTree(ctx context.Context, app ApplicationRef) (*ResourceTree, error) {
	var tree ResourceTree
	if err := c.get(ctx, app, opResourceTree, nil, &tree); err != nil {
		return nil, err
	}
	return &tree, nil
}

// Resource returns the live object ref names, one of the nodes of app's resource tree.
func (c *Client) Resource(ctx context.Context, app ApplicationRef, ref ResourceRef) (*unstructured.Unstructured, error) {
	query := url.Values{
		"namespace":    {ref.Namespace},
		"resourceName": {ref.Name},
		"group":        {ref.Group},
		"version":      {ref.Version},
		"kind":         {ref.Kind},
	}
	var response resourceResponse
	if err := c.get(ctx, app, opResource, query, &response); err != nil {
		return nil, err
	}
	if response.Manifest == nil {
		return nil, &APIError{Op: opResource, StatusCode: http.StatusOK, Message: "no manifest in response",
			kind: ErrResourceGone}
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON([]byte(*response.Manifest)); err != nil {
		return nil, fmt.Errorf("decoding manifest of %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
	}
	return obj, nil
}

// ResourceEvents returns the Kubernetes events recorded for ref, one of the nodes of
// app's resource tree, in the order argocd-server lists them.
func (c *Client) ResourceEvents(ctx context.Context, app ApplicationRef, ref ResourceRef) ([]corev1.Event, error) {
	query := url.Values{
		"resourceNamespace": {ref.Namespace},
		"resourceName":      {ref.Name},
		"resourceUID":       {ref.UID},
	}
	var events corev1.EventList
	if err := c.get(ctx, app, opEvents, query, &events); err != nil {
		return nil, err
	}
	return events.Items, nil
}

// get sends GET /api/v1/applications/{app.Name}/{op} and decodes a 200 answer into out.
func (c *Client) get(ctx context.Context, app ApplicationRef, op string, query url.Values, out any) error {
	token, err := c.token.read(ctx, c.reader)
	if err != nil {
		return fmt.Errorf("reading argocd-server token: %w", err)
	}

	if query == nil {
		query = url.Values{}
	}
	query.Set("appNamespace", app.Namespace)
	query.Set("project", app.Project)
	endpoint := c.server.JoinPath("api", "v1", "applications", app.Name, op)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	// A Secret created from a file usually ends in a newline.
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("argocd-server %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("argocd-server %s: reading response: %w", op, err)
	}

	if resp.StatusCode != http.StatusOK {
		message := string(body)
		var decoded errorResponse
		if json.Unmarshal(body, &decoded) == nil && decoded.Message != "" {
			message = decoded.Message
		}
		return &APIError{Op: op, StatusCode: resp.StatusCode, Message: message,
			kind: classify(op, resp.StatusCode, message)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("argocd-server %s: decoding response: %w", op, err)
	}
	return nil
}
