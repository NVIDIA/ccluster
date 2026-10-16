#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

dest="${1:?usage: install-prometheus-test-tools.sh DEST}"
version=3.4.1
case "$(uname -sm)" in
  'Darwin arm64') platform=darwin-arm64; sha=7a6372e2b6a1a498a6aa4e9936878ff8b42c9ed818305b915133600182e2051e ;;
  'Darwin x86_64') platform=darwin-amd64; sha=07b0442f50aed883afbcc5ca3136ce45bef99aa73b7aa2e8fa4247b26f2d523f ;;
  'Linux aarch64') platform=linux-arm64; sha=2a85be1dff46238c0d799674e856c8629c8526168dd26c3de2cecfbfc6f9a0a2 ;;
  'Linux x86_64') platform=linux-amd64; sha=09203151c132f36b004615de1a3dea22117ad17e6d7a59962e34f3abf328f312 ;;
  *) echo 'Unsupported Prometheus test platform' >&2; exit 1 ;;
esac
if [ -x "$dest/prometheus" ] && [ -x "$dest/promtool" ]; then exit 0; fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="prometheus-$version.$platform"
curl -fsSL "https://github.com/prometheus/prometheus/releases/download/v$version/$archive.tar.gz" -o "$tmp/archive.tar.gz"
printf '%s  %s\n' "$sha" "$tmp/archive.tar.gz" | shasum -a 256 -c -
mkdir -p "$dest"
tar -xzf "$tmp/archive.tar.gz" -C "$dest" --strip-components=1 "$archive/prometheus" "$archive/promtool"
