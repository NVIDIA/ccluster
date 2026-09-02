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

package cp

import (
	"context"
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/spire"
	spirecrds "github.com/NVIDIA/ccluster/catalogs/cluster_base/spire_crds"
	teleportkubeagent "github.com/NVIDIA/ccluster/catalogs/cluster_base/teleport_kube_agent"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

var _ = Describe("CCLDeployment Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			deploymentName = "test-deployment"
			namespace      = "default"
			repoURL        = "https://github.com/NVIDIA/ccluster"
			revision       = "test-revision"

			planeA = "plane-a"
			planeB = "plane-b"

			firstComponent  = "argo-rollouts"
			secondComponent = "keda"
		)

		ctx := context.Background()

		deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: namespace}

		// Built in BeforeEach: k8sClient does not exist until BeforeSuite has run.
		var reconciler *CCLDeploymentReconciler

		// Applications live alongside their CCLDeployment, not in the Argo CD namespace.
		appKey := func(component string) types.NamespacedName {
			return types.NamespacedName{
				Name:      applicationName(deploymentName, component),
				Namespace: namespace,
			}
		}

		// components is the two-component spec most specs use: one plain, one with
		// values, on two different infra planes.
		components := func() []cclusternvidiacomv1alpha1.ComponentSpec {
			return []cclusternvidiacomv1alpha1.ComponentSpec{
				{
					Name:          firstComponent,
					InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA},
					ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
						Chart:     "catalogs/cluster_base/argo_rollouts",
						Namespace: "argo-rollouts",
					},
				},
				{
					Name:          secondComponent,
					InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeB},
					ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
						Chart:     "catalogs/cluster_base/keda",
						Namespace: "keda",
						Values: &apiextensionsv1.JSON{
							Raw: []byte(`{"keda":{"operator":{"replicaCount":2}}}`),
						},
					},
				},
			}
		}

		createInfraPlane := func(name string, available bool) {
			GinkgoHelper()

			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{
						Name: name + "-credentials",
					},
				},
			}
			Expect(k8sClient.Create(ctx, infraPlane)).To(Succeed())

			status := metav1.ConditionFalse
			if available {
				status = metav1.ConditionTrue
			}
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:    conditionTypeAvailable,
				Status:  status,
				Reason:  reasonClusterSecretCreated,
				Message: "set by the test",
			})
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())
		}

		createDeployment := func(specComponents []cclusternvidiacomv1alpha1.ComponentSpec) {
			GinkgoHelper()

			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace},
				Spec:       cclusternvidiacomv1alpha1.CCLDeploymentSpec{Components: specComponents},
			})).To(Succeed())
		}

		reconcileAndGet := func() *cclusternvidiacomv1alpha1.CCLDeployment {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())

			refreshed := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, refreshed)).To(Succeed())
			return refreshed
		}

		// settle reconciles twice: the first pass only writes the finalizer.
		settle := func() *cclusternvidiacomv1alpha1.CCLDeployment {
			GinkgoHelper()

			reconcileAndGet()
			return reconcileAndGet()
		}

		// reconcileOnly is reconcileAndGet without the re-fetch, for passes after which
		// the CCLDeployment may no longer exist.
		reconcileOnly := func() {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())
		}

		// finishTeardown stands in for Argo CD completing an uninstall: it drops the
		// cascade finalizer so the terminating Application actually goes away.
		finishTeardown := func(component string) {
			GinkgoHelper()

			application := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, appKey(component), application)).To(Succeed())
			Expect(controllerutil.RemoveFinalizer(application,
				argocdv1alpha1.ResourcesFinalizer)).To(BeTrue())
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(
					k8sClient.Get(ctx, appKey(component), &argocdv1alpha1.Application{}))
			}).Should(BeTrue())
		}

		getApplication := func(component string) *argocdv1alpha1.Application {
			GinkgoHelper()

			application := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, appKey(component), application)).To(Succeed())
			return application
		}

		applicationExists := func(component string) bool {
			GinkgoHelper()

			err := k8sClient.Get(ctx, appKey(component), &argocdv1alpha1.Application{})
			if apierrors.IsNotFound(err) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return true
		}

		// setHealth stands in for Argo CD, which envtest does not run: it reports a
		// health and sync status on an Application the operator created.
		setHealth := func(component, health, sync string) {
			GinkgoHelper()

			application := getApplication(component)
			application.Status = &argocdv1alpha1.ApplicationStatus{
				Health: argocdv1alpha1.ApplicationHealthStatus{Status: health},
				Sync:   argocdv1alpha1.ApplicationSyncStatus{Status: sync},
			}
			Expect(k8sClient.Status().Update(ctx, application)).To(Succeed())
		}

		// release drives the rollout forward: each component is reported healthy,
		// which lets the operator create the next one's Application.
		release := func(componentNames ...string) {
			GinkgoHelper()

			for _, component := range componentNames {
				setHealth(component, argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced)
				reconcileAndGet()
			}
		}

		conditionOf := func(deployment *cclusternvidiacomv1alpha1.CCLDeployment, name string) *metav1.Condition {
			GinkgoHelper()

			condition := apimeta.FindStatusCondition(deployment.Status.Conditions, name)
			Expect(condition).NotTo(BeNil(), "expected a %q condition", name)
			return condition
		}

		BeforeEach(func() {
			reconciler = &CCLDeploymentReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}
		})

		AfterEach(func() {
			By("Cleanup the CCLDeployment")
			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			err := k8sClient.Get(ctx, deploymentKey, deployment)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())

				// Teardown removes one Application per pass and waits for it to go.
				// Nothing here runs Argo CD or a garbage collector, so the cascade
				// finalizers have to be cleared as we go, and the CCLDeployment
				// survives until they are.
				Eventually(func() bool {
					var applications argocdv1alpha1.ApplicationList
					Expect(k8sClient.List(ctx, &applications,
						client.InNamespace(namespace))).To(Succeed())
					for i := range applications.Items {
						application := &applications.Items[i]
						if application.DeletionTimestamp.IsZero() {
							continue
						}
						if controllerutil.RemoveFinalizer(application,
							argocdv1alpha1.ResourcesFinalizer) {
							Expect(client.IgnoreNotFound(
								k8sClient.Update(ctx, application))).To(Succeed())
						}
					}

					_, err := reconciler.Reconcile(ctx,
						reconcile.Request{NamespacedName: deploymentKey})
					Expect(err).NotTo(HaveOccurred())

					return apierrors.IsNotFound(k8sClient.Get(ctx, deploymentKey, deployment))
				}, "15s", "50ms").Should(BeTrue())
			} else {
				Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			}

			By("Cleanup the Applications")
			var applications argocdv1alpha1.ApplicationList
			Expect(k8sClient.List(ctx, &applications, client.InNamespace(namespace))).To(Succeed())
			for i := range applications.Items {
				application := &applications.Items[i]
				// Nothing runs Argo CD here, so its cascade finalizer would keep
				// every Application in the cluster forever.
				if controllerutil.RemoveFinalizer(application, argocdv1alpha1.ResourcesFinalizer) {
					Expect(k8sClient.Update(ctx, application)).To(Succeed())
				}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, application))).To(Succeed())
			}

			By("Cleanup the CCLInfraPlanes")
			var infraPlanes cclusternvidiacomv1alpha1.CCLInfraPlaneList
			Expect(k8sClient.List(ctx, &infraPlanes, client.InNamespace(namespace))).To(Succeed())
			for i := range infraPlanes.Items {
				Expect(k8sClient.Delete(ctx, &infraPlanes.Items[i])).To(Succeed())
			}
		})

		It("should create only the first component's Application until it is healthy", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())

			deployment := settle()
			Expect(conditionOf(deployment, conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))
			Expect(conditionOf(deployment, conditionTypeComponentsApplied).Reason).
				To(Equal(reasonApplicationsCreated))

			By("creating only the first component")
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("holding while it is still converging")
			setHealth(firstComponent, argocdv1alpha1.HealthStatusProgressing, "OutOfSync")
			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("releasing the second once the first is healthy")
			release(firstComponent)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			Expect(getApplication(firstComponent).Labels).To(HaveKeyWithValue(labelWave, "1"))
			Expect(getApplication(secondComponent).Labels).To(HaveKeyWithValue(labelWave, "2"))
		})

		It("should never withdraw a released component that becomes unhealthy", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()
			release(firstComponent)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			By("degrading the first component")
			setHealth(firstComponent, argocdv1alpha1.HealthStatusDegraded, "Synced")
			reconcileAndGet()

			// Withdrawing it would delete a running Application and, through the
			// cascade finalizer, uninstall the chart it is serving.
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should pass component values through untouched as structured Helm values", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()
			release(firstComponent)

			By("omitting the helm block for a component with no values")
			Expect(getApplication(firstComponent).Spec.Source.Helm).To(BeNil())

			By("copying values verbatim for a component that has them")
			helm := getApplication(secondComponent).Spec.Source.Helm
			Expect(helm).NotTo(BeNil())
			Expect(helm.ValuesObject).NotTo(BeNil())

			var decoded map[string]any
			Expect(json.Unmarshal(helm.ValuesObject.Raw, &decoded)).To(Succeed())
			Expect(decoded).To(HaveKey("keda"))
		})

		It("should leave the sync policy to Argo CD", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			// Argo CD's RollingSync gates Applications by switching automated sync
			// off and, as of v3.5.1, never switches it back on - which kills selfHeal.
			// Ordering is done by withholding Applications instead, so nothing here
			// may disable automated sync.
			policy := getApplication(firstComponent).Spec.SyncPolicy
			Expect(policy.Automated).NotTo(BeNil())
			Expect(policy.Automated.Prune).To(BeTrue())
			Expect(policy.Automated.SelfHeal).To(BeTrue())
			Expect(policy.SyncOptions).To(
				ConsistOf("CreateNamespace=true", "ServerSideApply=true"))
		})

		It("should target the component's infra plane from the operator's catalog flags", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			application := getApplication(firstComponent)
			Expect(application.Spec.Source.RepoURL).To(Equal(repoURL))
			Expect(application.Spec.Source.TargetRevision).To(Equal(revision))
			// path, not chart: the catalog is a Git repository.
			Expect(application.Spec.Source.Path).To(Equal("catalogs/cluster_base/argo_rollouts"))
			// By name, not by server: the name is the one the CCLInfraPlane's mirrored
			// Secret registered, which is prefixed with the namespace.
			Expect(application.Spec.Destination.Name).To(Equal(namespace + "-" + planeA))
			Expect(application.Spec.Destination.Namespace).To(Equal("argo-rollouts"))

			By("belonging to the namespace's AppProject, in the namespace itself")
			Expect(application.Namespace).To(Equal(namespace))
			Expect(application.Spec.Project).To(Equal(namespace))

			By("carrying the cascade finalizer")
			Expect(application.Finalizers).To(ContainElement(argocdv1alpha1.ResourcesFinalizer))

			By("carrying the back-reference to its CCLDeployment")
			Expect(application.Annotations).To(HaveKeyWithValue(annotationDeploymentName, deploymentName))
			Expect(application.Annotations).To(HaveKeyWithValue(annotationDeploymentNamespace, namespace))
			Expect(application.Labels).To(HaveKeyWithValue(labelDeploymentName, deploymentName))
		})

		// The catalog packages under catalogs/ are the only place the shape of a
		// chart's values is written down, and nothing checks them at admission. What
		// this does check is the other half: that entries built by those packages are
		// accepted by the installed CRD schema - the DNS-1123 patterns on name and
		// namespace, the 512 character limit on chart, and that catalogs.Entry's
		// marshalled values survive a round trip through the API server's
		// x-kubernetes-preserve-unknown-fields. A table test cannot cover that; it
		// needs an API server.
		It("should accept components built by the catalog packages", func() {
			crds, err := spirecrds.New("spire-system", spirecrds.Values{})
			Expect(err).NotTo(HaveOccurred())

			server, err := spire.New("spire-system", spire.Values{})
			Expect(err).NotTo(HaveOccurred())

			// teleport-kube-agent is the one component whose values are
			// mandatory, so it is also the one that proves New reports them
			// rather than building an entry full of placeholders.
			_, err = teleportkubeagent.New("teleport", teleportkubeagent.Values{})
			Expect(err).To(HaveOccurred())

			agent, err := teleportkubeagent.New("teleport", teleportkubeagent.Values{
				TeleportKubeAgent: &teleportkubeagent.ChartValues{
					ProxyAddr:           "teleport.example.com:443",
					TeleportClusterName: "prod",
					KubeClusterName:     planeA,
					JoinParams: teleportkubeagent.JoinParams{
						TokenName: "kube-token",
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			ref := cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA}
			specs := []cclusternvidiacomv1alpha1.ComponentSpec{
				{Name: "spire-crds", InfraPlaneRef: ref, ComponentEntry: crds},
				{Name: "spire", InfraPlaneRef: ref, ComponentEntry: server},
				{Name: "teleport-kube-agent", InfraPlaneRef: ref, ComponentEntry: agent},
			}

			createInfraPlane(planeA, true)
			createDeployment(specs)

			By("releasing the first component, and only that one")
			settle()

			application := getApplication("spire-crds")
			Expect(application.Spec.Source.Path).To(Equal("catalogs/cluster_base/spire_crds"))
			Expect(application.Spec.Destination.Namespace).To(Equal("spire-system"))
			Expect(applicationExists("spire")).To(BeFalse())
		})

		It("should reject duplicate component names", func() {
			duplicate := components()
			duplicate[1].Name = duplicate[0].Name

			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace},
				Spec:       cclusternvidiacomv1alpha1.CCLDeploymentSpec{Components: duplicate},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("component names must be unique"))
		})

		It("should reject a name too long to be a label value", func() {
			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      strings.Repeat("a", 64),
					Namespace: namespace,
				},
				Spec: cclusternvidiacomv1alpha1.CCLDeploymentSpec{Components: components()},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at most 63 characters"))
		})

		// This rule is load-bearing, not cosmetic: releaseCount clamps to the component
		// count precisely because an empty spec would otherwise slice Components[:1] and
		// panic. See TestReleaseCountEmptySpecDoesNotPanic. Both guards are kept - the
		// API rejects the object, and the controller survives it if the marker is ever
		// relaxed.
		It("should reject a CCLDeployment with no components", func() {
			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLDeploymentSpec{
					Components: []cclusternvidiacomv1alpha1.ComponentSpec{},
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("should have at least 1 items"))
		})

		It("should restore an Application spec after it drifts", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			By("repointing the Application at another repository")
			application := getApplication(firstComponent)
			application.Spec.Source.RepoURL = "https://example.com/tampered"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			reconcileAndGet()

			Expect(getApplication(firstComponent).Spec.Source.RepoURL).To(Equal(repoURL))
		})

		It("should prune the Application of a component removed from the spec", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()
			release(firstComponent)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			By("removing the second component")
			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			deployment.Spec.Components = deployment.Spec.Components[:1]
			Expect(k8sClient.Update(ctx, deployment)).To(Succeed())

			reconcileAndGet()

			// Argo CD is not running here, so its cascade finalizer holds the object
			// in the cluster while it would be uninstalling the chart. Deletion
			// having been requested is what the operator is responsible for.
			removed := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, appKey(secondComponent), removed)).To(Succeed())
			Expect(removed.DeletionTimestamp).NotTo(BeNil())
			Expect(removed.Finalizers).To(ContainElement(argocdv1alpha1.ResourcesFinalizer))

			By("leaving the surviving component alone")
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
		})

		It("should report InfraPlaneNotFound and apply nothing when a plane is missing", func() {
			createInfraPlane(planeA, true)
			// planeB is deliberately absent.
			createDeployment(components())

			deployment := settle()

			validated := conditionOf(deployment, conditionTypeValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(reasonInfraPlaneNotFound))
			Expect(validated.Message).To(ContainSubstring(planeB))

			Expect(applicationExists(firstComponent)).To(BeFalse())
		})

		It("should report InfraPlaneNotReady until the plane becomes Available", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, false)
			createDeployment(components())

			deployment := settle()
			validated := conditionOf(deployment, conditionTypeValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(reasonInfraPlaneNotReady))

			By("making the plane Available")
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: planeB, Namespace: namespace}, infraPlane)).To(Succeed())
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:    conditionTypeAvailable,
				Status:  metav1.ConditionTrue,
				Reason:  reasonClusterSecretCreated,
				Message: "set by the test",
			})
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())

			deployment = reconcileAndGet()
			Expect(conditionOf(deployment, conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))

			// Only the first: nothing is deployed yet, so there is nothing for the
			// second to follow.
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeFalse())
		})

		It("should report which component the rollout is waiting on", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())

			deployment := settle()
			progressing := conditionOf(deployment, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(reasonWaveSyncing))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))

			release(firstComponent)
			deployment = reconcileAndGet()
			Expect(conditionOf(deployment, conditionTypeProgressing).Message).
				To(ContainSubstring(secondComponent))

			release(secondComponent)
			deployment = reconcileAndGet()
			progressing = conditionOf(deployment, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressing.Reason).To(Equal(reasonRolloutComplete))
		})

		It("should enqueue the CCLDeployments that reference a CCLInfraPlane", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())

			requests := reconciler.deploymentsForInfraPlane(ctx,
				&cclusternvidiacomv1alpha1.CCLInfraPlane{
					ObjectMeta: metav1.ObjectMeta{Name: planeB, Namespace: namespace},
				})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: deploymentKey}))

			By("ignoring a CCLInfraPlane no component references")
			Expect(reconciler.deploymentsForInfraPlane(ctx,
				&cclusternvidiacomv1alpha1.CCLInfraPlane{
					ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: namespace},
				})).To(BeEmpty())
		})

		It("should map an Application back to its CCLDeployment", func() {
			requests := reconciler.deploymentForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      applicationName(deploymentName, firstComponent),
					Namespace: namespace,
					Labels:    map[string]string{labelDeploymentName: deploymentName},
				},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: deploymentKey}))

			By("ignoring an Application the operator did not create")
			Expect(reconciler.deploymentForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: namespace},
			})).To(BeEmpty())
		})

		It("should tear the components down in reverse order", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()
			release(firstComponent)

			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(deployment, deploymentFinalizerName)).To(BeTrue())
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())

			By("removing the last component first")
			reconcileOnly()
			Expect(getApplication(secondComponent).DeletionTimestamp).NotTo(BeNil())
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())

			By("waiting for it rather than starting the next")
			// The point of the exercise: an earlier component must outlive a later
			// one that may still be using it, so issuing both deletes at once - which
			// Argo CD would then run concurrently - is not good enough.
			reconcileOnly()
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed(),
				"the CCLDeployment must outlive its components")

			By("moving on once Argo CD has finished with it")
			finishTeardown(secondComponent)
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).NotTo(BeNil())
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())

			By("dropping the finalizer only when everything is gone")
			finishTeardown(firstComponent)
			reconcileOnly()
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, deploymentKey, deployment))
			}).Should(BeTrue())
		})

		It("should requeue while a component is still terminating", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())
			// A backstop for a missed Application watch event, so a teardown that
			// stalls does not do so silently.
			Expect(result.RequeueAfter).To(Equal(teardownRetryInterval))

			finishTeardown(firstComponent)
			result, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})

		It("should refuse to touch an Application owned by another CCLDeployment", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)

			By("planting an Application whose back-reference points at a CCLDeployment elsewhere")
			planted := &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      applicationName(deploymentName, firstComponent),
					Namespace: namespace,
					Annotations: map[string]string{
						annotationDeploymentName:      deploymentName,
						annotationDeploymentNamespace: "other-namespace",
					},
				},
				Spec: argocdv1alpha1.ApplicationSpec{
					Project: "default",
					Source:  argocdv1alpha1.ApplicationSource{RepoURL: "https://example.com/someone-elses"},
				},
			}
			Expect(k8sClient.Create(ctx, planted)).To(Succeed())

			createDeployment(components())
			deployment := settle()

			applied := conditionOf(deployment, conditionTypeComponentsApplied)
			Expect(applied.Status).To(Equal(metav1.ConditionFalse))
			Expect(applied.Reason).To(Equal(reasonApplicationConflict))

			By("leaving the foreign Application untouched")
			Expect(getApplication(firstComponent).Spec.Source.RepoURL).
				To(Equal("https://example.com/someone-elses"))

			By("leaving it in place on delete, too")
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
		})
	})
})
