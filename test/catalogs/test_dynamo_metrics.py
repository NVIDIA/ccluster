# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import os
import re
import socket
import subprocess
import threading
import time
from copy import deepcopy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import URLError
from urllib.parse import urlencode
from urllib.request import urlopen

import pytest
import yaml
from conftest import FIXTURES, ROOT, render

CHART = ROOT / "catalogs/deployments/dynamo_deployment"
REFERENCE = json.loads((FIXTURES / "dynamo-metrics-contract.json").read_text())


def metrics_contract(documents):
    services = {d["metadata"]["name"]: d for d in documents if d["kind"] == "Service"}
    contract = {}
    for monitor in (d for d in documents if d["kind"] == "ServiceMonitor"):
        service = services[monitor["metadata"]["name"]]
        role = service["spec"]["selector"]["ccluster.nvidia.com/component-type"]
        contract[role] = {
            "namespace": monitor["metadata"]["namespace"],
            "serviceNamespace": service["metadata"]["namespace"],
            "service": service["spec"],
            "monitor": monitor["spec"],
        }
        assert (
            monitor["spec"]["selector"]["matchLabels"].items()
            <= service["metadata"]["labels"].items()
        )
    return contract


def serving_mode(values, mode):
    values["servingMode"] = mode
    if mode == "disaggregated":
        pool = values.pop("workerPool")
        for role in ("prefill", "decode"):
            values[role + "Pool"] = {
                **deepcopy(pool),
                "replicas": 1,
                "tensorParallelSize": 1,
            }


def platform_values(value):
    # Optional direct comparison against the pinned Platform checkout, without a CI
    # dependency on that repository. User environment variable names stay literal.
    if not isinstance(value, dict):
        return value
    return {
        re.sub(r"[A-Z]", lambda m: "_" + m[0].lower(), key): val
        if key == "envVars"
        else platform_values(val)
        for key, val in value.items()
    }


@pytest.mark.parametrize("mode", ["aggregated", "disaggregated"])
@pytest.mark.parametrize("protocol", ["HTTP", "GRPC"])
@pytest.mark.parametrize("custom", [False, True])
def test_metrics_contract(dynamo_values, mode, protocol, custom):
    serving_mode(dynamo_values, mode)
    dynamo_values["backendProtocol"] = protocol
    if custom:
        dynamo_values.update(
            containerPort=8100,
            kserveGrpcMetricsPort=8789,
            envVars={
                "DYN_SYSTEM_PORT": "9091",
                "DYN_HTTP_SVC_METRICS_PATH": "/custom-metrics",
            },
        )
    roles = (
        ["frontend", "worker"]
        if mode == "aggregated"
        else ["frontend", "prefill", "decode"]
    )
    expected = {role: deepcopy(REFERENCE["roles"][role]) for role in roles}
    for role, contract in expected.items():
        port = (
            (8789 if custom else 8788)
            if protocol == "GRPC"
            else (8100 if custom else 8000)
        )
        if role != "frontend":
            port = 9091 if custom else 9090
        contract["service"]["ports"][0].update(port=port, targetPort=port)
        if custom and role == "frontend":
            contract["monitor"]["endpoints"][0]["path"] = "/custom-metrics"
    documents = render(CHART, dynamo_values)
    assert metrics_contract(documents) == expected
    dgd = next(d for d in documents if d["kind"] == "DynamoGraphDeployment")
    for component in dgd["spec"]["components"]:
        labels = component["podTemplate"]["metadata"]["labels"]
        assert labels["platform.centml.ai/revision-number"] == "2"
        role = labels["ccluster.nvidia.com/component-type"]
        assert expected[role]["service"]["selector"].items() <= labels.items()
    if chart := os.environ.get("PLATFORM_DYNAMO_CHART"):
        values = platform_values(dynamo_values)
        values.update(user_id=1340, deployment_id=1732)
        assert metrics_contract(render(chart, values)) == expected


def test_namespace_and_string_deployment_id(dynamo_values):
    dynamo_values["deploymentId"] = "deployment-abc"
    for contract in metrics_contract(render(CHART, dynamo_values, "tenant-b")).values():
        assert contract["namespace"] == contract["serviceNamespace"] == "tenant-b"
        assert (
            contract["service"]["selector"]["platform.centml.ai/deployment-id"]
            == "deployment-abc"
        )
        assert (
            contract["monitor"]["endpoints"][0]["relabelings"][0]["replacement"]
            == "deployment-abc"
        )


def prometheus_relabelings(rules):
    return [
        {
            re.sub(r"[A-Z]", lambda m: "_" + m[0].lower(), key): val
            for key, val in rule.items()
        }
        for rule in rules
    ]


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_for(check):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (URLError, ConnectionError):
            pass
        time.sleep(0.1)
    raise AssertionError("Timed out waiting for Prometheus")


@pytest.mark.parametrize("mode", ["aggregated", "disaggregated"])
def test_real_scrape_labels_and_usage_sources(
    dynamo_values, prometheus_tools, tmp_path, mode
):
    serving_mode(dynamo_values, mode)
    dynamo_values["envVars"] = {"DYN_HTTP_SVC_METRICS_PATH": "/custom-metrics"}
    current = metrics_contract(render(CHART, dynamo_values))
    fixture = json.loads((FIXTURES / "dynamo-usage-sources.json").read_text())
    failed = set()

    class Exporter(BaseHTTPRequestHandler):
        def do_GET(self):
            role = self.path.split("/")[1]
            path = "/" + self.path.split("/", 2)[2]
            expected_path = "/custom-metrics" if role == "frontend" else "/metrics"
            if role in failed or path != expected_path:
                self.send_error(503)
                return
            samples = []
            for metric in fixture["metrics"]:
                if role not in metric["roles"] or metric["source"] == "up":
                    continue
                dims = 'model="model-a",engine="0",worker_id="worker-a"'
                if metric["kind"] == "outcomes":
                    dims += ',endpoint="/v1/chat/completions",status="success",error_type="",request_type="chat"'
                if metric["kind"] == "histogram":
                    samples.extend(
                        f'{metric["source"]}_bucket{{{dims},le="{le}"}} {value}'
                        for le, value in [("0.1", 2), ("1", 4), ("+Inf", 5)]
                    )
                    samples.append(f"{metric['source']}_count{{{dims}}} 5")
                else:
                    samples.append(f"{metric['source']}{{{dims}}} 5")
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; version=0.0.4")
            self.end_headers()
            self.wfile.write(("\n".join(dict.fromkeys(samples)) + "\n").encode())

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Exporter)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    jobs = []
    for pipeline, contracts in [
        ("catalog", current),
        ("reference", REFERENCE["roles"]),
    ]:
        for role in current:
            endpoint = deepcopy(contracts[role]["monitor"]["endpoints"][0])
            if pipeline == "reference" and role == "frontend":
                endpoint["path"] = "/custom-metrics"
            for revision in ("1", "2"):
                jobs.append(
                    {
                        "job_name": f"{pipeline}-{role}-{revision}",
                        "metrics_path": f"/{role}{endpoint['path']}",
                        "static_configs": [
                            {
                                "targets": [f"127.0.0.1:{server.server_port}"],
                                "labels": {
                                    "pipeline": pipeline,
                                    "cluster": "test-cluster",
                                    "namespace": "user-1340",
                                    "pod": f"{role}-r{revision}",
                                    "endpoint": "metrics",
                                    "__meta_kubernetes_pod_label_platform_centml_ai_revision_number": revision,
                                    "__meta_kubernetes_pod_label_ccluster_nvidia_com_component_type": role,
                                },
                            }
                        ],
                        "relabel_configs": prometheus_relabelings(
                            endpoint["relabelings"]
                        ),
                        "metric_relabel_configs": prometheus_relabelings(
                            endpoint.get("metricRelabelings", [])
                        ),
                    }
                )
    config = tmp_path / "prometheus.yaml"
    config.write_text(
        yaml.safe_dump({"global": {"scrape_interval": "200ms"}, "scrape_configs": jobs})
    )
    port = free_port()

    def query(expression):
        with urlopen(
            f"http://127.0.0.1:{port}/api/v1/query?" + urlencode({"query": expression}),
            timeout=2,
        ) as response:
            result = json.load(response)
        assert result["status"] == "success", result
        return result["data"]["result"]

    with (tmp_path / "prometheus.log").open("w+") as log:
        proc = subprocess.Popen(
            [
                str(prometheus_tools / "prometheus"),
                f"--config.file={config}",
                f"--storage.tsdb.path={tmp_path / 'data'}",
                f"--web.listen-address=127.0.0.1:{port}",
            ],
            stdout=log,
            stderr=log,
        )
        try:
            wait_for(lambda: len(query("up == 1")) == len(jobs))
            for metric in fixture["metrics"]:
                for role in set(metric["roles"]) & current.keys():
                    source = metric["source"] + (
                        "_bucket" if metric["kind"] == "histogram" else ""
                    )
                    selector = (
                        f'{source}{{cluster="test-cluster",namespace="user-1340",'
                        f'deployment_id="1732",component="{role}"}}'
                    )
                    results = query(selector)
                    normalized = {}
                    for pipeline in ("catalog", "reference"):
                        rows = [
                            r for r in results if r["metric"]["pipeline"] == pipeline
                        ]
                        assert {r["metric"]["revision_number"] for r in rows} == {
                            "1",
                            "2",
                        }, metric
                        normalized[pipeline] = sorted(
                            [
                                (
                                    json.dumps(
                                        {
                                            k: v
                                            for k, v in r["metric"].items()
                                            if k not in {"pipeline", "job", "instance"}
                                        },
                                        sort_keys=True,
                                    ),
                                    r["value"][1],
                                )
                                for r in rows
                            ]
                        )
                        for row in rows:
                            labels = row["metric"]
                            if source != "up":
                                assert (
                                    labels["model"],
                                    labels["engine"],
                                    labels["worker_id"],
                                ) == ("model-a", "0", "worker-a")
                            if metric["kind"] == "outcomes":
                                assert (
                                    labels["exported_endpoint"]
                                    == "/v1/chat/completions"
                                )
                    assert normalized["catalog"] == normalized["reference"], metric
                    assert query(selector[:-1] + ',revision_number="99"}') == []
                    assert (
                        query(
                            selector.replace(
                                'deployment_id="1732"', 'deployment_id="other"'
                            )
                        )
                        == []
                    )
                    assert (
                        query(
                            selector.replace(
                                'namespace="user-1340"', 'namespace="other"'
                            )
                        )
                        == []
                    )
            failed.add("frontend")
            wait_for(
                lambda: (
                    len(
                        query(
                            'up{component="frontend",deployment_id="1732",revision_number=~"1|2"} == 0'
                        )
                    )
                    == 4
                )
            )
            assert len(query('up{component!="frontend"} == 1')) == len(jobs) - 4
        finally:
            proc.terminate()
            proc.wait(timeout=10)
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
            if proc.returncode not in (0, -15):
                log.seek(0)
                pytest.fail(log.read())
