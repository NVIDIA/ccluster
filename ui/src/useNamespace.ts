// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";

function current(): string {
  return new URLSearchParams(window.location.search).get("namespace") ?? "";
}

/**
 * The namespace the page shows, kept in ?namespace= so a view can be reloaded, linked
 * to, and left with the browser's back button. "" is all namespaces.
 */
export function useNamespace(): [string, (namespace: string) => void] {
  const [namespace, setState] = useState(current);

  useEffect(() => {
    const onPopState = () => setState(current());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const setNamespace = useCallback((next: string) => {
    const url = new URL(window.location.href);
    if (next) {
      url.searchParams.set("namespace", next);
    } else {
      url.searchParams.delete("namespace");
    }
    if (url.href !== window.location.href) {
      window.history.pushState(null, "", url);
    }
    setState(next);
  }, []);

  return [namespace, setNamespace];
}
