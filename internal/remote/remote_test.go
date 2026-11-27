/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package remote

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
)

func secretWith(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gpu-a-credentials"},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func TestRESTConfig(t *testing.T) {
	local := &rest.Config{Host: "https://local.example.com", BearerToken: "local-token"}

	tests := []struct {
		name    string
		data    map[string]string
		want    *rest.Config
		wantErr error
	}{
		{
			name: "remote cluster with token and CA",
			data: map[string]string{
				"server": "https://gpu-a.example.com:6443",
				// "Y2E=" and "c25p" are base64 for "ca" and "sni", as Argo CD writes []byte.
				"config": `{"bearerToken":"remote-token","tlsClientConfig":{"caData":"Y2E=","serverName":"api.gpu-a"}}`,
			},
			want: &rest.Config{
				Host:        "https://gpu-a.example.com:6443",
				BearerToken: "remote-token",
				TLSClientConfig: rest.TLSClientConfig{
					CAData:     []byte("ca"),
					ServerName: "api.gpu-a",
				},
				Timeout: RequestTimeout,
			},
		},
		{
			name: "remote cluster with client certificate, insecure",
			data: map[string]string{
				"server": "https://gpu-a.example.com:6443",
				"config": `{"tlsClientConfig":{"insecure":true,"certData":"Y2VydA==","keyData":"a2V5"}}`,
			},
			want: &rest.Config{
				Host: "https://gpu-a.example.com:6443",
				TLSClientConfig: rest.TLSClientConfig{
					Insecure: true,
					CertData: []byte("cert"),
					KeyData:  []byte("key"),
				},
				Timeout: RequestTimeout,
			},
		},
		{
			name: "remote cluster with http proxyUrl",
			data: map[string]string{
				"server": "https://gpu-a.example.com:6443",
				"config": `{"bearerToken":"remote-token","proxyUrl":"http://proxy.example.com:8080"}`,
			},
			want: &rest.Config{
				Host:        "https://gpu-a.example.com:6443",
				BearerToken: "remote-token",
				Proxy:       http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy.example.com:8080"}),
				Timeout:     RequestTimeout,
			},
		},
		{
			name: "remote cluster with https proxyUrl",
			data: map[string]string{
				"server": "https://gpu-a.example.com:6443",
				"config": `{"bearerToken":"remote-token","proxyUrl":"https://proxy.example.com:8443"}`,
			},
			want: &rest.Config{
				Host:        "https://gpu-a.example.com:6443",
				BearerToken: "remote-token",
				Proxy:       http.ProxyURL(&url.URL{Scheme: "https", Host: "proxy.example.com:8443"}),
				Timeout:     RequestTimeout,
			},
		},
		{
			name: "remote cluster with socks5 proxyUrl",
			data: map[string]string{
				"server": "https://gpu-a.example.com:6443",
				"config": `{"bearerToken":"remote-token","proxyUrl":"socks5://proxy.example.com:1080"}`,
			},
			want: &rest.Config{
				Host:        "https://gpu-a.example.com:6443",
				BearerToken: "remote-token",
				Proxy:       http.ProxyURL(&url.URL{Scheme: "socks5", Host: "proxy.example.com:1080"}),
				Timeout:     RequestTimeout,
			},
		},
		{
			name: "no server is the local cluster",
			data: map[string]string{},
			want: &rest.Config{Host: local.Host, BearerToken: local.BearerToken, Timeout: RequestTimeout},
		},
		{
			name: "in-cluster address without credentials is the local cluster",
			data: map[string]string{"server": argocdv1alpha1.InClusterServer, "config": `{"tlsClientConfig":{"insecure":false}}`},
			want: &rest.Config{Host: local.Host, BearerToken: local.BearerToken, Timeout: RequestTimeout},
		},
		{
			name: "in-cluster address with a token uses the token",
			data: map[string]string{"server": argocdv1alpha1.InClusterServer, "config": `{"bearerToken":"t"}`},
			want: &rest.Config{Host: argocdv1alpha1.InClusterServer, BearerToken: "t", Timeout: RequestTimeout},
		},
		{
			name: "exec plugin is unsupported",
			data: map[string]string{
				"server": "https://gpu-a.example.com",
				"config": `{"execProviderConfig":{"command":"gke-gcloud-auth-plugin"}}`,
			},
			wantErr: ErrUnsupportedCredentials,
		},
		{
			name: "AWS IAM is unsupported",
			data: map[string]string{
				"server": "https://gpu-a.example.com",
				"config": `{"awsAuthConfig":{"clusterName":"gpu-a"}}`,
			},
			wantErr: ErrUnsupportedCredentials,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RESTConfig(secretWith(tt.data), local)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("RESTConfig() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RESTConfig() error = %v", err)
			}
			if got.Host != tt.want.Host || got.BearerToken != tt.want.BearerToken ||
				got.Timeout != tt.want.Timeout ||
				got.Insecure != tt.want.Insecure || got.ServerName != tt.want.ServerName ||
				string(got.CAData) != string(tt.want.CAData) ||
				string(got.CertData) != string(tt.want.CertData) ||
				string(got.KeyData) != string(tt.want.KeyData) {
				t.Errorf("RESTConfig() = %+v, want %+v", got, tt.want)
			}
			if !sameProxy(t, got.Proxy, tt.want.Proxy) {
				t.Errorf("RESTConfig().Proxy did not match want")
			}
		})
	}
}

func sameProxy(t *testing.T, got, want func(*http.Request) (*url.URL, error)) bool {
	t.Helper()
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	req, err := http.NewRequest(http.MethodGet, "https://gpu-a.example.com:6443/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotURL, gotErr := got(req)
	wantURL, wantErr := want(req)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("Proxy() errors: got=%v want=%v", gotErr, wantErr)
	}
	return gotURL.String() == wantURL.String()
}

func TestRESTConfigDoesNotModifyLocal(t *testing.T) {
	local := &rest.Config{Host: "https://local.example.com"}
	if _, err := RESTConfig(secretWith(nil), local); err != nil {
		t.Fatal(err)
	}
	if local.Timeout != 0 {
		t.Errorf("local.Timeout = %v, want it untouched", local.Timeout)
	}
}

func TestRESTConfigRejectsMalformedConfig(t *testing.T) {
	_, err := RESTConfig(secretWith(map[string]string{"server": "https://x", "config": "{"}), &rest.Config{})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("RESTConfig() error = %v, want %v", err, ErrInvalidCredentials)
	}
}

func TestRESTConfigRejectsInvalidProxyURL(t *testing.T) {
	tests := []struct {
		name      string
		proxyURL  string
		forbidden []string
	}{
		{
			name:      "malformed URL with credentials",
			proxyURL:  "http://user:secret-pass@proxy\u007f.example.com:8080",
			forbidden: []string{"secret-pass", "proxy\u007f.example.com"},
		},
		{
			name:      "missing scheme",
			proxyURL:  "proxy.corp:3128",
			forbidden: []string{"proxy.corp"},
		},
		{
			name:      "unsupported scheme",
			proxyURL:  "ftp://user:secret-pass@proxy.example.com:21",
			forbidden: []string{"secret-pass", "proxy.example.com"},
		},
		{
			name:     "missing host",
			proxyURL: "http://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RESTConfig(secretWith(map[string]string{
				"server": "https://gpu-a.example.com",
				"config": `{"proxyUrl":"` + tt.proxyURL + `"}`,
			}), &rest.Config{})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("RESTConfig() error = %v, want %v", err, ErrInvalidCredentials)
			}
			if !strings.Contains(err.Error(), "proxyUrl") {
				t.Errorf("RESTConfig() error = %q, want it to name proxyUrl", err)
			}
			for _, secret := range tt.forbidden {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("RESTConfig() error = %q, must not quote %q", err, secret)
				}
			}
		})
	}
}
