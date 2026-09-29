/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/spire"
	spirecrds "github.com/NVIDIA/ccluster/catalogs/cluster_base/spire_crds"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

var _ = Describe("CCLComponentSet Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			componentSetName = "test-componentset"
			namespace        = "default"
			repoURL          = "https://github.com/NVIDIA/ccluster"
			revision         = "test-revision"

			planeA = "plane-a"
			planeB = "plane-b"

			firstComponent  = "argo-rollouts"
			secondComponent = "keda"
		)

		ctx := context.Background()

		componentSetKey := types.NamespacedName{Name: componentSetName, Namespace: namespace}

		// Built in BeforeEach: k8sClient does not exist until BeforeSuite has run.
		var reconciler *CCLComponentSetReconciler

		// Applications live alongside their CCLComponentSet, not in the Argo CD namespace.
		appKey := func(component string) types.NamespacedName {
			return types.NamespacedName{
				Name:      applicationName(componentSetName, component),
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
				Type:               conditionTypeAvailable,
				Status:             status,
				Reason:             reasonClusterSecretCreated,
				Message:            "set by the test",
				ObservedGeneration: infraPlane.Generation,
			})
			infraPlane.Status.ObservedGeneration = infraPlane.Generation
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())
		}

		createComponentSet := func(specComponents []cclusternvidiacomv1alpha1.ComponentSpec) {
			GinkgoHelper()

			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
				ObjectMeta: metav1.ObjectMeta{Name: componentSetName, Namespace: namespace},
				Spec:       cclusternvidiacomv1alpha1.CCLComponentSetSpec{Components: specComponents},
			})).To(Succeed())
		}

		reconcileAndGet := func() *cclusternvidiacomv1alpha1.CCLComponentSet {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(err).NotTo(HaveOccurred())

			refreshed := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, refreshed)).To(Succeed())
			return refreshed
		}

		// settle reconciles twice: the first pass only writes the finalizer.
		settle := func() *cclusternvidiacomv1alpha1.CCLComponentSet {
			GinkgoHelper()

			reconcileAndGet()
			return reconcileAndGet()
		}

		// reconcileOnly is reconcileAndGet without the re-fetch, for passes after which
		// the CCLComponentSet may no longer exist.
		reconcileOnly := func() {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
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

		// setHealth stands in for Argo CD, which envtest does not run: it takes the sync
		// the operator asked for, reports it succeeded, and reports a health and sync
		// status on the Application.
		//
		// It patches, because that is what Argo CD does. An update through the partial
		// Argo CD type would also wipe every spec field that type does not model, and
		// the Application CRD has no status subresource to stop it - so no test could
		// set up a spec the operator does not fully describe.
		setHealth := func(component, health, sync string) {
			GinkgoHelper()

			application := getApplication(component)
			status := map[string]any{
				"health": map[string]any{"status": health},
				"sync":   map[string]any{"status": sync},
			}
			patch := map[string]any{"status": status}
			if application.Operation != nil {
				// Argo CD copies the request into the state and clears the field. The real
				// CRD requires startedAt alongside the operation and its phase; the stub
				// here does not, so only the e2e tier catches a stand-in that omits it.
				status["operationState"] = map[string]any{
					"operation": application.Operation,
					"phase":     argocdv1alpha1.OperationSucceeded,
					"startedAt": metav1.Now(),
				}
				patch["operation"] = nil
			}
			raw, err := json.Marshal(patch)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Patch(ctx, application,
				client.RawPatch(types.MergePatchType, raw))).To(Succeed())
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

		conditionOf := func(componentSet *cclusternvidiacomv1alpha1.CCLComponentSet, name string) *metav1.Condition {
			GinkgoHelper()

			condition := apimeta.FindStatusCondition(componentSet.Status.Conditions, name)
			Expect(condition).NotTo(BeNil(), "expected a %q condition", name)
			return condition
		}

		BeforeEach(func() {
			reconciler = &CCLComponentSetReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}
		})

		AfterEach(func() {
			By("Cleanup the CCLComponentSet")
			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			err := k8sClient.Get(ctx, componentSetKey, componentSet)
			if err == nil {
				Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())

				// Teardown removes one Application per pass and waits for it to go.
				// Nothing here runs Argo CD or a garbage collector, so the cascade
				// finalizers have to be cleared as we go, and the CCLComponentSet
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
						reconcile.Request{NamespacedName: componentSetKey})
					Expect(err).NotTo(HaveOccurred())

					return apierrors.IsNotFound(k8sClient.Get(ctx, componentSetKey, componentSet))
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
			createComponentSet(components())

			componentSet := settle()
			Expect(conditionOf(componentSet, conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))
			Expect(conditionOf(componentSet, conditionTypeComponentsApplied).Reason).
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
			createComponentSet(components())
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
			createComponentSet(components())
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
			createComponentSet(components())
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
			createComponentSet(components())
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

			By("carrying the back-reference to its CCLComponentSet")
			Expect(application.Annotations).To(HaveKeyWithValue(annotationComponentSetName, componentSetName))
			Expect(application.Annotations).To(HaveKeyWithValue(annotationComponentSetNamespace, namespace))
			Expect(application.Labels).To(HaveKeyWithValue(labelComponentSetName, componentSetName))
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

			// kai_gpu_queues has required values, so it also proves New reports
			// them rather than building an entry the chart cannot render.
			_, err = kaigpuqueues.New("kai-scheduler", kaigpuqueues.Values{
				Queues: []kaigpuqueues.Queue{{GPUQuota: 4}},
			})
			Expect(err).To(HaveOccurred())

			queues, err := kaigpuqueues.New("kai-scheduler", kaigpuqueues.Values{
				Queues: []kaigpuqueues.Queue{{Name: "team-a", GPUQuota: 4}},
			})
			Expect(err).NotTo(HaveOccurred())

			ref := cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA}
			specs := []cclusternvidiacomv1alpha1.ComponentSpec{
				{Name: "spire-crds", InfraPlaneRef: ref, ComponentEntry: crds},
				{Name: "spire", InfraPlaneRef: ref, ComponentEntry: server},
				{Name: "kai-gpu-queues", InfraPlaneRef: ref, ComponentEntry: queues},
			}

			createInfraPlane(planeA, true)
			createComponentSet(specs)

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

			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
				ObjectMeta: metav1.ObjectMeta{Name: componentSetName, Namespace: namespace},
				Spec:       cclusternvidiacomv1alpha1.CCLComponentSetSpec{Components: duplicate},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("component names must be unique"))
		})

		It("should reject a name too long to be a label value", func() {
			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      strings.Repeat("a", 64),
					Namespace: namespace,
				},
				Spec: cclusternvidiacomv1alpha1.CCLComponentSetSpec{Components: components()},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at most 63 characters"))
		})

		// This rule is load-bearing, not cosmetic: releaseCount clamps to the component
		// count precisely because an empty spec would otherwise slice Components[:1] and
		// panic. See TestReleaseCountEmptySpecDoesNotPanic. Both guards are kept - the
		// API rejects the object, and the controller survives it if the marker is ever
		// relaxed.
		It("should reject a CCLComponentSet with no components", func() {
			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
				ObjectMeta: metav1.ObjectMeta{Name: componentSetName, Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLComponentSetSpec{
					Components: []cclusternvidiacomv1alpha1.ComponentSpec{},
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("should have at least 1 items"))
		})

		It("should restore an Application spec after it drifts", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
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
			createComponentSet(components())
			settle()
			release(firstComponent)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			By("removing the second component")
			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			componentSet.Spec.Components = componentSet.Spec.Components[:1]
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())

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
			createComponentSet(components())

			componentSet := settle()

			validated := conditionOf(componentSet, conditionTypeValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(reasonInfraPlaneNotFound))
			Expect(validated.Message).To(ContainSubstring(planeB))

			Expect(applicationExists(firstComponent)).To(BeFalse())
		})

		It("should report InfraPlaneNotReady until the plane becomes Available", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, false)
			createComponentSet(components())

			componentSet := settle()
			validated := conditionOf(componentSet, conditionTypeValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(reasonInfraPlaneNotReady))

			By("making the plane Available")
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: planeB, Namespace: namespace}, infraPlane)).To(Succeed())
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:               conditionTypeAvailable,
				Status:             metav1.ConditionTrue,
				Reason:             reasonClusterSecretCreated,
				Message:            "set by the test",
				ObservedGeneration: infraPlane.Generation,
			})
			infraPlane.Status.ObservedGeneration = infraPlane.Generation
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())

			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))

			// Only the first: nothing is deployed yet, so there is nothing for the
			// second to follow.
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeFalse())
		})

		// A plane whose status is still the previous generation's says nothing about the
		// cluster its spec now names: the credentials it points at may not have been
		// mirrored yet, so an Application written against it could address a cluster
		// Argo CD does not know. Available alone does not settle that, because the
		// condition outlives the spec it was computed for.
		It("should wait for a CCLInfraPlane whose status is behind its spec", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			Expect(conditionOf(settle(), conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))

			By("rotating the plane's credentials without letting its controller catch up")
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			planeKey := types.NamespacedName{Name: planeA, Namespace: namespace}
			Expect(k8sClient.Get(ctx, planeKey, infraPlane)).To(Succeed())
			infraPlane.Spec.ClusterCredentialsRef.Name = planeA + "-rotated"
			Expect(k8sClient.Update(ctx, infraPlane)).To(Succeed())

			validated := conditionOf(reconcileAndGet(), conditionTypeValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(reasonInfraPlaneNotReady))
			// Available still reads True here, and that is what kubectl prints, so the
			// message must not blame the condition.
			Expect(validated.Message).To(ContainSubstring("has not caught up with its spec"))

			By("letting the plane's controller catch up")
			Expect(k8sClient.Get(ctx, planeKey, infraPlane)).To(Succeed())
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:               conditionTypeAvailable,
				Status:             metav1.ConditionTrue,
				Reason:             reasonClusterSecretCreated,
				Message:            "set by the test",
				ObservedGeneration: infraPlane.Generation,
			})
			infraPlane.Status.ObservedGeneration = infraPlane.Generation
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())

			Expect(conditionOf(reconcileAndGet(), conditionTypeValidated).Status).
				To(Equal(metav1.ConditionTrue))
		})

		It("should report which component the rollout is waiting on", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())

			componentSet := settle()
			progressing := conditionOf(componentSet, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(reasonWaveSyncing))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))

			release(firstComponent)
			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, conditionTypeProgressing).Message).
				To(ContainSubstring(secondComponent))

			release(secondComponent)
			componentSet = reconcileAndGet()
			progressing = conditionOf(componentSet, conditionTypeProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressing.Reason).To(Equal(reasonRolloutComplete))
		})

		It("should preserve explicit null values when updating an Application", func() {
			createInfraPlane(planeA, true)
			spec := components()[:1]
			spec[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"config":{"removeMe":1,"keep":2}}`)}
			createComponentSet(spec)
			componentSet := settle()
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{
				Raw: []byte(`{"config":{"removeMe":null,"keep":2}}`),
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			componentSet = reconcileAndGet()
			Expect(string(getApplication(firstComponent).Spec.Source.Helm.ValuesObject.Raw)).
				To(MatchJSON(string(componentSet.Spec.Components[0].Values.Raw)))

			setHealth(firstComponent, argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced)
			Expect(conditionOf(reconcileAndGet(), conditionTypeProgressing).Status).To(Equal(metav1.ConditionFalse))
		})

		// The two halves of the write path. A read into the partial Argo CD type drops
		// spec fields the operator does not model, so an edit that adds nothing else is
		// invisible to the comparison and is not written back on its own. Once anything
		// the operator can see does change, /spec is replaced rather than merged, and
		// the invisible field goes with it.
		It("should leave an unchanged Application alone and replace its whole spec when it changes", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()

			By("adding a spec field the operator does not model")
			application := getApplication(firstComponent)
			Expect(k8sClient.Patch(ctx, application, client.RawPatch(types.MergePatchType,
				[]byte(`{"spec":{"syncPolicy":{"automated":{"enabled":false}}}}`)))).To(Succeed())

			version := getApplication(firstComponent).ResourceVersion
			reconcileAndGet()
			Expect(getApplication(firstComponent).ResourceVersion).To(Equal(version),
				"an Application that reads back unchanged must not be rewritten")

			By("drifting a modelled field, which the operator does see")
			application = getApplication(firstComponent)
			application.Spec.Source.Path = "catalogs/cluster_base/tampered"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			reconcileAndGet()
			restored := getApplication(firstComponent)
			Expect(restored.Spec.Source.Path).To(Equal(components()[0].Chart))

			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(argocdv1alpha1.GroupVersion.WithKind("Application"))
			Expect(k8sClient.Get(ctx, appKey(firstComponent), raw)).To(Succeed())
			_, found, err := unstructured.NestedBool(raw.Object, "spec", "syncPolicy", "automated", "enabled")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "the operator owns the entire spec")

			version = restored.ResourceVersion
			reconcileAndGet()
			Expect(getApplication(firstComponent).ResourceVersion).To(Equal(version))
		})

		It("should leave the status Argo CD owns alone when it rewrites an Application", func() {
			// Read unstructured to assert fields absent from the partial Go type.
			readApplication := func() *unstructured.Unstructured {
				GinkgoHelper()

				application := &unstructured.Unstructured{}
				application.SetGroupVersionKind(
					argocdv1alpha1.GroupVersion.WithKind("Application"))
				Expect(k8sClient.Get(ctx, appKey(firstComponent), application)).To(Succeed())
				return application
			}

			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()

			By("Argo CD writing the status it owns")
			application := readApplication()
			Expect(unstructured.SetNestedMap(application.Object, map[string]any{
				"health": map[string]any{"status": argocdv1alpha1.HealthStatusHealthy},
				"sync": map[string]any{
					"status":   argocdv1alpha1.SyncStatusSynced,
					"revision": "abc123",
				},
				"operationState":      map[string]any{"phase": "Succeeded"},
				"reconciledAt":        "2026-09-16T10:00:00Z",
				"controllerNamespace": "argocd",
			}, "status")).To(Succeed())
			Expect(unstructured.SetNestedMap(application.Object, map[string]any{
				"sync": map[string]any{"revision": "abc123"},
			}, "operation")).To(Succeed())
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			By("the operator rewriting the Application because the component's values changed")
			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{
				Raw: []byte(`{"replicaCount":3}`),
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()

			rewritten := readApplication()

			values, found, err := unstructured.NestedFieldNoCopy(
				rewritten.Object, "spec", "source", "helm", "valuesObject")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the operator did not write the new values")
			Expect(values).To(Equal(map[string]any{"replicaCount": int64(3)}))

			Expect(rewritten.Object["status"]).To(Equal(application.Object["status"]))

			// The operation is a request, not status. The one that was pending asked Argo CD
			// to sync the spec the operator has just replaced, so the operator's own request
			// takes its place, carrying the generation that produced the new spec.
			generation := strconv.FormatInt(componentSet.Generation, 10)
			Expect(rewritten.GetAnnotations()).To(
				HaveKeyWithValue(annotationComponentSetGeneration, generation))
			info, found, err := unstructured.NestedSlice(rewritten.Object, "operation", "info")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the operator did not ask Argo CD for a sync")
			Expect(info).To(ConsistOf(map[string]any{
				"name":  annotationComponentSetGeneration,
				"value": generation,
			}))
		})

		It("should reject a spec write after ownership changes concurrently", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			componentSet := settle()
			application := getApplication(firstComponent)
			Expect(componentSetOwner.ownedBy(application, componentSet)).To(BeTrue())
			// The read used for ownership has completed, but the spec write has not.
			concurrent := getApplication(firstComponent)
			concurrent.Annotations[annotationComponentSetName] = "another-set"
			Expect(k8sClient.Update(ctx, concurrent)).To(Succeed())
			before := application.DeepCopy()
			application.Spec.Source.Path = "must-not-be-written"
			_, err := createOrPatchArgoCDObject(ctx, k8sClient, before, application, application.Spec)
			Expect(apierrors.IsConflict(err)).To(BeTrue(), "stale metadata must not overwrite a new owner: %v", err)
			restored := getApplication(firstComponent)
			Expect(restored.Annotations).To(HaveKeyWithValue(annotationComponentSetName, "another-set"))
			Expect(restored.Spec.Source.Path).To(Equal(components()[0].Chart))
		})

		It("should retry a spec write that loses a race with an Argo CD status update", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			componentSet := settle()

			// Argo CD's status writes move the resource version, so a patch built on
			// an earlier read loses. A rollout must not stall on that.
			racing := &staleOnFirstWrite{Client: k8sClient}
			retrying := &CCLComponentSetReconciler{
				Client:          racing,
				Scheme:          k8sClient.Scheme(),
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}

			component := componentSet.Spec.Components[0]
			component.Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":5}`)}
			op, err := retrying.createOrPatchApplication(ctx, componentSet, component, 1)

			Expect(err).NotTo(HaveOccurred(), "the conflict reached the caller unretried")
			Expect(op).To(Equal(controllerutil.OperationResultUpdated))
			Expect(racing.patches).To(Equal(2), "the write was not attempted again")

			written := getApplication(firstComponent)
			Expect(written.Spec.Source.Helm.ValuesObject.Raw).To(MatchJSON(`{"replicaCount":5}`))
		})

		It("should retry a create the cache made look necessary", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())

			// Not reconciled yet, so this is the create path. AlreadyExists is what a
			// cache that has not seen the object produces, and it is not IsConflict.
			racing := &staleOnFirstWrite{Client: k8sClient}
			retrying := &CCLComponentSetReconciler{
				Client:          racing,
				Scheme:          k8sClient.Scheme(),
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}

			op, err := retrying.createOrPatchApplication(
				ctx, componentSet, componentSet.Spec.Components[0], 1)

			Expect(err).NotTo(HaveOccurred(), "the AlreadyExists reached the caller unretried")
			Expect(op).To(Equal(controllerutil.OperationResultCreated))
			Expect(racing.creates).To(Equal(2), "the create was not attempted again")
			Expect(getApplication(firstComponent).Spec.Source.Path).To(Equal(components()[0].Chart))
		})

		It("should enqueue the CCLComponentSets that reference a CCLInfraPlane", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())

			requests := reconciler.componentSetsForInfraPlane(ctx,
				&cclusternvidiacomv1alpha1.CCLInfraPlane{
					ObjectMeta: metav1.ObjectMeta{Name: planeB, Namespace: namespace},
				})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: componentSetKey}))

			By("ignoring a CCLInfraPlane no component references")
			Expect(reconciler.componentSetsForInfraPlane(ctx,
				&cclusternvidiacomv1alpha1.CCLInfraPlane{
					ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: namespace},
				})).To(BeEmpty())
		})

		It("should map an Application back to its CCLComponentSet", func() {
			requests := reconciler.componentSetForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      applicationName(componentSetName, firstComponent),
					Namespace: namespace,
					Labels:    map[string]string{labelComponentSetName: componentSetName},
				},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: componentSetKey}))

			By("ignoring an Application the operator did not create")
			Expect(reconciler.componentSetForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: namespace},
			})).To(BeEmpty())
		})

		It("should tear the components down in reverse order", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent)

			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(componentSet, componentSetFinalizerName)).To(BeTrue())
			Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())

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
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed(),
				"the CCLComponentSet must outlive its components")

			By("moving on once Argo CD has finished with it")
			finishTeardown(secondComponent)
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).NotTo(BeNil())
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())

			By("dropping the finalizer only when everything is gone")
			finishTeardown(firstComponent)
			reconcileOnly()
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, componentSetKey, componentSet))
			}).Should(BeTrue())
		})

		It("should requeue while a component is still terminating", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()

			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(err).NotTo(HaveOccurred())
			// A backstop for a missed Application watch event, so a teardown that
			// stalls does not do so silently.
			Expect(result.RequeueAfter).To(Equal(teardownRetryInterval))

			finishTeardown(firstComponent)
			result, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})

		It("should refuse to touch an Application owned by another CCLComponentSet", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)

			By("planting an Application whose back-reference points at a CCLComponentSet elsewhere")
			planted := &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      applicationName(componentSetName, firstComponent),
					Namespace: namespace,
					Annotations: map[string]string{
						annotationComponentSetName:      componentSetName,
						annotationComponentSetNamespace: "other-namespace",
					},
				},
				Spec: argocdv1alpha1.ApplicationSpec{
					Project: "default",
					Source:  argocdv1alpha1.ApplicationSource{RepoURL: "https://example.com/someone-elses"},
				},
			}
			Expect(k8sClient.Create(ctx, planted)).To(Succeed())

			createComponentSet(components())
			componentSet := settle()

			applied := conditionOf(componentSet, conditionTypeComponentsApplied)
			Expect(applied.Status).To(Equal(metav1.ConditionFalse))
			Expect(applied.Reason).To(Equal(reasonApplicationConflict))

			By("leaving the foreign Application untouched")
			Expect(getApplication(firstComponent).Spec.Source.RepoURL).
				To(Equal("https://example.com/someone-elses"))

			By("leaving it in place on delete, too")
			Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
		})
	})
})

// applicationsResource names the resource the faked errors below are about.
var applicationsResource = schema.GroupResource{
	Group:    argocdv1alpha1.GroupVersion.Group,
	Resource: "applications",
}

// staleOnFirstWrite rejects the first write of each kind the way the API server does when
// the read behind it is stale: Conflict for a patch naming an old resource version,
// AlreadyExists for a create the cache made look necessary. Later writes pass through.
type staleOnFirstWrite struct {
	client.Client
	patches int
	creates int
}

func (c *staleOnFirstWrite) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.PatchOption,
) error {
	c.patches++
	if c.patches == 1 {
		return apierrors.NewConflict(applicationsResource, obj.GetName(),
			errors.New("the object has been modified"))
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *staleOnFirstWrite) Create(
	ctx context.Context,
	obj client.Object,
	opts ...client.CreateOption,
) error {
	c.creates++
	if c.creates == 1 {
		return apierrors.NewAlreadyExists(applicationsResource, obj.GetName())
	}
	return c.Client.Create(ctx, obj, opts...)
}
