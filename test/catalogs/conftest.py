# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
import subprocess
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = Path(__file__).parent / "fixtures"


def render(chart, values, namespace="user-1340"):
    result = subprocess.run(
        [
            os.environ.get("HELM", "helm"),
            "template",
            "test-dynamo",
            str(chart),
            "--namespace",
            namespace,
            "--values",
            "-",
        ],
        input=yaml.safe_dump(values),
        capture_output=True,
        text=True,
        check=True,
    )
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


@pytest.fixture
def dynamo_values():
    return yaml.safe_load((FIXTURES / "dynamo-values.yaml").read_text())


@pytest.fixture
def prometheus_tools():
    directory = Path(
        os.environ.get("PROMETHEUS_TEST_TOOLS", ROOT / "bin/prometheus-3.4.1")
    )
    for name in ("prometheus", "promtool"):
        assert (directory / name).is_file(), (
            "Run make test-catalogs to install Prometheus test tools"
        )
    return directory
