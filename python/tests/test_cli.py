# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command prints its usage and version."""

import pytest
from click.testing import CliRunner

from ccluster.cli import main


@pytest.mark.parametrize("args", [[], ["--help"], ["-h"]])
def test_prints_usage(args):
    result = CliRunner().invoke(main, args, prog_name="ccluster")
    assert result.exit_code == 0, result.output
    assert result.output.startswith("Usage: ccluster")


def test_prints_version():
    result = CliRunner().invoke(main, ["--version"], prog_name="ccluster")
    assert result.exit_code == 0, result.output
    assert "version" in result.output
