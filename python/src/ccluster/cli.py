# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The ccluster command line.

main is the click group installed as the `ccluster` executable; run without a
subcommand, it prints its usage. register is a placeholder that does nothing yet.
"""

from __future__ import annotations

import click


@click.group(
    context_settings={"help_option_names": ["-h", "--help"]},
    invoke_without_command=True,
)
@click.version_option(package_name="ccluster-py")
@click.pass_context
def main(ctx: click.Context) -> None:
    """Manage ccluster infra planes and component sets."""
    if ctx.invoked_subcommand is None:
        click.echo(ctx.get_help())


@main.command()
def register() -> None:
    """Register a cluster with the control plane."""
