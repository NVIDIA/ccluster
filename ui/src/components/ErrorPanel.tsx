// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { Button, Stack, StatusMessage, Text } from "@nvidia/foundations-react-core";
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

  let heading: string;
  let hint: ReactNode = null;
  let action: ReactNode = null;
  if (reason === "ClusterReadForbidden") {
    heading = "Cannot read all namespaces";
    hint = (
      <>
        Your credentials cannot list infra planes and component sets across the cluster. Restart with{" "}
        <code>ccluster server -n &lt;namespace&gt;</code> to show one namespace you can read, or ask for
        cluster-wide read access.
      </>
    );
  } else if (reason === "NamespaceNotServed" && servedNamespace) {
    heading = `This server shows only namespace ${servedNamespace}`;
    action = (
      <Button kind="primary" color="brand" onClick={() => onNamespace("")}>
        Show {servedNamespace}
      </Button>
    );
  } else if (status === 403) {
    heading = namespace ? `Cannot read namespace ${namespace}` : "You don't have access";
    hint = "Kubernetes RBAC does not let your credentials read infra planes and component sets here.";
  } else if (status === 401) {
    heading = "Kubernetes did not accept your credentials";
    hint = "Log in to the control plane's kubeconfig context again, then reload.";
  } else if (reason === "ServerUnreachable") {
    heading = "ccluster server is not reachable";
    hint = (
      <>
        Start it again with <code>ccluster server</code>.
      </>
    );
  } else if (status === 502) {
    heading = "Kubernetes could not be reached";
  } else {
    heading = "Something went wrong";
  }

  return (
    <div role="alert">
      <StatusMessage
        slotHeading={heading}
        slotSubheading={
          <Stack gap="3" align="center">
            {hint && <span>{hint}</span>}
            <Text kind="mono/sm" className="error-detail">
              {status ? `${status} ` : ""}
              {message}
            </Text>
            <Text kind="body/regular/sm" className="muted">
              This page keeps trying every few seconds.
            </Text>
          </Stack>
        }
        slotFooter={action}
      />
    </div>
  );
}
