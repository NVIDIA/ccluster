/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package inferencedeployment describes the values of
// catalogs/deployments/inference_deployment: an inference service run as an Argo
// Rollout behind an nginx Ingress, autoscaled by KEDA on in-flight requests, and scraped
// by Prometheus through the Istio sidecar.
package inferencedeployment

import (
	"errors"
	"fmt"
	"regexp"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/deployments/inference_deployment"

// Values is the top-level values document for
// catalogs/deployments/inference_deployment.
type Values struct {
	deployments.Workload

	// Domain is the DNS zone the deployment's hostname is created in. The Ingress
	// host is <name>.<namespace>.<domain> unless DeploymentURL overrides it. Required.
	Domain string `json:"domain"`

	// DeploymentURL replaces the derived Ingress host with a full hostname.
	DeploymentURL *string `json:"deploymentUrl,omitempty"`

	// ContainerPort is the port the container serves on, and the port the Service,
	// Ingress and readiness probe target. Required; it may not be an Istio port.
	ContainerPort int32 `json:"containerPort"`

	// EndpointCertificateAuthority is a PEM CA bundle. When set, nginx requires
	// clients to present a certificate it signed.
	EndpointCertificateAuthority *string `json:"endpointCertificateAuthority,omitempty"`

	// EndpointBearerToken, when set, makes nginx reject requests whose Authorization
	// header is not "Bearer <token>".
	EndpointBearerToken *string `json:"endpointBearerToken,omitempty"`

	// HPA bounds the KEDA ScaledObject. The chart ships one fixed replica.
	HPA *deployments.HPA `json:"hpa,omitempty"`

	// Healthcheck is the path of the HTTP readiness probe on ContainerPort. The chart
	// ships "/"; an explicit "" switches to a TCP probe, for gRPC servers.
	Healthcheck *string `json:"healthcheck,omitempty"`

	// Command overrides the image's entrypoint.
	Command []string `json:"command,omitempty"`

	// Args overrides the image's arguments.
	Args []string `json:"args,omitempty"`

	// HFToken is rendered as the HF_TOKEN environment variable, after EnvVars, so it
	// wins over a token set there.
	HFToken *string `json:"hfToken,omitempty"`

	// EnableNodeModelCache mounts the namespace's node-model-cache-pvc at /tmp and
	// points HF_HUB_CACHE at it. The claim comes from catalogs/namespace_base.
	EnableNodeModelCache *bool `json:"enableNodeModelCache,omitempty"`

	// RolloutStrategy bounds the canary rollout. The chart ships one-at-a-time.
	RolloutStrategy *deployments.RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// RevisionNumber is this deployment's revision. It labels the pods and names the
	// config ConfigMap, so bumping it rolls the pods. The chart ships 1.
	RevisionNumber *int64 `json:"revisionNumber,omitempty"`

	// RolloutPaused pauses the Rollout. The chart ships false.
	RolloutPaused *bool `json:"rolloutPaused,omitempty"`

	// BackendProtocol is the protocol nginx speaks to the container. The chart
	// ships HTTP.
	//nolint:lll // A struct tag cannot wrap.
	BackendProtocol *deployments.BackendProtocol `json:"backendProtocol,omitempty" jsonschema:"title=BackendProtocol,enum=HTTP,enum=GRPC"`

	// SessionAffinity routes requests carrying the same X-Session-Id header to the
	// same pod through an Istio consistent-hash DestinationRule. Best effort: the pod a
	// session lands on changes whenever the endpoint set does. The chart ships false.
	SessionAffinity *bool `json:"sessionAffinity,omitempty"`

	// ConfigFile is a file mounted into the container from a ConfigMap.
	ConfigFile *deployments.ConfigFile `json:"configFile,omitempty"`

	// VolumeMounts are existing claims mounted into the container.
	VolumeMounts []deployments.VolumeMount `json:"volumeMounts,omitempty"`

	// Metrics is the application's own Prometheus endpoint. Unset, the chart scrapes
	// /metrics on ContainerPort.
	Metrics *Metrics `json:"metrics,omitempty"`
}

// Metrics is the application's own Prometheus endpoint, scraped through the Istio
// sidecar with a 2000-sample cap. Both fields are required when it is set.
type Metrics struct {
	// Port is the container port serving the metrics. It may not be an Istio port.
	Port int32 `json:"port"`

	// Path is the metrics path, starting with "/".
	Path string `json:"path"`
}

var metricsPathPattern = regexp.MustCompile(`^/[^\s?#]*$`)

// Validate reports a port outside the allowed range or a path that is not absolute.
// A nil receiver is valid: the endpoint is optional.
func (m *Metrics) Validate() error {
	if m == nil {
		return nil
	}
	var errs []error
	if err := deployments.ValidatePort("metrics.port", m.Port); err != nil {
		errs = append(errs, err)
	}
	if !metricsPathPattern.MatchString(m.Path) {
		errs = append(errs, fmt.Errorf("%w: metrics.path %q must start with \"/\" and carry no query or fragment",
			deployments.ErrInvalidValue, m.Path))
	}
	return errors.Join(errs...)
}

// New returns the inference_deployment chart entry, installed into namespace with
// values. The chart's objects are all created in that namespace, and its Ingress host
// is derived from it.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// Validate implements catalogs.Validator: the required values that are unset, and the
// optional ones that are malformed.
func (v Values) Validate() error {
	errs := []error{
		v.Workload.Validate(),
		deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "domain", Value: v.Domain},
		)),
		v.HPA.Validate(),
		v.RolloutStrategy.Validate(),
		v.ConfigFile.Validate(),
		deployments.ValidateVolumeMounts(v.VolumeMounts),
		v.Metrics.Validate(),
	}
	if v.ContainerPort == 0 {
		errs = append(errs, deployments.MissingError([]string{"containerPort"}))
	} else if err := deployments.ValidatePort("containerPort", v.ContainerPort); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, v.BackendProtocol.Validate())
	if v.RevisionNumber != nil && *v.RevisionNumber < 0 {
		errs = append(errs, fmt.Errorf("%w: revisionNumber %d is negative",
			deployments.ErrInvalidValue, *v.RevisionNumber))
	}
	return errors.Join(errs...)
}
