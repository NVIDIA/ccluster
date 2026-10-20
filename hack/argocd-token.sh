#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Generate an API token for the cp operator's Argo CD account and store it in the Secret
# the operator reads it from, replacing any token already there. Run by
# `make argocd-token-cp`, which sets the variables below from the deploy-cp ones.
#
# The token is generated inside the argocd-server Pod, with the argocd CLI its image
# ships, logged in as admin with the password from argocd-initial-admin-secret. Neither
# the password nor the token appears on a command line: both travel over stdin.
#
#   KUBECTL           kubectl binary
#   KUBE_CONTEXT      kubeconfig context; empty for the current one
#   NAMESPACE         namespace Argo CD runs in, where the Secret is written
#   SERVER_DEPLOYMENT argocd-server's Deployment
#   ACCOUNT           Argo CD account to generate the token for
#   TOKEN_SECRET      Secret to write; the token goes under the key "token"
set -euo pipefail

: "${KUBECTL:=kubectl}" "${NAMESPACE:?}" "${SERVER_DEPLOYMENT:?}" "${ACCOUNT:?}" "${TOKEN_SECRET:?}"

kubectl() {
  "$KUBECTL" ${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"} --namespace "$NAMESPACE" "$@"
}

if ! password="$(kubectl get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' 2>/dev/null)" \
   || [ -z "$password" ]; then
  echo "argocd-initial-admin-secret not found in namespace $NAMESPACE: generate a token for" >&2
  echo "account $ACCOUNT with the argocd CLI and store it under the key \"token\" of Secret $TOKEN_SECRET." >&2
  exit 1
fi

# The CLI keeps its session in a config file; /tmp is the server Pod's writable volume.
# argocd-server serves TLS on 8080 with a self-signed certificate unless configured
# otherwise, hence --insecure, which only skips verifying it over the Pod's loopback.
# shellcheck disable=SC2016 # Expanded by the Pod's shell, not this one.
token="$(base64 --decode <<<"$password" | kubectl exec -i "deploy/$SERVER_DEPLOYMENT" -- sh -c '
  set -e
  config="$(mktemp)"
  trap '\''rm -f "$config"'\'' EXIT
  read -r password
  argocd login localhost:8080 --insecure --username admin --password "$password" \
    --config "$config" >/dev/null
  argocd account generate-token --account "$1" --config "$config"
' sh "$ACCOUNT")"

printf '%s' "$token" | kubectl create secret generic "$TOKEN_SECRET" --from-file=token=/dev/stdin \
  --dry-run=client --output yaml | kubectl apply --filename -
echo "Stored a token for Argo CD account $ACCOUNT in Secret $NAMESPACE/$TOKEN_SECRET"
