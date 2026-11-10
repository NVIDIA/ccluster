// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { ApiError } from "../api";

interface ErrorPanelProps {
  error: unknown;
  /** The namespace the page asked for, or "" for all. */
  namespace: string;
  /** The only namespace the server serves, if any. */
  servedNamespace?: string;
  onNamespace: (namespace: string) => void;
}

/** Explains why the page cannot show anything, and what to do about it. */
export function ErrorPanel({ error, namespace, servedNamespace, onNamespace }: ErrorPanelProps) {
  const apiError = error instanceof ApiError ? error : null;
  const status = apiError?.status ?? 0;
  const reason = apiError?.reason ?? "";
  const message = error instanceof Error ? error.message : String(error);

  let title: string;
  let hint: ReactNode = null;
  if (reason === "ClusterReadForbidden") {
    title = "Cannot read all namespaces";
    hint = (
      <p>
        Your credentials cannot list infra planes and component sets across the cluster. Restart with{" "}
        <code>ccluster server -n &lt;namespace&gt;</code> to show one namespace you can read, or ask for
        cluster-wide read access.
      </p>
    );
  } else if (reason === "NamespaceNotServed" && servedNamespace) {
    title = `This server shows only namespace ${servedNamespace}`;
    hint = (
      <p>
        <button type="button" className="link" onClick={() => onNamespace("")}>
          Show {servedNamespace}
        </button>
      </p>
    );
  } else if (status === 403) {
    title = namespace ? `Cannot read namespace ${namespace}` : "Access denied";
    hint = <p>Kubernetes RBAC does not let your credentials read infra planes and component sets here.</p>;
  } else if (status === 401) {
    title = "Kubernetes did not accept your credentials";
    hint = <p>Log in to the control plane&apos;s kubeconfig context again, then reload.</p>;
  } else if (reason === "ServerUnreachable") {
    title = "ccluster server is not reachable";
    hint = <p>Start it again with <code>ccluster server</code>.</p>;
  } else if (status === 502) {
    title = "Kubernetes could not be reached";
  } else {
    title = "Something went wrong";
  }

  return (
    <section className="panel error-panel" role="alert">
      <h2>{title}</h2>
      {hint}
      <pre className="error-detail">
        {status ? `${status} ` : ""}
        {message}
      </pre>
      <p className="muted">This page keeps trying every few seconds.</p>
    </section>
  );
}
