// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Opens the namespace picked in the dropdown, and refreshes the page's content in place.
// The server sends only the content when a request carries X-Dashboard-Fragment
// (routes.FRAGMENT_HEADER). A failed refresh keeps what is on screen and says so;
// refreshing pauses while the tab is hidden.
(function () {
  "use strict";

  var form = document.getElementById("namespace-form");
  if (form) {
    form.namespace.addEventListener("change", function () {
      form.submit();
    });
  }

  var content = document.getElementById("content");
  var notice = document.getElementById("refresh-notice");
  var seconds = content && Number(content.dataset.refreshSeconds);
  if (!seconds) {
    return;
  }
  var inFlight = false;

  async function refresh() {
    if (document.visibilityState !== "visible" || inFlight) {
      return;
    }
    inFlight = true;
    try {
      var response = await fetch(window.location.pathname + window.location.search, {
        headers: { "X-Dashboard-Fragment": "1" },
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error("HTTP " + response.status);
      }
      content.innerHTML = await response.text();
      notice.hidden = true;
    } catch (error) {
      notice.textContent = "Could not refresh (" + error.message + "); showing older data.";
      notice.hidden = false;
    } finally {
      inFlight = false;
    }
  }

  window.setInterval(refresh, seconds * 1000);
  document.addEventListener("visibilitychange", refresh);
})();
