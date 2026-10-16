# Dynamo metrics parity

The reference is CentML/platform at
`32a382723374a768c43ce4581afe8fe9b1044207`. The fixtures in
`test/catalogs/fixtures` record the rendered Service/ServiceMonitor contract and
the complete `DYNAMO_METRICS` source inventory at that revision. Updating the
reference requires reviewing both the chart and `api/src/services/dynamo_usage.py`.

Run `make test-catalogs`. The Helm Charts workflow runs this on every PR. It
downloads SHA-256-verified Prometheus 3.4.1 tools into `bin/`, renders locally and
starts loopback-only processes with temporary storage. It needs no cluster or GPU;
missing tools are failures, not skipped tests. With a reference checkout available,
set `PLATFORM_DYNAMO_CHART=/absolute/path/to/platform/catalog/src/catalog/dynamo_deployment`
to compare both charts directly across the same eight configurations.

## Automated evidence

- Aggregated/disaggregated, HTTP/GRPC and default/custom ports and paths have the
  same Service selectors, monitor selectors, endpoints and relabeling rules.
  Tests check those selectors against rendered Dynamo Pod labels as well.
- A real Prometheus scrapes synthetic exporters through the rendered target and
  metric relabeling rules. Reference and ccluster results must match, including
  each Pod's revision during overlapping revisions and `up=0` on scrape failure.
  Negative deployment, namespace and revision selectors return no data.
- All 13 native Usage metric sources, plus the KEDA active-request gauge, are
  covered. Model/engine/worker labels, histogram buckets and counts, and the
  request route's `exported_endpoint` survive scraping. Worker metrics use the
  operator's port 9090 by default; frontend metrics use the HTTP listener or
  the separate GRPC metrics listener.

| Usage metric | Source | Roles |
| --- | --- | --- |
| TTFT | `dynamo_frontend_time_to_first_token_seconds` | frontend |
| Request latency | `dynamo_frontend_request_duration_seconds` | frontend |
| Output throughput | `dynamo_frontend_output_tokens_total` | frontend |
| Request outcomes | `dynamo_frontend_requests_total` | frontend |
| TPOT | `vllm:request_time_per_output_token_seconds` | worker, decode |
| Queue depth / running requests / KV cache | `vllm:num_requests_waiting`, `vllm:num_requests_running`, `vllm:kv_cache_usage_perc` | all workers |
| KV throughput / latency / transfers | `vllm:nixl_bytes_transferred_sum`, `vllm:nixl_xfer_time_seconds`, `vllm:nixl_xfer_time_seconds_count` | decode |
| KV failures | `vllm:nixl_num_failed_transfers_total` | all workers |
| Target health | `up` | all roles |
| CPU / memory | cAdvisor plus KSM resource limits and Pod labels | all workers |
| GPU / GPU memory | DCGM plus KSM Pod labels | all workers |

The last four resource metrics are recorded separately in the inventory. They
do not come from Dynamo's metrics listeners. Their end-to-end attribution also
requires KSM to expose deployment, revision and component Pod labels.

## AWS EFA acceptance still required

These tests prove catalog and scrape-label compatibility, not complete runtime
parity. Kubernetes discovery is represented by static target metadata; they do
not run Prometheus Operator, Dynamo, vLLM, KEDA, DCGM or the Platform Usage API.
They do not prove histogram/rate aggregation or Usage availability classification.

Before declaring end-to-end parity, run the following on an approved AWS EFA
deployment with the same runtime image and model on both catalog paths. Record
the catalog SHA, runtime digest, Prometheus configuration and time window.

1. Inspect actual ServiceMonitor-selected targets for every role, port and path.
   Confirm exactly one scrape per intended target, cross-deployment isolation,
   and correct Pod/revision/component labels through a rolling update.
2. Compare the inventory with real exporter families, buckets and dimensions.
   Capture idle and known successful/error request traffic, including NIXL
   transfers in disaggregated mode. Missing metrics are failures or documented
   runtime limitations, not zero-valued observations.
3. Query the real Platform Usage API for every applicable metric and role,
   filtered by revision and Pod. Compare label sets exactly and values using
   recorded workload/window tolerances. Check merged histogram percentiles,
   counter rates/resets, deduplication, and decode-only transfer accounting.
4. Exercise healthy idle, missing exporter, failed scrape, stale samples and
   partial availability. Verify `no_traffic`, `unavailable`, `stale` and
   `partial` against the API's probes rather than inferring health from zeros.
5. Confirm cAdvisor, DCGM and KSM resource joins. Verify the remote backend has
   the `cluster` label expected by Usage: local `externalLabels.cluster_name`
   alone does not establish that contract. Remote-write endpoints and cluster
   identity remain installation-specific.

The monitoring changes leave the DynamoGraphDeployment Pod templates unchanged.
Existing frontend monitor names and metric relabeling are retained. Rolling back
removes worker scraping and the new target labels, so Usage availability falls
back to its prior behavior without changing model Pods.

## Loading protection and rollout

Autoscaled aggregated workers have an additional KEDA `Value` trigger. It emits
1 while a non-Ready `VllmDecodeWorker` has been started by the kubelet for less
than 1800 seconds, with no restarts or deletion timestamp. The HPA then proposes
the current replica count; a higher active-request recommendation still wins.
Fixed pools and disaggregated serving do not receive this hold. Missing KSM data
returns 0, so the hold depends on observability being available.

`make test-catalogs` evaluates the rendered query with real promtool against 16
scenarios, including the exact 1800-second boundary, long scheduling delays,
unscheduled Pods, duplicate scrapes, frontend isolation and missing labels. It
also renders the actual pinned kube-prometheus-stack dependency and checks the
resulting KSM container arguments and ServiceMonitor discovery selectors.

The Prometheus catalog exports exactly these Pod labels by default:
`platform.centml.ai/deployment-id`, `platform.centml.ai/revision-number`,
`ccluster.nvidia.com/component-type`, and `nvidia.com/dynamo-component`.
An installation overriding `metricLabelsAllowlist` must retain these labels (or
already export all Pod labels). Apply the cluster-base change first and verify
the resulting `kube_pod_labels` series before enabling the workload hold. This
changes KSM arguments and rolls KSM, not model Pods. A workload-only rollback
can keep the additive KSM labels; rolling KSM back first disables the hold and
resource attribution until the labels return.

The shared Go/Python HPA model and both Helm charts now reject cooldowns outside
0–3600 and Helm rejects non-integer values. Audit persisted ComponentSet values
before upgrading and correct values outside that range. Valid wire values and
defaults remain compatible with old clients/catalogs; Dynamo preserves explicit
0, while inference retains its existing 0-to-1800 behavior. A rollback restores
the older validation without requiring a state migration.

AWS EFA acceptance must additionally demonstrate that replica count holds while
a worker loads, active requests can still scale up, and scale-down resumes after
Ready plus the configured HPA stabilization window (or the bounded loading
grace expires). Confirm the operator's actual `nvidia.com/dynamo-component`
labels and KEDA-generated HPA; query tests alone do not prove either controller.
