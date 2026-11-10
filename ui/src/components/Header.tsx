// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import type { Config } from "../api";
import { clock } from "../format";

interface HeaderProps {
  config?: Config;
  namespaces: string[];
  namespace: string;
  onNamespace: (namespace: string) => void;
  updatedAt?: number;
}

export function Header({ config, namespaces, namespace, onNamespace, updatedAt }: HeaderProps) {
  const locked = Boolean(config?.namespace);
  const choices = locked ? [config!.namespace] : namespaces;
  return (
    <header className="header">
      <div className="header-brand">
        <img src="/favicon.svg" alt="" width="28" height="28" />
        <div>
          <h1>ccluster</h1>
          {config && (
            <p className="header-context" title="Kubeconfig context of the control plane">
              {config.context}
            </p>
          )}
        </div>
      </div>
      <div className="header-controls">
        <label className="namespace-picker">
          <span>Namespace</span>
          <select
            value={locked ? config!.namespace : namespace}
            disabled={locked}
            onChange={(event) => onNamespace(event.target.value)}
          >
            {!locked && <option value="">All namespaces</option>}
            {choices.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
        {updatedAt ? (
          <p className="header-updated" aria-live="off">
            Updated {clock(updatedAt)}
          </p>
        ) : null}
      </div>
    </header>
  );
}
