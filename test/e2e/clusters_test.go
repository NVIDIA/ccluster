//go:build e2e
// +build e2e

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/ccluster/test/utils"
)

// ipNamespace is where `make deploy-ip` installs ccluster-ip-operator, on each cluster.
const ipNamespace = "ccluster-ip-system"

// cclusterConfigPath is the config file the ccluster CLI keeps its control plane in,
// created by BeforeSuite so a run never touches the user's own.
var cclusterConfigPath string

// target is a cluster the infra-plane specs register as a CCLInfraPlane, and how.
type target struct {
	// name labels the specs generated for the target.
	name string
	// cluster is where the CCLInfraPlane's workloads and CCLClusterConfig land.
	cluster utils.Cluster
	// register creates the credentials Secret and the CCLInfraPlane called planeName in
	// crNamespace, the way a user would for this kind of cluster. The spec deletes the
	// CCLInfraPlane itself, inline, while the operator can still run its finalizer; the
	// credentials Secret is removed by DeferCleanup.
	register func(planeName string)
}

// targets are the two clusters every infra-plane spec runs against:
//   - in-cluster: the control-plane cluster registered with itself, credential-free, as
//     examples/quickstart does. Argo CD and the cp operator each use their own
//     ServiceAccount.
//   - ex-cluster: the separate infra-plane cluster, registered with `ccluster register`,
//     which gives it a cluster-admin token and its CA, reached over Kind's Docker
//     network.
func targets() []target {
	return []target{
		{name: "in-cluster", cluster: utils.CP(), register: func(planeName string) {
			GinkgoHelper()
			registerInCluster(crNamespace, planeName)
		}},
		{name: "ex-cluster", cluster: utils.IP(), register: registerWithCLI},
	}
}

// exCluster is the ex-cluster target, for the specs only it can run.
func exCluster() target {
	return targets()[1]
}

// credentialsSecretName is the Secret a CCLInfraPlane's credentials live in. It is the name
// `ccluster register` uses, so both targets' Secrets are found the same way.
func credentialsSecretName(planeName string) string {
	return planeName + "-credentials"
}

// registerInCluster registers the control-plane cluster with itself as planeName in ns.
// The server must be exactly https://kubernetes.default.svc, with no credentials, for
// Argo CD to connect with its own ServiceAccount; the cp operator does the same with
// its own. See examples/quickstart/v1alpha1_cclinfraplane.yaml.
func registerInCluster(ns, planeName string) {
	GinkgoHelper()

	createInClusterCredentials(ns, credentialsSecretName(planeName))
	applyCCLInfraPlaneIn(ns, planeName, credentialsSecretName(planeName))
}

// createInClusterCredentials creates a credential-free credentials Secret for the
// control-plane cluster itself, deleted when the spec ends.
func createInClusterCredentials(ns, name string) {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic", name,
		"--from-literal=server=https://kubernetes.default.svc",
		`--from-literal=config={"tlsClientConfig":{"insecure":false}}`,
		"-n", ns))
	Expect(err).NotTo(HaveOccurred(), "Failed to create credentials Secret %s/%s", ns, name)
	DeferCleanup(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "secret", name, "-n", ns, "--ignore-not-found"))
	})
}

// registerWithCLI registers the separate infra-plane cluster as planeName with
// `ccluster register`, exactly as the Python README documents: a cluster-admin token
// created on that cluster, and its address on Kind's Docker network, where the
// control-plane cluster's Pods reach it.
func registerWithCLI(planeName string) {
	GinkgoHelper()

	ip := utils.IP()
	_, err := utils.Run(cclusterCLI("register", planeName,
		"--namespace", crNamespace,
		"--infra-plane", ip.Context(),
		"--domain", infraPlaneDomain(planeName),
		"--server", ip.Server()))
	Expect(err).NotTo(HaveOccurred(), "ccluster register failed")
	DeferCleanup(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "secret", credentialsSecretName(planeName),
			"-n", crNamespace, "--ignore-not-found"))
	})
}

// cclusterCLI returns the ccluster CLI from python/, run with uv against the suite's
// config file.
func cclusterCLI(args ...string) *exec.Cmd {
	uv := os.Getenv("UV")
	if uv == "" {
		uv = "uv"
	}
	cmd := exec.Command(uv, append([]string{"run", "--project", "python", "ccluster"}, args...)...)
	cmd.Env = append(os.Environ(), "CCLUSTER_CONFIG="+cclusterConfigPath)
	return cmd
}

// deleteInfraPlane deletes a CCLInfraPlane and waits for its finalizer, which needs the
// operator running: call it inline in the spec, never from DeferCleanup.
func deleteInfraPlane(planeName string) {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "delete", "cclinfraplane", planeName,
		"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m"))
	Expect(err).NotTo(HaveOccurred(), "Failed to delete CCLInfraPlane %s", planeName)
}

// deployIPOperator installs ccluster-ip-operator on c and waits for it to run.
func deployIPOperator(c utils.Cluster) {
	GinkgoHelper()

	By("deploying ccluster-ip-operator to " + c.Name)
	_, err := utils.Run(exec.Command("make", "deploy-ip",
		"IMG_IP="+ipManagerImage, "KUBE_CONTEXT_IP="+c.Context()))
	Expect(err).NotTo(HaveOccurred(), "Failed to deploy ccluster-ip-operator to %s", c.Name)
}

// undeployIPOperator removes ccluster-ip-operator from c, then the CCLClusterConfig CRD
// the chart keeps (helm.sh/resource-policy: keep), and its namespace.
func undeployIPOperator(c utils.Cluster) {
	By("undeploying ccluster-ip-operator from " + c.Name)
	_, _ = utils.Run(exec.Command("make", "undeploy-ip", "KUBE_CONTEXT_IP="+c.Context()))
	_, _ = utils.Run(c.Kubectl("delete", "crd", "cclclusterconfigs.ccluster.nvidia.com", "--ignore-not-found"))
	_, _ = utils.Run(c.Kubectl("delete", "ns", ipNamespace, "--ignore-not-found"))
}

// spireCRDs are the CRDs the spire_crds chart installs, which its chart keeps on
// uninstall.
var spireCRDs = []string{
	"clusterfederatedtrustdomains.spire.spiffe.io",
	"clusterspiffeids.spire.spiffe.io",
	"clusterstaticentries.spire.spiffe.io",
}

// installSpireCRDs installs catalogs/cluster_base/spire_crds on c. It is the one
// cluster_base component with a Verifier of its own, so installing it is what turns the
// infra-plane operator's Verified condition True.
func installSpireCRDs(c utils.Cluster) {
	GinkgoHelper()

	By("installing the spire_crds chart on " + c.Name)
	// From a copy, so fetching the upstream dependency leaves no Chart.lock or tarball
	// in the working tree.
	chartDir := filepath.Join(GinkgoT().TempDir(), "spire_crds")
	_, err := utils.Run(exec.Command("cp", "-R", filepath.Join("catalogs", "cluster_base", "spire_crds"), chartDir))
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("helm", "upgrade", "--install", "spire-crds", chartDir,
		"--kube-context", c.Context(), "--dependency-update",
		"--namespace", "spire-system", "--create-namespace", "--wait"))
	Expect(err).NotTo(HaveOccurred(), "Failed to install the spire_crds chart on %s", c.Name)
}

// uninstallSpireCRDs removes what installSpireCRDs installed from c.
func uninstallSpireCRDs(c utils.Cluster) {
	By("removing the spire_crds chart and its CRDs from " + c.Name)
	_, _ = utils.Run(exec.Command("helm", "uninstall", "spire-crds", "--kube-context", c.Context(),
		"--namespace", "spire-system", "--ignore-not-found"))
	_, _ = utils.Run(c.Kubectl(append([]string{"delete", "crd", "--ignore-not-found"}, spireCRDs...)...))
	_, _ = utils.Run(c.Kubectl("delete", "ns", "spire-system", "--ignore-not-found"))
}

// expectOnlySpireCRDsFailing asserts that a Verified message names spire_crds as the
// only failing cluster_base component, for want of its CRDs - "not found" rather than a
// read error, which proves the ip chart's ClusterRole lets the manager read CRDs.
func expectOnlySpireCRDsFailing(g Gomega, message string) {
	g.Expect(message).To(ContainSubstring(
		"failing: catalogs/cluster_base/spire_crds (CRD " + spireCRDs[0] + " not found"))
	g.Expect(strings.Count(message, "catalogs/cluster_base/")).To(Equal(1))
}

// clusterConfigField reads a field of a CCLInfraPlane's copy of its infra plane's
// CCLClusterConfig status, by JSONPath relative to .status.clusterConfig.
func clusterConfigField(planeName, path string) string {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane", planeName,
		"-n", crNamespace, "-o", fmt.Sprintf("jsonpath={.status.clusterConfig%s}", path)))
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// clusterConfigOn reads a field of the CCLClusterConfig singleton on c, by JSONPath.
func clusterConfigOn(c utils.Cluster, path string) (string, error) {
	out, err := utils.Run(c.Kubectl("get", "cclclusterconfig", "cluster", "-o", "jsonpath={"+path+"}"))
	return strings.TrimSpace(out), err
}

// crossClusterTimeout is the patience for infra-plane state that crosses both clusters:
// a write to the remote cluster, a verification there, and the status coming back.
const crossClusterTimeout = 3 * time.Minute

// watchTimeout is the patience for a status change on an infra-plane cluster to reach the
// CCLInfraPlane, which only the remote watch brings over in time. It covers the
// infra-plane operator's one-minute recheck of a failing component and stays well inside
// the cp operator's default --infraplane-resync-period of 5m, so a resync cannot stand in
// for a broken watch.
const watchTimeout = 2 * time.Minute
