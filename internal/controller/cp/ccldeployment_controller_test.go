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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

var _ = Describe("CCLDeployment Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			deploymentName  = "test-deployment"
			namespace       = "default"
			argoCDNamespace = "argocd"
			repoURL         = "https://github.com/NVIDIA/ccluster"
			revision        = "test-revision"

			planeA = "plane-a"
			planeB = "plane-b"

			firstComponent  = "argo-rollouts"
			secondComponent = "keda"
		)

		ctx := context.Background()

		deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: namespace}
		appSetKey := types.NamespacedName{Name: deploymentName, Namespace: argoCDNamespace}

		// Built in BeforeEach: k8sClient does not exist until BeforeSuite has run.
		var reconciler *CCLDeploymentReconciler

		// components is the two-component spec most specs use: one plain, one with
		// values, on two different infra planes.
		components := func() []cclusternvidiacomv1alpha1.ComponentSpec {
			return []cclusternvidiacomv1alpha1.ComponentSpec{
				{
					Name:          firstComponent,
					InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA},
					Chart:         "catalogs/cluster_base/argo_rollouts",
					Namespace:     "argo-rollouts",
				},
				{
					Name:          secondComponent,
					InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeB},
					Chart:         "catalogs/cluster_base/keda",
					Namespace:     "keda",
					Values: &apiextensionsv1.JSON{
						// Deliberately pretty-printed: the operator must compact
						// it, because helm.values is templated into a manifest.
						Raw: []byte("{\n  \"keda\": {\n    \"replicas\": 2\n  }\n}"),
					},
				},
			}
		}

		// createInfraPlane creates a CCLInfraPlane and reports it Available or not.
		createInfraPlane := func(name string, available bool) {
			GinkgoHelper()

			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalSecretReference{
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

		// createDeployment creates the CCLDeployment under test.
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

		getAppSet := func() *argocdv1alpha1.ApplicationSet {
			GinkgoHelper()

			appSet := &argocdv1alpha1.ApplicationSet{}
			Expect(k8sClient.Get(ctx, appSetKey, appSet)).To(Succeed())
			return appSet
		}

		conditionOf := func(deployment *cclusternvidiacomv1alpha1.CCLDeployment, name string) *metav1.Condition {
			GinkgoHelper()

			condition := apimeta.FindStatusCondition(deployment.Status.Conditions, name)
			Expect(condition).NotTo(BeNil(), "expected a %q condition", name)
			return condition
		}

		// createApplication stands in for Argo CD's applicationset-controller, which
		// envtest does not run: it creates the Application the ApplicationSet would
		// have generated for a component, with the labels the template stamps on.
		createApplication := func(component, health, sync string) {
			GinkgoHelper()

			application := &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName + "-" + component,
					Namespace: argoCDNamespace,
					Labels: map[string]string{
						labelDeploymentName:      deploymentName,
						labelDeploymentNamespace: namespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, application)).To(Succeed())

			// status is a subresource, so it does not survive Create.
			application.Status = argocdv1alpha1.ApplicationStatus{
				Health: argocdv1alpha1.ApplicationHealthStatus{Status: health},
				Sync:   argocdv1alpha1.ApplicationSyncStatus{Status: sync},
			}
			Expect(k8sClient.Status().Update(ctx, application)).To(Succeed())
		}

		BeforeEach(func() {
			reconciler = &CCLDeploymentReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				ArgoCDNamespace: argoCDNamespace,
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}

			By("creating the Argo CD namespace")
			err := k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: argoCDNamespace},
			})
			if !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		})

		AfterEach(func() {
			By("Cleanup the CCLDeployment")
			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			err := k8sClient.Get(ctx, deploymentKey, deployment)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())

				// envtest runs no garbage collector, so the object survives until
				// the controller drops its finalizer.
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
				Expect(err).NotTo(HaveOccurred())
				Eventually(func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, deploymentKey, deployment))
				}).Should(BeTrue())
			} else {
				Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			}

			By("Cleanup the ApplicationSet")
			appSet := &argocdv1alpha1.ApplicationSet{}
			if err := k8sClient.Get(ctx, appSetKey, appSet); err == nil {
				Expect(k8sClient.Delete(ctx, appSet)).To(Succeed())
			} else {
				Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			}

			By("Cleanup the generated Applications")
			var applications argocdv1alpha1.ApplicationList
			Expect(k8sClient.List(ctx, &applications, client.InNamespace(argoCDNamespace))).To(Succeed())
			for i := range applications.Items {
				Expect(k8sClient.Delete(ctx, &applications.Items[i])).To(Succeed())
			}

			By("Cleanup the CCLInfraPlanes")
			var infraPlanes cclusternvidiacomv1alpha1.CCLInfraPlaneList
			Expect(k8sClient.List(ctx, &infraPlanes, client.InNamespace(namespace))).To(Succeed())
			for i := range infraPlanes.Items {
				infraPlane := &infraPlanes.Items[i]
				// The CCLInfraPlane controller never ran here, so nothing would
				// remove a finalizer if one had been added.
				Expect(k8sClient.Delete(ctx, infraPlane)).To(Succeed())
			}
		})

		It("should build one generator element per component, in spec order", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())

			deployment := settle()
			Expect(conditionOf(deployment, conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))
			Expect(conditionOf(deployment, conditionTypeComponentsApplied).Reason).
				To(Equal(reasonApplicationSetCreated))

			spec := getAppSet().Spec
			Expect(spec.GoTemplate).To(BeTrue())
			Expect(spec.GoTemplateOptions).To(ConsistOf("missingkey=error"))
			Expect(spec.Generators).To(HaveLen(1))

			elements := spec.Generators[0].List.Elements
			Expect(elements).To(HaveLen(2))

			Expect(elements[0].Name).To(Equal(firstComponent))
			Expect(elements[0].Path).To(Equal("catalogs/cluster_base/argo_rollouts"))
			Expect(elements[0].InfraPlane).To(Equal(planeA))
			Expect(elements[0].Namespace).To(Equal("argo-rollouts"))
			Expect(elements[0].Wave).To(Equal("1"))

			Expect(elements[1].Name).To(Equal(secondComponent))
			Expect(elements[1].InfraPlane).To(Equal(planeB))
			Expect(elements[1].Wave).To(Equal("2"))
		})

		It("should compact values to single-line JSON, and default to an empty object", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			elements := getAppSet().Spec.Generators[0].List.Elements

			// A component with no values still gets a key: a Go template can
			// substitute one, but cannot decide whether it exists.
			Expect(elements[0].Values).To(Equal("{}"))

			Expect(elements[1].Values).To(Equal(`{"keda":{"replicas":2}}`))
			Expect(elements[1].Values).NotTo(ContainSubstring("\n"),
				"a multi-line value would break the manifest it is templated into")
		})

		It("should emit one RollingSync step per wave, in order", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			strategy := getAppSet().Spec.Strategy
			Expect(strategy).NotTo(BeNil())
			Expect(strategy.Type).To(Equal("RollingSync"))
			Expect(strategy.RollingSync).NotTo(BeNil())

			steps := strategy.RollingSync.Steps
			Expect(steps).To(HaveLen(2))
			for i, wave := range []string{"1", "2"} {
				Expect(steps[i].MatchExpressions).To(HaveLen(1))
				Expect(steps[i].MatchExpressions[0].Key).To(Equal(labelWave))
				Expect(steps[i].MatchExpressions[0].Operator).To(Equal("In"))
				Expect(steps[i].MatchExpressions[0].Values).To(ConsistOf(wave))
				Expect(steps[i].MaxUpdate).NotTo(BeNil())
				Expect(steps[i].MaxUpdate.String()).To(Equal("100%"))
			}
		})

		It("should template the Application from the operator's catalog flags", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			template := getAppSet().Spec.Template
			Expect(template.Name).To(Equal(deploymentName + "-{{.name}}"))
			Expect(template.Labels).To(HaveKeyWithValue(labelWave, "{{.wave}}"))
			Expect(template.Labels).To(HaveKeyWithValue(labelDeploymentName, deploymentName))
			Expect(template.Labels).To(HaveKeyWithValue(labelDeploymentNamespace, namespace))

			// Without this, deleting the CCLDeployment - or just dropping a
			// component - would leave the chart installed but unmanaged.
			Expect(template.Finalizers).To(ConsistOf(argocdv1alpha1.ResourcesFinalizer))

			source := template.Spec.Source
			Expect(source.RepoURL).To(Equal(repoURL))
			Expect(source.TargetRevision).To(Equal(revision))
			// path, not chart: the catalog is a Git repository.
			Expect(source.Path).To(Equal("{{.path}}"))
			Expect(source.Helm).NotTo(BeNil())
			Expect(source.Helm.Values).To(Equal("{{.values}}"))

			// By name, not by server: the name is the CCLInfraPlane's.
			Expect(template.Spec.Destination.Name).To(Equal("{{.infraPlane}}"))
			Expect(template.Spec.Destination.Namespace).To(Equal("{{.namespace}}"))

			Expect(template.Spec.SyncPolicy.Automated.Prune).To(BeTrue())
			Expect(template.Spec.SyncPolicy.Automated.SelfHeal).To(BeTrue())
			Expect(template.Spec.SyncPolicy.SyncOptions).To(ConsistOf("CreateNamespace=true"))
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
			// The name is stamped onto every generated Application as a label, and a
			// label value cannot exceed 63 characters.
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

		It("should preserve component order across an update", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			By("reversing the component order")
			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			reversed := components()
			deployment.Spec.Components = []cclusternvidiacomv1alpha1.ComponentSpec{
				reversed[1], reversed[0],
			}
			Expect(k8sClient.Update(ctx, deployment)).To(Succeed())

			reconcileAndGet()

			// listType=atomic is what makes this hold: an associative list would be
			// free to reorder, and the order here IS the rollout order.
			elements := getAppSet().Spec.Generators[0].List.Elements
			Expect(elements[0].Name).To(Equal(secondComponent))
			Expect(elements[0].Wave).To(Equal("1"))
			Expect(elements[1].Name).To(Equal(firstComponent))
			Expect(elements[1].Wave).To(Equal("2"))
		})

		It("should restore the ApplicationSet spec after it drifts", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			By("tampering with the ApplicationSet")
			appSet := getAppSet()
			appSet.Spec.Strategy = nil
			appSet.Spec.Generators[0].List.Elements = nil
			Expect(k8sClient.Update(ctx, appSet)).To(Succeed())

			reconcileAndGet()

			restored := getAppSet()
			Expect(restored.Spec.Strategy).NotTo(BeNil())
			Expect(restored.Spec.Generators[0].List.Elements).To(HaveLen(2))
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

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, appSetKey, &argocdv1alpha1.ApplicationSet{}))).To(BeTrue())
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
			Expect(getAppSet().Spec.Generators[0].List.Elements).To(HaveLen(2))
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

		It("should name the wave the rollout is waiting on", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			By("reporting the first wave while its Application has not appeared")
			deployment := reconcileAndGet()
			progressing := conditionOf(deployment, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(reasonWaveSyncing))
			Expect(progressing.Message).To(ContainSubstring("wave 1"))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))

			By("reporting the second wave once the first is healthy")
			createApplication(firstComponent, argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced)
			createApplication(secondComponent, argocdv1alpha1.HealthStatusProgressing, "OutOfSync")

			deployment = reconcileAndGet()
			progressing = conditionOf(deployment, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Message).To(ContainSubstring("Wave 2"))
			Expect(progressing.Message).To(ContainSubstring(secondComponent))

			By("completing once every component is healthy and synced")
			application := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      deploymentName + "-" + secondComponent,
				Namespace: argoCDNamespace,
			}, application)).To(Succeed())
			application.Status.Health.Status = argocdv1alpha1.HealthStatusHealthy
			application.Status.Sync.Status = argocdv1alpha1.SyncStatusSynced
			Expect(k8sClient.Status().Update(ctx, application)).To(Succeed())

			deployment = reconcileAndGet()
			progressing = conditionOf(deployment, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressing.Reason).To(Equal(reasonRolloutComplete))
		})

		It("should map a generated Application back to its CCLDeployment", func() {
			requests := reconciler.deploymentForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName + "-" + firstComponent,
					Namespace: argoCDNamespace,
					Labels: map[string]string{
						labelDeploymentName:      deploymentName,
						labelDeploymentNamespace: namespace,
					},
				},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: deploymentKey}))

			By("ignoring an Application Argo CD did not generate for us")
			Expect(reconciler.deploymentForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: argoCDNamespace},
			})).To(BeEmpty())
		})

		It("should delete the ApplicationSet when the CCLDeployment is deleted", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createDeployment(components())
			settle()

			deployment := &cclusternvidiacomv1alpha1.CCLDeployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(deployment, deploymentFinalizerName)).To(BeTrue())
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, appSetKey, &argocdv1alpha1.ApplicationSet{}))).To(BeTrue())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, deploymentKey, deployment))
			}).Should(BeTrue())
		})

		It("should refuse to overwrite an ApplicationSet owned by another resource", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)

			By("planting an ApplicationSet claimed by a CCLDeployment in another namespace")
			// The collision the ownership check exists for: two CCLDeployments of the
			// same name in different namespaces map onto one ApplicationSet name in
			// the Argo CD namespace.
			Expect(k8sClient.Create(ctx, &argocdv1alpha1.ApplicationSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: argoCDNamespace,
					Annotations: map[string]string{
						annotationDeploymentName:      deploymentName,
						annotationDeploymentNamespace: "other-namespace",
					},
				},
			})).To(Succeed())

			createDeployment(components())
			deployment := settle()

			applied := conditionOf(deployment, conditionTypeComponentsApplied)
			Expect(applied.Status).To(Equal(metav1.ConditionFalse))
			Expect(applied.Reason).To(Equal(reasonApplicationSetConflict))

			By("leaving the foreign ApplicationSet untouched")
			Expect(getAppSet().Spec.Generators).To(BeEmpty())

			By("leaving it in place on delete, too")
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, appSetKey, &argocdv1alpha1.ApplicationSet{})).To(Succeed())
		})
	})
})
