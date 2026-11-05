/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package cclclient is a client for the control-plane operator's API.
//
// Client registers infra-plane clusters with the control plane and removes them again,
// and creates, polls and deletes the component sets deployed onto them. It works in one
// namespace, fixed at construction, over clientsets the caller supplies. It is the Go
// counterpart of the Python package's CCLClient, and the two write the same objects
// under the same field manager, so either can update what the other applied.
//
// Build each component from the entry a chart package's New returns, with
// catalogs.Component.
package cclclient

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	cclusterac "github.com/NVIDIA/ccluster/pkg/client/applyconfiguration/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

// FieldManager is the server-side apply field manager every write is made under.
// Applying under one fixed name is what lets a later ApplyInfraPlane update the Secret
// this client wrote without conflicting with itself, and what marks the objects as ours
// in managedFields. The Python CCLClient uses the same name.
const FieldManager = "ccluster"

// TLSClientConfig is the tlsClientConfig block of an Argo CD cluster Secret's config.
type TLSClientConfig struct {
	Insecure bool `json:"insecure"`
	// CAData is the API server's CA certificate, PEM. encoding/json base64-encodes it,
	// which is the form Argo CD reads.
	CAData []byte `json:"caData,omitempty"`
}

// ClusterConfig is the config key of an Argo CD cluster Secret: how Argo CD reaches the
// cluster. Only the fields this client sets are modelled; see
// https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#clusters
type ClusterConfig struct {
	BearerToken     string          `json:"bearerToken,omitempty"`
	TLSClientConfig TLSClientConfig `json:"tlsClientConfig"`
}

// InfraPlaneOptions describes the cluster ApplyInfraPlane registers.
type InfraPlaneOptions struct {
	// Name is the CCLInfraPlane's name. Its credentials Secret is "<Name>-credentials".
	Name string
	// Domain is the cluster's DNS domain. The operator writes it to the cluster's
	// CCLClusterConfig, and it cannot be changed once the CCLInfraPlane exists.
	Domain string
	// Server is the API server URL Argo CD in the control plane connects to. Empty means
	// https://kubernetes.default.svc, the address Argo CD treats as its own cluster.
	Server string
	// BearerToken is the token Argo CD and the operator authenticate with. Optional.
	BearerToken string
	// CACert is the API server's CA certificate, PEM. Optional.
	CACert []byte
}

// CredentialsSecretName is the name of the Secret a CCLInfraPlane called name points at.
func CredentialsSecretName(name string) string {
	return name + "-credentials"
}

// Client creates and deletes infra planes (cluster registrations) and component sets, in
// one namespace.
//
// The namespace is where the CCLInfraPlanes, their credentials Secrets and the
// CCLComponentSets are read and written, and the client reaches no other. It is the
// isolation boundary: whoever constructs the client decides which namespace it serves.
// Construct one client per namespace; clients built on the same clientsets share their
// connections. Constructing a client reads and writes no object.
type Client struct {
	namespace string
	kube      kubernetes.Interface
	ccl       versioned.Interface
}

// New returns a client for namespace over the given clientsets. kube reaches Namespaces
// and Secrets, ccl the ccluster API.
func New(namespace string, kube kubernetes.Interface, ccl versioned.Interface) *Client {
	return &Client{namespace: namespace, kube: kube, ccl: ccl}
}

// NewForConfig returns a client for namespace with clientsets built from config. In a
// pod, pass rest.InClusterConfig(); from a workstation, a config loaded from the
// kubeconfig. To serve several namespaces, build the clientsets once and call New for
// each.
func NewForConfig(namespace string, config *rest.Config) (*Client, error) {
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes clientset: %w", err)
	}
	ccl, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating ccluster clientset: %w", err)
	}
	return New(namespace, kube, ccl), nil
}

// Namespace returns the namespace the client serves.
func (c *Client) Namespace() string {
	return c.namespace
}

// EnsureNamespace creates the client's namespace if it does not exist.
//
// Another caller creating it between this read and this create is not an error. No
// other method creates one, so call this before registering the first CCLInfraPlane of a
// namespace. A CCLComponentSet needs an Available CCLInfraPlane already in its namespace,
// so it can never be the first object written there. Creation is separate from the
// writes so that a mistyped namespace fails against the API server rather than quietly
// gaining a working infra plane.
func (c *Client) EnsureNamespace(ctx context.Context) error {
	namespaces := c.kube.CoreV1().Namespaces()
	_, err := namespaces.Get(ctx, c.namespace, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = namespaces.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: c.namespace},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// ApplyInfraPlane creates or updates the CCLInfraPlane called opts.Name, for the cluster
// at opts.Server.
//
// It server-side applies the CCLInfraPlane, then the Secret "<name>-credentials" it
// points at, in Argo CD's cluster-secret shape. The plane goes first so that a domain
// the API server rejects - malformed, or changed on an existing plane - fails the call
// before the credentials are written: the operator acts on a changed Secret at once, so
// new credentials under the old domain would retarget the existing plane. The call
// returns as soon as both objects are applied; it does not wait for the operator to
// report Available.
//
// Server, BearerToken and CACert are all optional. With none given, this registers the
// cluster the operator itself runs in: Server defaults to https://kubernetes.default.svc,
// and the token and CA stay unset, which Argo CD accepts for that address by connecting
// with its own ServiceAccount instead.
//
// The namespace must already exist; call EnsureNamespace first for the first plane of a
// namespace.
func (c *Client) ApplyInfraPlane(
	ctx context.Context,
	opts InfraPlaneOptions,
) (*cclusternvidiacomv1alpha1.CCLInfraPlane, error) {
	server := opts.Server
	if server == "" {
		server = argocdv1alpha1.InClusterServer
	}
	config, err := json.Marshal(ClusterConfig{
		BearerToken:     opts.BearerToken,
		TLSClientConfig: TLSClientConfig{CAData: opts.CACert},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling cluster config: %w", err)
	}
	secretName := CredentialsSecretName(opts.Name)

	plane, err := c.ccl.CclusterV1alpha1().CCLInfraPlanes(c.namespace).Apply(ctx,
		cclusterac.CCLInfraPlane(opts.Name, c.namespace).
			WithSpec(cclusterac.CCLInfraPlaneSpec().
				WithClusterCredentialsRef(cclusterac.LocalObjectReference().WithName(secretName)).
				WithDomain(opts.Domain)),
		applyOptions())
	if err != nil {
		return nil, err
	}
	if _, err := c.kube.CoreV1().Secrets(c.namespace).Apply(ctx,
		corev1ac.Secret(secretName, c.namespace).
			WithType(corev1.SecretTypeOpaque).
			WithStringData(map[string]string{
				argocdv1alpha1.ClusterSecretServerKey: server,
				argocdv1alpha1.ClusterSecretConfigKey: string(config),
			}),
		applyOptions()); err != nil {
		return nil, err
	}
	return plane, nil
}

// DeleteInfraPlane removes the CCLInfraPlane called name and its credentials Secret.
//
// The CCLInfraPlane goes first so its finalizer can remove the mirrored Argo CD cluster
// Secret. Either object already being gone is not an error, and neither is a namespace
// that does not exist: the API server answers both with the same 404. The namespace is
// left in place.
func (c *Client) DeleteInfraPlane(ctx context.Context, name string) error {
	if err := ignoreNotFound(c.ccl.CclusterV1alpha1().CCLInfraPlanes(c.namespace).
		Delete(ctx, name, metav1.DeleteOptions{})); err != nil {
		return err
	}
	return ignoreNotFound(c.kube.CoreV1().Secrets(c.namespace).
		Delete(ctx, CredentialsSecretName(name), metav1.DeleteOptions{}))
}

// GetInfraPlane returns the CCLInfraPlane called name. A missing one is an error for
// which apierrors.IsNotFound is true.
func (c *Client) GetInfraPlane(
	ctx context.Context,
	name string,
) (*cclusternvidiacomv1alpha1.CCLInfraPlane, error) {
	return c.ccl.CclusterV1alpha1().CCLInfraPlanes(c.namespace).Get(ctx, name, metav1.GetOptions{})
}

// ApplyComponentSet creates or updates the CCLComponentSet called name.
//
// components deploy in list order, each onto the CCLInfraPlane its InfraPlaneRef names;
// those planes must live in the client's namespace and already be Available. One set
// may target several planes. Build each with catalogs.Component. Applying under
// FieldManager means calling again with a different list updates the set in place.
//
// The call returns as soon as the object is applied; it does not wait for the rollout to
// finish. Poll IsComponentSetFinished, or check a set already in hand with
// ComponentSetFinished, to see when it has converged.
//
// The client's namespace is the set's own, on the control plane. The namespace a
// component installs into is on its infra plane, created there by Argo CD from the
// CreateNamespace=true sync option the operator puts on every Application.
func (c *Client) ApplyComponentSet(
	ctx context.Context,
	name string,
	components []cclusternvidiacomv1alpha1.ComponentSpec,
) (*cclusternvidiacomv1alpha1.CCLComponentSet, error) {
	specs := make([]*cclusterac.ComponentSpecApplyConfiguration, 0, len(components))
	for _, component := range components {
		specs = append(specs, componentSpec(component))
	}
	return c.ccl.CclusterV1alpha1().CCLComponentSets(c.namespace).Apply(ctx,
		cclusterac.CCLComponentSet(name, c.namespace).
			WithSpec(cclusterac.CCLComponentSetSpec().WithComponents(specs...)),
		applyOptions())
}

// IsComponentSetFinished reads the CCLComponentSet called name once and reports
// ComponentSetFinished for it. The caller controls polling and timeouts. A missing set
// is an error for which apierrors.IsNotFound is true.
func (c *Client) IsComponentSetFinished(ctx context.Context, name string) (bool, error) {
	componentSet, err := c.GetComponentSet(ctx, name)
	if err != nil {
		return false, err
	}
	return ComponentSetFinished(componentSet), nil
}

// ComponentSetFinished reports whether componentSet's current generation is applied and
// reported converged. It returns false during deletion or unless validation, Application
// writes and rollout completion are confirmed for this generation.
//
// It makes no API call, so a program that already holds the object, from an informer's
// cache or a list, can check it directly.
func ComponentSetFinished(componentSet *cclusternvidiacomv1alpha1.CCLComponentSet) bool {
	// The finalizer never writes conditions, so a set being torn down still carries the
	// ones its last generation finished with.
	if componentSet.DeletionTimestamp != nil {
		return false
	}

	conditions := ComponentSetCurrentConditions(componentSet)
	expected := map[string]metav1.ConditionStatus{
		cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated:         metav1.ConditionTrue,
		cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied: metav1.ConditionTrue,
		cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing:       metav1.ConditionFalse,
	}
	for conditionType, status := range expected {
		condition, ok := conditions[conditionType]
		if !ok || condition.Status != status {
			return false
		}
	}
	return true
}

// DeleteComponentSet removes the CCLComponentSet called name.
//
// It returns as soon as the delete is accepted. The operator's finalizer then removes the
// set's Argo CD Applications in reverse order, one at a time, so the object stays in
// Terminating until every chart is uninstalled. Already being gone is not an error, and
// neither is a namespace that does not exist: the API server answers both with the same
// 404. The referenced CCLInfraPlanes and the namespace are left in place.
func (c *Client) DeleteComponentSet(ctx context.Context, name string) error {
	return ignoreNotFound(c.ccl.CclusterV1alpha1().CCLComponentSets(c.namespace).
		Delete(ctx, name, metav1.DeleteOptions{}))
}

// GetComponentSet returns the CCLComponentSet called name. A missing one is an error for
// which apierrors.IsNotFound is true.
func (c *Client) GetComponentSet(
	ctx context.Context,
	name string,
) (*cclusternvidiacomv1alpha1.CCLComponentSet, error) {
	return c.ccl.CclusterV1alpha1().CCLComponentSets(c.namespace).Get(ctx, name, metav1.GetOptions{})
}

// ComponentSetCurrentConditions returns the conditions computed from the set's current
// generation, keyed by type.
//
// The operator stamps the generation only on the conditions it writes, so after a failed
// validation or apply the other types keep an older observedGeneration. Those are
// dropped; what remains is what the operator concluded about the spec as it now stands,
// and the reason and message behind it.
func ComponentSetCurrentConditions(
	componentSet *cclusternvidiacomv1alpha1.CCLComponentSet,
) map[string]metav1.Condition {
	conditions := map[string]metav1.Condition{}
	for _, condition := range componentSet.Status.Conditions {
		if condition.ObservedGeneration == componentSet.Generation {
			conditions[condition.Type] = condition
		}
	}
	return conditions
}

// componentSpec is component as an apply configuration. Every field of ComponentSpec is
// copied; an unset Values stays unset.
func componentSpec(
	component cclusternvidiacomv1alpha1.ComponentSpec,
) *cclusterac.ComponentSpecApplyConfiguration {
	spec := cclusterac.ComponentSpec().
		WithName(component.Name).
		WithInfraPlaneRef(cclusterac.LocalObjectReference().WithName(component.InfraPlaneRef.Name)).
		WithChart(component.Chart).
		WithNamespace(component.Namespace)
	if component.Values != nil {
		spec.WithValues(*component.Values)
	}
	return spec
}

func applyOptions() metav1.ApplyOptions {
	return metav1.ApplyOptions{FieldManager: FieldManager, Force: true}
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
