// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { Select } from "@nvidia/foundations-react-core";

/** Select's value for all namespaces. Namespace names are DNS labels, so it cannot clash. */
export const allNamespaces = "_all";

/** The namespace a value picked in the Select stands for; "" is all namespaces. */
export function namespaceOf(value: string): string {
  return value === allNamespaces ? "" : value;
}

interface NamespaceSelectProps {
  /** The only namespace the server serves, if any. */
  lockedNamespace?: string;
  namespaces: string[];
  namespace: string;
  onNamespace: (namespace: string) => void;
}

/** The page's one filter, the namespace, shown in the app bar. */
export function NamespaceSelect({ lockedNamespace, namespaces, namespace, onNamespace }: NamespaceSelectProps) {
  const items = lockedNamespace
    ? [lockedNamespace]
    : [{ value: allNamespaces, children: "All namespaces" }, ...namespaces];
  return (
    <Select
      aria-label="Namespace"
      size="small"
      className="namespace-select"
      items={items}
      value={lockedNamespace ?? (namespace || allNamespaces)}
      onValueChange={(value: string) => onNamespace(namespaceOf(value))}
      readOnly={Boolean(lockedNamespace)}
    />
  );
}
