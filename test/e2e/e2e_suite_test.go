//go:build e2e
// +build e2e

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/ccluster/test/utils"
)

var (
	// managerImage is the manager image to be built and loaded for testing.
	managerImage = "example.com/ccluster-cp-operator:v0.0.1"
	// ipManagerImage is the infra-plane operator image, built the same way and run on
	// both clusters: each is an infra plane.
	ipManagerImage = "example.com/ccluster-ip-operator:v0.0.1"
)

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind, and two Kind clusters - a
// control plane ($KIND_CLUSTER) and a separate infra plane ($KIND_CLUSTER_IP) - which
// `make test-e2e` creates and deletes. The control-plane cluster is the current
// kubeconfig context, so a plain `kubectl` reaches it; utils.IP().Kubectl reaches the
// other.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting ccluster e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	cmd := exec.Command("make", "docker-build-cp", fmt.Sprintf("IMG_CP=%s", managerImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager image on the control-plane cluster")
	err = utils.LoadImageToKindClusterWithName(managerImage, utils.CP())
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	By("building the infra-plane manager image")
	cmd = exec.Command("make", "docker-build-ip", fmt.Sprintf("IMG_IP=%s", ipManagerImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the infra-plane manager image")

	for _, c := range []utils.Cluster{utils.CP(), utils.IP()} {
		By("loading the infra-plane manager image on " + c.Name)
		err = utils.LoadImageToKindClusterWithName(ipManagerImage, c)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the infra-plane manager image into Kind")
	}

	// kind create leaves the context on the last cluster it made; the Makefile makes the
	// control plane last, and this makes sure of it, since every plain kubectl relies on
	// it.
	By("making the control-plane cluster the current context")
	_, err = utils.Run(exec.Command("kubectl", "config", "use-context", utils.CP().Context()))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	// Both clusters are infra planes - the control plane registered with itself, and
	// the separate one - so both run the infra-plane operator for the whole suite.
	for _, c := range []utils.Cluster{utils.CP(), utils.IP()} {
		deployIPOperator(c)
	}

	By("building the ccluster CLI")
	cclusterBinary = filepath.Join(GinkgoT().TempDir(), "ccluster")
	_, err = utils.Run(exec.Command("go", "build", "-o", cclusterBinary, "./cmd/ccluster"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the ccluster CLI")
	cclusterConfigPath = filepath.Join(GinkgoT().TempDir(), "ccluster", "config.yaml")
})

var _ = AfterSuite(func() {
	for _, c := range []utils.Cluster{utils.CP(), utils.IP()} {
		undeployIPOperator(c)
	}
})
