/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package dynamodeployment describes the values of
// catalogs/deployments/dynamo_deployment: an inference service on NVIDIA Dynamo. The
// chart renders a DynamoGraphDeployment with a CPU-only Frontend and vLLM workers, in
// one of two serving modes: aggregated, one worker pool scaled by KEDA on in-flight
// Frontend requests, or disaggregated, fixed prefill and decode pools. Both use the KV
// router and the vLLM backend.
//
// Dynamo pods are outside the Istio mesh: worker responses stream back to the Frontend
// over raw TCP on ephemeral ports, which STRICT mTLS cannot enrol. The chart's own
// NetworkPolicy opens what the namespace baseline would otherwise block.
package dynamodeployment

import (
	"errors"
	"fmt"
	"strings"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/deployments/dynamo_deployment"

const (
	// DefaultContainerPort is the Frontend port the chart ships.
	DefaultContainerPort int32 = 8000

	// KServeGRPCMetricsPort is where the Frontend serves HTTP metrics and health when
	// its KServe gRPC server is on, so ContainerPort may not equal it in GRPC mode. It
	// is the chart's kserveGrpcMetricsPort value; keep the two in step.
	KServeGRPCMetricsPort int32 = 8788
)

// ServingMode selects the worker topology. Fields of this type carry the enum and a
// title in their jsonschema tag; see deployments.BackendProtocol for why.
type ServingMode string

const (
	// Aggregated runs one worker pool, scaled by KEDA between hpa.minReplicas and
	// hpa.maxReplicas. It is the chart default.
	Aggregated ServingMode = "aggregated"

	// Disaggregated runs fixed prefill and decode pools connected by NIXL.
	Disaggregated ServingMode = "disaggregated"
)

// Values is the top-level values document for catalogs/deployments/dynamo_deployment.
// Unlike the other workload charts it does not embed deployments.Workload: resources
// and placement belong to each worker pool, not to the deployment.
type Values struct {
	// Name names the DynamoGraphDeployment and the objects around it. Required.
	Name string `json:"name"`

	// DeploymentID identifies the deployment across revisions. It labels every object
	// the chart creates as platform.centml.ai/deployment-id and isolates the
	// deployment's metrics. Required.
	DeploymentID string `json:"deploymentId"`

	// ImageURL is the Dynamo runtime image for the Frontend and every worker, for
	// example nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.4.0. Required.
	ImageURL string `json:"imageUrl"`

	// ImagePullSecret, when set, creates a dockerconfigjson Secret for ImageURL's
	// registry and adds it to every pod's imagePullSecrets.
	ImagePullSecret *deployments.ImagePullSecret `json:"imagePullSecret,omitempty"`

	// ImagePullSecretNames are Secrets in the release namespace, added to every pod's
	// imagePullSecrets after the chart's own; a name already listed is skipped. The
	// chart never creates or reads them. The kubelet reads them on each pull, so a
	// Secret refreshed in place needs no pod restart.
	ImagePullSecretNames []string `json:"imagePullSecretNames,omitempty"`

	// Domain is the infra plane's domain, <plane>.<namespace>.<base domain>, which its
	// CCLInfraPlane shows as status.domain. The Ingress host is
	// <name>.ingress.<domain>, under the *.ingress.<domain> record the control-plane
	// operator publishes for the cluster's ingress, unless DeploymentURL overrides it. The
	// host does not include the namespace, so deployment names must be unique across the
	// plane. Required.
	Domain string `json:"domain"`

	// DeploymentURL replaces the derived Ingress host with a full hostname.
	DeploymentURL *string `json:"deploymentUrl,omitempty"`

	// ContainerPort is the port the Frontend listens on, from 1024 to 65535 and not
	// KServeGRPCMetricsPort in GRPC mode. The Dynamo operator's Service exposes 8000
	// and forwards to it. The chart ships 8000.
	ContainerPort *int32 `json:"containerPort,omitempty"`

	// EndpointCertificateAuthority is a PEM CA bundle. When set, nginx requires
	// clients to present a certificate it signed.
	EndpointCertificateAuthority *string `json:"endpointCertificateAuthority,omitempty"`

	// EndpointBearerToken, when set, makes nginx reject requests whose Authorization
	// header is not "Bearer <token>".
	EndpointBearerToken *string `json:"endpointBearerToken,omitempty"`

	// Model is the Hugging Face model identifier passed to dynamo.vllm, for example
	// Qwen/Qwen3-0.6B. Required.
	Model string `json:"model"`

	// ServedModelName overrides the model name the OpenAI API reports.
	ServedModelName *string `json:"servedModelName,omitempty"`

	// HFToken is stored in a Secret and exposed to the Frontend and workers as
	// HF_TOKEN; gated models need it. SecretEnvVars may not also set HF_TOKEN.
	HFToken *string `json:"hfToken,omitempty"`

	// EnvVars are set in the Frontend and every worker, except the variables the
	// chart owns (the DYN_* plane and router settings and PYTHONHASHSEED), which are
	// dropped. DYN_ROUTER_MODE is the one exception: it overrides the Frontend's
	// default of kv.
	EnvVars map[string]string `json:"envVars,omitempty"`

	// SecretEnvVars set variables in the Frontend and every worker from keys of Secrets
	// in the release namespace, after EnvVars. The chart never creates or reads the
	// Secrets. A container does not start until each Secret has its key, and keeps the
	// values it started with. A variable may not also be in EnvVars, or be one the
	// chart sets itself. DYN_ROUTER_MODE overrides the Frontend's default of kv, as in
	// EnvVars.
	SecretEnvVars map[string]deployments.SecretKeyRef `json:"secretEnvVars,omitempty"`

	// ExtraArgv are literal arguments appended to python3 -m dynamo.vllm on every
	// worker, never through a shell. The chart owns --runner (and, aggregated,
	// --tensor-parallel-size), so a --runner option here is rejected.
	ExtraArgv []string `json:"extraArgv,omitempty"`

	// HPA bounds the KEDA ScaledObject on the aggregated worker pool; disaggregated
	// pools ignore it. hpa.minReplicas must be at least 1. The chart ships one fixed
	// worker.
	HPA *deployments.HPA `json:"hpa,omitempty"`

	// ServingMode is aggregated or disaggregated. The chart ships aggregated.
	//nolint:lll // A struct tag cannot wrap.
	ServingMode *ServingMode `json:"servingMode,omitempty" jsonschema:"title=ServingMode,enum=aggregated,enum=disaggregated"`

	// WorkerPool is the aggregated mode's single pool. Required in aggregated mode;
	// not allowed otherwise.
	WorkerPool *WorkerPool `json:"workerPool,omitempty"`

	// PrefillPool and DecodePool are the disaggregated mode's fixed pools. Both are
	// required in disaggregated mode; neither is allowed otherwise.
	PrefillPool *StaticWorkerPool `json:"prefillPool,omitempty"`
	DecodePool  *StaticWorkerPool `json:"decodePool,omitempty"`

	// KVTransfer selects how the disaggregated pools move the KV cache. Only allowed
	// in disaggregated mode. Unset leaves the NIXL backend to the runtime's default.
	KVTransfer *KVTransfer `json:"kvTransfer,omitempty"`

	// VolumeMounts are existing claims mounted at the same paths into the Frontend
	// and every worker.
	VolumeMounts []deployments.VolumeMount `json:"volumeMounts,omitempty"`

	// BackendProtocol is the Frontend's protocol. HTTP keeps the OpenAI HTTP
	// frontend; GRPC starts Dynamo's KServe gRPC server and moves metrics to
	// KServeGRPCMetricsPort. The chart ships HTTP.
	//nolint:lll // A struct tag cannot wrap.
	BackendProtocol *deployments.BackendProtocol `json:"backendProtocol,omitempty" jsonschema:"title=BackendProtocol,enum=HTTP,enum=GRPC"`

	// RolloutStrategy is the surge and unavailable pair the Dynamo operator rolls the
	// workers with. The chart ships one-at-a-time.
	RolloutStrategy *deployments.RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// RevisionNumber is this deployment's revision. It labels the pods, so bumping
	// it rolls them. The chart ships 1.
	RevisionNumber *int64 `json:"revisionNumber,omitempty"`

	// EnableNodeModelCache mounts the namespace's node-model-cache-pvc into every
	// worker and points HF_HUB_CACHE at it, so one download per node serves every
	// worker on it. The claim comes from catalogs/namespace_base.
	EnableNodeModelCache *bool `json:"enableNodeModelCache,omitempty"`

	// EnableLogging ships container logs to the cluster's log pipeline. The chart
	// ships true; false marks the pods platform.centml.ai/log-exclude.
	EnableLogging *bool `json:"enableLogging,omitempty"`

	// SchedulerName is the pods' scheduler. The chart ships kai-scheduler; an
	// explicit "" falls back to the cluster's default scheduler.
	SchedulerName *string `json:"schedulerName,omitempty"`

	// KaiQueue labels the pods kai.scheduler/queue with one of the leaf queues
	// catalogs/resources/kai_gpu_queues creates.
	KaiQueue *string `json:"kaiQueue,omitempty"`

	// PriorityClassName is the pods' PriorityClass.
	PriorityClassName *string `json:"priorityClassName,omitempty"`

	// UserAnnotations are copied onto every pod as both labels and annotations, each
	// key prefixed ccluster.nvidia.com/user-.
	UserAnnotations map[string]string `json:"userAnnotations,omitempty"`

	// PodLabels are added verbatim to the labels of the Frontend and every worker pod.
	// A key the chart sets itself keeps the chart's value. They carry the caller's own
	// metadata, such as the keys its log pipeline or usage metering reads, and must
	// never carry tenant input: that belongs in UserAnnotations.
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// PodAnnotations are added verbatim to the annotations of the Frontend and every
	// worker pod, on the same terms as PodLabels.
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
}

// WorkerPool is the resources and placement of one set of vLLM workers.
type WorkerPool struct {
	// Resources are each worker's limits and requests. Required, with at least one
	// accelerator: the aggregated pool's requested accelerator count is its tensor
	// parallel size.
	Resources deployments.ResourceRequirements `json:"resources"`

	// NodeAffinityLabels pins the workers to nodes carrying every one of these
	// labels, as a required node affinity.
	NodeAffinityLabels map[string]string `json:"nodeAffinityLabels,omitempty"`

	// DevShmSize is the size of each worker's /dev/shm tmpfs. The chart ships 512Mi.
	DevShmSize *string `json:"devShmSize,omitempty"`
}

// Validate reports missing quantities and a pool without accelerators.
func (p *WorkerPool) validate(name string) error {
	if p == nil {
		return nil
	}
	errs := []error{p.Resources.Validate()}
	for _, side := range []struct {
		name  string
		count *int32
	}{
		{"limits", p.Resources.Limits.AcceleratorCount},
		{"requests", p.Resources.Requests.AcceleratorCount},
	} {
		if side.count == nil || *side.count < 1 {
			errs = append(errs, fmt.Errorf("%w: %s.resources.%s.acceleratorCount must be at least 1",
				deployments.ErrInvalidValue, name, side.name))
		}
	}
	return errors.Join(errs...)
}

// StaticWorkerPool is a disaggregated prefill or decode pool: a fixed replica count
// and an explicit tensor parallel size.
type StaticWorkerPool struct {
	WorkerPool

	// Replicas is the fixed number of workers. The chart ships 1.
	Replicas *int32 `json:"replicas,omitempty"`

	// TensorParallelSize is vLLM's --tensor-parallel-size, which must equal the
	// pool's requested and limited accelerator count. Required.
	TensorParallelSize int32 `json:"tensorParallelSize"`
}

func (p *StaticWorkerPool) validate(name string) error {
	if p == nil {
		return nil
	}
	errs := []error{p.WorkerPool.validate(name)}
	if p.Replicas != nil && *p.Replicas < 1 {
		errs = append(errs, fmt.Errorf("%w: %s.replicas %d is below 1",
			deployments.ErrInvalidValue, name, *p.Replicas))
	}
	if p.TensorParallelSize < 1 {
		errs = append(errs, fmt.Errorf("%w: %s.tensorParallelSize must be at least 1",
			deployments.ErrInvalidValue, name))
	}
	for _, count := range []*int32{p.Resources.Requests.AcceleratorCount, p.Resources.Limits.AcceleratorCount} {
		if count != nil && *count != p.TensorParallelSize {
			errs = append(errs, fmt.Errorf("%w: %s.tensorParallelSize %d must match its accelerator counts",
				deployments.ErrInvalidValue, name, p.TensorParallelSize))
			break
		}
	}
	return errors.Join(errs...)
}

// KVTransferBackend is the NIXL backend the disaggregated pools move the KV cache over.
// Fields of this type carry the enum and a title in their jsonschema tag; see
// deployments.BackendProtocol for why.
type KVTransferBackend string

const (
	// UCX is NIXL's UCX backend.
	UCX KVTransferBackend = "UCX"

	// Libfabric is NIXL's libfabric backend. The chart keeps the KV buffer in GPU
	// memory for it, and it requires EFA.
	Libfabric KVTransferBackend = "LIBFABRIC"
)

// KVTransfer is the NIXL configuration shared by the prefill and decode pools.
type KVTransfer struct {
	// Backend is UCX or LIBFABRIC. Required.
	//nolint:lll // A struct tag cannot wrap.
	Backend KVTransferBackend `json:"backend" jsonschema:"title=KVTransferBackend,enum=UCX,enum=LIBFABRIC"`

	// EFA gives every worker an Elastic Fabric Adapter. Required with LIBFABRIC; not
	// allowed with UCX.
	EFA *EFA `json:"efa,omitempty"`
}

// EFADeviceClassName is the DeviceClass the DRANet driver publishes EFA devices under.
const EFADeviceClassName = "efa.networking.k8s.aws"

// EFA gives every disaggregated worker one exclusive EFA device, allocated by DRA. The
// chart renders a ResourceClaimTemplate per revision and claims it in both pools, sets
// FI_PROVIDER=efa with device RDMA on and shared-memory transfer off, and labels the
// workers ccluster.nvidia.com/kv-transfer: efa. A required pod affinity on that label
// keeps prefill and decode in one zone.
//
// The infra plane must serve resource.k8s.io/v1 (Kubernetes 1.34 or later) and run the
// DRANet driver with the efa.networking.k8s.aws DeviceClass, and ImageURL must be an
// EFA-capable Dynamo runtime.
type EFA struct {
	// DeviceClassName is the DeviceClass each worker's claim requests. The chart ships
	// efa.networking.k8s.aws, the only class it supports.
	DeviceClassName *string `json:"deviceClassName,omitempty"`

	// DevicesPerWorker is how many devices each worker claims. The chart ships 1, the
	// only count it supports.
	DevicesPerWorker *int32 `json:"devicesPerWorker,omitempty"`
}

func (e *EFA) validate() error {
	if e == nil {
		return nil
	}
	var errs []error
	if e.DeviceClassName != nil && *e.DeviceClassName != EFADeviceClassName {
		errs = append(errs, fmt.Errorf("%w: kvTransfer.efa.deviceClassName %q is not %s",
			deployments.ErrInvalidValue, *e.DeviceClassName, EFADeviceClassName))
	}
	if e.DevicesPerWorker != nil && *e.DevicesPerWorker != 1 {
		errs = append(errs, fmt.Errorf("%w: kvTransfer.efa.devicesPerWorker %d is not 1",
			deployments.ErrInvalidValue, *e.DevicesPerWorker))
	}
	return errors.Join(errs...)
}

// New returns the dynamo_deployment chart entry, installed into namespace with values.
// The chart's objects are all created in that namespace, and its Ingress host is
// derived from it.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// chartEnv are the variables templates/dgd.yaml and templates/_helpers.tpl can set in
// the Frontend or a worker. DYN_ROUTER_MODE is not one: it is the caller's router knob,
// and the Frontend takes it from secretEnvVars or envVars.
var chartEnv = []string{
	"DYN_EVENT_PLANE",
	"DYN_REQUEST_PLANE",
	"PYTHONHASHSEED",
	"DYN_ROUTER_QUEUE_THRESHOLD",
	"DYN_DISABLE_FRONTEND_ADMIN_API",
	"DYN_DISABLE_FRONTEND_NVEXT",
	"DYN_VLLM_ENABLE_INFERENCE_V1_GENERATE",
	"DYN_SELF_HOST_METADATA",
	"FI_PROVIDER",
	"FI_EFA_USE_DEVICE_RDMA",
	"FI_EFA_ENABLE_SHM_TRANSFER",
	"HF_HUB_CACHE",
}

// Validate implements catalogs.Validator: the required values that are unset, the
// pools the serving mode needs, and the knobs the Frontend cannot start with.
func (v Values) Validate() error {
	errs := []error{
		deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "name", Value: v.Name},
			deployments.Field{Name: "deploymentId", Value: v.DeploymentID},
			deployments.Field{Name: "imageUrl", Value: v.ImageURL},
			deployments.Field{Name: "domain", Value: v.Domain},
			deployments.Field{Name: "model", Value: strings.TrimSpace(v.Model)},
		)),
		v.ImagePullSecret.Validate(),
		deployments.ValidateImagePullSecretNames(v.ImagePullSecretNames),
		deployments.ValidateSecretEnvVars(v.SecretEnvVars, v.EnvVars),
		deployments.ValidateChartEnv(v.SecretEnvVars, chartEnv),
		v.HPA.Validate(),
		v.RolloutStrategy.Validate(),
		v.BackendProtocol.Validate(),
		v.validateServingMode(),
		v.validateKVTransfer(),
		v.validateExtraArgv(),
		deployments.ValidateVolumeMounts(v.VolumeMounts),
	}

	if minReplicas, _ := v.HPA.Bounds(); minReplicas < 1 {
		errs = append(errs, fmt.Errorf("%w: hpa.minReplicas must be at least 1; Dynamo does not scale to zero",
			deployments.ErrInvalidValue))
	}

	port := DefaultContainerPort
	if v.ContainerPort != nil {
		port = *v.ContainerPort
	}
	switch {
	case port < 1024 || port > 65535:
		errs = append(errs, fmt.Errorf("%w: containerPort %d is outside 1024-65535",
			deployments.ErrInvalidValue, port))
	case v.BackendProtocol != nil && *v.BackendProtocol == deployments.GRPC && port == KServeGRPCMetricsPort:
		errs = append(errs, fmt.Errorf("%w: containerPort %d is the KServe gRPC metrics port",
			deployments.ErrInvalidValue, port))
	}

	if v.RevisionNumber != nil && *v.RevisionNumber < 0 {
		errs = append(errs, fmt.Errorf("%w: revisionNumber %d is negative",
			deployments.ErrInvalidValue, *v.RevisionNumber))
	}
	if _, ok := v.SecretEnvVars["HF_TOKEN"]; ok && v.HFToken != nil && *v.HFToken != "" {
		errs = append(errs, fmt.Errorf("%w: secretEnvVars.HF_TOKEN is also set by hfToken",
			deployments.ErrInvalidValue))
	}
	return errors.Join(errs...)
}

func (v Values) validateServingMode() error {
	mode := Aggregated
	if v.ServingMode != nil {
		mode = *v.ServingMode
	}
	switch mode {
	case Aggregated:
		var errs []error
		if v.WorkerPool == nil {
			errs = append(errs, fmt.Errorf("%w: workerPool; aggregated serving needs one",
				deployments.ErrMissingValue))
		}
		if v.PrefillPool != nil || v.DecodePool != nil {
			errs = append(errs, fmt.Errorf("%w: prefillPool and decodePool are only for disaggregated serving",
				deployments.ErrInvalidValue))
		}
		return errors.Join(append(errs, v.WorkerPool.validate("workerPool"))...)
	case Disaggregated:
		var errs []error
		if v.WorkerPool != nil {
			errs = append(errs, fmt.Errorf("%w: workerPool is only for aggregated serving",
				deployments.ErrInvalidValue))
		}
		var missing []string
		if v.PrefillPool == nil {
			missing = append(missing, "prefillPool")
		}
		if v.DecodePool == nil {
			missing = append(missing, "decodePool")
		}
		errs = append(errs, deployments.MissingError(missing),
			v.PrefillPool.validate("prefillPool"), v.DecodePool.validate("decodePool"))
		return errors.Join(errs...)
	default:
		return fmt.Errorf("%w: servingMode %q is not aggregated or disaggregated",
			deployments.ErrInvalidValue, mode)
	}
}

// validateKVTransfer rejects kvTransfer outside disaggregated serving, an unknown
// backend, and EFA settings that do not match the backend.
func (v Values) validateKVTransfer() error {
	kv := v.KVTransfer
	if kv == nil {
		return nil
	}
	var errs []error
	if v.ServingMode == nil || *v.ServingMode != Disaggregated {
		errs = append(errs, fmt.Errorf("%w: kvTransfer is only for disaggregated serving",
			deployments.ErrInvalidValue))
	}
	switch kv.Backend {
	case UCX:
		if kv.EFA != nil {
			errs = append(errs, fmt.Errorf("%w: kvTransfer.efa is not allowed with UCX",
				deployments.ErrInvalidValue))
		}
	case Libfabric:
		if kv.EFA == nil {
			errs = append(errs, fmt.Errorf("%w: kvTransfer.efa; LIBFABRIC needs it",
				deployments.ErrMissingValue))
		}
	default:
		errs = append(errs, fmt.Errorf("%w: kvTransfer.backend %q is not UCX or LIBFABRIC",
			deployments.ErrInvalidValue, kv.Backend))
	}
	return errors.Join(append(errs, kv.EFA.validate())...)
}

// validateExtraArgv rejects a --runner option, in any spelling vLLM's parser accepts
// (an unambiguous prefix of --runner, with - or _), since the chart appends
// "--runner generate" itself. Tokens after a "--" separator are positional and pass.
func (v Values) validateExtraArgv() error {
	for _, token := range v.ExtraArgv {
		if token == "--" {
			return nil
		}
		head, _, _ := strings.Cut(token, "=")
		head = strings.ReplaceAll(head, "_", "-")
		if strings.HasPrefix(head, "--") && len(head) >= len("--ru") && strings.HasPrefix("--runner", head) {
			return fmt.Errorf("%w: extraArgv %q; the chart sets --runner generate",
				deployments.ErrInvalidValue, token)
		}
	}
	return nil
}
