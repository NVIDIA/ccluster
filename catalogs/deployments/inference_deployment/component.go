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

const (
	// DefaultMinReplicas and DefaultMaxReplicas are the chart's HPA bounds: one
	// replica, autoscaling off.
	DefaultMinReplicas int32 = 1
	DefaultMaxReplicas int32 = 1

	// MaxAverageValue is 2^53, the largest integer a float64 holds exactly. KEDA
	// multiplies the threshold by 1000, so anything larger overflows; the chart ships
	// it as the default, which effectively disables the trigger.
	MaxAverageValue int64 = 1 << 53

	// DefaultMaxUnavailable and DefaultMaxSurge are the chart's rollout strategy:
	// replace one pod at a time without surging.
	DefaultMaxUnavailable int32 = 1
	DefaultMaxSurge       int32 = 0
)

// BackendProtocol is the protocol nginx speaks to the container. Its field carries the
// enum and a title in its jsonschema tag: the schema reflector inlines named string
// types, and the title is what names the generated Python enum.
type BackendProtocol string

const (
	HTTP BackendProtocol = "HTTP"
	GRPC BackendProtocol = "GRPC"
)

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
	HPA *HPA `json:"hpa,omitempty"`

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
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// RevisionNumber is this deployment's revision. It labels the pods and names the
	// config ConfigMap, so bumping it rolls the pods. The chart ships 1.
	RevisionNumber *int64 `json:"revisionNumber,omitempty"`

	// RolloutPaused pauses the Rollout. The chart ships false.
	RolloutPaused *bool `json:"rolloutPaused,omitempty"`

	// BackendProtocol is the protocol nginx speaks to the container. The chart
	// ships HTTP.
	//nolint:lll // A struct tag cannot wrap.
	BackendProtocol *BackendProtocol `json:"backendProtocol,omitempty" jsonschema:"title=BackendProtocol,enum=HTTP,enum=GRPC"`

	// SessionAffinity routes requests carrying the same X-Session-Id header to the
	// same pod through an Istio consistent-hash DestinationRule. Best effort: the pod a
	// session lands on changes whenever the endpoint set does. The chart ships false.
	SessionAffinity *bool `json:"sessionAffinity,omitempty"`

	// ConfigFile is a file mounted into the container from a ConfigMap.
	ConfigFile *deployments.ConfigFile `json:"configFile,omitempty"`

	// Metrics is the application's own Prometheus endpoint. Unset, the chart scrapes
	// /metrics on ContainerPort.
	Metrics *Metrics `json:"metrics,omitempty"`
}

// HPA bounds the KEDA ScaledObject that scales the Rollout.
type HPA struct {
	// MinReplicas is the lower bound. 0 enables scale-to-zero, which needs the
	// cluster's Prometheus to scrape nginx ingress controller metrics; without them
	// the deployment never wakes. The chart ships 1.
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the upper bound, at least MinReplicas. The chart ships 1.
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// AverageValue is the target number of in-flight requests per pod, from 1 to
	// 2^53. The chart ships 2^53, which effectively disables the trigger.
	AverageValue *int64 `json:"averageValue,omitempty"`

	// CooldownPeriod is how many seconds KEDA waits after the last active trigger
	// before scaling down. The chart ships 1800.
	CooldownPeriod *int64 `json:"cooldownPeriod,omitempty"`
}

// Validate reports bounds that are negative, inverted or outside what KEDA accepts.
// A nil receiver is valid: the chart's defaults are.
func (h *HPA) Validate() error {
	if h == nil {
		return nil
	}
	minReplicas, maxReplicas := DefaultMinReplicas, DefaultMaxReplicas
	if h.MinReplicas != nil {
		minReplicas = *h.MinReplicas
	}
	if h.MaxReplicas != nil {
		maxReplicas = *h.MaxReplicas
	}

	var errs []error
	if minReplicas < 0 {
		errs = append(errs, fmt.Errorf("%w: hpa.minReplicas %d is negative",
			deployments.ErrInvalidValue, minReplicas))
	}
	if maxReplicas < minReplicas {
		errs = append(errs, fmt.Errorf("%w: hpa.maxReplicas %d is below hpa.minReplicas %d",
			deployments.ErrInvalidValue, maxReplicas, minReplicas))
	}
	if h.AverageValue != nil && (*h.AverageValue < 1 || *h.AverageValue > MaxAverageValue) {
		errs = append(errs, fmt.Errorf("%w: hpa.averageValue %d is outside 1-%d",
			deployments.ErrInvalidValue, *h.AverageValue, MaxAverageValue))
	}
	if h.CooldownPeriod != nil && *h.CooldownPeriod < 0 {
		errs = append(errs, fmt.Errorf("%w: hpa.cooldownPeriod %d is negative",
			deployments.ErrInvalidValue, *h.CooldownPeriod))
	}
	return errors.Join(errs...)
}

// RolloutStrategy bounds the Rollout's canary steps, as absolute pod counts.
type RolloutStrategy struct {
	// MaxUnavailable is the most pods that may be unavailable during an update. The
	// chart ships 1.
	MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`

	// MaxSurge is the most pods that may be scheduled above the desired count during
	// an update. The chart ships 0.
	MaxSurge *int32 `json:"maxSurge,omitempty"`
}

// Validate reports a negative bound, or a pair that sums to zero and so could never
// make progress. A nil receiver is valid: the chart's defaults are.
func (s *RolloutStrategy) Validate() error {
	if s == nil {
		return nil
	}
	maxUnavailable, maxSurge := DefaultMaxUnavailable, DefaultMaxSurge
	if s.MaxUnavailable != nil {
		maxUnavailable = *s.MaxUnavailable
	}
	if s.MaxSurge != nil {
		maxSurge = *s.MaxSurge
	}
	switch {
	case maxUnavailable < 0 || maxSurge < 0:
		return fmt.Errorf("%w: rolloutStrategy bounds must not be negative",
			deployments.ErrInvalidValue)
	case maxUnavailable+maxSurge < 1:
		return fmt.Errorf("%w: rolloutStrategy.maxUnavailable %d and maxSurge %d must sum to at least 1",
			deployments.ErrInvalidValue, maxUnavailable, maxSurge)
	}
	return nil
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
		v.Metrics.Validate(),
	}
	if v.ContainerPort == 0 {
		errs = append(errs, deployments.MissingError([]string{"containerPort"}))
	} else if err := deployments.ValidatePort("containerPort", v.ContainerPort); err != nil {
		errs = append(errs, err)
	}
	if p := v.BackendProtocol; p != nil && *p != HTTP && *p != GRPC {
		errs = append(errs, fmt.Errorf("%w: backendProtocol %q is not HTTP or GRPC",
			deployments.ErrInvalidValue, *p))
	}
	if v.RevisionNumber != nil && *v.RevisionNumber < 0 {
		errs = append(errs, fmt.Errorf("%w: revisionNumber %d is negative",
			deployments.ErrInvalidValue, *v.RevisionNumber))
	}
	return errors.Join(errs...)
}
