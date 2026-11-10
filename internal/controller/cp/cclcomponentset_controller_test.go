/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

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

	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	certmanager "github.com/NVIDIA/ccluster/catalogs/cluster_base/cert_manager"
	ingressnginx "github.com/NVIDIA/ccluster/catalogs/cluster_base/ingress_nginx"
	"github.com/NVIDIA/ccluster/catalogs/cluster_base/istio"
	kaigpuqueues "github.com/NVIDIA/ccluster/catalogs/resources/kai_gpu_queues"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	driftregistry "github.com/NVIDIA/ccluster/internal/registry/drift_registry"
	healthregistry "github.com/NVIDIA/ccluster/internal/registry/health_registry"
)

var _ = Describe("CCLComponentSet Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			componentSetName = "test-componentset"
			namespace        = "default"
			repoURL          = "https://github.com/NVIDIA/ccluster"
			revision         = "test-revision"
			resolvedRevision = "test-commit"

			planeA = "plane-a"
			planeB = "plane-b"

			firstComponent  = "argo-rollouts"
			secondComponent = "keda"

			// otherNamespace is where a foreign CCLComponentSet's back-reference points.
			otherNamespace = "other-namespace"
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
				Type:               cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
				Status:             status,
				Reason:             cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterSecretCreated,
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

		// terminating reports whether the operator has deleted component's Application.
		// Nothing runs Argo CD here, so its cascade finalizer keeps the object.
		terminating := func(component string) bool {
			GinkgoHelper()

			return !getApplication(component).DeletionTimestamp.IsZero()
		}

		// onPlaneA is a further component on planeA, for specs that need more than two.
		// Its chart need not exist: nothing here runs Argo CD.
		onPlaneA := func(name string) cclusternvidiacomv1alpha1.ComponentSpec {
			return cclusternvidiacomv1alpha1.ComponentSpec{
				Name:          name,
				InfraPlaneRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA},
				ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{
					Chart:     "catalogs/cluster_base/" + name,
					Namespace: name,
				},
			}
		}

		// setComponents replaces the CCLComponentSet's components.
		setComponents := func(specComponents []cclusternvidiacomv1alpha1.ComponentSpec) {
			GinkgoHelper()

			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			componentSet.Spec.Components = specComponents
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
		}

		// reportComparison simulates Argo CD by reporting health, sync and comparedTo,
		// and completing a pending sync request. A raw patch preserves unmodelled spec
		// fields, which a whole-object update through the partial type would discard.
		reportComparison := func(component, health, sync string, mutate func(source map[string]any)) {
			GinkgoHelper()

			// Copy the stored source so comparedTo includes unmodelled fields.
			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(argocdv1alpha1.GroupVersion.WithKind("Application"))
			Expect(k8sClient.Get(ctx, appKey(component), raw)).To(Succeed())
			source, found, err := unstructured.NestedMap(raw.Object, "spec", "source")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			if mutate != nil {
				mutate(source)
			}
			// Argo CD records the rules it compared with beside the source.
			ignoreDifferences, _, err := unstructured.NestedSlice(raw.Object, "spec", "ignoreDifferences")
			Expect(err).NotTo(HaveOccurred())

			application := getApplication(component)
			status := map[string]any{
				"health": map[string]any{"status": health},
				"sync": map[string]any{
					"status":   sync,
					"revision": resolvedRevision,
					"comparedTo": map[string]any{
						"source":            source,
						"destination":       application.Spec.Destination,
						"ignoreDifferences": ignoreDifferences,
					},
				},
				"reconciledAt": metav1.Now(),
			}
			// Every operation is Argo CD's own now: the operator asks for no sync, and
			// automated sync runs one whenever a comparison comes out OutOfSync. A
			// spec.operation that is there was staged by a spec, and Argo CD consumes it.
			operation := map[string]any{"sync": map[string]any{"source": source}}
			if application.Operation != nil {
				operation = map[string]any{"sync": application.Operation.Sync}
			}
			// Match the real CRD's required operationState fields, including startedAt.
			status["operationState"] = map[string]any{
				"operation": operation,
				"phase":     argocdv1alpha1.OperationSucceeded,
				"syncResult": argocdv1alpha1.SyncOperationResult{
					Source: application.Spec.Source, Revision: resolvedRevision,
				},
				"startedAt":  metav1.Now(),
				"finishedAt": metav1.NewTime(time.Now().Add(-2 * time.Second)),
			}
			patch := map[string]any{"status": status, "operation": nil}
			// Clear comparedTo so removed source keys do not survive the next merge patch.
			// Argo CD achieves this by diffing old and new status. Include sync.status
			// because the CRD requires it whenever status.sync is present.
			clear, err := json.Marshal(map[string]any{"status": map[string]any{
				"sync": map[string]any{"status": sync, "comparedTo": nil},
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Patch(ctx, application,
				client.RawPatch(types.MergePatchType, clear))).To(Succeed())

			encoded, err := json.Marshal(patch)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Patch(ctx, application,
				client.RawPatch(types.MergePatchType, encoded))).To(Succeed())
		}

		// reportCurrentComparison reports status for the Application's current source.
		reportCurrentComparison := func(component, health, sync string) {
			GinkgoHelper()

			reportComparison(component, health, sync, nil)
		}

		// reportStaleComparison reports status for a source that differs from the current spec.
		reportStaleComparison := func(component, health, sync string) {
			GinkgoHelper()

			reportComparison(component, health, sync, func(source map[string]any) {
				source["path"] = "catalogs/cluster_base/previous"
			})
		}

		// pendingOperation is the sync Argo CD's automated sync leaves in spec.operation
		// before it picks it up. The operator writes none, so every spec that needs one
		// is staging what Argo CD does.
		pendingOperation := func(spec argocdv1alpha1.ApplicationSpec) *argocdv1alpha1.Operation {
			return &argocdv1alpha1.Operation{
				Sync: &argocdv1alpha1.SyncOperation{Source: spec.Source.DeepCopy()},
			}
		}

		// release drives the rollout forward: each component is reported healthy,
		// which lets the operator create the next one's Application.
		release := func(componentNames ...string) {
			GinkgoHelper()

			for _, component := range componentNames {
				reportCurrentComparison(component, argocdv1alpha1.HealthStatusHealthy,
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
				Drift:           driftregistry.Get(),
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
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated).Status).
				To(Equal(metav1.ConditionTrue))
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated))

			By("creating only the first component")
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("holding while it is still converging")
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusProgressing, "OutOfSync")
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
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded, "Synced")
			reconcileAndGet()

			// Withdrawing it would delete a running Application and, through the
			// cascade finalizer, uninstall the chart it is serving.
			Expect(applicationExists(firstComponent)).To(BeTrue())
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should pass component values through as a Helm values document", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent)

			By("omitting the helm block for a component with no values")
			Expect(getApplication(firstComponent).Spec.Source.Helm).To(BeNil())

			By("rendering the component's values for a component that has them")
			helm := getApplication(secondComponent).Spec.Source.Helm
			Expect(helm).NotTo(BeNil())

			var decoded map[string]any
			Expect(yaml.Unmarshal([]byte(helm.Values), &decoded)).To(Succeed())
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
			issuer, err := certmanager.New("cert-manager", certmanager.Values{})
			Expect(err).NotTo(HaveOccurred())

			ingress, err := ingressnginx.New("ingress-nginx", ingressnginx.Values{})
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
				{Name: "cert-manager", InfraPlaneRef: ref, ComponentEntry: issuer},
				{Name: "ingress-nginx", InfraPlaneRef: ref, ComponentEntry: ingress},
				{Name: "kai-gpu-queues", InfraPlaneRef: ref, ComponentEntry: queues},
			}

			createInfraPlane(planeA, true)
			createComponentSet(specs)

			By("releasing the first component, and only that one")
			settle()

			application := getApplication("cert-manager")
			Expect(application.Spec.Source.Path).To(Equal("catalogs/cluster_base/cert_manager"))
			Expect(application.Spec.Destination.Namespace).To(Equal("cert-manager"))
			Expect(applicationExists("ingress-nginx")).To(BeFalse())
		})

		// istiod patches its validating webhooks' failurePolicy from the rendered Ignore to
		// Fail once it serves. Diffed, the istio Application never reports Synced and
		// every later wave stalls behind it.
		It("should ignore istiod's webhook failurePolicy, and wait for a comparison that did", func() {
			mesh, err := istio.New("istio-system", istio.Values{})
			Expect(err).NotTo(HaveOccurred())
			specs := components()
			specs[0] = cclusternvidiacomv1alpha1.ComponentSpec{
				Name:           istio.Name,
				InfraPlaneRef:  cclusternvidiacomv1alpha1.LocalObjectReference{Name: planeA},
				ComponentEntry: mesh,
			}

			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(specs)
			settle()

			application := getApplication(istio.Name)
			rules := istio.Drift()
			want := make([]argocdv1alpha1.ResourceIgnoreDifferences, 0, len(rules))
			for _, rule := range rules {
				want = append(want, argocdv1alpha1.ResourceIgnoreDifferences{
					Group: rule.Group, Kind: rule.Kind, JQPathExpressions: rule.JQPaths,
				})
			}
			Expect(application.Spec.IgnoreDifferences).To(Equal(want),
				"every rule applies to its whole kind, so it names no object")
			Expect(application.Spec.SyncPolicy.SyncOptions).To(ConsistOf(
				"CreateNamespace=true", "ServerSideApply=true", "RespectIgnoreDifferences=true"),
				"without it every sync reapplies Ignore and istiod patches Fail back")

			By("holding the rollout on a comparison made before the rules applied")
			reportCurrentComparison(istio.Name, argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced)
			Expect(k8sClient.Patch(ctx, getApplication(istio.Name), client.RawPatch(types.MergePatchType,
				[]byte(`{"status":{"sync":{"comparedTo":{"ignoreDifferences":null}}}}`)))).To(Succeed())
			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Message).To(ContainSubstring("waiting for Argo CD to compare"))
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("releasing the next wave once Argo CD compared with them")
			release(istio.Name)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			By("leaving charts without rules diffed in full")
			keda := getApplication(secondComponent)
			Expect(keda.Spec.IgnoreDifferences).To(BeEmpty())
			Expect(keda.Spec.SyncPolicy.SyncOptions).NotTo(ContainElement("RespectIgnoreDifferences=true"))
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

		// The controller survives an empty spec too, since it writes at most as many
		// components as the spec has, but nothing about it is meaningful.
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
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

			By("repointing the Application at another repository")
			application := getApplication(firstComponent)
			application.Spec.Source.RepoURL = "https://example.com/tampered"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			reconcileAndGet()

			Expect(getApplication(firstComponent).Spec.Source.RepoURL).To(Equal(repoURL))
		})

		// The source fields the operator does not model are the ones a drift test would
		// normally miss, and the ones that decide what Argo CD renders. helm.valueFiles
		// adds a whole values file; nothing in ApplicationSource names it.
		//
		// Both halves matter. If the operator cannot see it, it never writes it away, and
		// status.sync.comparedTo carries it while still matching the source the operator
		// wants - so the component completes on manifests nobody asked for, for good.
		It("should restore a source field the operator does not model, and not complete until it has", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(secondComponent), "wave 1 should have completed first")

			By("adding helm.valueFiles, which Argo CD would render with")
			application := getApplication(firstComponent)
			Expect(k8sClient.Patch(ctx, application, client.RawPatch(types.MergePatchType,
				[]byte(`{"spec":{"source":{"helm":{"valueFiles":["values-prod.yaml"]}}}}`)))).To(Succeed())

			By("reporting Healthy and Synced for that source, which must not complete wave 1")
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(argocdv1alpha1.GroupVersion.WithKind("Application"))
			Expect(k8sClient.Get(ctx, appKey(firstComponent), raw)).To(Succeed())
			compared, found, err := unstructured.NestedStringSlice(raw.Object,
				"status", "sync", "comparedTo", "source", "helm", "valueFiles")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the stand-in did not record the drifted source")
			Expect(compared).To(Equal([]string{"values-prod.yaml"}))

			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Message).To(ContainSubstring(firstComponent),
				"wave 1 completed on a comparison of a source the operator did not write")

			By("clearing it from the spec")
			Expect(k8sClient.Get(ctx, appKey(firstComponent), raw)).To(Succeed())
			_, found, err = unstructured.NestedStringSlice(raw.Object,
				"spec", "source", "helm", "valueFiles")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "the operator left a source field it does not model in place")

			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(secondComponent))
		})

		// Component reports must identify comparisons that no longer match the desired spec.
		It("should report every released component, and mark a verdict about an older spec", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()

			By("reporting only the components that have an Application")
			componentSet := reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(1))
			Expect(componentSet.Status.ComponentSyncStatuses[0].Name).To(Equal(firstComponent))

			release(firstComponent)
			componentSet = reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(2))
			first := componentSet.Status.ComponentSyncStatuses[0]
			Expect(first.ComparedToMatchesSpec).To(BeTrue())
			Expect(first.Sync).To(Equal(argocdv1alpha1.SyncStatusSynced))

			By("marking a Healthy and Synced that Argo CD reached for an older spec")
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()
			reportStaleComparison(firstComponent, "Healthy", "Synced")

			componentSet = reconcileAndGet()
			first = componentSet.Status.ComponentSyncStatuses[0]
			Expect(first.ComparedToMatchesSpec).To(BeFalse(),
				"a verdict about the previous spec was reported as one about this one")
			Expect(first.Sync).To(Equal(argocdv1alpha1.SyncStatusSynced))

			By("keeping the entry of a removed component, marked, until its Application is gone")
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			componentSet.Spec.Components = componentSet.Spec.Components[:1]
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			componentSet = reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(2))
			Expect(componentSet.Status.ComponentSyncStatuses[0].Name).To(Equal(firstComponent))
			Expect(componentSet.Status.ComponentSyncStatuses[0].Removal).To(BeEmpty())
			removed := componentSet.Status.ComponentSyncStatuses[1]
			Expect(removed.Name).To(Equal(secondComponent))
			Expect(removed.Removal).To(Equal(cclusternvidiacomv1alpha1.ComponentRemovalDeletePending),
				"deleted before the first component finished")
			Expect(removed.ComparedToMatchesSpec).To(BeFalse())

			release(firstComponent)
			componentSet = reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(2))
			Expect(componentSet.Status.ComponentSyncStatuses[1].Removal).To(Equal(cclusternvidiacomv1alpha1.ComponentRemovalDeleting))

			finishTeardown(secondComponent)
			componentSet = reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(1))
			Expect(componentSet.Status.ComponentSyncStatuses[0].Name).To(Equal(firstComponent))
		})

		// status.observedGeneration vouches for componentSyncStatuses, and Progressing is read from
		// the same Applications, so a pass that moves it on must rebuild both for that spec,
		// even when the pass stops short.
		It("should rebuild componentSyncStatuses and Progressing on a pass that fails validation", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent, secondComponent)
			componentSet := reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses[0].ComparedToMatchesSpec).To(BeTrue())
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))

			By("moving the first component to a CCLInfraPlane that does not exist")
			componentSet.Spec.Components[0].InfraPlaneRef.Name = "missing-plane"
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())

			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound))
			Expect(componentSet.Status.ObservedGeneration).To(Equal(componentSet.Generation))
			first := componentSet.Status.ComponentSyncStatuses[0]
			Expect(first.Name).To(Equal(firstComponent))
			Expect(first.ComparedToMatchesSpec).To(BeFalse(),
				"a comparison against the previous destination was reported for this generation")
			Expect(first.Sync).To(Equal(argocdv1alpha1.SyncStatusSynced))

			// The first component now waits for a write the operator will not make
			// until the reference is fixed, so Progressing names that, not the
			// previous spec's completion or a write about to happen.
			progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.ObservedGeneration).To(Equal(componentSet.Generation))
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))
			Expect(progressing.Message).To(ContainSubstring("missing-plane"))
		})

		It("should rebuild componentSyncStatuses and Progressing on a pass that fails to apply", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent, secondComponent)
			componentSet := reconcileAndGet()
			Expect(componentSet.Status.ComponentSyncStatuses[1].ComparedToMatchesSpec).To(BeTrue())

			// The first component's write is the one that fails: components are written
			// in order, so the second's would not be reached on this pass.
			By("changing both components while the first's Application belongs elsewhere")
			for i := range componentSet.Spec.Components {
				componentSet.Spec.Components[i].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			application := getApplication(firstComponent)
			application.Annotations[annotationComponentSetNamespace] = otherNamespace
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict))
			Expect(componentSet.Status.ObservedGeneration).To(Equal(componentSet.Generation))
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(1),
				"the Application claimed by another CCLComponentSet was reported")
			second := componentSet.Status.ComponentSyncStatuses[0]
			Expect(second.Name).To(Equal(secondComponent))
			Expect(second.ComparedToMatchesSpec).To(BeFalse(),
				"a comparison against the previous values was reported for this generation")

			progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.ObservedGeneration).To(Equal(componentSet.Generation))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))
		})

		It("should leave an Application claimed by another CCLComponentSet out of the status", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent, secondComponent)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))

			By("claiming the second component's Application for a CCLComponentSet elsewhere")
			application := getApplication(secondComponent)
			base := application.DeepCopy()
			application.Annotations[annotationComponentSetNamespace] = otherNamespace
			Expect(k8sClient.Patch(ctx, application, client.MergeFrom(base))).To(Succeed())

			componentSet := reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict))
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(1))
			Expect(componentSet.Status.ComponentSyncStatuses[0].Name).To(Equal(firstComponent))

			progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing),
				"the rollout was reported complete on an Application the operator refuses to write")
			Expect(progressing.Message).To(ContainSubstring(secondComponent))
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
			pruned := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, appKey(secondComponent), pruned)).To(Succeed())
			Expect(pruned.DeletionTimestamp).NotTo(BeNil())
			Expect(pruned.Finalizers).To(ContainElement(argocdv1alpha1.ResourcesFinalizer))

			By("leaving the surviving component alone")
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
		})

		It("should write a later component's changed spec only after the components before it finish", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent, secondComponent)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))

			By("changing both components")
			updated := components()
			for i := range updated {
				updated[i].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			}
			setComponents(updated)
			previous := getApplication(secondComponent)
			componentSet := reconcileAndGet()

			By("writing the first and holding the second")
			Expect(getApplication(firstComponent).Spec.Source.Helm.Values).To(Equal("replicaCount: 3\n"))
			Expect(getApplication(secondComponent).ResourceVersion).To(Equal(previous.ResourceVersion),
				"the second component was written before the first finished")
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Message).
				To(ContainSubstring("Applied 1 of 2 components"))

			By("holding it while the first is degraded on its new spec")
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced)
			reconcileAndGet()
			Expect(getApplication(secondComponent).ResourceVersion).To(Equal(previous.ResourceVersion))

			By("writing it once the first has finished")
			release(firstComponent)
			Expect(getApplication(secondComponent).Spec.Source.Helm.Values).To(Equal("replicaCount: 3\n"))
		})

		// Whether a component has finished is judged on live Applications. One whose
		// Application is terminating, or gone, has not, so the components after it wait.
		It("should hold later components while an earlier one has no live Application", func() {
			const third = "vault"
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(append(components(), onPlaneA(third)))
			settle()
			release(firstComponent, secondComponent, third)

			By("changing the third component and deleting the second's Application by hand")
			updated := append(components(), onPlaneA(third))
			updated[2].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			setComponents(updated)
			Expect(k8sClient.Delete(ctx, getApplication(secondComponent))).To(Succeed())
			previous := getApplication(third)

			By("holding the third while the second's Application terminates")
			reconcileAndGet()
			Expect(getApplication(third).ResourceVersion).To(Equal(previous.ResourceVersion),
				"a terminating Application was taken as finished")

			By("holding it after the second's Application is gone and has been recreated")
			finishTeardown(secondComponent)
			reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeFalse())
			Expect(getApplication(third).ResourceVersion).To(Equal(previous.ResourceVersion))

			By("writing it once the second has finished")
			release(secondComponent)
			Expect(getApplication(third).Spec.Source.Helm.Values).To(Equal("replicaCount: 3\n"))
		})

		// The case the removal order exists for: an ExternalSecret producer replaced by a
		// renamed one. Pods of the component after it keep reading the old Secret until
		// they restart on the new spec, so the old producer must outlive that rollout.
		It("should delete a replaced component's Application only after every remaining component finishes", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent, secondComponent)

			By("replacing the first component with a renamed one and changing the second")
			const replacement = firstComponent + "-v2"
			updated := components()
			updated[0].Name = replacement
			updated[1].Values = &apiextensionsv1.JSON{Raw: []byte(`{"keda":{"operator":{"replicaCount":3}}}`)}
			setComponents(updated)
			componentSet := reconcileAndGet()
			Expect(applicationExists(replacement)).To(BeTrue())
			Expect(terminating(firstComponent)).To(BeFalse(), "removed before its replacement finished")
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(replacement))

			By("keeping it while the second component rolls out its new spec")
			release(replacement)
			Expect(getApplication(secondComponent).Spec.Source.Helm.Values).To(ContainSubstring("replicaCount: 3"))
			Expect(terminating(firstComponent)).To(BeFalse(),
				"removed while the component after it was still converging")

			By("deleting it once every remaining component has finished")
			reportCurrentComparison(secondComponent, argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced)
			componentSet = reconcileAndGet()
			Expect(terminating(firstComponent)).To(BeTrue())
			progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue),
				"the rollout was reported complete with a removal outstanding")
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonComponentRemoving))
			Expect(progressing.Message).To(ContainSubstring(appKey(firstComponent).Name))
			Expect(componentSet.Status.ComponentSyncStatuses).To(HaveLen(3))
			Expect(componentSet.Status.ComponentSyncStatuses[2].Name).To(Equal(firstComponent))
			Expect(componentSet.Status.ComponentSyncStatuses[2].Removal).To(Equal(cclusternvidiacomv1alpha1.ComponentRemovalDeleting))

			By("completing the rollout once it is gone")
			finishTeardown(firstComponent)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
		})

		It("should delete removed components highest wave first, one at a time", func() {
			// Last in name order, so name order and wave order disagree.
			const third = "vault"
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(append(components(), onPlaneA(third)))
			settle()
			release(firstComponent, secondComponent, third)

			By("removing two components while the remaining one is degraded")
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced)
			setComponents(components()[:1])
			componentSet := reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeFalse())
			Expect(terminating(third)).To(BeFalse())
			listed := make([]string, 0, len(componentSet.Status.ComponentSyncStatuses))
			for _, entry := range componentSet.Status.ComponentSyncStatuses {
				listed = append(listed, entry.Name)
			}
			Expect(listed).To(Equal([]string{firstComponent, third, secondComponent}),
				"removed components are not listed in the order they are deleted")

			By("deleting the highest wave once it recovers")
			release(firstComponent)
			Expect(terminating(third)).To(BeTrue())
			Expect(terminating(secondComponent)).To(BeFalse())

			By("waiting for it to be gone before deleting the next")
			reconcileAndGet()
			reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeFalse())

			finishTeardown(third)
			reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeTrue())

			finishTeardown(secondComponent)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
			Expect(terminating(firstComponent)).To(BeFalse())
		})

		It("should delete a removed component without a usable wave label after the labelled ones, in name order", func() {
			// By position, vault would go first and cert-manager second; by name, the
			// other way round.
			const third, fourth = "cert-manager", "vault"
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(append(components(), onPlaneA(third), onPlaneA(fourth)))
			settle()
			release(firstComponent, secondComponent, third, fourth)

			By("removing three components, two of them with an unusable wave label")
			setComponents(components()[:1])
			// An empty value stands for a missing label.
			for component, label := range map[string]string{third: "", fourth: "not-a-wave"} {
				application := getApplication(component)
				base := application.DeepCopy()
				if label == "" {
					delete(application.Labels, labelWave)
				} else {
					application.Labels[labelWave] = label
				}
				Expect(k8sClient.Patch(ctx, application, client.MergeFrom(base))).To(Succeed())
			}

			By("deleting the labelled one first")
			reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeTrue())
			Expect(terminating(third)).To(BeFalse())
			Expect(terminating(fourth)).To(BeFalse())

			By("deleting the unlabelled ones in name order")
			finishTeardown(secondComponent)
			reconcileAndGet()
			Expect(terminating(third)).To(BeTrue())
			Expect(terminating(fourth)).To(BeFalse())

			finishTeardown(third)
			reconcileAndGet()
			Expect(terminating(fourth)).To(BeTrue())
		})

		// The cost DESIGN.md §5 gives for waiting: a broken component, such as a webhook
		// that rejects a later component's Pods, cannot be removed on its own while that
		// later component fails because of it.
		It("should not remove a broken component alone while a remaining one fails because of it", func() {
			const third = "vault"
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(append(components(), onPlaneA(third)))
			settle()
			release(firstComponent, secondComponent, third)

			By("removing only the second component while it and the third are degraded")
			reportCurrentComparison(secondComponent, argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced)
			reportCurrentComparison(third, argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced)
			setComponents([]cclusternvidiacomv1alpha1.ComponentSpec{components()[0], onPlaneA(third)})
			reconcileAndGet()
			componentSet := reconcileAndGet()
			Expect(terminating(secondComponent)).To(BeFalse(),
				"removed while a remaining component had not finished")
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(third))
			statuses := componentSet.Status.ComponentSyncStatuses
			Expect(statuses[len(statuses)-1].Name).To(Equal(secondComponent))
			Expect(statuses[len(statuses)-1].Removal).To(Equal(cclusternvidiacomv1alpha1.ComponentRemovalDeletePending))

			By("removing the third as well, which lets both go, one at a time")
			setComponents(components()[:1])
			for range 2 {
				reconcileAndGet()
				var deleting []string
				for _, component := range []string{secondComponent, third} {
					if applicationExists(component) && terminating(component) {
						deleting = append(deleting, component)
					}
				}
				Expect(deleting).To(HaveLen(1))
				finishTeardown(deleting[0])
			}
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
		})

		It("should report InfraPlaneNotFound and apply nothing when a plane is missing", func() {
			createInfraPlane(planeA, true)
			// planeB is deliberately absent.
			createComponentSet(components())

			componentSet := settle()

			validated := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotFound))
			Expect(validated.Message).To(ContainSubstring(planeB))

			Expect(applicationExists(firstComponent)).To(BeFalse())
		})

		It("should report InfraPlaneNotReady until the plane becomes Available", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, false)
			createComponentSet(components())

			componentSet := settle()
			validated := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotReady))

			By("making the plane Available")
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: planeB, Namespace: namespace}, infraPlane)).To(Succeed())
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:               cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
				Status:             metav1.ConditionTrue,
				Reason:             cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterSecretCreated,
				Message:            "set by the test",
				ObservedGeneration: infraPlane.Generation,
			})
			infraPlane.Status.ObservedGeneration = infraPlane.Generation
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())

			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated).Status).
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
			Expect(conditionOf(settle(), cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated).Status).
				To(Equal(metav1.ConditionTrue))

			By("rotating the plane's credentials without letting its controller catch up")
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			planeKey := types.NamespacedName{Name: planeA, Namespace: namespace}
			Expect(k8sClient.Get(ctx, planeKey, infraPlane)).To(Succeed())
			infraPlane.Spec.ClusterCredentialsRef.Name = planeA + "-rotated"
			Expect(k8sClient.Update(ctx, infraPlane)).To(Succeed())

			validated := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated)
			Expect(validated.Status).To(Equal(metav1.ConditionFalse))
			Expect(validated.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonInfraPlaneNotReady))
			// Available still reads True here, and that is what kubectl prints, so the
			// message must not blame the condition.
			Expect(validated.Message).To(ContainSubstring("has not caught up with its spec"))

			By("letting the plane's controller catch up")
			Expect(k8sClient.Get(ctx, planeKey, infraPlane)).To(Succeed())
			apimeta.SetStatusCondition(&infraPlane.Status.Conditions, metav1.Condition{
				Type:               cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
				Status:             metav1.ConditionTrue,
				Reason:             cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterSecretCreated,
				Message:            "set by the test",
				ObservedGeneration: infraPlane.Generation,
			})
			infraPlane.Status.ObservedGeneration = infraPlane.Generation
			Expect(k8sClient.Status().Update(ctx, infraPlane)).To(Succeed())

			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated).Status).
				To(Equal(metav1.ConditionTrue))
		})

		It("should report which component the rollout is waiting on", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())

			componentSet := settle()
			progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
			Expect(progressing.Message).To(ContainSubstring(firstComponent))

			release(firstComponent)
			componentSet = reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(secondComponent))

			release(secondComponent)
			componentSet = reconcileAndGet()
			progressing = conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
		})

		It("should preserve explicit null values when updating an Application", func() {
			createInfraPlane(planeA, true)
			spec := components()[:1]
			spec[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"config":{"removeMe":1,"keep":2}}`)}
			createComponentSet(spec)
			componentSet := settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{
				Raw: []byte(`{"config":{"removeMe":null,"keep":2}}`),
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()
			// A merge patch would have dropped the null; the operator writes a JSON
			// Patch, and the values are one string inside it either way.
			Expect(getApplication(firstComponent).Spec.Source.Helm.Values).
				To(ContainSubstring("removeMe: null"))

			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusHealthy, argocdv1alpha1.SyncStatusSynced)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Status).To(Equal(metav1.ConditionFalse))
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
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

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

		// A previous spec's Healthy/Synced status must not release the next component.
		It("should not release the next component on a verdict reached for the previous spec", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent)
			Expect(applicationExists(secondComponent)).To(BeTrue())

			By("changing the first component, which Argo CD has not compared yet")
			componentSet := reconcileAndGet()
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()
			reportStaleComparison(firstComponent, "Healthy", "Synced")

			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
			Expect(progressing.Message).To(ContainSubstring("waiting for Argo CD to compare"))
			Expect(progressing.Message).NotTo(ContainSubstring("health"),
				"a verdict about the previous spec must not be repeated as the current one")

			By("comparing the spec the operator wrote")
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Message).
				To(ContainSubstring(secondComponent), "wave 1 should no longer be the one holding the rollout")

			release(secondComponent)
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
		})

		// What comparing the source buys over tracking a sync: there is no result to
		// catch, so no window in which catching it can be missed. Argo CD overwrites
		// status.operationState whenever it starts another operation, and an operator
		// that was not watching at the time never sees the one before it. Here there is
		// no operationState at all - an Application Argo CD found already in sync and
		// never had to sync - and the comparison still completes the component.
		It("should complete a component Argo CD compared but never synced", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

			By("taking the operation state away, leaving only the comparison")
			application := getApplication(firstComponent)
			Expect(k8sClient.Patch(ctx, application, client.RawPatch(types.MergePatchType,
				[]byte(`{"status":{"operationState":null}}`)))).To(Succeed())
			Expect(getApplication(firstComponent).Status.OperationState).To(BeNil())

			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
		})

		It("should wait for pending and running operations despite a Healthy and Synced verdict", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

			By("waiting for a pending operation")
			application := getApplication(firstComponent)
			application.Operation = pendingOperation(application.Spec)
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("waiting while a PostSync hook runs or terminates")
			// Argo CD keeps spec.operation set until the operation reaches a terminal
			// phase, so the request staged above stays while the hook runs.
			for _, phase := range []string{"Running", "Terminating"} {
				application = getApplication(firstComponent)
				Expect(application.Operation).NotTo(BeNil())
				application.Status.OperationState.Phase = phase
				application.Status.OperationState.Message = "waiting for PostSync hook"
				Expect(k8sClient.Update(ctx, application)).To(Succeed())
				progressing = conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
				Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
				Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
				Expect(progressing.Message).To(ContainSubstring("waiting for PostSync hook"))
				Expect(applicationExists(secondComponent)).To(BeFalse())
			}

			By("releasing the next component once the hook succeeds")
			application = getApplication(firstComponent)
			// Argo CD clears spec.operation in the write that records the terminal phase.
			application.Operation = nil
			application.Status.OperationState.Phase = argocdv1alpha1.OperationSucceeded
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should block rollout on a failed PostSync hook despite a Healthy and Synced verdict", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

			for _, phase := range []string{argocdv1alpha1.OperationFailed, argocdv1alpha1.OperationError} {
				application := getApplication(firstComponent)
				// Auto-sync need not capture a source in the request; syncResult does.
				application.Status.OperationState.Operation.Sync.Source = nil
				application.Status.OperationState.Phase = phase
				application.Status.OperationState.Message = "PostSync hook failed"
				Expect(k8sClient.Update(ctx, application)).To(Succeed())
				progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
				Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
				Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed))
				Expect(progressing.Message).To(ContainSubstring("PostSync hook failed"))
				Expect(applicationExists(secondComponent)).To(BeFalse())
			}

			By("resuming after a successful retry")
			application := getApplication(firstComponent)
			application.Operation = pendingOperation(application.Spec)
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should not attribute the previous source's failure to the current spec", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, "Degraded", "OutOfSync")
			application := getApplication(firstComponent)
			application.Status.OperationState.Phase = argocdv1alpha1.OperationFailed
			application.Status.OperationState.Message = "previous source failed"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			componentSet := reconcileAndGet()
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()

			By("comparing the new source before Argo CD picks up its own sync")
			application = getApplication(firstComponent)
			application.Operation = pendingOperation(application.Spec)
			application.Status.Sync.ComparedTo.Source = application.Spec.Source
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
			Expect(progressing.Message).NotTo(ContainSubstring("previous source failed"))
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("retaining the old failure after the pending request disappears")
			application = getApplication(firstComponent)
			application.Operation = nil
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing))
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("converging on the new source with the old failure still recorded")
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should not block a converged new Git revision on an older revision's failed sync", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			application := getApplication(firstComponent)
			application.Status.OperationState.Phase = argocdv1alpha1.OperationFailed
			application.Status.Sync.Revision = "new-commit"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should attribute an early sync error using the captured request source", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			application := getApplication(firstComponent)
			application.Status.OperationState.Phase = argocdv1alpha1.OperationError
			application.Status.OperationState.SyncResult = nil
			application.Status.OperationState.Message = "sync initialization failed"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed))
			Expect(progressing.Message).To(ContainSubstring("sync initialization failed"))
		})

		It("should report a failed sync and resume when a later sync of the same spec succeeds", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded, "OutOfSync")

			application := getApplication(firstComponent)
			application.Status.OperationState.Phase = argocdv1alpha1.OperationFailed
			application.Status.OperationState.Message = "one or more objects failed to apply"
			Expect(k8sClient.Update(ctx, application)).To(Succeed())

			progressing := conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
			Expect(progressing.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncFailed))
			Expect(progressing.Message).To(ContainSubstring("one or more objects failed to apply"))
			Expect(applicationExists(secondComponent)).To(BeFalse())

			By("letting a later sync of the same spec finish it, with no operator resubmission")
			application = getApplication(firstComponent)
			Expect(application.Operation).To(BeNil())
			application.Operation = pendingOperation(application.Spec)
			Expect(k8sClient.Update(ctx, application)).To(Succeed())
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			reconcileAndGet()
			Expect(applicationExists(secondComponent)).To(BeTrue())
		})

		It("should keep a component pending when an Application list returns the previous spec", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			componentSet := reconcileAndGet()
			Expect(conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Status).To(Equal(metav1.ConditionFalse))
			previous := getApplication(firstComponent)

			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			defer func() { reconciler.Client = k8sClient }()
			reconciler.Client = &staleApplicationList{Client: k8sClient, application: previous}

			updated := reconcileAndGet()
			Expect(getApplication(firstComponent).Spec.Source.Helm.Values).
				To(ContainSubstring("replicaCount: 3"))
			Expect(conditionOf(updated, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Status).To(Equal(metav1.ConditionTrue))
			Expect(conditionOf(updated, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).ObservedGeneration).To(Equal(updated.Generation))
		})

		// The operator writes no spec.operation, so an Argo CD operation in flight is
		// nothing for it to collide with. Holding the write back is what used to lose a
		// pending operation. What holds the next component's write back is the ordering:
		// it waits for this component to finish, operation included.
		It("should write a changed spec while Argo CD has an operation in flight", func() {
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(components())
			settle()
			release(firstComponent)
			release(secondComponent)
			componentSet := reconcileAndGet()

			By("putting an operation Argo CD has not started on the first component")
			first := getApplication(firstComponent)
			first.Operation = pendingOperation(first.Spec)
			Expect(k8sClient.Update(ctx, first)).To(Succeed())

			for i := range componentSet.Spec.Components {
				componentSet.Spec.Components[i].Values = &apiextensionsv1.JSON{Raw: []byte(`{"replicaCount":3}`)}
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			updated := reconcileAndGet()

			By("writing the first component and leaving the operation where it was")
			Expect(getApplication(firstComponent).Spec.Source.Helm.Values).To(ContainSubstring("replicaCount: 3"))
			Expect(getApplication(firstComponent).Operation).To(Equal(first.Operation),
				"the operator wrote over an operation Argo CD had not started")
			Expect(conditionOf(updated, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated))
			Expect(conditionOf(updated, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Status).To(Equal(metav1.ConditionTrue))

			By("writing the second once the first has finished, without asking for a sync")
			release(firstComponent)
			Expect(getApplication(secondComponent).Spec.Source.Helm.Values).To(ContainSubstring("replicaCount: 3"))
			Expect(getApplication(secondComponent).Operation).To(BeNil(),
				"the operator asked Argo CD for a sync")
		})

		It("should rewrite the spec when operator configuration changes without a parent spec update", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()
			reportCurrentComparison(firstComponent, "Healthy", "Synced")
			Expect(conditionOf(reconcileAndGet(), cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))

			reconciler.CatalogRevision = "another-revision"
			updated := reconcileAndGet()
			application := getApplication(firstComponent)
			Expect(application.Spec.Source.TargetRevision).To(Equal("another-revision"))
			Expect(application.Operation).To(BeNil(), "the operator asked Argo CD for a sync")
			Expect(conditionOf(updated, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Status).To(Equal(metav1.ConditionTrue),
				"the previous comparison completed the component under its new spec")
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
			application = readApplication()
			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			componentSet.Spec.Components[0].Values = &apiextensionsv1.JSON{
				Raw: []byte(`{"replicaCount":3}`),
			}
			Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())
			reconcileAndGet()
			rewritten := readApplication()

			// The pending operation is Argo CD's. The rewrite goes out beside it rather
			// than waiting for it, and must not take it with it.
			Expect(rewritten.Object["operation"]).To(Equal(application.Object["operation"]))

			values, found, err := unstructured.NestedString(
				rewritten.Object, "spec", "source", "helm", "values")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "the operator did not write the new values")
			Expect(values).To(Equal("replicaCount: 3\n"))

			Expect(rewritten.Object["status"]).To(Equal(application.Object["status"]))
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
			reportCurrentComparison(firstComponent, "Healthy", "Synced")

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
			Expect(written.Spec.Source.Helm.Values).To(Equal("replicaCount: 5\n"))
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

		It("should requeue, not fail, when its status write loses a race", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()
			// Something new to report, so the pass below has a status to write.
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced)

			// The CCLComponentSet moves on between the rollout's read and its status
			// write, as it does when the cache lags the operator's previous write or
			// another controller writes the object. The API server rejects the write.
			racing := &movedBeforeStatusWrite{Client: k8sClient}
			racer := &CCLComponentSetReconciler{
				Client:          racing,
				Scheme:          k8sClient.Scheme(),
				CatalogRepoURL:  repoURL,
				CatalogRevision: revision,
			}

			result, err := racer.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(racing.moved).To(BeTrue(), "the race was not staged")
			Expect(err).NotTo(HaveOccurred(), "an expected conflict was returned as a reconcile error")
			Expect(result.RequeueAfter).To(Equal(statusConflictRetryInterval))
		})

		It("should not write a status that has not changed", func() {
			createInfraPlane(planeA, true)
			createComponentSet(components()[:1])
			settle()

			// An Application status write enqueues its CCLComponentSet, including Argo
			// CD's periodic refresh, which changes nothing the status reports. The
			// requests are counted because the resource version cannot tell: the API
			// server drops a patch that changes nothing without moving it.
			counting := &countingStatusWrites{Client: k8sClient}
			idle := *reconciler
			idle.Client = counting

			_, err := idle.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(counting.writes).To(BeZero(), "an unchanged status was written")

			By("writing it once the Application reports something new")
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusHealthy,
				argocdv1alpha1.SyncStatusSynced)
			_, err = idle.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(counting.writes).To(Equal(1))
			after := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, after)).To(Succeed())
			Expect(conditionOf(after, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing).Reason).
				To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonRolloutComplete))
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
			requests := componentSetForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      applicationName(componentSetName, firstComponent),
					Namespace: namespace,
					Labels:    map[string]string{labelComponentSetName: componentSetName},
				},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: componentSetKey}))

			By("ignoring an Application the operator did not create")
			Expect(componentSetForApplication(ctx, &argocdv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: namespace},
			})).To(BeEmpty())
		})

		Context("judging component health", func() {
			judgedAt := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

			// firstHealth is what the first component's chart reports; specs change it
			// between passes.
			var firstHealth catalogs.Health

			// judgeWith makes both controllers judge health through argoCD: the first
			// component as firstHealth says and the second as Healthy.
			judgeWith := func(argoCD ArgoCDServer) {
				healthFuncs := &healthregistry.Registry{}
				healthFuncs.Register("catalogs/cluster_base/argo_rollouts",
					func(context.Context, cclusternvidiacomv1alpha1.ComponentEntry, catalogs.Observation) catalogs.Health {
						return firstHealth
					})
				healthFuncs.Register("catalogs/cluster_base/keda",
					func(context.Context, cclusternvidiacomv1alpha1.ComponentEntry, catalogs.Observation) catalogs.Health {
						return catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy}
					})
				reconciler.ArgoCD, reconciler.Health = argoCD, healthFuncs
			}

			healthReconciler := func(now *time.Time) *ComponentHealthReconciler {
				return &ComponentHealthReconciler{
					Client: k8sClient,
					ArgoCD: reconciler.ArgoCD,
					Health: reconciler.Health,
					now:    func() time.Time { return *now },
				}
			}

			// reconcileHealth runs one health pass and returns the requeue interval and the
			// CCLComponentSet it left.
			reconcileHealth := func(r *ComponentHealthReconciler) (time.Duration, *cclusternvidiacomv1alpha1.CCLComponentSet) {
				GinkgoHelper()

				result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: componentSetKey})
				Expect(err).NotTo(HaveOccurred())
				refreshed := &cclusternvidiacomv1alpha1.CCLComponentSet{}
				Expect(k8sClient.Get(ctx, componentSetKey, refreshed)).To(Succeed())
				return result.RequeueAfter, refreshed
			}

			BeforeEach(func() {
				firstHealth = catalogs.Health{
					Status:  cclusternvidiacomv1alpha1.ComponentHealthProgressing,
					Reason:  "Unschedulable",
					Message: "Pod web-0: 0/3 nodes are available",
				}
				createInfraPlane(planeA, true)
				createInfraPlane(planeB, true)
				createComponentSet(components())
				settle()
			})

			It("should report released components only, writing when their health changes", func() {
				now := judgedAt
				argoCD := &fakeArgoCD{}
				judgeWith(argoCD)
				r := healthReconciler(&now)

				requeue, componentSet := reconcileHealth(r)
				Expect(componentSet.Status.ComponentHealthStatuses).To(HaveLen(1),
					"the second component has no Application, so nothing of it runs")
				first := componentSet.Status.ComponentHealthStatuses[0]
				Expect(first.Name).To(Equal(firstComponent))
				Expect(first.Health).To(Equal(cclusternvidiacomv1alpha1.ComponentHealthProgressing))
				Expect(first.Reason).To(Equal("Unschedulable"))
				Expect(first.Message).To(Equal("Pod web-0: 0/3 nodes are available"))
				Expect(first.LastTransitionTime.Time).To(BeTemporally("==", judgedAt))
				Expect(requeue).To(Equal(healthPollSettling))
				Expect(argoCD.treesRead()).To(ConsistOf(apiclient.ApplicationRef{
					Namespace: namespace,
					Name:      applicationName(componentSetName, firstComponent),
					Project:   projectName(namespace),
				}))

				By("writing nothing when a later pass judges the same")
				now = judgedAt.Add(time.Minute)
				_, again := reconcileHealth(r)
				Expect(again.ResourceVersion).To(Equal(componentSet.ResourceVersion))

				By("keeping lastTransitionTime when only the message changes")
				firstHealth.Message = "Pod web-0: 0/4 nodes are available"
				_, componentSet = reconcileHealth(r)
				first = componentSet.Status.ComponentHealthStatuses[0]
				Expect(first.Message).To(Equal("Pod web-0: 0/4 nodes are available"))
				Expect(first.LastTransitionTime.Time).To(BeTemporally("==", judgedAt))

				By("moving lastTransitionTime when health changes")
				firstHealth = catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy}
				_, componentSet = reconcileHealth(r)
				first = componentSet.Status.ComponentHealthStatuses[0]
				Expect(first.Health).To(Equal(cclusternvidiacomv1alpha1.ComponentHealthHealthy))
				Expect(first.LastTransitionTime.Time).To(BeTemporally("==", now))

				By("leaving the rollout's own status alone")
				Expect(componentSet.Status.Conditions).To(Equal(again.Status.Conditions))
				Expect(componentSet.Status.ComponentSyncStatuses).To(Equal(again.Status.ComponentSyncStatuses))

				By("keeping componentHealthStatuses through a rollout pass, before health runs again")
				healthWritten := componentSet.Status.ComponentHealthStatuses
				reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced)
				afterRollout := reconcileAndGet()
				Expect(afterRollout.ResourceVersion).NotTo(Equal(componentSet.ResourceVersion),
					"the rollout pass wrote nothing, so it proves nothing")
				Expect(afterRollout.Status.ComponentHealthStatuses).To(Equal(healthWritten))

				By("polling less often once every component has settled")
				requeue, componentSet = reconcileHealth(r)
				Expect(componentSet.Status.ComponentHealthStatuses).To(HaveLen(2))
				Expect(requeue).To(Equal(healthPollSettled))
			})

			It("should report Argo CD's aggregate health when the resource tree cannot be read", func() {
				now := judgedAt
				reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded,
					argocdv1alpha1.SyncStatusSynced)
				judgeWith(&fakeArgoCD{treeErr: errors.New("resource tree not cached yet")})
				r := healthReconciler(&now)

				_, componentSet := reconcileHealth(r)
				first := componentSet.Status.ComponentHealthStatuses[0]
				Expect(first.Health).To(Equal(cclusternvidiacomv1alpha1.ComponentHealthDegraded))
				Expect(first.Reason).To(Equal(cclusternvidiacomv1alpha1.ComponentHealthReasonResourceTreeUnavailable))
				Expect(first.Message).To(ContainSubstring("resource tree not cached yet"))
			})

			It("should report a removed component by the default health function until its Application is gone", func() {
				now := judgedAt
				firstHealth = catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy}
				judgeWith(&fakeArgoCD{})
				r := healthReconciler(&now)
				release(firstComponent)
				// The second's chart function reports Healthy; Argo CD reports it Degraded.
				reportCurrentComparison(secondComponent, argocdv1alpha1.HealthStatusDegraded,
					argocdv1alpha1.SyncStatusSynced)

				By("removing the second component from the spec")
				componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
				Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
				componentSet.Spec.Components = componentSet.Spec.Components[:1]
				Expect(k8sClient.Update(ctx, componentSet)).To(Succeed())

				_, componentSet = reconcileHealth(r)
				Expect(componentSet.Status.ComponentHealthStatuses).To(HaveLen(2))
				removed := componentSet.Status.ComponentHealthStatuses[1]
				Expect(removed.Name).To(Equal(secondComponent))
				Expect(removed.Health).To(Equal(cclusternvidiacomv1alpha1.ComponentHealthDegraded),
					"the removed component was judged by its chart's function")

				By("keeping it while its Application is terminating")
				reconcileAndGet()
				Expect(getApplication(secondComponent).DeletionTimestamp.IsZero()).To(BeFalse())
				_, componentSet = reconcileHealth(r)
				Expect(componentSet.Status.ComponentHealthStatuses).To(HaveLen(2))
				Expect(componentSet.Status.ComponentHealthStatuses[1].Name).To(Equal(secondComponent))

				By("dropping it once the Application is gone")
				finishTeardown(secondComponent)
				_, componentSet = reconcileHealth(r)
				Expect(componentSet.Status.ComponentHealthStatuses).To(HaveLen(1))
				Expect(componentSet.Status.ComponentHealthStatuses[0].Name).To(Equal(firstComponent))
			})

			It("should release the next component only once the chart judges the previous one healthy", func() {
				judgeWith(&fakeArgoCD{})
				secondApplication := func() error {
					return k8sClient.Get(ctx, appKey(secondComponent), &argocdv1alpha1.Application{})
				}

				By("holding it while Argo CD reports Healthy but the chart does not")
				reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusHealthy,
					argocdv1alpha1.SyncStatusSynced)
				componentSet := reconcileAndGet()
				Expect(apierrors.IsNotFound(secondApplication())).To(BeTrue())
				progressing := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing)
				Expect(progressing.Status).To(Equal(metav1.ConditionTrue))
				Expect(progressing.Message).To(ContainSubstring(`has health "Progressing"`))
				Expect(progressing.Message).To(ContainSubstring("0/3 nodes are available"))

				By("releasing it once the chart judges a component healthy that Argo CD calls Progressing")
				firstHealth = catalogs.Health{Status: cclusternvidiacomv1alpha1.ComponentHealthHealthy, Reason: "Running"}
				reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusProgressing,
					argocdv1alpha1.SyncStatusSynced)
				reconcileAndGet()
				Expect(secondApplication()).To(Succeed())
			})

			It("should judge by Argo CD's aggregate health when the resource tree cannot be read", func() {
				judgeWith(&fakeArgoCD{treeErr: errors.New("argocd-server unavailable")})
				release(firstComponent)
				Expect(k8sClient.Get(ctx, appKey(secondComponent), &argocdv1alpha1.Application{})).To(Succeed(),
					"a component Argo CD reports Healthy was held although its chart could not judge it")
			})
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

		// A removal waiting for the remaining components can still be waiting when the
		// CCLComponentSet is deleted. Removed components go first, in the order removal
		// uses: one may depend on a component that remains.
		It("should tear down removed components first, highest wave first, then the rest", func() {
			// Last in name order, so name order and wave order disagree.
			const third = "vault"
			createInfraPlane(planeA, true)
			createInfraPlane(planeB, true)
			createComponentSet(append(components(), onPlaneA(third)))
			settle()
			release(firstComponent, secondComponent, third)

			By("removing two components while the remaining one is degraded")
			reportCurrentComparison(firstComponent, argocdv1alpha1.HealthStatusDegraded, argocdv1alpha1.SyncStatusSynced)
			setComponents(components()[:1])
			reconcileAndGet()
			Expect(terminating(third)).To(BeFalse())

			componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
			Expect(k8sClient.Get(ctx, componentSetKey, componentSet)).To(Succeed())
			Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())

			By("removing the removed ones first, highest wave first")
			reconcileOnly()
			Expect(terminating(third)).To(BeTrue())
			Expect(terminating(secondComponent)).To(BeFalse())
			Expect(terminating(firstComponent)).To(BeFalse())

			finishTeardown(third)
			reconcileOnly()
			Expect(terminating(secondComponent)).To(BeTrue())
			Expect(terminating(firstComponent)).To(BeFalse())

			By("then the component still in the spec")
			finishTeardown(secondComponent)
			reconcileOnly()
			Expect(terminating(firstComponent)).To(BeTrue())
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
						annotationComponentSetNamespace: otherNamespace,
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

			applied := conditionOf(componentSet, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied)
			Expect(applied.Status).To(Equal(metav1.ConditionFalse))
			Expect(applied.Reason).To(Equal(cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict))

			By("leaving the foreign Application untouched")
			Expect(getApplication(firstComponent).Spec.Source.RepoURL).
				To(Equal("https://example.com/someone-elses"))

			By("leaving it in place on delete, too")
			Expect(k8sClient.Delete(ctx, componentSet)).To(Succeed())
			reconcileOnly()
			Expect(getApplication(firstComponent).DeletionTimestamp).To(BeNil())
		})

		It("should report ApplicationConflict on exactly one of two CCLComponentSets racing for an Application name", func() {
			createInfraPlane(planeA, true)

			// The name joins with "-", so set "x" with component "cart-api" and set
			// "x-cart" with component "api" want the same Application.
			otherKey := types.NamespacedName{Name: componentSetName + "-cart", Namespace: namespace}
			shared := applicationName(componentSetName, "cart-api")
			Expect(applicationName(otherKey.Name, "api")).To(Equal(shared))

			component := components()[0]
			component.Name = "cart-api"
			createComponentSet([]cclusternvidiacomv1alpha1.ComponentSpec{component})
			component.Name = "api"
			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLComponentSet{
				ObjectMeta: metav1.ObjectMeta{Name: otherKey.Name, Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLComponentSetSpec{
					Components: []cclusternvidiacomv1alpha1.ComponentSpec{component},
				},
			})).To(Succeed())
			DeferCleanup(func() {
				// AfterEach tears down only componentSetKey's set.
				other := &cclusternvidiacomv1alpha1.CCLComponentSet{}
				if err := k8sClient.Get(ctx, otherKey, other); apierrors.IsNotFound(err) {
					return
				}
				controllerutil.RemoveFinalizer(other, componentSetFinalizerName)
				Expect(client.IgnoreNotFound(k8sClient.Update(ctx, other))).To(Succeed())
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, other))).To(Succeed())
			})

			keys := []types.NamespacedName{componentSetKey, otherKey}
			for _, key := range keys {
				// The first pass only writes the finalizer.
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}

			By("reconciling both at once, each seeing the Application missing")
			var arrived sync.WaitGroup
			arrived.Add(len(keys))
			racers := make([]*meetAtApplicationGet, len(keys))
			errs := make(chan error, len(keys))
			for i, key := range keys {
				racers[i] = &meetAtApplicationGet{Client: k8sClient, arrived: &arrived}
				racer := *reconciler
				racer.Client = racers[i]
				go func() {
					defer GinkgoRecover()
					_, err := racer.Reconcile(ctx, reconcile.Request{NamespacedName: key})
					errs <- err
				}()
			}
			for range keys {
				var err error
				Eventually(errs, "10s").Should(Receive(&err))
				Expect(err).NotTo(HaveOccurred())
			}
			for _, racer := range racers {
				Expect(racer.staged).To(BeTrue(), "the race to create was not staged")
			}

			// What each set's ComponentsApplied reports, by set name.
			reasons := func() map[string]string {
				GinkgoHelper()

				byName := map[string]string{}
				for _, key := range keys {
					componentSet := &cclusternvidiacomv1alpha1.CCLComponentSet{}
					Expect(k8sClient.Get(ctx, key, componentSet)).To(Succeed())
					byName[key.Name] = conditionOf(componentSet,
						cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied).Reason
				}
				return byName
			}
			raced := reasons()
			Expect(raced).To(ConsistOf(
				cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated,
				cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationConflict))

			application := &argocdv1alpha1.Application{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shared, Namespace: namespace},
				application)).To(Succeed())
			Expect(raced).To(HaveKeyWithValue(application.Annotations[annotationComponentSetName],
				cclusternvidiacomv1alpha1.CCLComponentSetReasonApplicationsCreated),
				"the Application belongs to the CCLComponentSet reporting the conflict")

			By("keeping the outcome when both are reconciled again, one after the other")
			for _, key := range keys {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(reasons()).To(Equal(raced))
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

// movedBeforeStatusWrite writes the object through the API server just before the first
// status write, so that write names a resource version the server has moved past and gets
// a real Conflict back. Later writes pass through.
type movedBeforeStatusWrite struct {
	client.Client
	moved bool
}

func (c *movedBeforeStatusWrite) Status() client.SubResourceWriter {
	return &movedStatusWriter{SubResourceWriter: c.Client.Status(), racing: c}
}

type movedStatusWriter struct {
	client.SubResourceWriter
	racing *movedBeforeStatusWrite
}

func (w *movedStatusWriter) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.SubResourcePatchOption,
) error {
	if !w.racing.moved {
		w.racing.moved = true
		current := &cclusternvidiacomv1alpha1.CCLComponentSet{}
		if err := w.racing.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return err
		}
		base := current.DeepCopy()
		metav1.SetMetaDataAnnotation(&current.ObjectMeta, "test.ccluster.nvidia.com/moved", "true")
		if err := w.racing.Patch(ctx, current, client.MergeFrom(base)); err != nil {
			return err
		}
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

// countingStatusWrites counts the status writes made through it.
type countingStatusWrites struct {
	client.Client
	writes int
}

func (c *countingStatusWrites) Status() client.SubResourceWriter {
	return &countedStatusWriter{SubResourceWriter: c.Client.Status(), counting: c}
}

type countedStatusWriter struct {
	client.SubResourceWriter
	counting *countingStatusWrites
}

func (w *countedStatusWriter) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.SubResourcePatchOption,
) error {
	w.counting.writes++
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func (w *countedStatusWriter) Update(
	ctx context.Context,
	obj client.Object,
	opts ...client.SubResourceUpdateOption,
) error {
	w.counting.writes++
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

// meetAtApplicationGet holds the first Application read made through it until every client
// sharing arrived has made its own, so all of them act on the same read. staged records
// whether they all met and that read found nothing.
//
// The wait is bounded, so a client that never reads an Application fails the spec on
// staged rather than holding the others forever.
type meetAtApplicationGet struct {
	client.Client
	arrived *sync.WaitGroup
	once    sync.Once
	staged  bool
}

func (c *meetAtApplicationGet) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if _, ok := obj.(*argocdv1alpha1.Application); ok {
		c.once.Do(func() {
			c.arrived.Done()
			met := make(chan struct{})
			go func() {
				c.arrived.Wait()
				close(met)
			}()
			select {
			case <-met:
				c.staged = apierrors.IsNotFound(err)
			case <-time.After(5 * time.Second): // Inside the spec's 10s wait for the racers.
			}
		})
	}
	return err
}

// staleApplicationList serves the rollout an Application list from before the last write,
// which is what an informer that has not caught up does.
type staleApplicationList struct {
	client.Client
	application *argocdv1alpha1.Application
}

func (c *staleApplicationList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if applications, ok := list.(*argocdv1alpha1.ApplicationList); ok {
		applications.Items = []argocdv1alpha1.Application{*c.application.DeepCopy()}
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}
