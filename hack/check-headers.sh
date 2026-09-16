#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fail if a tracked source file lacks the SPDX license header. Hand-written Go, Python,
# shell, YAML, TOML, Helm template, Makefile and Dockerfile sources must start with
#
#   SPDX-FileCopyrightText: Copyright (c) <year> NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#   SPDX-License-Identifier: Apache-2.0
#
# in the comment syntax of the file. Generated files whose generator does not take a
# header (CRDs, cp RBAC, operator chart templates) and third-party test fixtures are
# excluded below; generated Go and Python carry the header via hack/boilerplate.*.txt.
set -euo pipefail

cd "$(dirname "$0")/.."

exclude='(zz_generated\.|^config/crd/bases/|^config/cp/rbac/role\.yaml$|^charts/(cp|ip)/chart/(templates|crds)/|^test/testdata/crd/|^hack/boilerplate\.)'

fail=0
while IFS= read -r f; do
  [ -f "$f" ] && [ ! -L "$f" ] || continue
  case "$f" in
    *.go|*.py|*.sh|*.yaml|*.yml|*.tpl|*.toml|Makefile|*/Makefile|Dockerfile|*/Dockerfile) ;;
    *) continue ;;
  esac
  if [[ "$f" =~ $exclude ]]; then continue; fi
  if ! head -n 10 "$f" | grep -q 'SPDX-License-Identifier: Apache-2.0' \
     || ! head -n 10 "$f" | grep -q 'SPDX-FileCopyrightText: Copyright (c) [0-9]* NVIDIA CORPORATION & AFFILIATES\. All rights reserved\.'; then
    echo "missing SPDX header: $f"
    fail=1
  fi
done < <(git ls-files)

if [ "$fail" -ne 0 ]; then
  echo "Add the two SPDX lines from hack/boilerplate.py.txt in the file's comment syntax." >&2
  exit 1
fi
echo "All tracked source files carry the SPDX header."
