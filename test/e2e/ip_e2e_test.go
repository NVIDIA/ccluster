//go:build e2e
// +build e2e

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/ccluster/test/utils"
)

// This container tests the infra-plane operator on its own, on the separate infra-plane
// cluster, with no control plane involved: it applies the CCLClusterConfig by hand, as a
// cluster run without CCluster's control plane would. The suite deploys the operator to
// both clusters (e2e_suite_test.go); this container only removes what it applied, so
// the order Ginkgo runs the containers in does not matter.
var _ = Describe("IP operator", Ordered, func() {
	ip := utils.IP()
	var ipControllerPodName string

	AfterAll(func() {
		By("removing the CCLClusterConfig")
		_, _ = utils.Run(ip.Kubectl("delete", "cclclusterconfig", "cluster", "--ignore-not-found"))
		uninstallMetricsServer(ip)
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		By("Fetching infra-plane controller manager pod logs")
		out, err := utils.Run(ip.Kubectl("logs", ipControllerPodName, "-n", ipNamespace))
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
			cmd := ip.Kubectl("get",
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

			cmd = ip.Kubectl("get",
				"pods", ipControllerPodName, "-o", "jsonpath={.status.phase}",
				"-n", ipNamespace,
			)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Running"), "Incorrect infra-plane controller-manager pod status")
		}
		Eventually(verifyControllerUp).Should(Succeed())
	})

	// metrics_server is the one registered cluster_base component with a check of its
	// own, and kind runs no metrics-server, so the condition starts False naming it
	// alone, and comes up True once its chart is installed - which proves the chart's CRD
	// and RBAC let a real manager reconcile the singleton, read APIServices and write its
	// status.
	It("should report the cluster_base components verified", func() {
		By("applying the CCLClusterConfig")
		_, err := utils.Run(ip.Kubectl("apply", "-f",
			filepath.Join("test", "e2e", "testdata", "cclclusterconfig.yaml")))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the CCLClusterConfig")

		verified := func(g Gomega) (string, string) {
			out, err := utils.Run(ip.Kubectl("get", "cclclusterconfig", "cluster", "-o",
				`jsonpath={range .status.conditions[?(@.type=="Verified")]}{.status}{"\n"}{.message}{end}`))
			g.Expect(err).NotTo(HaveOccurred())
			parts := strings.SplitN(out, "\n", 2)
			g.Expect(parts).To(HaveLen(2), "Verified condition not reported yet")
			return parts[0], parts[1]
		}

		By("reporting metrics_server as the only failing component")
		Eventually(func(g Gomega) {
			status, message := verified(g)
			g.Expect(status).To(Equal("False"))
			expectOnlyMetricsServerFailing(g, message)
		}).Should(Succeed())

		installMetricsServer(ip)

		// The operator rechecks failing components every minute.
		By("reporting every component verified")
		Eventually(func(g Gomega) {
			status, message := verified(g)
			g.Expect(status).To(Equal("True"), message)
			g.Expect(message).To(HavePrefix("All "))
		}).WithTimeout(3 * time.Minute).Should(Succeed())
	})
})
