#!/usr/bin/env python3
# Copyright 2026 NVIDIA CORPORATION & AFFILIATES.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Write the __init__.py of one generated API version package of ccluster-py.

Given the kinds of a version (as the Makefile reads them from the CRDs), it declares the
Kind enum and one concrete CustomResource[<Kind>Spec, <Kind>Status] per kind, importing
the template from .base and each kind's Spec and Status from its generated module. It is
the one piece of Python shaped by hand in the pipeline, kept to a template so that adding
a CRD needs no edit anywhere: see python-generate in the Makefile.

    crd-python-init.py --version v1alpha1 --out python/src/ccluster/v1alpha1/__init__.py \\
        --header hack/boilerplate.py.txt CCLComponentSet CCLInfraPlane
"""

import argparse
import re
from pathlib import Path

GROUP = "ccluster.nvidia.com"


def enum_member(kind: str) -> str:
    """CamelCase kind to UPPER_SNAKE: CCLComponentSet -> CCL_COMPONENT_SET."""
    snake = re.sub(r"([A-Z]+)([A-Z][a-z])", r"\1_\2", kind)
    snake = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", snake)
    return snake.upper()


def render(version: str, kinds: list[str], header: str) -> str:
    kinds = sorted(kinds)
    lines = [header.rstrip("\n"), ""]
    lines += [
        f'"""The {GROUP}/{version} kinds: one concrete CustomResource per CRD, and the Kind',
        "enum naming them. The template is in .base; each kind's Spec and Status are in its",
        'own module."""',
        "",
        "from enum import Enum",
        "from typing import Literal",
        "",
        "from .base import API_VERSION, CustomResource, ObjectMeta",
    ]
    for kind in kinds:
        lines.append(f"from .{kind.lower()} import {kind}Spec, {kind}Status")
    exports = ["API_VERSION", *kinds, "CustomResource", "Kind", "ObjectMeta"]
    lines += ["", "__all__ = [", *[f'    "{name}",' for name in exports], "]", "", ""]
    lines += ["class Kind(str, Enum):", f'    """The kinds of {GROUP}/{version}, as they appear on the wire."""', ""]
    lines += [f'    {enum_member(kind)} = "{kind}"' for kind in kinds]
    for kind in kinds:
        lines += [
            "",
            "",
            f"class {kind}(CustomResource[{kind}Spec, {kind}Status]):",
            f'    """A {kind} object; see {kind}Spec and {kind}Status for its contents."""',
            "",
            f"    kind: Literal[Kind.{enum_member(kind)}] = Kind.{enum_member(kind)}",
        ]
    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--version", required=True, help="API version, e.g. v1alpha1")
    parser.add_argument("--out", required=True, type=Path, help="__init__.py to write")
    parser.add_argument("--header", required=True, type=Path, help="file header for generated Python")
    parser.add_argument("kinds", nargs="+", help="the version's kinds")
    args = parser.parse_args()
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(render(args.version, args.kinds, args.header.read_text()))


if __name__ == "__main__":
    main()
