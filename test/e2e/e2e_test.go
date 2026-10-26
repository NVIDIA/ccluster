//go:build e2e
// +build e2e

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
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
	"text/template"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cclv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"

	"github.com/NVIDIA/ccluster/test/utils"
)

// namespace where the project is deployed in
const namespace = "ccluster-cp-system"

// crNamespace is where the specs create their CCLInfraPlanes and CCLComponentSets, and
// therefore the name of the Argo CD AppProject the operator creates for them and the
// namespace it writes their Applications into. Not `default`: that would make the operator
// adopt and rewrite Argo CD's built-in `default` project, and an e2e run should not depend
// on that behaviour. Created in BeforeAll and deleted in AfterAll.
const crNamespace = "ccluster-e2e"

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

		By("creating the namespace the CCLInfraPlanes and CCLComponentSets live in")
		cmd = exec.Command("kubectl", "create", "ns", crNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the CR namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		// No `make install` here: the chart owns the CRDs, and Helm refuses to adopt
		// CRDs that already exist without its ownership metadata.
		//
		// Always without Argo CD, whichever tier is running: the quick specs need
		// the Application CRD genuinely absent when the manager starts, and they run
		// in both tiers. The full tier adds Argo CD later, in the BeforeAll of the
		// "with real Argo CD" container below, once the quick specs are done. The
		// Makefile's -ginkgo.label-filter decides whether that container runs at all
		// - see the E2E_FULL comment there.
		By("deploying the controller-manager without Argo CD")
		cmd = exec.Command("make", "deploy-cp", fmt.Sprintf("IMG_CP=%s", managerImage), "ARGOCD=false")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		// Only now: init checks that the cluster serves CCLInfraPlane, whose CRD the cp
		// chart just installed.
		By("pointing the ccluster CLI at the control-plane cluster")
		_, err = utils.Run(cclusterCLI("init", "--control-plane", utils.CP().Context()))
		Expect(err).NotTo(HaveOccurred(), "ccluster init failed")
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

		// Safety net, not the normal path: every spec above deletes its own CRs
		// inline so its finalizer clears while the operator is still running. But a
		// spec that fails mid-assertion skips that inline delete entirely (Gomega's
		// Fail unwinds the rest of the It body), which would otherwise leave a
		// finalized CR behind that `make uninstall` then waits on forever, with no
		// controller left to ever clear it.
		//
		// This has to run after undeploy-cp above, not before: stripping a
		// finalizer while the operator is still running races its reconcile loop,
		// which re-adds the finalizer on its next pass as a matter of course
		// (ensuring it's present is part of normal reconcile, independent of
		// whether anything is deleting the object yet) - so a strip issued too
		// early can lose the race and be back in place by the time deletion
		// actually starts. With the operator gone, nothing can re-add anything.
		By("force-clearing finalizers from any CRs a failed spec left behind")
		for _, resource := range []string{
			"cclcomponentsets", "cclinfraplanes", "applications.argoproj.io", "appprojects.argoproj.io",
		} {
			out, err := utils.Run(exec.Command("kubectl", "get", resource, "-A", "-o",
				"jsonpath={range .items[*]}{.metadata.namespace}{\" \"}{.metadata.name}{\"\\n\"}{end}"))
			if err != nil {
				continue // the CRD may not exist at all, e.g. Application in the quick tier
			}
			for _, line := range utils.GetNonEmptyLines(out) {
				fields := strings.Fields(line)
				if len(fields) != 2 {
					continue
				}
				_, _ = utils.Run(exec.Command("kubectl", "patch", resource, fields[1],
					"-n", fields[0], "--type=merge", "-p", `{"metadata":{"finalizers":[]}}`))
			}
		}

		// The chart keeps its CRDs on uninstall (helm.sh/resource-policy: keep), so
		// delete them explicitly to leave the cluster clean for a rerun. Only the cp
		// chart's: this cluster also runs the infra-plane operator until AfterSuite,
		// which removes the CCLClusterConfig CRD with it.
		By("uninstalling the cp chart's CRDs")
		cmd = exec.Command("kubectl", "delete", "crd", "--ignore-not-found",
			"cclinfraplanes.ccluster.nvidia.com", "cclcomponentsets.ccluster.nvidia.com")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)

		By("removing the CR namespace")
		cmd = exec.Command("kubectl", "delete", "ns", crNamespace)
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
		// get/list/watch/create/update. The equivalent CCLComponentSet coverage -
		// including the `argoproj.io/applications` RBAC rule - is the real-rollout
		// spec further down, which needs Argo CD actually installed.
		It("should mirror an infra plane's credentials Secret using the operator's own RBAC", func() {
			const infraPlaneName = "e2e-infra-plane"
			const credentialsName = "e2e-infra-plane-credentials"

			// deploy-cp leaves --argocd-namespace unset, so the operator uses its own
			// namespace and the mirror lands alongside the manager.
			const mirrorNamespace = namespace

			By("creating the cluster credentials Secret the CCLInfraPlane references")
			cmd := exec.Command("kubectl", "create", "secret", "generic", credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", crNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the credentials Secret")

			DeferCleanup(func() {
				By("cleaning up the credentials Secret")
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", crNamespace, "--ignore-not-found"))
			})

			By("creating the CCLInfraPlane")
			applyCCLInfraPlane(infraPlaneName, credentialsName)

			// The mirror is named, and registers the cluster as, <namespace>-<name>.
			mirrorName := crNamespace + "-" + infraPlaneName

			By("waiting for the operator to mirror the Secret into the Argo CD namespace")
			verifyMirrored := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "secret", mirrorName,
					"-n", mirrorNamespace, "-o", "jsonpath={.data.name} {.data.project}"))
				g.Expect(err).NotTo(HaveOccurred(), "Mirrored Secret not created")

				fields := strings.Fields(out)
				g.Expect(fields).To(HaveLen(2))
				// The operator owns both keys and overwrites whatever the source held:
				// the cluster name, and the AppProject the cluster is scoped to.
				name, err := base64.StdEncoding.DecodeString(fields[0])
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(name)).To(Equal(mirrorName))
				project, err := base64.StdEncoding.DecodeString(fields[1])
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(project)).To(Equal(crNamespace))
			}
			Eventually(verifyMirrored, 2*time.Minute).Should(Succeed())

			By("verifying the operator could patch the CCLInfraPlane status subresource")
			verifyAvailable := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					infraPlaneName, "-n", crNamespace, "-o",
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
				infraPlaneName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the CCLInfraPlane")

			By("verifying the operator removed the mirrored Secret and the finalizer")
			verifyGone := func(g Gomega) {
				_, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					infraPlaneName, "-n", crNamespace))
				g.Expect(err).To(HaveOccurred(), "CCLInfraPlane still present")

				_, err = utils.Run(exec.Command("kubectl", "get", "secret",
					mirrorName, "-n", mirrorNamespace))
				g.Expect(err).To(HaveOccurred(), "Mirrored Secret was not cleaned up")
			}
			Eventually(verifyGone, 2*time.Minute).Should(Succeed())
		})

		// envtest never starts a controller - every spec there calls Reconcile directly,
		// so the Watches on Secrets are exercised only as plain map functions and never
		// as live event sources. This is the one place a running manager exists, so
		// it is the one place the day-2 path can be proven: a source Secret edited
		// after the fact reaches the mirror, and a mirror deleted behind the
		// operator's back is put back. Neither involves creating or deleting a CR.
		It("should propagate source Secret changes to the mirror and re-create a deleted mirror", func() {
			const infraPlaneName = "e2e-secret-watch-plane"
			const credentialsName = "e2e-secret-watch-plane-credentials"
			const mirrorNamespace = namespace
			mirrorName := crNamespace + "-" + infraPlaneName

			By("creating the cluster credentials Secret and the CCLInfraPlane")
			_, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic", credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", crNamespace, "--ignore-not-found"))
			})
			applyCCLInfraPlane(infraPlaneName, credentialsName)
			waitInfraPlaneAvailable(infraPlaneName)

			By("waiting for the initial mirror")
			Eventually(func(g Gomega) {
				g.Expect(mirrorSecretKey(mirrorName, "server")).
					To(Equal("https://infra.example.com:6443"))
			}, 2*time.Minute).Should(Succeed())

			// The source-Secret watch: the CCLInfraPlane itself is untouched, so the
			// only thing that can enqueue a reconcile is the Secret event.
			By("changing the source Secret and waiting for the mirror to follow")
			_, err = utils.Run(exec.Command("kubectl", "patch", "secret", credentialsName, "-n", crNamespace,
				"--type=merge", "-p", `{"stringData":{"server":"https://changed.example.com:6443"}}`))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				g.Expect(mirrorSecretKey(mirrorName, "server")).
					To(Equal("https://changed.example.com:6443"))
				// The operator-owned keys survive the rewrite.
				g.Expect(mirrorSecretKey(mirrorName, "name")).To(Equal(mirrorName))
				g.Expect(mirrorSecretKey(mirrorName, "project")).To(Equal(crNamespace))
			}, 2*time.Minute).Should(Succeed())

			// The mirror-Secret watch: a delete event on an annotated Secret in the Argo
			// CD namespace maps back to its CCLInfraPlane, which recreates it.
			By("deleting the mirror and waiting for the operator to re-create it")
			_, err = utils.Run(exec.Command("kubectl", "delete", "secret", mirrorName, "-n", mirrorNamespace))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				g.Expect(mirrorSecretKey(mirrorName, "name")).To(Equal(mirrorName))
				g.Expect(mirrorSecretKey(mirrorName, "server")).
					To(Equal("https://changed.example.com:6443"))
			}, 2*time.Minute).Should(Succeed())

			By("deleting the CCLInfraPlane while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				infraPlaneName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())
		})

		// This spec exists for RBAC coverage of the CCLComponentSet controller that the
		// mirror spec above does not touch: cclcomponentsets get/list/watch,
		// cclcomponentsets/status patch, and cclcomponentsets/finalizers update. It
		// deliberately asserts only the one condition reachable without a real
		// infra-plane target. The rollout itself - wave ordering, drift restore,
		// reverse teardown - is covered by envtest and the unit tier, which reach far
		// more states than a cluster run ever could; the real-Argo-CD spec further
		// down covers just enough of a genuine rollout to prove RBAC and reconciliation
		// actually work end to end.

		It("should report InfraPlaneNotFound for a CCLComponentSet whose plane is missing", func() {
			const componentSetName = "e2e-missing-plane"

			By("creating a CCLComponentSet referencing a CCLInfraPlane that does not exist")
			applyCCLComponentSet(componentSetName, "no-such-plane")

			By("waiting for the operator to reject it via the status subresource")
			Eventually(func(g Gomega) {
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "status")).
					To(Equal("False"))
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "reason")).
					To(Equal("InfraPlaneNotFound"))
			}, 2*time.Minute).Should(Succeed())

			By("verifying the operator could add its finalizer")
			out, err := utils.Run(exec.Command("kubectl", "get", "cclcomponentset",
				componentSetName, "-n", crNamespace, "-o", "jsonpath={.metadata.finalizers}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("ccluster.nvidia.com/argocd-applications"))

			// Inline, not DeferCleanup: see the note in the next spec. A finalized CR
			// outliving the operator wedges the CRD and hangs `make uninstall`.
			By("deleting the CCLComponentSet while the operator can still run its finalizer")
			deleteCCLComponentSet(componentSetName)
		})

		// The CCLComponentSet reconciler watches CCLInfraPlanes and fans out to the
		// CCLComponentSets that reference them (componentSetsForInfraPlane). envtest
		// exercises that map function directly; only a running manager proves the
		// event actually arrives. Create the CCLComponentSet first, then its plane, and
		// never touch the CCLComponentSet again - the only thing that can move it from
		// InfraPlaneNotFound to Validated is the plane's own event.
		It("should unblock a CCLComponentSet created before its infra plane once the plane appears", func() {
			const componentSetName = "e2e-plane-later"
			const planeName = "e2e-plane-later-plane"
			const credentialsName = "e2e-plane-later-plane-credentials"

			By("creating the CCLComponentSet before its plane exists")
			applyCCLComponentSet(componentSetName, planeName)
			Eventually(func(g Gomega) {
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "reason")).
					To(Equal("InfraPlaneNotFound"))
			}, 2*time.Minute).Should(Succeed())

			// The control-plane cluster itself, not a fake endpoint: in the full tier
			// Argo CD really deploys this component and really has to reach the
			// destination to run the cascade teardown on delete. An unreachable
			// cluster would leave the Application stuck on its finalizer.
			By("creating the plane it references")
			createInClusterCredentials(crNamespace, credentialsName)
			applyCCLInfraPlane(planeName, credentialsName)

			// Only Validated is asserted: ComponentsApplied reads ApplicationCRDMissing
			// in the quick tier and ApplicationsCreated in the full one, and this spec
			// runs in both.
			By("waiting for the untouched CCLComponentSet to become Validated")
			Eventually(func(g Gomega) {
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "status")).To(Equal("True"))
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "reason")).
					To(Equal("ComponentsAccepted"))
			}, 2*time.Minute).Should(Succeed())

			By("deleting both CRs while the operator can still run their finalizers")
			// In the full tier an Application was created; its cascade teardown through
			// Argo CD is what the longer timeout is for.
			_, err := utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
				"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())
		})

		// The operator writes the CCLClusterConfig singleton into each infra plane and
		// watches it; the infra-plane operator there verifies the cluster against it, and
		// its verdict and the cluster's ingress LoadBalancer come back onto the
		// CCLInfraPlane. Both targets run the same spec:
		// in-cluster proves the cp operator's own RBAC covers CCLClusterConfig, ex-cluster
		// that a `ccluster register` token reaches another cluster over the network. The
		// verdict flipping only after spire_crds is installed there, well inside
		// --infraplane-resync-period, proves the remote watch is wired to the controller.
		//
		// ex-cluster is also registered before the ip chart is installed on it, the order
		// a user who runs `ccluster register` first meets. Only a real API server proves
		// that a missing CRD reads as CRDMissing, and that the remote watch, which cannot
		// start without it, starts once it is installed and delivers the verdict.
		for _, t := range targets() {
			It("should write the CCLClusterConfig on the "+t.name+" infra plane and sync its verification back", func() {
				planeName := "e2e-cluster-config-" + t.name
				beforeIPChart := t.name == exCluster().name

				if beforeIPChart {
					undeployIPOperator(t.cluster)
					DeferCleanup(func() {
						if CurrentSpecReport().Failed() {
							deployIPOperator(t.cluster)
						}
					})
				}

				By("registering the " + t.name + " plane")
				t.register(planeName)

				if beforeIPChart {
					By("reporting CRDMissing while the ip chart is not installed")
					Eventually(func(g Gomega) {
						g.Expect(infraPlaneCondition(planeName, conditionAvailable, "status")).To(Equal("True"))
						g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "reason")).
							To(Equal("CRDMissing"))
					}, crossClusterTimeout).Should(Succeed())

					deployIPOperator(t.cluster)
				}

				// After a late CRD install the operator notices it through its retry of
				// a missing CRD, every minute: nothing it watches changes.
				By("writing the CCLClusterConfig on the plane's cluster with the plane's domain")
				Eventually(func(g Gomega) {
					g.Expect(infraPlaneCondition(planeName, conditionAvailable, "status")).To(Equal("True"))
					g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "status")).To(Equal("True"),
						infraPlaneCondition(planeName, conditionClusterConfigApplied, "message"))
					domain, err := clusterConfigOn(t.cluster, ".spec.domain")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(domain).To(Equal(infraPlaneDomain(planeName)))
					owner, err := clusterConfigOn(t.cluster, `.metadata.annotations.ccluster\.nvidia\.com/infraplane-name`)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(owner).To(Equal(planeName))
				}, crossClusterTimeout).Should(Succeed())

				By("copying the infra-plane operator's verdict onto the CCLInfraPlane")
				Eventually(func(g Gomega) {
					g.Expect(clusterConfigField(planeName, `.conditions[?(@.type=="Verified")].status`)).To(Equal("False"))
					expectOnlySpireCRDsFailing(g,
						clusterConfigField(planeName, `.conditions[?(@.type=="Verified")].message`))
				}, watchTimeout).Should(Succeed())

				installSpireCRDs(t.cluster)
				DeferCleanup(uninstallSpireCRDs, t.cluster)

				// The infra-plane operator rechecks a failing component every minute.
				By("following the verdict as it changes on the plane's cluster")
				Eventually(func(g Gomega) {
					g.Expect(clusterConfigField(planeName, `.conditions[?(@.type=="Verified")].status`)).To(Equal("True"),
						clusterConfigField(planeName, `.conditions[?(@.type=="Verified")].message`))
				}, watchTimeout).Should(Succeed())

				By("reporting no ingress LoadBalancer while the plane has none")
				Expect(clusterConfigField(planeName, ".loadBalancers")).To(BeEmpty())

				// kind has no load balancer controller, so the spec assigns the address
				// itself. The infra-plane operator watches the labelled Service, so the
				// address reaches the CCLInfraPlane without waiting for a periodic pass.
				By("following the ingress LoadBalancer's IP onto the CCLInfraPlane")
				createIngressLoadBalancer(t.cluster, "e2e-ingress", ingressLoadBalancerIP)
				Eventually(func(g Gomega) {
					g.Expect(clusterConfigField(planeName, `.loadBalancers[?(@.name=="ingress")].ip`)).
						To(Equal(ingressLoadBalancerIP))
				}, crossClusterTimeout).Should(Succeed())

				// The suite deploys the cp chart as the quickstart does, without a parent
				// hosted zone, so the operator calls no AWS API.
				By("reporting DNS as not configured, with an ingress address to publish")
				Expect(infraPlaneCondition(planeName, conditionDNSReady, "status")).To(Equal("False"))
				Expect(infraPlaneCondition(planeName, conditionDNSReady, "reason")).To(Equal("NotConfigured"))

				By("deleting the CCLClusterConfig from the plane's cluster with the CCLInfraPlane")
				deleteInfraPlane(planeName)
				out, err := utils.Run(t.cluster.Kubectl("get", "cclclusterconfig", "--ignore-not-found", "-o", "name"))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(BeEmpty())
			})
		}

		// ex-cluster only: the in-cluster plane has no token, and the cp operator writes
		// it as itself. Swapping a registered plane's token for one with no rights on the
		// cluster proves the write runs as the Secret's identity, not the operator's -
		// and that a credentials change reaches it.
		It("should write the CCLClusterConfig as the registered token's identity", func() {
			t := exCluster()
			planeName := "e2e-token-identity-" + t.name
			const serviceAccount = "e2e-unprivileged"
			const clusterRole = "e2e-cclclusterconfig-writer"

			By("registering the " + t.name + " plane")
			t.register(planeName)
			Eventually(func(g Gomega) {
				g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "status")).To(Equal("True"))
			}, crossClusterTimeout).Should(Succeed())

			By("creating a ServiceAccount with no rights on the plane's cluster")
			_, err := utils.Run(t.cluster.Kubectl("create", "serviceaccount", serviceAccount, "-n", "default"))
			Expect(err).NotTo(HaveOccurred())
			tokenSecret := t.cluster.Kubectl("apply", "-f", "-")
			tokenSecret.Stdin = strings.NewReader(fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %[1]s-token
  namespace: default
  annotations:
    kubernetes.io/service-account.name: %[1]s
type: kubernetes.io/service-account-token
`, serviceAccount))
			_, err = utils.Run(tokenSecret)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(t.cluster.Kubectl("delete", "clusterrolebinding", clusterRole, "--ignore-not-found"))
				_, _ = utils.Run(t.cluster.Kubectl("delete", "clusterrole", clusterRole, "--ignore-not-found"))
				_, _ = utils.Run(t.cluster.Kubectl("delete", "secret", serviceAccount+"-token",
					"-n", "default", "--ignore-not-found"))
				_, _ = utils.Run(t.cluster.Kubectl("delete", "serviceaccount", serviceAccount,
					"-n", "default", "--ignore-not-found"))
			})
			var token []byte
			Eventually(func(g Gomega) {
				out, err := utils.Run(t.cluster.Kubectl("get", "secret", serviceAccount+"-token", "-n", "default",
					"-o", "jsonpath={.data.token}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "token not issued yet")
				token, err = base64.StdEncoding.DecodeString(strings.TrimSpace(out))
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("swapping the plane's token for the unprivileged one")
			out, err := utils.Run(exec.Command("kubectl", "get", "secret", credentialsSecretName(planeName),
				"-n", crNamespace, "-o", "jsonpath={.data.config}"))
			Expect(err).NotTo(HaveOccurred())
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
			Expect(err).NotTo(HaveOccurred())
			var config map[string]any
			Expect(json.Unmarshal(raw, &config)).To(Succeed())
			config["bearerToken"] = string(token)
			swapped, err := json.Marshal(config)
			Expect(err).NotTo(HaveOccurred())
			patch, err := json.Marshal(map[string]any{"stringData": map[string]string{"config": string(swapped)}})
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "patch", "secret", credentialsSecretName(planeName),
				"-n", crNamespace, "--type=merge", "-p", string(patch)))
			Expect(err).NotTo(HaveOccurred())

			By("reporting the write forbidden to that identity")
			Eventually(func(g Gomega) {
				g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "reason")).
					To(Equal("AccessDenied"))
				g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "message")).
					To(ContainSubstring("forbidden"))
			}, crossClusterTimeout).Should(Succeed())

			By("granting that identity the CCLClusterConfig and writing it again")
			_, err = utils.Run(t.cluster.Kubectl("create", "clusterrole", clusterRole,
				"--verb=get,list,watch,create,update,patch,delete",
				"--resource=cclclusterconfigs.ccluster.nvidia.com"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(t.cluster.Kubectl("create", "clusterrolebinding", clusterRole,
				"--clusterrole="+clusterRole, "--serviceaccount=default:"+serviceAccount))
			Expect(err).NotTo(HaveOccurred())
			// The watch restarted with the new token keeps retrying its list until the grant
			// lets it sync, and the CCLClusterConfig it then lists reconciles the plane. Had
			// it given up first, after remote.SyncTimeout, the write's one-minute retry would.
			Eventually(func(g Gomega) {
				g.Expect(infraPlaneCondition(planeName, conditionClusterConfigApplied, "status")).To(Equal("True"),
					infraPlaneCondition(planeName, conditionClusterConfigApplied, "message"))
			}, crossClusterTimeout).Should(Succeed())

			By("deleting the CCLClusterConfig as that identity with the CCLInfraPlane")
			deleteInfraPlane(planeName)
			out, err = utils.Run(t.cluster.Kubectl("get", "cclclusterconfig", "--ignore-not-found", "-o", "name"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(out)).To(BeEmpty())
		})

		// The three specs below need Argo CD genuinely absent: BeforeAll deploys with
		// ARGOCD=false, so the Application CRD does not exist yet when the manager
		// starts - the exact precondition each of them needs. Like every other spec
		// outside the "with real Argo CD" container they run in both tiers; the full
		// tier only installs Argo CD after them. Specs carry exactly one label or
		// none: "full" for the nested container, nothing for everything else.

		It("should report ApplicationCRDMissing when Argo CD is not installed", func() {
			const componentSetName = "e2e-no-argocd"
			const planeName = "e2e-deploy-plane"
			const credentialsName = "e2e-deploy-plane-credentials"

			By("creating an available CCLInfraPlane for the component to target")
			_, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic",
				credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", crNamespace, "--ignore-not-found"))
			})

			applyCCLInfraPlane(planeName, credentialsName)
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					planeName, "-n", crNamespace, "-o",
					"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 2*time.Minute).Should(Succeed())

			By("creating a CCLComponentSet against it")
			applyCCLComponentSet(componentSetName, planeName)

			By("waiting for the operator to report the missing Application CRD")
			// This is the graceful-degradation path: Argo CD absent is reported on the
			// status rather than retried, because no amount of retrying installs it.
			Eventually(func(g Gomega) {
				g.Expect(componentSetCondition(componentSetName, conditionValidated, "status")).
					To(Equal("True"))
				g.Expect(componentSetCondition(componentSetName, conditionComponentsApplied, "reason")).
					To(Equal("ApplicationCRDMissing"))
			}, 2*time.Minute).Should(Succeed())

			// Both CRs are deleted here, in the spec body, for the same reason the
			// mirror spec deletes its CCLInfraPlane inline: each carries a finalizer
			// only the operator can clear, and AfterAll undeploys the operator before
			// deleting the CRDs. DeferCleanup runs after AfterAll in an Ordered
			// container, which is too late - a finalized CR left behind wedges its CRD
			// in Terminating and `make uninstall` blocks forever.
			By("deleting both CRs while the operator can still run their finalizers")
			deleteCCLComponentSet(componentSetName)
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())
		})

		// Closes the `argoproj.io/applications` and `argoproj.io/appprojects` RBAC
		// gaps without paying for a full Argo CD install: the CRDs are static
		// resources with no images to pull, so they can be rendered straight out of
		// the vendored argo-cd dependency with `helm template --show-only` and
		// applied on their own. Both reconcilers only watch their Argo CD kind if
		// the CRD is already served at SetupWithManager time, so the manager has to
		// be restarted afterwards to pick them up - the same operational sequence
		// AGENTS.md documents for installing Argo CD after the operator. Nothing here
		// reconciles the Application the way a live Argo CD would, so the rollout
		// itself stays out of scope - that's the full tier's job.
		It("should create a real Application once the Argo CD CRDs become available", func() {
			const componentSetName = "e2e-real-application"
			const planeName = "e2e-real-application-plane"
			const credentialsName = "e2e-real-application-plane-credentials"
			applicationName := componentSetName + "-argo-rollouts"

			By("rendering the Argo CD Application and AppProject CRDs from the vendored dependency chart")
			cmd := exec.Command("helm", "template", "charts/cp/chart",
				"--set", "argo-cd.enabled=true",
				"--show-only", "charts/argo-cd/templates/crds/crd-application.yaml",
				"--show-only", "charts/argo-cd/templates/crds/crd-appproject.yaml")
			argoCDCRDs, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to render the Argo CD CRDs")

			By("installing the Argo CD CRDs")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(argoCDCRDs)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to install the Argo CD CRDs")
			// No DeferCleanup for the CRDs here: the pruning spec that follows needs
			// them and the watches this restart registers, and a second restart would
			// double the cost. That spec removes them; a run that fails here skips it
			// (Ordered), and the e2e cluster is deleted afterwards regardless.

			By("restarting the controller-manager so it picks up the Application and AppProject watches")
			_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "rollout", "restart",
				"deploy/ccluster-cp-controller-manager"))
			Expect(err).NotTo(HaveOccurred())
			_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "rollout", "status",
				"deploy/ccluster-cp-controller-manager", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())

			By("tracking the restarted pod so AfterEach can still fetch its logs on failure")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "pods",
					"-l", "control-plane=controller-manager", "-n", namespace,
					"-o", "go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}{{ \"\\n\" }}{{ end }}{{ end }}"))
				g.Expect(err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(out)
				g.Expect(podNames).To(HaveLen(1))
				controllerPodName = podNames[0]
			}, 2*time.Minute).Should(Succeed())

			By("creating an available CCLInfraPlane for the component to target")
			_, err = utils.Run(exec.Command("kubectl", "create", "secret", "generic",
				credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", crNamespace, "--ignore-not-found"))
			})

			applyCCLInfraPlane(planeName, credentialsName)
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
					planeName, "-n", crNamespace, "-o",
					"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal("True"))
			}, 2*time.Minute).Should(Succeed())

			// The AppProject is the `appprojects` half of the RBAC this spec exists
			// for. Named after the CRs' namespace, written to the Argo CD namespace.
			By("verifying the operator created the namespace's AppProject under its own RBAC")
			out, err := utils.Run(exec.Command("kubectl", "get", "appproject", crNamespace,
				"-n", namespace, "-o", "jsonpath={.spec.sourceNamespaces[0]}"))
			Expect(err).NotTo(HaveOccurred(), "AppProject was not created")
			Expect(strings.TrimSpace(out)).To(Equal(crNamespace))

			By("creating a CCLComponentSet against it")
			applyCCLComponentSet(componentSetName, planeName)

			By("waiting for the operator to create the Application under its own RBAC")
			Eventually(func(g Gomega) {
				g.Expect(componentSetCondition(componentSetName, conditionComponentsApplied, "reason")).
					To(Equal("ApplicationsCreated"))
			}, 2*time.Minute).Should(Succeed())

			By("verifying the Application carries the expected ownership back-reference")
			out, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName,
				"-n", crNamespace, "-o",
				"jsonpath={.metadata.labels['ccluster\\.nvidia\\.com/componentset-name']}"))
			Expect(err).NotTo(HaveOccurred(), "Application was not created")
			Expect(strings.TrimSpace(out)).To(Equal(componentSetName))

			By("verifying the Application is in the namespace's project and targets the prefixed cluster name")
			out, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName,
				"-n", crNamespace, "-o", "jsonpath={.spec.project} {.spec.destination.name}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(out)).To(Equal([]string{crNamespace, crNamespace + "-" + planeName}))

			// Both reconcilers register their argoproj Watches only when the CRD is
			// served at SetupWithManager time, which the restart above just made true.
			// Drift on either object has to come back on its own, with no CR touched,
			// or the watch is not wired. Done here rather than in a sibling spec
			// because the CRD install and manager restart are already paid for.
			By("drifting the Application spec and waiting for the operator to restore it")
			// This tier has no Argo CD controller. Complete its pending request before
			// testing a new spec write; the operator must preserve in-flight operations.
			markConverged(applicationName)
			revision, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
				"-n", crNamespace, "-o", "jsonpath={.spec.source.targetRevision}"))
			Expect(err).NotTo(HaveOccurred())
			revision = strings.TrimSpace(revision)
			Expect(revision).NotTo(Equal("drifted"))
			_, err = utils.Run(exec.Command("kubectl", "patch", "application", applicationName, "-n", crNamespace,
				"--type=merge", "-p", `{"spec":{"source":{"targetRevision":"drifted"}}}`))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
					"-n", crNamespace, "-o", "jsonpath={.spec.source.targetRevision}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Equal(revision))
			}, 2*time.Minute).Should(Succeed())

			By("drifting the AppProject's sourceRepos and waiting for the operator to restore it")
			_, err = utils.Run(exec.Command("kubectl", "patch", "appproject", crNamespace, "-n", namespace,
				"--type=merge", "-p", `{"spec":{"sourceRepos":["https://evil.example.com/repo"]}}`))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "appproject", crNamespace,
					"-n", namespace, "-o", "jsonpath={.spec.sourceRepos}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(ContainSubstring("evil.example.com"))
				// The catalog, and nothing else - the tenancy invariant DESIGN.md pins.
				var repos []string
				g.Expect(json.Unmarshal([]byte(out), &repos)).To(Succeed())
				g.Expect(repos).To(HaveLen(1))
			}, 2*time.Minute).Should(Succeed())

			// Deleted inline, not via DeferCleanup, for the same reason as the specs
			// above: the finalizer needs the operator running to clear it, and
			// DeferCleanup runs after AfterAll has already undeployed it.
			//
			// Delete without waiting: the operator's finalize() deletes the
			// Application, which stays Terminating on its own
			// resources-finalizer.argocd.argoproj.io finalizer - nothing here runs
			// the Argo CD controller that would normally remove it once the
			// resources it manages are gone.
			By("deleting the CCLComponentSet while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
				"-n", crNamespace, "--wait=false"))
			Expect(err).NotTo(HaveOccurred())

			// Strip the Application's finalizer only once it is actually
			// terminating, not before: stripping it any earlier races the
			// operator's own reconcile loop, which re-adds the finalizer on every
			// pass as part of ensuring the Application matches spec, whether or not
			// anything is deleting it yet - stripping first can lose that race and
			// leave the finalizer back in place by the time the real delete lands.
			// Kubernetes refuses to add a finalizer to an object that already has a
			// deletionTimestamp, so once the Application is terminating this
			// removal is final.
			By("stripping the Argo CD cascade finalizer once the Application starts terminating")
			Eventually(func(g Gomega) {
				_, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName, "-n", crNamespace))
				if err != nil {
					return // already gone - nothing left to strip
				}
				_, err = utils.Run(exec.Command("kubectl", "patch", "application", applicationName,
					"-n", crNamespace, "--type=json",
					"-p", `[{"op": "remove", "path": "/metadata/finalizers"}]`))
				g.Expect(err).NotTo(HaveOccurred())
			}, 2*time.Minute).Should(Succeed())

			By("waiting for the operator to finish deleting the CCLComponentSet")
			_, err = utils.Run(exec.Command("kubectl", "wait", "cclcomponentset", componentSetName,
				"-n", crNamespace, "--for=delete", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CCLInfraPlane while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the operator deleted the Application")
			_, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName, "-n", crNamespace))
			Expect(err).To(HaveOccurred(), "Application was not cleaned up")
		})

		// Runs right after the spec above, with the argoproj CRDs it installed still
		// present and the manager already watching them, and removes the CRDs when it
		// is done - which is also what lets the full tier's Argo CD install, which
		// ships the same CRDs, go in cleanly afterwards. With no Argo CD to converge
		// anything, the rollout is driven by
		// hand-written Application status - the Application CRD has no status
		// subresource, so a plain merge patch sets it - which is what makes the two
		// spec-change paths reachable cheaply: a second component released after the
		// first converges, then pruned when it is removed from the spec.
		It("should prune the Application of a component removed from the spec", func() {
			const componentSetName = "e2e-prune"
			const planeName = "e2e-prune-plane"
			const credentialsName = "e2e-prune-plane-credentials"
			rolloutsApp := componentSetName + "-argo-rollouts"
			kedaApp := componentSetName + "-keda"

			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "crd",
					"applications.argoproj.io", "appprojects.argoproj.io", "--ignore-not-found"))
			})

			By("creating an available CCLInfraPlane for the components to target")
			_, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic",
				credentialsName,
				"--from-literal=name=placeholder",
				"--from-literal=server=https://infra.example.com:6443",
				"--from-literal=config={\"bearerToken\":\"fake-token\"}",
				"-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "secret",
					credentialsName, "-n", crNamespace, "--ignore-not-found"))
			})
			applyCCLInfraPlane(planeName, credentialsName)
			waitInfraPlaneAvailable(planeName)

			By("creating a two-component CCLComponentSet: argo-rollouts then keda")
			applyOrderedCCLComponentSet(componentSetName, planeName)

			By("verifying only the first component is released and Progressing names it")
			Eventually(func(g Gomega) {
				g.Expect(applicationExists(rolloutsApp)).To(BeTrue(), "argo-rollouts Application missing")
				g.Expect(applicationExists(kedaApp)).To(BeFalse(), "keda Application released early")
				g.Expect(componentSetCondition(componentSetName, conditionProgressing, "status")).To(Equal("True"))
				g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).To(Equal("WaveSyncing"))
				g.Expect(componentSetCondition(componentSetName, conditionProgressing, "message")).
					To(ContainSubstring(`component "argo-rollouts"`))
			}, 2*time.Minute).Should(Succeed())

			By("standing in for Argo CD on argo-rollouts by hand so keda is released")
			markConverged(rolloutsApp)
			Eventually(func(g Gomega) {
				g.Expect(applicationExists(kedaApp)).To(BeTrue(), "keda Application not released")
			}, 2*time.Minute).Should(Succeed())

			By("removing keda from the spec")
			applyCCLComponentSet(componentSetName, planeName)

			// The operator deletes it; the Argo CD cascade finalizer it set keeps it
			// Terminating, since nothing here runs the controller that would clear it.
			// Strip only once it is actually terminating - see the note in the spec above.
			By("waiting for the operator to delete keda's Application")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "application", kedaApp, "-n", crNamespace,
					"-o", "jsonpath={.metadata.deletionTimestamp}"))
				if err != nil {
					return // already gone
				}
				g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "keda Application not being deleted")
				_, err = utils.Run(exec.Command("kubectl", "patch", "application", kedaApp, "-n", crNamespace,
					"--type=json", "-p", `[{"op": "remove", "path": "/metadata/finalizers"}]`))
				g.Expect(err).NotTo(HaveOccurred())
			}, 2*time.Minute).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(applicationExists(kedaApp)).To(BeFalse(), "keda Application not pruned")
				g.Expect(applicationExists(rolloutsApp)).To(BeTrue(), "argo-rollouts Application pruned too")
				g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
					To(Equal("RolloutComplete"))
			}, 2*time.Minute).Should(Succeed())

			By("deleting the CCLComponentSet while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
				"-n", crNamespace, "--wait=false"))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				_, err := utils.Run(exec.Command("kubectl", "get", "application", rolloutsApp, "-n", crNamespace))
				if err != nil {
					return
				}
				_, err = utils.Run(exec.Command("kubectl", "patch", "application", rolloutsApp, "-n", crNamespace,
					"--type=json", "-p", `[{"op": "remove", "path": "/metadata/finalizers"}]`))
				g.Expect(err).NotTo(HaveOccurred())
			}, 2*time.Minute).Should(Succeed())
			_, err = utils.Run(exec.Command("kubectl", "wait", "cclcomponentset", componentSetName,
				"-n", crNamespace, "--for=delete", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CCLInfraPlane while the operator can still run its finalizer")
			_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
				planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
			Expect(err).NotTo(HaveOccurred())
		})

		// Every spec above ran against a release deployed without Argo CD. The
		// container below is the full tier: it upgrades that same release to add the
		// chart's Argo CD dependency, restarts the manager so it registers the
		// argoproj watches, and only then runs the specs that need a real rollout.
		// Installing Argo CD after the operator is a supported operational sequence,
		// so the fixture doubles as a test of it. The quick specs are not skipped in
		// the full run; they simply ran first, while the Application CRD was still
		// genuinely absent.
		Context("with real Argo CD", Label("full"), Ordered, func() {
			BeforeAll(func() {
				// Argo CD clones the catalog itself. If the catalog repository is
				// private, GITHUB_TOKEN must be set in the environment: `make deploy-cp`
				// picks it up and registers the repository credential with Argo CD.
				// `helm --wait` gets a longer timeout to pull and start every Argo CD
				// component's images.
				By("upgrading the release to add Argo CD")
				cmd := exec.Command("make", "deploy-cp",
					fmt.Sprintf("IMG_CP=%s", managerImage), "HELM_TIMEOUT=10m")
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to deploy Argo CD alongside the controller-manager")

				// The upgrade adds the argocd-server flags, so Helm rolls the manager, but
				// nothing orders its new Pod after the Argo CD CRDs, and both reconcilers
				// only watch their Argo CD kind if the CRD is served at SetupWithManager
				// time.
				By("restarting the controller-manager so it picks up the Application and AppProject watches")
				_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "rollout", "restart",
					"deploy/ccluster-cp-controller-manager"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "rollout", "status",
					"deploy/ccluster-cp-controller-manager", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())

				By("tracking the restarted pod so AfterEach can still fetch its logs on failure")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "pods",
						"-l", "control-plane=controller-manager", "-n", namespace,
						"-o", "go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}"+
							"{{ .metadata.name }}{{ \"\\n\" }}{{ end }}{{ end }}"))
					g.Expect(err).NotTo(HaveOccurred())
					podNames := utils.GetNonEmptyLines(out)
					g.Expect(podNames).To(HaveLen(1))
					controllerPodName = podNames[0]
				}, 2*time.Minute).Should(Succeed())
			})

			// The upgrade above added Argo CD to an existing release, so the argocd-token
			// dependency's post-upgrade hook created the operator's token, and deploy-cp
			// pointed the operator at the new argocd-server Service. The token is checked
			// against that Service, from inside the server Pod, whose image has the CLI;
			// the token goes over stdin rather than onto a command line.
			It("should give the operator an Argo CD API token and argocd-server Service when installing Argo CD", func() {
				token, err := utils.Run(exec.Command("kubectl", "get", "secret", "ccluster-cp-argocd-token",
					"-n", namespace, "-o", "go-template={{ .data.token | base64decode }}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(token).NotTo(BeEmpty(), "the hook Job stored no token")

				args, err := utils.Run(exec.Command("kubectl", "get", "deploy/ccluster-cp-controller-manager",
					"-n", namespace, "-o", "jsonpath={.spec.template.spec.containers[0].args}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(args).To(ContainSubstring("--argocd-server-service=ccluster-cp-argocd-server"))
				serverHost := fmt.Sprintf("ccluster-cp-argocd-server.%s.svc", namespace)

				cmd := exec.Command("kubectl", "exec", "-i", "-n", namespace, "deploy/ccluster-cp-argocd-server",
					"--", "sh", "-c",
					`argocd account get-user-info --server "$1:443" --insecure --auth-token "$(cat)"`,
					"sh", serverHost)
				cmd.Stdin = strings.NewReader(token)
				out, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("Logged In: true"))
				Expect(out).To(ContainSubstring("Username: ccluster-cp"))
			})

			// The specs above never touch a real Application: nothing creates one under
			// the operator's own RBAC, and nothing exercises the rest of a rollout - Argo
			// CD actually cloning the catalog, installing the chart, and the operator
			// reading Application health back. Only this container has real Argo CD
			// (see its BeforeAll). This spec runs once per target - the control-plane
			// cluster registered with itself, as examples/quickstart documents, and the
			// separate cluster registered with `ccluster register`, which Argo CD reaches
			// over the network - and drives one catalog component all the way to
			// Healthy/Synced, which is what `make test-e2e-argocd` never actually did
			// before it was retired: that target only asserted the argoproj CRDs
			// existed, not that a rollout worked.
			for _, t := range targets() {
				It("should roll out a real catalog component under real Argo CD on the "+t.name+" infra plane", func() {
					componentSetName := "e2e-real-rollout-" + t.name
					planeName := "e2e-real-rollout-plane-" + t.name
					applicationName := componentSetName + "-argo-rollouts"

					By("registering the " + t.name + " plane")
					t.register(planeName)
					var err error

					Eventually(func(g Gomega) {
						out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
							planeName, "-n", crNamespace, "-o",
							"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(out)).To(Equal("True"))
					}, 2*time.Minute).Should(Succeed())

					By("creating a CCLComponentSet against it")
					applyCCLComponentSet(componentSetName, planeName)

					By("waiting for the operator to create the Application under its own RBAC")
					Eventually(func(g Gomega) {
						g.Expect(componentSetCondition(componentSetName, conditionComponentsApplied, "reason")).
							To(Equal("ApplicationsCreated"))
					}, 2*time.Minute).Should(Succeed())

					By("verifying the Application carries the expected ownership back-reference")
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o",
						"jsonpath={.metadata.labels['ccluster\\.nvidia\\.com/componentset-name']}"))
					Expect(err).NotTo(HaveOccurred(), "Application was not created")
					Expect(strings.TrimSpace(out)).To(Equal(componentSetName))

					By("waiting for Argo CD to actually reconcile it to Healthy and Synced")
					Eventually(func(g Gomega) {
						health, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
							"-n", crNamespace, "-o", "jsonpath={.status.health.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(health)).To(Equal("Healthy"))

						sync, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
							"-n", crNamespace, "-o", "jsonpath={.status.sync.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(sync)).To(Equal("Synced"))
					}, 8*time.Minute).Should(Succeed())

					// argo-rollouts' rollouts.argoproj.io CRD is around 600KB (DESIGN.md §6) -
					// too large for client-side apply's last-applied-configuration
					// annotation, which is exactly why ServerSideApply=true and
					// controller.diff.server.side are mandatory. A regression there would
					// already fail the aggregate Synced check above; this asserts it on the
					// CRD resource specifically so a future failure here points straight at
					// the CRD instead of a generic "Application not Synced".
					By("verifying the large CRD resource itself reports Synced, not just the aggregate status")
					// Selected by name: the chart ships five CRDs, and selecting on kind would
					// return all five statuses in one string.
					Eventually(func(g Gomega) {
						out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
							"-n", crNamespace, "-o",
							`jsonpath={.status.resources[?(@.name=="rollouts.argoproj.io")].status}`))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(out)).To(Equal("Synced"))
					}, 8*time.Minute).Should(Succeed())

					By("verifying the operator's Progressing condition reflects a completed rollout")
					Eventually(func(g Gomega) {
						g.Expect(componentSetCondition(componentSetName, conditionProgressing, "status")).
							To(Equal("False"))
						g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
							To(Equal("RolloutComplete"))
					}, 2*time.Minute).Should(Succeed())

					// Deleted inline, not via DeferCleanup: each CR's finalizer needs the
					// operator running to clear it, and DeferCleanup runs after AfterAll has
					// already undeployed it. Teardown here is a real cascade through Argo CD -
					// deleting the CCLComponentSet deletes the Application, whose
					// resources-finalizer.argocd.argoproj.io finalizer makes Argo CD
					// uninstall the chart before the Application itself goes away - so this
					// gets a longer timeout than the status-only specs above.
					By("deleting the CCLComponentSet and CCLInfraPlane, letting Argo CD uninstall the chart")
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
						"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
					Expect(err).NotTo(HaveOccurred())
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
						planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
					Expect(err).NotTo(HaveOccurred())

					By("verifying the operator deleted the Application")
					_, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName, "-n", crNamespace))
					Expect(err).To(HaveOccurred(), "Application was not cleaned up")
				})
			}

			// DESIGN.md §5: `releaseCount` withholds a component's Application until
			// every earlier one is Healthy and Synced, and `teardownNext` deletes them
			// in reverse, one at a time, waiting for each to be gone before starting the
			// next. Both are proven only against hand-written status in envtest today;
			// this spec drives the same two invariants against a real Argo CD
			// reconciling two real components (matching
			// examples/quickstart/v1alpha1_cclcomponentset.yaml), combined into one spec so the
			// suite pays for one KEDA + argo-rollouts image pull cycle per target, on each
			// target's node.
			for _, t := range targets() {
				It("should release components in order and tear them down in reverse on the "+t.name+" infra plane", func() {
					componentSetName := "e2e-ordered-release-" + t.name
					planeName := "e2e-ordered-release-plane-" + t.name
					rolloutsApp := componentSetName + "-argo-rollouts"
					kedaApp := componentSetName + "-keda"

					By("registering the " + t.name + " plane")
					t.register(planeName)
					var err error

					Eventually(func(g Gomega) {
						out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
							planeName, "-n", crNamespace, "-o",
							"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(out)).To(Equal("True"))
					}, 2*time.Minute).Should(Succeed())

					By("creating a two-component CCLComponentSet: argo-rollouts then keda")
					applyOrderedCCLComponentSet(componentSetName, planeName)

					By("waiting for the first component's Application to appear")
					Eventually(func(g Gomega) {
						_, err := utils.Run(exec.Command("kubectl", "get", "application", rolloutsApp, "-n", crNamespace))
						g.Expect(err).NotTo(HaveOccurred())
					}, 2*time.Minute).Should(Succeed())

					// State-based, not a fixed window: how long argo-rollouts takes to converge
					// depends on whether its images are already on the node (they are, after
					// the spec above), and it has been seen to finish in under 20 seconds. So
					// keda's Application is required to be absent for exactly as long as
					// argo-rollouts is not yet Healthy and Synced. keda is read first: if it
					// exists and argo-rollouts, read afterwards, is still converging, the
					// operator released it early.
					By("confirming the second component's Application is withheld until the first converges")
					Eventually(func(g Gomega) {
						_, kedaErr := utils.Run(exec.Command("kubectl", "get", "application", kedaApp, "-n", crNamespace))
						converged := applicationHealthyAndSynced(rolloutsApp)
						if kedaErr == nil {
							Expect(converged).To(BeTrue(), "keda Application created before argo-rollouts converged")
						}
						g.Expect(converged).To(BeTrue(), "argo-rollouts has not converged yet")
					}, 8*time.Minute, 2*time.Second).Should(Succeed())

					By("waiting for keda's Application to appear now that argo-rollouts has converged")
					Eventually(func(g Gomega) {
						_, err := utils.Run(exec.Command("kubectl", "get", "application", kedaApp, "-n", crNamespace))
						g.Expect(err).NotTo(HaveOccurred())
					}, 2*time.Minute).Should(Succeed())

					By("waiting for keda to also converge")
					Eventually(func(g Gomega) {
						health, err := utils.Run(exec.Command("kubectl", "get", "application", kedaApp,
							"-n", crNamespace, "-o", "jsonpath={.status.health.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(health)).To(Equal("Healthy"))

						sync, err := utils.Run(exec.Command("kubectl", "get", "application", kedaApp,
							"-n", crNamespace, "-o", "jsonpath={.status.sync.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(sync)).To(Equal("Synced"))
					}, 8*time.Minute).Should(Succeed())

					By("deleting the CCLComponentSet and observing reverse teardown: keda first, then argo-rollouts")
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
						"-n", crNamespace, "--wait=false"))
					Expect(err).NotTo(HaveOccurred())

					// State-based, as with the withholding check: teardownNext deletes exactly
					// one Application per reconcile and waits for it to be gone before starting
					// the next, so argo-rollouts' Application must exist for as long as keda's
					// does - but the moment keda's is gone the operator deletes argo-rollouts',
					// and Argo CD finishes that in seconds, so nothing can be asserted about a
					// window *after* keda disappears. argo-rollouts is read first: if it is
					// gone while keda, read afterwards, is still present, it was deleted early.
					By("confirming argo-rollouts' Application outlives keda's")
					Eventually(func(g Gomega) {
						_, rolloutsErr := utils.Run(exec.Command("kubectl", "get", "application", rolloutsApp, "-n", crNamespace))
						_, kedaErr := utils.Run(exec.Command("kubectl", "get", "application", kedaApp, "-n", crNamespace))
						if kedaErr == nil {
							Expect(rolloutsErr).NotTo(HaveOccurred(),
								"argo-rollouts Application deleted before keda's was gone")
						}
						g.Expect(kedaErr).To(HaveOccurred(), "keda Application should be gone")
					}, 5*time.Minute, 2*time.Second).Should(Succeed())

					By("waiting for argo-rollouts' Application to be deleted too")
					Eventually(func(g Gomega) {
						_, err := utils.Run(exec.Command("kubectl", "get", "application", rolloutsApp, "-n", crNamespace))
						g.Expect(err).To(HaveOccurred())
					}, 5*time.Minute).Should(Succeed())

					By("deleting the CCLInfraPlane while the operator can still run its finalizer")
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
						planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
					Expect(err).NotTo(HaveOccurred())
				})
			}

			// The generated Application's SyncPolicy always sets Automated{SelfHeal:true}
			// (cclcomponentset_builder.go), but nothing exercises that Argo CD actually
			// acts on it. This mutates a live resource Argo CD manages and confirms it
			// gets reverted, using its own single-component fixture (rather than
			// reusing the rollout or ordered-release specs' resources) so a failure
			// here is isolated and easy to read.
			for _, t := range targets() {
				It("should revert drift on a live resource via selfHeal on the "+t.name+" infra plane", func() {
					componentSetName := "e2e-selfheal-" + t.name
					planeName := "e2e-selfheal-plane-" + t.name
					applicationName := componentSetName + "-argo-rollouts"

					By("registering the " + t.name + " plane")
					t.register(planeName)
					var err error

					Eventually(func(g Gomega) {
						out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane",
							planeName, "-n", crNamespace, "-o",
							"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(out)).To(Equal("True"))
					}, 2*time.Minute).Should(Succeed())

					By("creating a CCLComponentSet against it")
					applyCCLComponentSet(componentSetName, planeName)

					By("waiting for Argo CD to reconcile it to Healthy and Synced")
					Eventually(func(g Gomega) {
						health, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
							"-n", crNamespace, "-o", "jsonpath={.status.health.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(health)).To(Equal("Healthy"))

						sync, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
							"-n", crNamespace, "-o", "jsonpath={.status.sync.status}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.TrimSpace(sync)).To(Equal("Synced"))
					}, 8*time.Minute).Should(Succeed())

					// The Deployment's name is the chart's business - the upstream chart names
					// it after the Helm release, which Argo CD sets to the Application name -
					// so it is taken from the resources the Application reports managing.
					By("finding the Deployment Argo CD manages for the component")
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", `jsonpath={.status.resources[?(@.kind=="Deployment")].name}`))
					Expect(err).NotTo(HaveOccurred())
					managedDeployments := strings.Fields(out)
					Expect(managedDeployments).To(HaveLen(1), "expected the argo-rollouts chart to install exactly one Deployment")
					controllerDeployment := managedDeployments[0]

					By("mutating a live resource Argo CD manages")
					_, err = utils.Run(t.cluster.Kubectl("scale", "deployment",
						controllerDeployment, "-n", "argo-rollouts", "--replicas=5"))
					Expect(err).NotTo(HaveOccurred())

					// selfHeal is driven by the application controller's watch on managed
					// resources, not the reconciliation poll (unmodified from Argo CD's
					// stock default here - charts/cp/chart/values.yaml sets no
					// timeout.reconciliation override), so it normally reverts within
					// seconds; this budgets past that default poll interval for margin
					// under CI load rather than cutting it close.
					By("waiting for Argo CD's selfHeal to revert the drift")
					Eventually(func(g Gomega) {
						out, err := utils.Run(t.cluster.Kubectl("get", "deployment", controllerDeployment,
							"-n", "argo-rollouts", "-o", "jsonpath={.spec.replicas}"))
						g.Expect(err).NotTo(HaveOccurred())
						// The chart's controller.replicas, catalogs/cluster_base/argo_rollouts/values.yaml.
						g.Expect(strings.TrimSpace(out)).To(Equal("2"))
					}, 4*time.Minute, 2*time.Second).Should(Succeed())

					By("deleting the CCLComponentSet and CCLInfraPlane, letting Argo CD uninstall the chart")
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
						"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
					Expect(err).NotTo(HaveOccurred())
					_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
						planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
					Expect(err).NotTo(HaveOccurred())
				})
			}

			// Self-heal, the spec above, creates again any object a chart renders that
			// disappears. A job_deployment Job that deleted itself after it finished would
			// therefore run again, so the finished Job stays, and deleting the set is what
			// removes it.
			It("should keep a finished Job until its CCLComponentSet is deleted", func() {
				const componentSetName = "e2e-job"
				const planeName = "e2e-job-plane"
				const credentialsName = "e2e-job-plane-credentials"
				const jobNamespace = "e2e-job"
				const jobName = "e2e-job"

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error
				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				By("creating a CCLComponentSet with one job_deployment component")
				applyManifest("cclcomponentset_job.yaml", map[string]string{
					"Name": componentSetName, "Namespace": crNamespace, "PlaneName": planeName,
					"JobNamespace": jobNamespace, "JobName": jobName,
				})

				By("waiting for the Job to succeed and the rollout to complete")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "job", jobName,
						"-n", jobNamespace, "-o", "jsonpath={.status.succeeded}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).To(Equal("1"))
					g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
						To(Equal("RolloutComplete"))
				}, 8*time.Minute, 2*time.Second).Should(Succeed())

				By("verifying nothing deletes the finished Job")
				out, err := utils.Run(exec.Command("kubectl", "get", "job", jobName,
					"-n", jobNamespace, "-o", "jsonpath={.spec.ttlSecondsAfterFinished}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(out)).To(BeEmpty(),
					"self-heal would create the Job again after its TTL deleted it")

				By("deleting the CCLComponentSet, letting Argo CD delete the Job")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
					"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				Eventually(func(g Gomega) {
					_, err := utils.Run(exec.Command("kubectl", "get", "job", jobName, "-n", jobNamespace))
					g.Expect(err).To(HaveOccurred())
				}, 2*time.Minute, 2*time.Second).Should(Succeed(), "the Job outlived its CCLComponentSet")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// The day-2 upgrade path: every full-tier spec above creates a CCLComponentSet
			// and later deletes it, so nothing proves that editing one rolls the change
			// forward. The operator side (the Application's helm values follow the spec)
			// and the Argo CD side (the managed Deployment follows the Application) are
			// asserted separately so a failure names the half that broke.
			It("should roll a values change forward through Argo CD", func() {
				const componentSetName = "e2e-values-change"
				const planeName = "e2e-values-change-plane"
				const credentialsName = "e2e-values-change-plane-credentials"
				applicationName := componentSetName + "-keda"

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error
				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				By("deploying keda with one operator replica")
				applyKedaCCLComponentSet(componentSetName, planeName, 1)
				Eventually(func(g Gomega) {
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue())
				}, 8*time.Minute).Should(Succeed())

				// The Deployment's name is the chart's business, so it is taken from the
				// resources the Application reports managing: the keda chart installs an
				// operator, a metrics apiserver and an admission webhook Deployment.
				By("finding the keda operator Deployment Argo CD manages")
				out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
					"-n", crNamespace, "-o", `jsonpath={.status.resources[?(@.kind=="Deployment")].name}`))
				Expect(err).NotTo(HaveOccurred())
				var operatorDeployment string
				for name := range strings.FieldsSeq(out) {
					if strings.Contains(name, "operator") && !strings.Contains(name, "metrics") {
						Expect(operatorDeployment).To(BeEmpty(), "more than one operator Deployment: %s", out)
						operatorDeployment = name
					}
				}
				Expect(operatorDeployment).NotTo(BeEmpty(), "no operator Deployment among %q", out)
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "deployment", operatorDeployment,
						"-n", "keda", "-o", "jsonpath={.spec.replicas}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).To(Equal("1"))
				}, 2*time.Minute).Should(Succeed())

				By("raising the replica count in the CCLComponentSet")
				applyKedaCCLComponentSet(componentSetName, planeName, 2)

				By("waiting for the operator to rewrite the Application's values")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={.spec.source.helm.values}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(ContainSubstring("replicaCount: 2"))
				}, 2*time.Minute).Should(Succeed())

				By("waiting for Argo CD to roll the change onto the managed Deployment")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "deployment", operatorDeployment,
						"-n", "keda", "-o", "jsonpath={.spec.replicas}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).To(Equal("2"))
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue(), "Application not back to Healthy/Synced")
				}, 8*time.Minute).Should(Succeed())

				By("deleting the CCLComponentSet and CCLInfraPlane, letting Argo CD uninstall the chart")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
					"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// Verify with real Argo CD that comparedTo retains the desired source,
			// including nulls in YAML values, and that completion waits for the updated
			// workload. The simulated controller in envtest cannot prove this behavior.
			It("should record the compared source and wait for the updated Deployment", func() {
				const componentSetName = "e2e-sync-generation"
				const planeName = "e2e-sync-generation-plane"
				const credentialsName = "e2e-sync-generation-plane-credentials"
				applicationName := componentSetName + "-keda"

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error
				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				readSet := func(g Gomega) *cclv1alpha1.CCLComponentSet {
					out, err := utils.Run(exec.Command("kubectl", "get", "cclcomponentset",
						componentSetName, "-n", crNamespace, "-o", "json"))
					g.Expect(err).NotTo(HaveOccurred())
					var set cclv1alpha1.CCLComponentSet
					g.Expect(json.Unmarshal([]byte(out), &set)).To(Succeed())
					return &set
				}
				// The spec source and the source Argo CD says it compared, as re-encoded
				// JSON so key order cannot make them differ.
				sources := func(g Gomega) (spec, compared string) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "json"))
					g.Expect(err).NotTo(HaveOccurred())
					var application struct {
						Spec struct {
							Source map[string]any `json:"source"`
						} `json:"spec"`
						Status struct {
							Sync struct {
								ComparedTo struct {
									Source map[string]any `json:"source"`
								} `json:"comparedTo"`
							} `json:"sync"`
						} `json:"status"`
					}
					g.Expect(json.Unmarshal([]byte(out), &application)).To(Succeed())
					specJSON, err := json.Marshal(application.Spec.Source)
					g.Expect(err).NotTo(HaveOccurred())
					comparedJSON, err := json.Marshal(application.Status.Sync.ComparedTo.Source)
					g.Expect(err).NotTo(HaveOccurred())
					return string(specJSON), string(comparedJSON)
				}

				By("deploying keda with one operator replica")
				applyKedaCCLComponentSet(componentSetName, planeName, 1)
				var createdGeneration int64
				Eventually(func(g Gomega) {
					createdGeneration = readSet(g).Generation
					spec, compared := sources(g)
					g.Expect(compared).To(Equal(spec),
						"Argo CD did not record the source it compared verbatim")
					g.Expect(spec).To(ContainSubstring("replicaCount: 1"))
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue())
				}, 8*time.Minute).Should(Succeed())

				By("finding the managed operator Deployment")
				out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
					"-n", crNamespace, "-o", `jsonpath={.status.resources[?(@.kind=="Deployment")].name}`))
				Expect(err).NotTo(HaveOccurred())
				var operatorDeployment string
				for name := range strings.FieldsSeq(out) {
					if strings.Contains(name, "operator") && !strings.Contains(name, "metrics") {
						Expect(operatorDeployment).To(BeEmpty(), "multiple operator Deployments: %s", out)
						operatorDeployment = name
					}
				}
				Expect(operatorDeployment).NotTo(BeEmpty())
				readDeployment := func(g Gomega) *appsv1.Deployment {
					out, err := utils.Run(exec.Command("kubectl", "get", "deployment", operatorDeployment,
						"-n", "keda", "-o", "json"))
					g.Expect(err).NotTo(HaveOccurred())
					var deployment appsv1.Deployment
					g.Expect(json.Unmarshal([]byte(out), &deployment)).To(Succeed())
					return &deployment
				}

				By("raising the replica count and checking that completion never precedes convergence")
				applyKedaCCLComponentSet(componentSetName, planeName, 2)
				Eventually(func(g Gomega) {
					set := readSet(g)
					g.Expect(set.Generation).NotTo(Equal(createdGeneration))
					progress := apimeta.FindStatusCondition(set.Status.Conditions, conditionProgressing)
					g.Expect(progress).NotTo(BeNil())
					if progress.ObservedGeneration == set.Generation && progress.Status == metav1.ConditionFalse {
						// Use Expect: premature completion is a safety failure, not an Eventually retry.
						spec, compared := sources(g)
						Expect(spec).To(ContainSubstring("replicaCount: 2"))
						Expect(compared).To(Equal(spec),
							"the rollout completed on a comparison of some other source")
						deployment := readDeployment(g)
						Expect(*deployment.Spec.Replicas).To(Equal(int32(2)))
						Expect(deployment.Status.ObservedGeneration).To(BeNumerically(">=", deployment.Generation))
						Expect(deployment.Status.UpdatedReplicas).To(Equal(int32(2)))
						Expect(deployment.Status.AvailableReplicas).To(Equal(int32(2)))
					}
					g.Expect(progress.ObservedGeneration).To(Equal(set.Generation))
					g.Expect(progress.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(progress.Reason).To(Equal("RolloutComplete"))
				}, 8*time.Minute).Should(Succeed())

				By("deleting the CCLComponentSet and CCLInfraPlane, letting Argo CD uninstall the chart")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
					"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// Verify that Argo CD removes deleted source fields from comparedTo.
			// Its status merge patch must explicitly clear removed keys; otherwise the
			// comparison would continue to describe a source the operator no longer wants.
			It("should drop a source key from comparedTo when it leaves the spec", func() {
				const componentSetName = "e2e-source-key-removed"
				const planeName = "e2e-source-key-removed-plane"
				const credentialsName = "e2e-source-key-removed-plane-credentials"
				applicationName := componentSetName + "-keda"

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error
				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				// The helm block as stored and as Argo CD recorded comparing it. A
				// missing one decodes to an empty RawMessage, which is the state under
				// test.
				helmBlocks := func(g Gomega) (spec, compared string) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application",
						applicationName, "-n", crNamespace, "-o", "json"))
					g.Expect(err).NotTo(HaveOccurred())
					var application struct {
						Spec struct {
							Source struct {
								Helm json.RawMessage `json:"helm"`
							} `json:"source"`
						} `json:"spec"`
						Status struct {
							Sync struct {
								ComparedTo struct {
									Source struct {
										Helm json.RawMessage `json:"helm"`
									} `json:"source"`
								} `json:"comparedTo"`
							} `json:"sync"`
						} `json:"status"`
					}
					g.Expect(json.Unmarshal([]byte(out), &application)).To(Succeed())
					return string(application.Spec.Source.Helm),
						string(application.Status.Sync.ComparedTo.Source.Helm)
				}

				By("deploying keda with values, so the source has a helm block")
				applyKedaCCLComponentSet(componentSetName, planeName, 1)
				Eventually(func(g Gomega) {
					spec, compared := helmBlocks(g)
					g.Expect(spec).To(ContainSubstring("replicaCount: 1"))
					// The source comparison spec covers equality; this checks that the Helm
					// block is present before its removal.
					g.Expect(compared).To(ContainSubstring("replicaCount: 1"),
						"Argo CD did not record the helm block it compared\nspec.source.helm:       %s\ncomparedTo.source.helm: %s",
						spec, compared)
					g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
						To(Equal("RolloutComplete"))
				}, 8*time.Minute).Should(Succeed())

				// Logged whether or not this spec fails: the two blocks are the only
				// direct evidence of how Argo CD stores what it compared.
				specHelm, comparedHelm := helmBlocks(Default)
				GinkgoWriter.Printf("spec.source.helm:        %s\ncomparedTo.source.helm:  %s\n",
					specHelm, comparedHelm)

				By("removing the values, which takes helm out of the source")
				applyKedaCCLComponentSetWithoutValues(componentSetName, planeName)
				Eventually(func(g Gomega) {
					spec, _ := helmBlocks(g)
					g.Expect(spec).To(BeEmpty(), "the operator did not drop the helm block")
				}, 2*time.Minute).Should(Succeed())

				By("waiting for Argo CD to drop it from comparedTo and the rollout to finish")
				Eventually(func(g Gomega) {
					spec, compared := helmBlocks(g)
					g.Expect(spec).To(BeEmpty())
					g.Expect(compared).To(BeEmpty(),
						"helm stayed in comparedTo, so no comparison can match the spec again: %s",
						compared)
					g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
						To(Equal("RolloutComplete"))
				}, 8*time.Minute).Should(Succeed())

				By("deleting the CCLComponentSet and CCLInfraPlane")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
					"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// The two halves of the field-ownership contract with Argo CD, which only a
			// real Argo CD can settle. envtest's stand-in writes whatever the test tells
			// it to, so it can show that the operator does not discard status, but never
			// that Argo CD leaves spec alone or that its own records survive a rewrite.
			//
			// status.history is the case that does not heal: Argo CD rebuilds health,
			// sync and resources after any write, but a dropped history stays dropped,
			// and a rollback reads it.
			It("should preserve the Application fields Argo CD owns across a spec rewrite", func() {
				const componentSetName = "e2e-field-ownership"
				const planeName = "e2e-field-ownership-plane"
				const credentialsName = "e2e-field-ownership-plane-credentials"
				applicationName := componentSetName + "-keda"

				readApplication := func(g Gomega, jsonPath string) string {
					out, err := utils.Run(exec.Command("kubectl", "get", "application",
						applicationName, "-n", crNamespace, "-o", "jsonpath="+jsonPath))
					g.Expect(err).NotTo(HaveOccurred())
					return strings.TrimSpace(out)
				}
				// The spec is compared as re-encoded JSON so key order cannot make an
				// unchanged spec look rewritten.
				specJSON := func(g Gomega) string {
					out, err := utils.Run(exec.Command("kubectl", "get", "application",
						applicationName, "-n", crNamespace, "-o", "json"))
					g.Expect(err).NotTo(HaveOccurred())
					var application struct {
						Spec map[string]any `json:"spec"`
					}
					g.Expect(json.Unmarshal([]byte(out), &application)).To(Succeed())
					encoded, err := json.Marshal(application.Spec)
					g.Expect(err).NotTo(HaveOccurred())
					return string(encoded)
				}
				historyIDs := func(g Gomega) []string {
					return strings.Fields(readApplication(g, "{.status.history[*].id}"))
				}

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error
				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				By("deploying keda and letting Argo CD record the first sync")
				applyKedaCCLComponentSet(componentSetName, planeName, 1)
				Eventually(func(g Gomega) {
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue())
					g.Expect(historyIDs(g)).To(HaveLen(1))
				}, 8*time.Minute).Should(Succeed())

				By("confirming Argo CD does not write into the spec the operator owns")
				var before string
				var reconciled string
				Eventually(func(g Gomega) {
					before = specJSON(g)
					reconciled = readApplication(g, "{.status.reconciledAt}")
					g.Expect(reconciled).NotTo(BeEmpty())
				}, 2*time.Minute).Should(Succeed())

				_, err = utils.Run(exec.Command("kubectl", "annotate", "application",
					applicationName, "-n", crNamespace,
					"argocd.argoproj.io/refresh=normal", "--overwrite"))
				Expect(err).NotTo(HaveOccurred())
				Eventually(func(g Gomega) {
					g.Expect(readApplication(g, "{.status.reconciledAt}")).NotTo(Equal(reconciled))
				}, 5*time.Minute).Should(Succeed())
				Expect(specJSON(Default)).To(Equal(before),
					"Argo CD changed the spec, which the operator rewrites from scratch")

				By("changing the values so the operator rewrites the Application")
				applyKedaCCLComponentSet(componentSetName, planeName, 2)
				Eventually(func(g Gomega) {
					g.Expect(readApplication(g,
						"{.spec.source.helm.values}")).To(ContainSubstring("replicaCount: 2"))
				}, 2*time.Minute).Should(Succeed())

				By("confirming Argo CD's record of the earlier sync survived the rewrite")
				Eventually(func(g Gomega) {
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue())
					// A history that restarts at one entry means the rewrite took Argo
					// CD's records with it and Argo CD started over.
					g.Expect(len(historyIDs(g))).To(BeNumerically(">=", 2))
				}, 8*time.Minute).Should(Succeed())

				By("deleting the CCLComponentSet and CCLInfraPlane")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset",
					componentSetName, "-n", crNamespace, "--ignore-not-found",
					"--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// DESIGN.md "One AppProject per namespace": the project is the tenancy
			// boundary, enforced by PermitOnlyProjectScopedClusters on the AppProject plus
			// data.project on the mirrored cluster Secret. envtest asserts those fields
			// are written; only real Argo CD proves they mean anything. Two namespaces,
			// each with its own plane and project; an Application hand-written into the
			// first, under the first's project, that targets the second's cluster must be
			// refused. Argo CD skips cluster Secrets with a duplicate server URL, so the two
			// planes are the two targets: the control-plane cluster registered with itself
			// in the second namespace, and the separate cluster in the first.
			It("should refuse an Application that targets another namespace's cluster", func() {
				const otherNamespace = crNamespace + "-b"
				const planeName = "e2e-tenancy-plane"
				const applicationName = "e2e-tenancy-cross"

				By("creating a second tenant namespace with a self-registered plane")
				_, err := utils.Run(exec.Command("kubectl", "create", "ns", otherNamespace))
				Expect(err).NotTo(HaveOccurred())
				registerInCluster(otherNamespace, planeName)
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane", planeName,
						"-n", otherNamespace, "-o",
						"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).To(Equal("True"))
				}, 2*time.Minute).Should(Succeed())

				By("registering the separate cluster in the first namespace, so its AppProject exists")
				exCluster().register(planeName)
				waitInfraPlaneAvailable(planeName)

				// The same source the operator would write, read from the manager's own
				// flags, so the only thing wrong with this Application is its destination.
				// A source outside sourceRepos would be refused too, for the wrong reason.
				By("hand-writing an Application in the first namespace that targets the second's cluster")
				repoURL, revision := catalogSource()
				applyManifest("application_cross_tenant.yaml", map[string]string{
					"Name":            applicationName,
					"Namespace":       crNamespace,
					"Project":         crNamespace,
					"RepoURL":         repoURL,
					"Revision":        revision,
					"DestinationName": otherNamespace + "-" + planeName,
				})

				// Argo CD reports a project-scoped cluster it may not use either as not
				// permitted in the project or as not existing at all, depending on the
				// code path; both are refusals.
				By("waiting for Argo CD to refuse the destination")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={range .status.conditions[*]}{.type}: {.message}{\"\\n\"}{end}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(MatchRegexp(`(?i)(InvalidSpecError|ComparisonError).*` +
						`(not permitted|not allowed|do not match any of the allowed destinations|` +
						`no clusters|unable to find destination)`))
				}, 4*time.Minute).Should(Succeed())

				By("confirming nothing is ever synced to it")
				Consistently(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={.status.sync.status}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).NotTo(Equal("Synced"))
				}, 30*time.Second, 5*time.Second).Should(Succeed())

				// No cascade finalizer was set on the hand-written Application, so this is
				// a plain delete.
				By("cleaning up the Application, both planes and the second namespace")
				_, err = utils.Run(exec.Command("kubectl", "delete", "application", applicationName,
					"-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", otherNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "ns", otherNamespace,
					"--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})

			// istio renders its validating webhooks with failurePolicy Ignore and istiod
			// patches them to Fail once it serves, so without the chart's drift rules the
			// Application never reports Synced and every later component waits on it.
			// Envtest only proves the operator writes the rules; this proves Argo CD,
			// given them, reports the live Fail as Synced and leaves it in place. It runs
			// last: istio-cni chains itself into the node's CNI configuration.
			It("should report istio Synced while istiod owns its webhooks' failurePolicy", func() {
				const componentSetName = "e2e-istio"
				const planeName = "e2e-istio-plane"
				const credentialsName = "e2e-istio-plane-credentials"
				applicationName := componentSetName + "-istio"

				By("registering the control-plane cluster with itself as an infra plane")
				createInClusterCredentials(crNamespace, credentialsName)
				var err error

				applyCCLInfraPlane(planeName, credentialsName)
				waitInfraPlaneAvailable(planeName)

				By("creating a CCLComponentSet with the istio component")
				applyIstioCCLComponentSet(componentSetName, planeName)

				By("verifying the operator wrote the chart's drift rules into the Application")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={.spec.ignoreDifferences[*].kind}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(out)).To(Equal("ValidatingWebhookConfiguration"))

					out, err = utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={.spec.syncPolicy.syncOptions}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(ContainSubstring("RespectIgnoreDifferences=true"))
				}, 2*time.Minute).Should(Succeed())

				// The configurations' names depend on istiod's revision and namespace, so
				// they are taken from the resources the Application reports managing.
				By("finding the validating webhooks Argo CD manages for the component")
				var webhooks []string
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o",
						`jsonpath={.status.resources[?(@.kind=="ValidatingWebhookConfiguration")].name}`))
					g.Expect(err).NotTo(HaveOccurred())
					webhooks = strings.Fields(out)
					g.Expect(webhooks).To(HaveLen(2), "expected istiod's and istio-base's webhooks")
				}, 8*time.Minute).Should(Succeed())

				// The chart renders Ignore. Fail on both is istiod's own patch, without which
				// this spec would pass with no drift to ignore.
				By("waiting for istiod to patch every webhook's failurePolicy to Fail")
				webhookPolicies := func(g Gomega) {
					for _, webhook := range webhooks {
						out, err := utils.Run(exec.Command("kubectl", "get",
							"validatingwebhookconfiguration", webhook,
							"-o", "jsonpath={.webhooks[*].failurePolicy}"))
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(strings.Fields(out)).To(HaveEach("Fail"), "webhook %s", webhook)
					}
				}
				Eventually(webhookPolicies, 8*time.Minute, 5*time.Second).Should(Succeed())

				By("waiting for Argo CD to report the Application Healthy and Synced")
				Eventually(func(g Gomega) {
					g.Expect(applicationHealthyAndSynced(applicationName)).To(BeTrue())
					g.Expect(componentSetCondition(componentSetName, conditionProgressing, "reason")).
						To(Equal("RolloutComplete"))
				}, 8*time.Minute, 5*time.Second).Should(Succeed())

				// The result holds: nothing reports drift or reverts istiod's Fail. No sync
				// runs while the Application is Synced, so this does not exercise
				// RespectIgnoreDifferences; envtest checks the operator sets it.
				By("verifying it stays Synced with istiod's failurePolicy left in place")
				Consistently(func(g Gomega) {
					webhookPolicies(g)
					sync, err := utils.Run(exec.Command("kubectl", "get", "application", applicationName,
						"-n", crNamespace, "-o", "jsonpath={.status.sync.status}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(strings.TrimSpace(sync)).To(Equal("Synced"))
				}, time.Minute, 5*time.Second).Should(Succeed())

				By("deleting the CCLComponentSet and CCLInfraPlane, letting Argo CD uninstall the chart")
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", componentSetName,
					"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=5m"))
				Expect(err).NotTo(HaveOccurred())
				_, err = utils.Run(exec.Command("kubectl", "delete", "cclinfraplane",
					planeName, "-n", crNamespace, "--wait=true", "--timeout=2m"))
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})

// conditionValidated, conditionComponentsApplied and conditionProgressing are the
// CCLComponentSet condition types the e2e specs assert on.
const (
	conditionValidated         = "Validated"
	conditionComponentsApplied = "ComponentsApplied"
	conditionProgressing       = "Progressing"
)

// conditionAvailable and conditionClusterConfigApplied are the CCLInfraPlane condition
// types the e2e specs assert on.
const (
	conditionAvailable            = "Available"
	conditionClusterConfigApplied = "ClusterConfigApplied"
	conditionDNSReady             = "DNSReady"
)

// applyManifest renders one of the Go-templated manifests under test/e2e/testdata with data
// and pipes it through `kubectl apply`. The templates are kept as YAML files rather than
// Go string literals so they read as the manifests a user would write.
func applyManifest(file string, data any) {
	GinkgoHelper()

	projectDir, err := utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())
	tmpl, err := template.New(file).Option("missingkey=error").
		ParseFiles(filepath.Join(projectDir, "test", "e2e", "testdata", file))
	Expect(err).NotTo(HaveOccurred(), "Failed to parse manifest template %s", file)

	var manifest strings.Builder
	Expect(tmpl.Execute(&manifest, data)).To(Succeed(), "Failed to render manifest %s", file)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest.String())
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply manifest %s", file)
}

// applyCCLInfraPlane creates a CCLInfraPlane in crNamespace.
func applyCCLInfraPlane(name, credentialsName string) {
	GinkgoHelper()
	applyCCLInfraPlaneIn(crNamespace, name, credentialsName)
}

// applyCCLInfraPlaneIn creates a CCLInfraPlane in ns, referencing a credentials Secret there.
func applyCCLInfraPlaneIn(ns, name, credentialsName string) {
	GinkgoHelper()
	applyManifest("cclinfraplane.yaml", map[string]string{
		"Name": name, "Namespace": ns, "CredentialsName": credentialsName,
		"Domain": infraPlaneDomain(name),
	})
}

// infraPlaneDomain is the spec.domain applyCCLInfraPlane gives the CCLInfraPlane name.
func infraPlaneDomain(name string) string {
	return name + ".e2e.example.com"
}

// applyCCLComponentSet creates a single-component CCLComponentSet targeting planeName.
func applyCCLComponentSet(name, planeName string) {
	GinkgoHelper()
	applyManifest("cclcomponentset.yaml", map[string]string{
		"Name": name, "Namespace": crNamespace, "PlaneName": planeName,
	})
}

// applyOrderedCCLComponentSet creates a two-component CCLComponentSet (argo-rollouts then
// keda) targeting planeName, matching examples/quickstart/v1alpha1_cclcomponentset.yaml.
func applyOrderedCCLComponentSet(name, planeName string) {
	GinkgoHelper()
	applyManifest("cclcomponentset_ordered.yaml", map[string]string{
		"Name": name, "Namespace": crNamespace, "PlaneName": planeName,
	})
}

// applyIstioCCLComponentSet creates a single-component istio CCLComponentSet targeting
// planeName, with values that fit the kind e2e cluster.
func applyIstioCCLComponentSet(name, planeName string) {
	GinkgoHelper()
	applyManifest("cclcomponentset_istio.yaml", map[string]string{
		"Name": name, "Namespace": crNamespace, "PlaneName": planeName,
	})
}

// applicationHealthyAndSynced reports whether an Application in crNamespace is Healthy and Synced.
// Rollout completion also requires matching spec and comparedTo, no pending or running
// operation, and no terminal failure attributed to the current source and revision.
func applicationHealthyAndSynced(name string) bool {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "application", name,
		"-n", crNamespace, "-o", "jsonpath={.status.health.status} {.status.sync.status}"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "Healthy Synced"
}

// markConverged simulates Argo CD in the quick tier by reporting Healthy/Synced,
// recording the stored source and destination in comparedTo, and completing the sync.
// Copying the stored source preserves unmodelled fields.
//
// The sync is Argo CD's own: the operator asks for none, so automated sync is what runs
// one after a comparison comes out OutOfSync.
func markConverged(name string) {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "application", name,
		"-n", crNamespace, "-o", "json"))
	Expect(err).NotTo(HaveOccurred())
	var application struct {
		Operation json.RawMessage `json:"operation"`
		Spec      struct {
			Source      json.RawMessage `json:"source"`
			Destination json.RawMessage `json:"destination"`
		} `json:"spec"`
	}
	Expect(json.Unmarshal([]byte(out), &application)).To(Succeed())

	now := time.Now().UTC()
	status := map[string]any{
		"health": map[string]any{"status": "Healthy"},
		"sync": map[string]any{
			"status": "Synced",
			"comparedTo": map[string]any{
				"source":      application.Spec.Source,
				"destination": application.Spec.Destination,
			},
		},
		"reconciledAt": now.Format(time.RFC3339),
	}
	patch := map[string]any{
		"status": status,
		"metadata": map[string]any{"annotations": map[string]any{
			"argocd.argoproj.io/refresh": nil,
		}},
	}
	operation := map[string]any{"sync": map[string]any{"source": application.Spec.Source}}
	if len(application.Operation) > 0 {
		// Argo CD copies a pending operation into the state and clears the field. The
		// CRD requires startedAt alongside the operation and its phase.
		operation = nil
		Expect(json.Unmarshal(application.Operation, &operation)).To(Succeed())
	}
	status["operationState"] = map[string]any{
		"operation":  operation,
		"phase":      "Succeeded",
		"startedAt":  now.Add(-2 * time.Second).Format(time.RFC3339),
		"finishedAt": now.Add(-time.Second).Format(time.RFC3339),
	}
	patch["operation"] = nil
	// Argo CD builds its status patch by diffing the old status against the new one, so
	// a key that is no longer there is sent as null and removed. A patch written by hand
	// only ever adds, so comparedTo is cleared first to get the same effect.
	_, err = utils.Run(exec.Command("kubectl", "patch", "application", name,
		"-n", crNamespace, "--type=merge", "-p",
		`{"status":{"sync":{"status":"Synced","comparedTo":null}}}`))
	Expect(err).NotTo(HaveOccurred())

	body, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command("kubectl", "patch", "application", name,
		"-n", crNamespace, "--type=merge", "-p", string(body)))
	Expect(err).NotTo(HaveOccurred())
}

// applyKedaCCLComponentSet creates or updates a single-component CCLComponentSet running keda
// with the given operator replica count, so a values change can be applied over an
// existing deployment.
func applyKedaCCLComponentSet(name, planeName string, operatorReplicas int) {
	GinkgoHelper()
	applyManifest("cclcomponentset_keda.yaml", map[string]any{
		"Name": name, "Namespace": crNamespace, "PlaneName": planeName,
		"WithValues": true, "OperatorReplicas": operatorReplicas,
	})
}

// applyKedaCCLComponentSetWithoutValues is the same component with no values at all, so
// that applying it over the one above takes helm back out of the Application's source.
// kubectl apply removes a field the manifest no longer has.
func applyKedaCCLComponentSetWithoutValues(name, planeName string) {
	GinkgoHelper()
	applyManifest("cclcomponentset_keda.yaml", map[string]any{
		"Name": name, "Namespace": crNamespace, "PlaneName": planeName,
		"WithValues": false, "OperatorReplicas": 0,
	})
}

// waitInfraPlaneAvailable blocks until a CCLInfraPlane in crNamespace reports Available=True.
func waitInfraPlaneAvailable(name string) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane", name,
			"-n", crNamespace, "-o",
			"jsonpath={.status.conditions[?(@.type=='Available')].status}"))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(Equal("True"))
	}, 2*time.Minute).Should(Succeed())
}

// applicationExists reports whether an Application of that name exists in crNamespace.
func applicationExists(name string) bool {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "get", "application", name, "-n", crNamespace))
	return err == nil
}

// mirrorSecretKey returns one decoded data key of a mirrored cluster Secret in the Argo CD
// namespace, or "" if the Secret or key is absent.
func mirrorSecretKey(name, key string) string {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "secret", name, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", key)))
	if err != nil {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return ""
	}
	return string(decoded)
}

// catalogSource reads the catalog repository and revision the deployed manager was given
// on its command line, so a hand-written Application can use exactly the source the
// operator would.
func catalogSource() (repoURL, revision string) {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "deployment", "ccluster-cp-controller-manager",
		"-n", namespace, "-o", `jsonpath={.spec.template.spec.containers[?(@.name=="manager")].args}`))
	Expect(err).NotTo(HaveOccurred())
	var args []string
	Expect(json.Unmarshal([]byte(out), &args)).To(Succeed())
	for _, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--catalog-repo-url="); ok {
			repoURL = v
		}
		if v, ok := strings.CutPrefix(arg, "--catalog-revision="); ok {
			revision = v
		}
	}
	Expect(repoURL).NotTo(BeEmpty(), "manager has no --catalog-repo-url: %s", out)
	Expect(revision).NotTo(BeEmpty(), "manager has no --catalog-revision: %s", out)
	return repoURL, revision
}

// deleteCCLComponentSet removes a CCLComponentSet and waits for its finalizer to clear.
func deleteCCLComponentSet(name string) {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "delete", "cclcomponentset", name,
		"-n", crNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m"))
	Expect(err).NotTo(HaveOccurred(), "Failed to delete CCLComponentSet %s", name)
}

// infraPlaneCondition reads one field of one status condition off a CCLInfraPlane.
func infraPlaneCondition(name, conditionType, field string) string {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "cclinfraplane", name,
		"-n", crNamespace, "-o", fmt.Sprintf(
			"jsonpath={.status.conditions[?(@.type=='%s')].%s}", conditionType, field)))
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// componentSetCondition reads one field of one status condition off a CCLComponentSet.
func componentSetCondition(name, conditionType, field string) string {
	GinkgoHelper()

	out, err := utils.Run(exec.Command("kubectl", "get", "cclcomponentset", name,
		"-n", crNamespace, "-o", fmt.Sprintf(
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
