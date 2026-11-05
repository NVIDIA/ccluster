/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/ccluster/pkg/cclclient"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

// One API server serving the ccluster CRDs stands in for both clusters: the kubeconfig the
// tests write names it as context cp and as context ip. A third context, bare, is a server
// that serves no API at all.
var (
	apiServer  *rest.Config
	kubeClient kubernetes.Interface
	clientset  versioned.Interface
)

const (
	token  = "infra-token"
	domain = "edge-1.example.com"
)

var caCert = []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")

func TestMain(m *testing.M) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	// As in the controller suites: outside make, use the binaries make setup-envtest put
	// in bin/k8s.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if entries, err := os.ReadDir(filepath.Join("..", "..", "bin", "k8s")); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					env.BinaryAssetsDirectory = filepath.Join("..", "..", "bin", "k8s", entry.Name())
					break
				}
			}
		}
	}

	var err error
	if apiServer, err = env.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "starting envtest:", err)
		os.Exit(1)
	}
	if kubeClient, err = kubernetes.NewForConfig(apiServer); err != nil {
		fmt.Fprintln(os.Stderr, "creating Kubernetes clientset:", err)
		os.Exit(1)
	}
	if clientset, err = versioned.NewForConfig(apiServer); err != nil {
		fmt.Fprintln(os.Stderr, "creating clientset:", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "stopping envtest:", err)
	}
	os.Exit(code)
}

func TestPrintsUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("ccluster %v: %v", args, err)
		}
		if !strings.HasPrefix(out, "Manage ccluster infra planes and component sets.") ||
			!strings.Contains(out, "Usage:\n  ccluster") {
			t.Errorf("ccluster %v printed %q, want the usage", args, out)
		}
		for _, command := range []string{"init", "register", "unregister"} {
			if !strings.Contains(out, command) {
				t.Errorf("ccluster %v does not list %s", args, command)
			}
		}
	}
}

func TestPrintsVersion(t *testing.T) {
	out, err := run(t, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ccluster version "+Version()+"\n" {
		t.Errorf("ccluster --version printed %q", out)
	}
}

func TestRegisterDescribesItself(t *testing.T) {
	out, err := run(t, "register", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Register a cluster with the control plane.") {
		t.Errorf("ccluster register --help printed %q", out)
	}
}

func TestInitSavesTheControlPlane(t *testing.T) {
	configFile := setUp(t)

	out, err := run(t, "init", "--control-plane", "cp")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("Initialized ccluster with control plane cp (saved to %s)\n", configFile); out != want {
		t.Errorf("init printed %q, want %q", out, want)
	}
	if got := savedConfig(t, configFile); got != "cp" {
		t.Errorf("saved control plane = %q, want cp", got)
	}

	// Again, with another context, replaces it.
	if _, err := run(t, "init", "--control-plane", "ip"); err != nil {
		t.Fatal(err)
	}
	if got := savedConfig(t, configFile); got != "ip" {
		t.Errorf("saved control plane = %q, want ip", got)
	}
}

func TestInitRejects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"an unknown context", []string{"init", "--control-plane", "nope"}, "kubeconfig could not be loaded"},
		{
			"a cluster without the ccluster API", []string{"init", "--control-plane", "bare"},
			"control plane bare does not serve CCLInfraPlane; is the ccluster-cp operator installed?",
		},
		{"no control plane", []string{"init"}, `required flag(s) "control-plane" not set`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configFile := setUp(t)

			expectError(t, test.want, test.args...)
			if _, err := os.Stat(configFile); !os.IsNotExist(err) {
				t.Errorf("config file: %v, want none written", err)
			}
		})
	}
}

func TestConfigPathFollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(configEnv, "")
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "ccluster", "config.yaml"); got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestRegisterCreatesAdminTokenAndInfraPlane(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	fillTokenSecret(t)

	out, err := run(t, "register", "edge-1", "--namespace", namespace, "--infra-plane", "ip", "--domain", domain)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("Registered CCLInfraPlane %s/edge-1 for %s\n", namespace, apiServer.Host); out != want {
		t.Errorf("register printed %q, want %q", out, want)
	}

	ctx := t.Context()
	binding, err := kubeClient.RbacV1().ClusterRoleBindings().Get(ctx, clusterRoleBindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccountName, Namespace: "kube-system"}}
	if binding.RoleRef.Name != "cluster-admin" || !reflect.DeepEqual(binding.Subjects, wantSubjects) {
		t.Errorf("ClusterRoleBinding = %v -> %v, want cluster-admin -> %v", binding.Subjects, binding.RoleRef, wantSubjects)
	}
	if _, err := kubeClient.CoreV1().ServiceAccounts("kube-system").Get(ctx, serviceAccountName,
		metav1.GetOptions{}); err != nil {
		t.Errorf("ServiceAccount: %v", err)
	}

	plane, err := clientset.CclusterV1alpha1().CCLInfraPlanes(namespace).Get(ctx, "edge-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plane.Spec.Domain != domain {
		t.Errorf("domain = %q, want %q", plane.Spec.Domain, domain)
	}
	credentials := credentialsConfig(t, namespace, "edge-1")
	if credentials.server != apiServer.Host || credentials.config.BearerToken != token ||
		string(credentials.config.TLSClientConfig.CAData) != string(caCert) {
		t.Errorf("credentials = %+v, want the kubeconfig's server, the token and the CA", credentials)
	}
}

func TestRegisterAgainReusesTheTokenAndHonoursServer(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	fillTokenSecret(t)
	args := []string{"register", "edge-1", "-n", namespace, "--infra-plane", "ip", "--domain", domain}

	if _, err := run(t, args...); err != nil {
		t.Fatal(err)
	}
	// Every object now exists; registering again keeps them and reads the same token.
	if _, err := run(t, append(args, "--server", "https://edge-1:6443")...); err != nil {
		t.Fatal(err)
	}

	credentials := credentialsConfig(t, namespace, "edge-1")
	if credentials.server != "https://edge-1:6443" || credentials.config.BearerToken != token {
		t.Errorf("credentials = %+v, want server https://edge-1:6443 and the same token", credentials)
	}
}

func TestRegisterFailsWhenTokenNeverArrives(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	tokenTimeout = 50 * time.Millisecond

	expectError(t, "secret kube-system/ccluster-manager-token was not given a token within 50ms",
		"register", "edge-1", "-n", namespace, "--infra-plane", "ip", "--domain", domain)
	if _, err := kubeClient.CoreV1().Namespaces().Get(t.Context(), namespace,
		metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("namespace %s: error = %v, want the control plane left alone", namespace, err)
	}
}

func TestRegisterRejects(t *testing.T) {
	tests := []struct {
		name   string
		config string // the config file's content; empty means there is none
		args   []string
		want   string
	}{
		{
			name: "an unknown context", config: "controlPlane:\n  context: cp\n",
			args: []string{"--infra-plane", "nope", "--domain", domain},
			want: "kubeconfig could not be loaded",
		},
		{
			name: "a missing init",
			args: []string{"--infra-plane", "ip", "--domain", domain},
			want: "no control plane is configured; run ccluster init first",
		},
		{
			name: "a config file without a control plane", config: "- cp\n",
			args: []string{"--infra-plane", "ip", "--domain", domain},
			want: "no control plane is configured; run ccluster init first",
		},
		{
			name: "an invalid config file", config: "controlPlane: [",
			args: []string{"--infra-plane", "ip", "--domain", domain},
			want: "is not valid YAML",
		},
		{
			// The infra-plane objects are created before the domain reaches the API
			// server, which then rejects it.
			name: "a malformed domain", config: "controlPlane:\n  context: cp\n",
			args: []string{"--infra-plane", "ip", "--domain", "Not_A_Domain"},
			want: "API request failed (422): CCLInfraPlane.ccluster.nvidia.com \"edge-1\" is invalid",
		},
		{name: "no infra plane", args: []string{"--domain", domain}, want: `required flag(s) "infra-plane" not set`},
		{name: "no domain", args: []string{"--infra-plane", "ip"}, want: `required flag(s) "domain" not set`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configFile := setUp(t)
			fillTokenSecret(t)
			if test.config != "" {
				writeConfig(t, configFile, test.config)
			}
			expectError(t, test.want, append([]string{"register", "edge-1", "-n", newNamespaceName()},
				test.args...)...)
		})
	}

	t.Run("no namespace flag", func(t *testing.T) {
		setUpInitialized(t)
		expectError(t, `required flag(s) "namespace" not set`,
			"register", "edge-1", "--infra-plane", "ip", "--domain", domain)
	})
}

func TestUnregisterDeletesTheInfraPlane(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := cclclient.New(namespace, kubeClient, clientset)
	if err := client.EnsureNamespace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyInfraPlane(t.Context(), cclclient.InfraPlaneOptions{Name: "edge-1", Domain: domain}); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "unregister", "edge-1", "--namespace", namespace)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("Unregistered CCLInfraPlane %s/edge-1\n", namespace); out != want {
		t.Errorf("unregister printed %q, want %q", out, want)
	}
	if _, err := client.GetInfraPlane(t.Context(), "edge-1"); !apierrors.IsNotFound(err) {
		t.Errorf("GetInfraPlane() error = %v, want NotFound", err)
	}
	if _, err := kubeClient.CoreV1().Secrets(namespace).Get(t.Context(), "edge-1-credentials",
		metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("credentials Secret: error = %v, want NotFound", err)
	}

	// Already gone is not an error.
	if _, err := run(t, "unregister", "edge-1", "--namespace", namespace); err != nil {
		t.Errorf("unregister again: %v", err)
	}
}

func TestUnregisterRejects(t *testing.T) {
	tests := []struct {
		name        string
		initialized bool
		args        []string
		want        string
	}{
		{"a missing init", false, []string{"-n", "tenant-a"}, "run ccluster init first"},
		{"no namespace", true, nil, `required flag(s) "namespace" not set`},
		{"an infra plane", true, []string{"-n", "tenant-a", "--infra-plane", "ip"}, "unknown flag: --infra-plane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.initialized {
				setUpInitialized(t)
			} else {
				setUp(t)
			}
			expectError(t, test.want, append([]string{"unregister", "edge-1"}, test.args...)...)
		})
	}
}

// setUp points the commands at a kubeconfig with the contexts cp, ip and bare, and at a
// config file of the test's own, which does not exist yet; it returns the file's path.
func setUp(t *testing.T) string {
	t.Helper()

	bare := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bare.Close)

	dir := t.TempDir()
	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters["envtest"] = &clientcmdapi.Cluster{
		Server:                   apiServer.Host,
		CertificateAuthorityData: apiServer.CAData,
	}
	kubeconfig.Clusters["bare"] = &clientcmdapi.Cluster{Server: bare.URL}
	kubeconfig.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: apiServer.CertData,
		ClientKeyData:         apiServer.KeyData,
		Token:                 apiServer.BearerToken,
	}
	kubeconfig.AuthInfos["bare"] = &clientcmdapi.AuthInfo{}
	for _, name := range []string{"cp", "ip"} {
		kubeconfig.Contexts[name] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	}
	kubeconfig.Contexts["bare"] = &clientcmdapi.Context{Cluster: "bare", AuthInfo: "bare"}
	kubeconfigPath := filepath.Join(dir, "kubeconfig")
	if err := clientcmd.WriteToFile(*kubeconfig, kubeconfigPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfigPath)

	configFile := filepath.Join(dir, "ccluster", "config.yaml")
	t.Setenv(configEnv, configFile)

	timeout, interval := tokenTimeout, tokenPollInterval
	tokenTimeout, tokenPollInterval = 10*time.Second, 10*time.Millisecond
	t.Cleanup(func() { tokenTimeout, tokenPollInterval = timeout, interval })

	t.Cleanup(func() { deleteInfraPlaneObjects(t) })
	return configFile
}

// setUpInitialized is setUp with init already run against the control plane cp.
func setUpInitialized(t *testing.T) {
	t.Helper()

	writeConfig(t, setUp(t), "controlPlane:\n  context: cp\n")
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func savedConfig(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		t.Fatalf("config file %q: %v", data, err)
	}
	if cfg.ControlPlane == nil {
		t.Fatalf("config file %q has no controlPlane", data)
	}
	return cfg.ControlPlane.Context
}

// fillTokenSecret plays the token controller, which envtest does not run: once register
// creates the token Secret, it writes a token and CA into it.
func fillTokenSecret(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		secrets := kubeClient.CoreV1().Secrets(serviceAccountNamespace)
		for ctx.Err() == nil {
			secret, err := secrets.Get(ctx, tokenSecretName, metav1.GetOptions{})
			if err == nil && len(secret.Data) == 0 {
				secret.Data = map[string][]byte{
					corev1.ServiceAccountTokenKey:  []byte(token),
					corev1.ServiceAccountRootCAKey: caCert,
				}
				_, _ = secrets.Update(ctx, secret, metav1.UpdateOptions{})
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

// deleteInfraPlaneObjects removes what register creates on the infra plane, so each test
// starts without it.
func deleteInfraPlaneObjects(t *testing.T) {
	t.Helper()

	// t.Context is already cancelled when cleanups run.
	ctx := context.Background()
	for _, err := range []error{
		kubeClient.CoreV1().Secrets(serviceAccountNamespace).Delete(ctx, tokenSecretName, metav1.DeleteOptions{}),
		kubeClient.CoreV1().ServiceAccounts(serviceAccountNamespace).Delete(ctx, serviceAccountName, metav1.DeleteOptions{}),
		kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, clusterRoleBindingName, metav1.DeleteOptions{}),
	} {
		if err != nil && !apierrors.IsNotFound(err) {
			t.Error(err)
		}
	}
}

// newNamespaceName returns a namespace name no other test uses: envtest runs no namespace
// controller, so a namespace is never really deleted.
func newNamespaceName() string {
	return "tenant-" + rand.String(8)
}

type credentials struct {
	server string
	config cclclient.ClusterConfig
}

func credentialsConfig(t *testing.T, namespace, plane string) credentials {
	t.Helper()

	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(t.Context(),
		cclclient.CredentialsSecretName(plane), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var config cclclient.ClusterConfig
	if err := yaml.Unmarshal(secret.Data["config"], &config); err != nil {
		t.Fatal(err)
	}
	return credentials{server: string(secret.Data["server"]), config: config}
}

// run executes ccluster with args and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// expectError runs ccluster with args and fails unless it returns an error containing
// want.
func expectError(t *testing.T, want string, args ...string) {
	t.Helper()

	_, err := run(t, args...)
	if err == nil {
		t.Fatalf("ccluster %v succeeded, want an error containing %q", args, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("ccluster %v: error = %q, want one containing %q", args, err, want)
	}
}
