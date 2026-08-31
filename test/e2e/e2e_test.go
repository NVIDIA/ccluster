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
		// ARGOCD defaults to true: this suite installs the real Argo CD dependency
		// alongside the operator, which is what lets the specs below exercise an
		// actual rollout - Application CRUD under the operator's real RBAC, and Argo
		// CD reconciling one to Healthy/Synced. `helm --wait` therefore blocks on
		// pulling and starting every Argo CD component's images, so this deploy gets
		// a longer timeout than the operator alone would need.
		By("deploying the controller-manager and Argo CD")
		cmd = exec.Command("make", "deploy-cp", fmt.Sprintf("IMG_CP=%s", managerImage), "HELM_TIMEOUT=10m")
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

		// envtest runs the reconciler as the test binary's own identity, so a missing
		// +kubebuilder:rbac marker is invisible there; here the manager runs under
		// its ServiceAccount, from the built image, with the chart's Role. The
		// mirroring path was chosen because it touches the widest CCLInfraPlane
		// permission surface: get/list/watch, its status subresource, and Secret
		// get/list/watch/create/update. The equivalent CCLDeployment coverage -
		// including the `argoproj.io/applications` RBAC rule - is the real-rollout
		// spec further down, which needs Argo CD actually installed.
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

		// This spec exists for RBAC coverage of the CCLDeployment controller that the
		// mirror spec above does not touch: ccldeployments get/list/watch,
		// ccldeployments/status patch, and ccldeployments/finalizers update. It
		// deliberately asserts only the one condition reachable without a real
		// infra-plane target. The rollout itself - wave ordering, drift restore,
		// reverse teardown - is covered by envtest and the unit tier, which reach far
		// more states than a cluster run ever could; the real-Argo-CD spec further
		// down covers just enough of a genuine rollout to prove RBAC and reconciliation
		// actually work end to end.

		It("should report InfraPlaneNotFound for a CCLDeployment whose plane is missing", func() {
			const deploymentName = "e2e-missing-plane"

			By("creating a CCLDeployment referencing a CCLInfraPlane that does not exist")
			applyCCLDeployment(deploymentName, "no-such-plane")

			By("waiting for the operator to reject it via the status subresource")
			Eventually(func(g Gomega) {
				g.Expect(deploymentCondition(deploymentName, conditionValidated, "status")).
					To(Equal("False"))
				g.Expect(deploymentCondition(deploymentName, conditionValidated, "reason")).
					To(Equal("InfraPlaneNotFound"))
			}, 2*time.Minute).Should(Succeed())

			By("verifying the operator could add its finalizer")
			out, err := utils.Run(exec.Command("kubectl", "get", "ccldeployment",
				deploymentName, "-n", "default", "-o", "jsonpath={.metadata.finalizers}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("ccluster.nvidia.com/argocd-applications"))

			// Inline, not DeferCleanup: see the note in the next spec. A finalized CR
			// outliving the operator wedges the CRD and hangs `make uninstall`.
			By("deleting the CCLDeployment while the operator can still run its finalizer")
			deleteCCLDeployment(deploymentName)
		})

		// The specs above never touch a real Application: nothing creates one under
		// the operator's own RBAC, and nothing exercises the rest of a rollout - Argo
		// CD actually cloning the catalog, installing the chart, and the operator
		// reading Application health back. This suite installs real Argo CD (see
		// BeforeAll), so this spec registers the control-plane cluster with itself -
		// the same trick config/samples/v1alpha1_cclinfraplane.yaml documents - and
		// drives one catalog component all the way to Healthy/Synced, which is what
		// `make test-e2e-argocd` never actually did before it was retired: that
		// target only asserted the argoproj CRDs existed, not that a rollout worked.
		It("should roll out a real catalog component under real Argo CD reconciliation", func() {
			const deploymentName = "e2e-real-rollout"
			const planeName = "e2e-real-rollout-plane"
			const credentialsName = "e2e-real-rollout-plane-credentials"
			applicationName := deploymentName + "-argo-rollouts"

			// server must be exactly this string for Argo CD to connect using its own
			// ServiceAccount rather than credentials in "config" - see the sample
			// this mirrors, config/samples/v1alpha1_cclinfraplane.yaml.
			By("registering the control-plane cluster with itself as an infra plane")
			_, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic",
				credentialsName,
				"--from-literal=server=https://kubernetes.default.svc",
				`--from-literal=config={"tlsClientConfig":{"insecure":false}}`,
				"-n", "default"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", "default", "--ignore-not-found"))
			})

			applyCCLInfraPlane(planeName, credentialsName)
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					planeName, "-n", "default", "-o",
					"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 2*time.Minute).Should(Succeed())

			By("creating a CCLDeployment against it")
			applyCCLDeployment(deploymentName, planeName)

			By("waiting for the operator to create the Application under its own RBAC")
			Eventually(func(g Gomega) {
				g.Expect(deploymentCondition(deploymentName, conditionComponentsApplied, "reason")).
					To(Equal("ApplicationsCreated"))
			}, 2*time.Minute).Should(Succeed())

			By("verifying the Application carries the expected ownership back-reference")
			out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
				"-n", namespace, "-o",
				"jsonpath={.metadata.labels['ccluster\\.nvidia\\.com/deployment-name']}"))
			Expect(err).NotTo(HaveOccurred(), "Application was not created")
			Expect(strings.TrimSpace(out)).To(Equal(deploymentName))

			By("waiting for Argo CD to actually reconcile it to Healthy and Synced")
			Eventually(func(g Gomega) {
				health, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
					"-n", namespace, "-o", "jsonpath={.status.health.status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(health)).To(Equal("Healthy"))

				sync, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
					"-n", namespace, "-o", "jsonpath={.status.sync.status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(sync)).To(Equal("Synced"))
			}, 8*time.Minute).Should(Succeed())

			By("verifying the operator's Progressing condition reflects a completed rollout")
			Eventually(func(g Gomega) {
				g.Expect(deploymentCondition(deploymentName, conditionProgressing, "status")).
					To(Equal("False"))
				g.Expect(deploymentCondition(deploymentName, conditionProgressing, "reason")).
					To(Equal("RolloutComplete"))
			}, 2*time.Minute).Should(Succeed())

			// Deleted inline, not via DeferCleanup: each CR's finalizer needs the
			// operator running to clear it, and DeferCleanup runs after AfterAll has
			// already undeployed it. Teardown here is a real cascade through Argo CD -
			// deleting the CCLDeployment deletes the Application, whose
			// resources-finalizer.argocd.argoproj.io finalizer makes Argo CD
			// uninstall the chart before the Application itself goes away - so this
			// gets a longer timeout than the status-only specs above.
			By("deleting the CCLDeployment and CCLInfraPlane, letting Argo CD uninstall the chart")
			_, err = utils.Run(exec.Command("kubectl", "delete", "ccldeployment", deploymentName,
				"-n", "default", "--ignore-not-found", "--wait=true", "--timeout=5m"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				planeName, "-n", "default", "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the operator deleted the Application")
			_, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName, "-n", namespace))
			Expect(err).To(HaveOccurred(), "Application was not cleaned up")
		})
	})
})

// conditionValidated, conditionComponentsApplied and conditionProgressing are the
// CCLDeployment condition types the e2e specs assert on.
const (
	conditionValidated         = "Validated"
	conditionComponentsApplied = "ComponentsApplied"
	conditionProgressing       = "Progressing"
)

// applyCCLInfraPlane creates a CCLInfraPlane in the default namespace.
func applyCCLInfraPlane(name, credentialsName string) {
	GinkgoHelper()

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLInfraPlane
metadata:
  name: %s
  namespace: default
spec:
  clusterCredentialsRef:
    name: %s
`, name, credentialsName))
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create CCLInfraPlane %s", name)
}

// applyCCLDeployment creates a single-component CCLDeployment targeting planeName.
func applyCCLDeployment(name, planeName string) {
	GinkgoHelper()

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`apiVersion: ccluster.nvidia.com/v1alpha1
kind: CCLDeployment
metadata:
  name: %s
  namespace: default
spec:
  components:
    - name: argo-rollouts
      infraPlaneRef:
        name: %s
      chart: catalogs/cluster_base/argo_rollouts
      namespace: argo-rollouts
`, name, planeName))
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create CCLDeployment %s", name)
}

// deleteCCLDeployment removes a CCLDeployment and waits for its finalizer to clear.
func deleteCCLDeployment(name string) {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "delete", "ccldeployment", name,
		"-n", "default", "--ignore-not-found", "--wait=true", "--timeout=2m"))
	Expect(err).NotTo(HaveOccurred(), "Failed to delete CCLDeployment %s", name)
}

// deploymentCondition reads one field of one status condition off a CCLDeployment.
func deploymentCondition(name, conditionType, field string) string {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "ccldeployment", name,
		"-n", "default", "-o", fmt.Sprintf(
			"jsonpath={.status.conditions[?(@.type=='%s')].%s}", conditionType, field)))
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

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
