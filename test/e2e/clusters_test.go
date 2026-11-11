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

// cclusterBinary is the ccluster CLI, built from cmd/ccluster by BeforeSuite.
var cclusterBinary string

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
// `ccluster register`, exactly as the README documents: a cluster-admin token
// created on that cluster, and its address on Kind's Docker network, where the
// control-plane cluster's Pods reach it.
func registerWithCLI(planeName string) {
	GinkgoHelper()

	ip := utils.IP()
	_, err := utils.Run(cclusterCLI("register", planeName,
		"--namespace", crNamespace,
		"--infra-plane", ip.Context(),
		"--server", ip.Server()))
	Expect(err).NotTo(HaveOccurred(), "ccluster register failed")
	DeferCleanup(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "secret", credentialsSecretName(planeName),
			"-n", crNamespace, "--ignore-not-found"))
	})
}

// cclusterCLI returns the ccluster CLI built by BeforeSuite, run against the suite's
// config file.
func cclusterCLI(args ...string) *exec.Cmd {
	cmd := exec.Command(cclusterBinary, args...)
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

// installClusterBaseChart installs the catalogs/cluster_base chart dir on c as a Helm
// release, with the values file from test/e2e/testdata. kind ships none of the platform,
// and a chart's Verifier looks at live objects, so installing it is what stops the
// infra-plane operator's Verified condition naming that component.
func installClusterBaseChart(c utils.Cluster, dir, release, namespace, valuesFile string, timeout time.Duration) {
	GinkgoHelper()

	By(fmt.Sprintf("installing the %s chart on %s", dir, c.Name))
	// From a copy, so fetching the upstream dependency leaves no Chart.lock or tarball
	// in the working tree.
	chartDir := filepath.Join(GinkgoT().TempDir(), dir)
	_, err := utils.Run(exec.Command("cp", "-R", filepath.Join("catalogs", "cluster_base", dir), chartDir))
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("helm", "upgrade", "--install", release, chartDir,
		"--kube-context", c.Context(), "--dependency-update",
		"--values", filepath.Join("test", "e2e", "testdata", valuesFile),
		"--namespace", namespace, "--create-namespace", "--wait", "--timeout", timeout.String()))
	Expect(err).NotTo(HaveOccurred(), "Failed to install the %s chart on %s", dir, c.Name)
}

// uninstallClusterBaseChart removes the Helm release installClusterBaseChart installed.
func uninstallClusterBaseChart(c utils.Cluster, release, namespace string) {
	By(fmt.Sprintf("removing the %s release from %s", release, c.Name))
	_, _ = utils.Run(exec.Command("helm", "uninstall", release, "--kube-context", c.Context(),
		"--namespace", namespace, "--ignore-not-found", "--wait"))
}

// installClusterBase installs the cluster_base charts that have a Verifier of their own,
// which fail on a bare kind cluster: argo_rollouts, for the Rollout CRD, cert_manager, for
// its admission webhook, metrics_server, for the resource metrics API, and
// kube_prometheus_stack, for the Prometheus Service the catalog's consumers address.
func installClusterBase(c utils.Cluster) {
	GinkgoHelper()

	installClusterBaseChart(c, "argo_rollouts", "argo-rollouts", "argo-rollouts",
		"argo-rollouts-kind-values.yaml", 3*time.Minute)
	installClusterBaseChart(c, "cert_manager", "cert-manager", "cert-manager",
		"cert-manager-kind-values.yaml", 5*time.Minute)
	installClusterBaseChart(c, "metrics_server", "metrics-server", "kube-system",
		"metrics-server-kind-values.yaml", 3*time.Minute)
	installClusterBaseChart(c, "kube_prometheus_stack", "kube-prometheus-stack", "prometheus",
		"kube-prometheus-stack-kind-values.yaml", 5*time.Minute)

	// helm --wait covers only the operator: it creates the Prometheus StatefulSet after
	// helm returns. Waiting here keeps a slow image pull out of the callers' verdict
	// timeouts, where it would look like a broken watch. Eventually, because kubectl wait
	// fails while no pod matches yet.
	By("waiting for the Prometheus pod on " + c.Name)
	Eventually(func(g Gomega) {
		_, err := utils.Run(c.Kubectl("wait", "pod", "--namespace", "prometheus",
			"--selector", "app.kubernetes.io/name=prometheus",
			"--for", "condition=Ready", "--timeout", "30s"))
		g.Expect(err).NotTo(HaveOccurred())
	}, 5*time.Minute, 5*time.Second).Should(Succeed())
}

// argoRolloutsCRDs are the CRDs the argo_rollouts chart installs. It marks them
// helm.sh/resource-policy: keep (the upstream keepCRDs), so they outlive the release.
var argoRolloutsCRDs = []string{
	"analysisruns.argoproj.io",
	"analysistemplates.argoproj.io",
	"clusteranalysistemplates.argoproj.io",
	"experiments.argoproj.io",
	"rollouts.argoproj.io",
}

// uninstallClusterBase removes what installClusterBase installed from c. The CRDs the
// Prometheus chart installed stay, as Helm leaves them, and go with the cluster. The
// Argo Rollouts CRDs are deleted by name, because the argo_rollouts Verifier checks only
// that the Rollout CRD exists: left behind, they would verify argo_rollouts for the
// next spec on the cluster, which expects it to fail. By name, not by group, so the
// control-plane cluster keeps Argo CD's argoproj.io CRDs.
func uninstallClusterBase(c utils.Cluster) {
	uninstallClusterBaseChart(c, "kube-prometheus-stack", "prometheus")
	uninstallClusterBaseChart(c, "metrics-server", "kube-system")
	uninstallClusterBaseChart(c, "cert-manager", "cert-manager")
	uninstallClusterBaseChart(c, "argo-rollouts", "argo-rollouts")
	_, _ = utils.Run(c.Kubectl(append([]string{"delete", "crd", "--ignore-not-found", "--wait"},
		argoRolloutsCRDs...)...))
}

// expectBaseComponentsFailing asserts that a Verified message names argo_rollouts,
// cert_manager, metrics_server and kube_prometheus_stack among the failing cluster_base
// components, for want of what they provide on a bare kind cluster: the Rollout CRD,
// cert-manager's webhook, the APIService and the Prometheus Service. "Not found" rather
// than a read error proves the ip chart's ClusterRole lets the manager read
// CustomResourceDefinitions, ValidatingWebhookConfigurations, APIServices and Services.
// Other failing components are not asserted on, so a cluster_base chart gaining a check
// of its own does not break the suite.
func expectBaseComponentsFailing(g Gomega, message string) {
	g.Expect(message).To(ContainSubstring(
		"catalogs/cluster_base/argo_rollouts (CustomResourceDefinition rollouts.argoproj.io not found"))
	g.Expect(message).To(ContainSubstring(
		"catalogs/cluster_base/cert_manager (no ValidatingWebhookConfiguration has webhook webhook.cert-manager.io"))
	g.Expect(message).To(ContainSubstring(
		"catalogs/cluster_base/metrics_server (APIService v1beta1.metrics.k8s.io not found"))
	g.Expect(message).To(ContainSubstring(
		"catalogs/cluster_base/kube_prometheus_stack (no Service prometheus/prometheus-op-kube-prometh-prometheus found"))
}

// expectBaseComponentsVerified asserts that a Verified message no longer names
// argo_rollouts, cert_manager, metrics_server or kube_prometheus_stack as failing, once installClusterBase has
// installed them. It says nothing about the other components, but an empty message,
// such as a copy that lost its conditions, fails.
//
// This, not expectBaseComponentsFailing, proves the ip chart's ClusterRole lets the
// manager read EndpointSlices: the Prometheus Verifier lists them only once the Service
// exists. Without the permission the cached List times out and kube_prometheus_stack
// stays in the message.
func expectBaseComponentsVerified(g Gomega, message string) {
	g.Expect(message).NotTo(BeEmpty())
	g.Expect(message).NotTo(ContainSubstring("catalogs/cluster_base/argo_rollouts"))
	g.Expect(message).NotTo(ContainSubstring("catalogs/cluster_base/cert_manager"))
	g.Expect(message).NotTo(ContainSubstring("catalogs/cluster_base/metrics_server"))
	g.Expect(message).NotTo(ContainSubstring("catalogs/cluster_base/kube_prometheus_stack"))
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

// ingressLoadBalancerIP is the address createIngressLoadBalancer assigns, from the
// documentation range (RFC 5737).
const ingressLoadBalancerIP = "192.0.2.10"

// createIngressLoadBalancer creates, in c's default namespace, a LoadBalancer Service
// labelled as the cluster's ingress, as the catalog's ingress_nginx chart does, and sets
// the address a cloud provider would. The Service is deleted when the spec ends.
func createIngressLoadBalancer(c utils.Cluster, name, ip string) {
	GinkgoHelper()

	service := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: default
  labels:
    ccluster.nvidia.com/loadbalancer: nginx
spec:
  type: LoadBalancer
  ports:
    - name: http
      port: 80
`, name)
	apply := c.Kubectl("apply", "-f", "-")
	apply.Stdin = strings.NewReader(service)
	_, err := utils.Run(apply)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		_, err := utils.Run(c.Kubectl("delete", "service", name, "-n", "default", "--ignore-not-found"))
		Expect(err).NotTo(HaveOccurred())
	})

	_, err = utils.Run(c.Kubectl("patch", "service", name, "-n", "default", "--subresource=status",
		"--type=merge", "-p", fmt.Sprintf(`{"status":{"loadBalancer":{"ingress":[{"ip":%q}]}}}`, ip)))
	Expect(err).NotTo(HaveOccurred())
}
