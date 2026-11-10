/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package server

import (
	"net"
	"net/http"
	"strings"
)

// contentSecurityPolicy allows only the server's own scripts, styles, images and API:
// the UI has no inline script or style and loads nothing from elsewhere, and no other
// site may frame it. Images may also be data: URIs, which is how Kaizen UI's stylesheet
// draws its icons.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// secureHeaders sets the headers every response carries.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentSecurityPolicy)
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// checkHost refuses a request whose Host header names none of hosts, compared without
// the port and ignoring case. A page on another site that rebinds its own host name to
// 127.0.0.1 can make the browser send requests here, but they still carry that site's
// name, so this keeps it from reading the dashboard with the user's credentials. An empty
// hosts accepts every request.
func checkHost(hosts []string, next http.Handler) http.Handler {
	if len(hosts) == 0 {
		return next
	}
	allowed := map[string]bool{}
	for _, host := range hosts {
		allowed[strings.ToLower(host)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
		if !allowed[host] {
			http.Error(w, "This server answers only to "+strings.Join(hosts, ", ")+".",
				http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoopbackHosts are the host names a browser on the same machine reaches a loopback
// listener by.
var LoopbackHosts = []string{"localhost", "127.0.0.1", "::1"}
