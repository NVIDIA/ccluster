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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/ccluster/test/utils"
)

// ipNamespace is where `make deploy-ip` installs ccluster-ip-operator.
const ipNamespace = "ccluster-ip-system"

// This container deploys the infra-plane operator into the same kind cluster the
// control-plane specs use. The two operators keep to their own namespaces and CRDs, so
// the order Ginkgo runs the containers in does not matter: the control-plane AfterAll's
// `make uninstall` deletes every CRD, and this container's AfterAll deletes its own.
var _ = Describe("IP operator", Ordered, func() {
	var ipControllerPodName string

	BeforeAll(func() {
		By("deploying ccluster-ip-operator")
		cmd := exec.Command("make", "deploy-ip", fmt.Sprintf("IMG_IP=%s", ipManagerImage))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy ccluster-ip-operator")
	})

	AfterAll(func() {
		By("removing the CCLClusterConfig")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "cclclusterconfig", "cluster", "--ignore-not-found"))

		// The chart keeps its CRDs on uninstall (helm.sh/resource-policy: keep).
		By("removing the spire_crds chart and its CRDs")
		_, _ = utils.Run(exec.Command("helm", "uninstall", "spire-crds", "--namespace", "spire-system", "--ignore-not-found"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "crd", "--ignore-not-found",
			"clusterfederatedtrustdomains.spire.spiffe.io",
			"clusterspiffeids.spire.spiffe.io",
			"clusterstaticentries.spire.spiffe.io"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", "spire-system", "--ignore-not-found"))

		By("undeploying ccluster-ip-operator")
		_, _ = utils.Run(exec.Command("make", "undeploy-ip"))

		// The chart keeps its CRD on uninstall (helm.sh/resource-policy: keep).
		By("removing the CCLClusterConfig CRD")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "crd",
			"cclclusterconfigs.ccluster.nvidia.com", "--ignore-not-found"))

		By("removing the infra-plane manager namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", ipNamespace, "--ignore-not-found"))
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		By("Fetching infra-plane controller manager pod logs")
		out, err := utils.Run(exec.Command("kubectl", "logs", ipControllerPodName, "-n", ipNamespace))
		if err == nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "IP controller logs:\n %s", out)
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get IP controller logs: %s", err)
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	It("should run successfully", func() {
		verifyControllerUp := func(g Gomega) {
			cmd := exec.Command("kubectl", "get",
				"pods", "-l", "control-plane=controller-manager",
				"-o", "go-template={{ range .items }}"+
					"{{ if not .metadata.deletionTimestamp }}"+
					"{{ .metadata.name }}"+
					"{{ \"\\n\" }}{{ end }}{{ end }}",
				"-n", ipNamespace,
			)
			podOutput, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve infra-plane controller-manager pod information")
			podNames := utils.GetNonEmptyLines(podOutput)
			g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
			ipControllerPodName = podNames[0]
			g.Expect(ipControllerPodName).To(ContainSubstring("controller-manager"))

			cmd = exec.Command("kubectl", "get",
				"pods", ipControllerPodName, "-o", "jsonpath={.status.phase}",
				"-n", ipNamespace,
			)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Running"), "Incorrect infra-plane controller-manager pod status")
		}
		Eventually(verifyControllerUp).Should(Succeed())
	})

	// The quickstart example is the CCLClusterConfig under test, so the documented
	// object is the one proven to work. spire_crds is the one cluster_base component with
	// a check of its own, so the condition starts False naming it alone, and comes up
	// True once its chart is installed - which proves the chart's CRD and RBAC let a real
	// manager reconcile the singleton, read CRDs and write its status.
	It("should report the cluster_base components verified", func() {
		By("applying the quickstart CCLClusterConfig")
		cmd := exec.Command("kubectl", "apply", "-f", filepath.Join("examples", "quickstart", "v1alpha1_cclclusterconfig.yaml"))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the CCLClusterConfig")

		verified := func(g Gomega) (string, string) {
			out, err := utils.Run(exec.Command("kubectl", "get", "cclclusterconfig", "cluster", "-o",
				`jsonpath={range .status.conditions[?(@.type=="Verified")]}{.status}{"\n"}{.message}{end}`))
			g.Expect(err).NotTo(HaveOccurred())
			parts := strings.SplitN(out, "\n", 2)
			g.Expect(parts).To(HaveLen(2), "Verified condition not reported yet")
			return parts[0], parts[1]
		}

		By("reporting spire_crds as the only failing component")
		Eventually(func(g Gomega) {
			status, message := verified(g)
			g.Expect(status).To(Equal("False"))
			// "not found" rather than a read error: the ClusterRole lets the
			// manager read CRDs, and there are none yet.
			g.Expect(message).To(ContainSubstring(
				"failing: catalogs/cluster_base/spire_crds (CRD clusterfederatedtrustdomains.spire.spiffe.io not found"))
			g.Expect(strings.Count(message, "catalogs/cluster_base/")).To(Equal(1))
		}).Should(Succeed())

		By("installing the spire_crds chart")
		// From a copy, so fetching the upstream dependency leaves no Chart.lock or
		// tarball in the working tree.
		chartDir := filepath.Join(GinkgoT().TempDir(), "spire_crds")
		_, err = utils.Run(exec.Command("cp", "-R", filepath.Join("catalogs", "cluster_base", "spire_crds"), chartDir))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("helm", "upgrade", "--install", "spire-crds", chartDir,
			"--dependency-update", "--namespace", "spire-system", "--create-namespace", "--wait"))
		Expect(err).NotTo(HaveOccurred(), "Failed to install the spire_crds chart")

		// The operator rechecks failing components every minute.
		By("reporting every component verified")
		Eventually(func(g Gomega) {
			status, message := verified(g)
			g.Expect(status).To(Equal("True"), message)
			g.Expect(message).To(HavePrefix("All "))
		}).WithTimeout(3 * time.Minute).Should(Succeed())
	})
})
