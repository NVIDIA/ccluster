/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/api/argocd/v1alpha1"
	externaldnsv1alpha1 "github.com/NVIDIA/ccluster/internal/api/externaldns/v1alpha1"
	// +kubebuilder:scaffold:imports
)

// Values more than one test file uses.
const (
	// defaultNamespace is the namespace most tests work in. The operator names a
	// namespace's Argo CD project after it, so it names a project too.
	defaultNamespace = "default"
	// otherNamespace is a second namespace, such as one a foreign back-reference points at.
	otherNamespace = "other-namespace"
	// team is a tenant namespace, and the Argo CD project named after it.
	team = "team-a"

	planeA = "plane-a"
	planeB = "plane-b"

	// foreign marks a value another owner put in place.
	foreign = "someone-elses"

	// lbIP and otherLBIP are LoadBalancer addresses, and ingressLoadBalancer and
	// apiLoadBalancer the names of labelled LoadBalancers.
	lbIP                = "192.0.2.10"
	otherLBIP           = "192.0.2.20"
	ingressLoadBalancer = "ingress"
	apiLoadBalancer     = "api"

	// web is the Application, and the Deployment it runs, that the health tests read;
	// webPod is that Deployment's Pod, which the reader addresses as webPodKey.
	web       = "web"
	webPod    = "web-0"
	podKind   = "Pod"
	webPodKey = podKind + "/" + webPod

	// reasonRunning is the reason a health function gives a component that runs.
	reasonRunning = "Running"
	// The non-terminal phases of a sync operation, which the Argo CD API package leaves
	// unnamed.
	operationRunning     = "Running"
	operationTerminating = "Terminating"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = cclusternvidiacomv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = argocdv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = externaldnsv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
			// Argo CD's Application and AppProject, which the controllers create and
			// read back, and external-dns's DNSEndpoint, which the CCLInfraPlane
			// controller writes.
			filepath.Join("..", "..", "..", "test", "testdata", "crd"),
		},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Eventually(func() error {
		return testEnv.Stop()
	}, time.Minute, time.Second).Should(Succeed())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	// Three levels up from internal/controller/cp: the scaffold assumes a two-level
	// controller package, this repo nests one deeper.
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
