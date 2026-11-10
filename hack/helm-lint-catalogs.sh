#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Lint and render every chart under catalogs/. A chart renders once per ci/*-values.yaml
# file it carries, or once with its own defaults when it has none; a chart whose required
# values have no default needs such a file. `helm lint` alone is not enough: in lint mode
# `required` and `fail` only warn, so a missing required value surfaces in `helm template`.
#
# Charts with dependencies get them through `helm dependency build`, which needs network.
# The work happens on a copy of catalogs/, because building dependencies writes charts/
# and, for a chart without Chart.lock, a new Chart.lock. Repositories are registered in a
# throwaway Helm repository config, so the developer's own repo list is left alone.
#
# Usage: HELM=helm YQ=yq hack/helm-lint-catalogs.sh (make helm-lint-catalogs passes the
# yq it pins in bin/)
set -euo pipefail

cd "$(dirname "$0")/.."

HELM=${HELM:-helm}
YQ=${YQ:-yq}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R catalogs "$work/"
export HELM_REPOSITORY_CONFIG="$work/repositories.yaml"
export HELM_REPOSITORY_CACHE="$work/repository-cache"

# Remote chart repositories flake; retry before failing the run. Both repo add and
# dependency build download from them.
retry() {
  local attempt
  for attempt in 1 2 3; do
    "$@" >/dev/null && return 0
    echo "$* failed (attempt $attempt)" >&2
    sleep $((attempt * 5))
  done
  return 1
}

# `helm dependency build` resolves a locked dependency through the local repo list, so
# every repository a catalog chart names is registered first. Matching is by URL. OCI
# registries are not chart repositories: helm pulls from them directly, and repo add
# refuses them.
n=0
while IFS= read -r url; do
  n=$((n + 1))
  retry "$HELM" repo add "catalog-$n" "$url"
done < <(find catalogs -name Chart.yaml -exec "$YQ" --no-doc '.dependencies[]?.repository' {} + | grep -v '^oci://' | sort -u)

failed=()
while IFS= read -r chart; do
  dir="$work/$chart"
  if [ "$("$YQ" '.dependencies | length' "$dir/Chart.yaml")" -gt 0 ] && ! retry "$HELM" dependency build "$dir"; then
    failed+=("$chart (dependency build)")
    continue
  fi
  values=("$dir"/ci/*-values.yaml)
  [ -e "${values[0]}" ] || values=("")
  for v in "${values[@]}"; do
    label=$chart
    args=(--namespace ccluster-ci)
    if [ -n "$v" ]; then
      label="$chart with ci/$(basename "$v")"
      args+=(--values "$v")
    fi
    echo "==> $label"
    if ! "$HELM" lint "$dir" "${args[@]}" >"$work/lint.log" 2>&1; then
      cat "$work/lint.log"
      failed+=("$label (lint)")
    fi
    if ! "$HELM" template ci "$dir" "${args[@]}" >/dev/null; then
      failed+=("$label (template)")
    fi
  done
done < <(find catalogs -name Chart.yaml -exec dirname {} \; | sort)

if [ "${#failed[@]}" -ne 0 ]; then
  printf 'failed: %s\n' "${failed[@]}" >&2
  exit 1
fi
echo "All catalog charts lint and render."
