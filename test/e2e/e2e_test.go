//go:build e2e
// +build e2e

/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/base64"
	"encoding/json"
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

// namespace where the project is deployed in
const namespace = "ccluster-cp-system"

// serviceAccountName created for the project
const serviceAccountName = "ccluster-cp-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "ccluster-cp-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "ccluster-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		// No `make install` here: the chart owns the CRDs, and Helm refuses to adopt
		// CRDs that already exist without its ownership metadata.
		//
		// ARGOCD=false: the cp chart pulls in Argo CD, whose images are not loaded
		// into this kind cluster. Installing it here would make `helm --wait` block
		// on several image pulls from the internet, so this suite covers the operator
		// only. `make test-e2e-argocd` exercises the chart with Argo CD enabled.
		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy-cp", fmt.Sprintf("IMG_CP=%s", managerImage), "ARGOCD=false")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy-cp")
		_, _ = utils.Run(cmd)

		// The chart keeps its CRDs on uninstall (helm.sh/resource-policy: keep), so
		// delete them explicitly to leave the cluster clean for a rerun.
		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall", "ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=ccluster-cp-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// This is the only assertion in the repository that exercises the operator's
		// real RBAC. envtest runs the reconciler as the test binary's own identity, so
		// a missing +kubebuilder:rbac marker is invisible there; here the manager runs
		// under its ServiceAccount, from the built image, with the chart's Role. The
		// mirroring path was chosen because it touches the widest permission surface:
		// CCLInfraPlane get/list/watch, its status subresource, and Secret
		// get/list/watch/create/update.
		//
		// Deliberately no CCLDeployment equivalent: this suite runs with ARGOCD=false,
		// so the Application CRD does not exist and a rollout could never advance. That
		// belongs in `make test-e2e-argocd`, if anywhere.
		It("should mirror an infra plane's credentials Secret using the operator's own RBAC", func() {
			const infraPlaneName = "e2e-infra-plane"
			const credentialsName = "e2e-infra-plane-credentials"

			// deploy-cp overrides --argocd-namespace to the release namespace, so the
			// mirror lands alongside the manager rather than in `argocd`.
			const mirrorNamespace = namespace

			By("creating the cluster credentials Secret the CCLInfraPlane references")
			cmd := exec.Command("kubectl", "create", "secret", "generic", credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", "default")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the credentials Secret")

			DeferCleanup(func() {
				By("cleaning up the credentials Secret")
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", "default", "--ignore-not-found"))
			})

			By("creating the CCLInfraPlane")
			manifest := fmt.Sprintf(`apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLInfraPlane
metadata:
  name: %s
  namespace: default
spec:
  clusterCredentialsRef:
    name: %s
`, infraPlaneName, credentialsName)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the CCLInfraPlane")

			By("waiting for the operator to mirror the Secret into the Argo CD namespace")
			verifyMirrored := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "secret", infraPlaneName,
					"-n", mirrorNamespace, "-o", "jsonpath={.data.name}"))
				g.Expect(err).NotTo(HaveOccurred(), "Mirrored Secret not created")

				decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
				g.Expect(err).NotTo(HaveOccurred())
				// The operator owns this key and overwrites whatever the source held.
				g.Expect(string(decoded)).To(Equal(infraPlaneName))
			}
			Eventually(verifyMirrored, 2*time.Minute).Should(Succeed())

			By("verifying the operator could patch the CCLInfraPlane status subresource")
			verifyAvailable := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					infraPlaneName, "-n", "default", "-o",
					"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}
			Eventually(verifyAvailable, 2*time.Minute).Should(Succeed())

			// Delete the CCLInfraPlane here, in the spec body, rather than from
			// DeferCleanup. The CCLInfraPlane carries a finalizer that only the
			// operator can remove, and AfterAll undeploys the operator and then
			// deletes the CRDs. A finalized CR outliving its controller wedges the
			// CRD in Terminating and `make uninstall` blocks forever, so the delete
			// has to complete while the manager is still running.
			By("deleting the CCLInfraPlane while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				infraPlaneName, "-n", "default", "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the CCLInfraPlane")

			By("verifying the operator removed the mirrored Secret and the finalizer")
			verifyGone := func(g Gomega) {
				_, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					infraPlaneName, "-n", "default"))
				g.Expect(err).To(HaveOccurred(), "CCLInfraPlane still present")

				_, err = utils.Run(exec.Command("kubectl", "get", "secret",
					infraPlaneName, "-n", mirrorNamespace))
				g.Expect(err).To(HaveOccurred(), "Mirrored Secret was not cleaned up")
			}
			Eventually(verifyGone, 2*time.Minute).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
