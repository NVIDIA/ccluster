/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/remote"
)

var _ = Describe("CCLInfraPlane Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName    = "test-resource"
			secretName      = "test-resource-credentials"
			namespace       = "default"
			argoCDNamespace = "argocd"
			serverURL       = "https://infra.example.com:6443"
			repoURL         = "https://github.com/NVIDIA/ccluster"
			domain          = "gpu-a.example.com"

			// resyncPeriod differs from clusterConfigRetryInterval, so a spec can tell
			// which one a reconcile asked for.
			resyncPeriod = 7 * time.Minute

			// mirrorName is what the cluster is registered as: prefixed with the
			// namespace, because Argo CD's cluster names are global.
			mirrorName = namespace + "-" + resourceName
		)

		// credentials is the opaque payload the controller must clone byte for byte.
		// "name" deliberately disagrees with the CCLInfraPlane's name: the operator
		// owns that key and must overwrite it.
		credentials := map[string]string{
			"name":   "a-name-the-operator-should-replace",
			"server": serverURL,
			"config": `{"bearerToken":"fake-token"}`,
		}

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: namespace,
		}
		secretNamespacedName := types.NamespacedName{
			Name:      secretName,
			Namespace: namespace,
		}
		mirrorNamespacedName := types.NamespacedName{
			Name:      mirrorName,
			Namespace: argoCDNamespace,
		}
		// The namespace's AppProject, named after the namespace, in the Argo CD namespace.
		projectNamespacedName := types.NamespacedName{
			Name:      namespace,
			Namespace: argoCDNamespace,
		}

		// Built in BeforeEach: k8sClient does not exist until BeforeSuite has run.
		var reconciler *CCLInfraPlaneReconciler

		// remoteClient is what the reconciler's ClientFactory hands out for every
		// credentials Secret. It defaults to k8sClient: envtest itself plays the
		// infra-plane cluster, so the CCLClusterConfig lands beside the CCLInfraPlane.
		var remoteClient client.Client

		clusterConfigKey := types.NamespacedName{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName}

		// getClusterConfig returns the CCLClusterConfig on the "infra-plane cluster".
		getClusterConfig := func() *cclusternvidiacomv1alpha1.CCLClusterConfig {
			GinkgoHelper()

			config := &cclusternvidiacomv1alpha1.CCLClusterConfig{}
			Expect(k8sClient.Get(ctx, clusterConfigKey, config)).To(Succeed())
			return config
		}

		clusterConfigExists := func() bool {
			GinkgoHelper()

			err := k8sClient.Get(ctx, clusterConfigKey, &cclusternvidiacomv1alpha1.CCLClusterConfig{})
			if apierrors.IsNotFound(err) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return true
		}

		// reconcileKey reconciles the named CCLInfraPlane and asserts no error is returned.
		reconcileKey := func(key types.NamespacedName) {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		// reconcileAndGet reconciles the CCLInfraPlane, asserts no error is returned,
		// and returns the refreshed object.
		reconcileAndGet := func() *cclusternvidiacomv1alpha1.CCLInfraPlane {
			GinkgoHelper()

			reconcileKey(typeNamespacedName)

			refreshed := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, refreshed)).To(Succeed())
			return refreshed
		}

		// settle reconciles until the finalizer has been added and the mirroring pass
		// has run. The first reconcile only writes the finalizer and returns.
		settle := func() *cclusternvidiacomv1alpha1.CCLInfraPlane {
			GinkgoHelper()

			reconcileAndGet()
			return reconcileAndGet()
		}

		// createInfraPlane creates a second CCLInfraPlane in the given namespace, for the
		// specs about a project shared between planes. Cleaned up by AfterEach.
		createInfraPlane := func(key types.NamespacedName) {
			GinkgoHelper()

			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
				Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{
						Name: key.Name + "-credentials",
					},
					Domain: domain,
				},
			})).To(Succeed())
		}

		// deleteAndFinalize deletes a CCLInfraPlane and runs the reconcile that clears
		// its finalizer, then waits for it to be gone.
		deleteAndFinalize := func(key types.NamespacedName) {
			GinkgoHelper()

			resource := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			reconcileKey(key)
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, key, resource))
			}).Should(BeTrue())
		}

		// getProject returns the namespace's AppProject.
		getProject := func(key types.NamespacedName) *argocdv1alpha1.AppProject {
			GinkgoHelper()

			project := &argocdv1alpha1.AppProject{}
			Expect(k8sClient.Get(ctx, key, project)).To(Succeed())
			return project
		}

		projectExists := func(key types.NamespacedName) bool {
			GinkgoHelper()

			err := k8sClient.Get(ctx, key, &argocdv1alpha1.AppProject{})
			if apierrors.IsNotFound(err) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return true
		}

		// createSecret creates the referenced source Secret with the given keys.
		createSecret := func(data map[string]string) {
			GinkgoHelper()

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				StringData: data,
			})).To(Succeed())
		}

		// getMirror returns the mirrored Secret in the Argo CD namespace.
		getMirror := func() *corev1.Secret {
			GinkgoHelper()

			mirror := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, mirrorNamespacedName, mirror)).To(Succeed())
			return mirror
		}

		BeforeEach(func() {
			remoteClient = k8sClient
			reconciler = &CCLInfraPlaneReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				ArgoCDNamespace: argoCDNamespace,
				CatalogRepoURL:  repoURL,
				RemoteClients: remote.ClientFactoryFunc(func(*corev1.Secret) (client.Client, error) {
					return remoteClient, nil
				}),
				ResyncPeriod: resyncPeriod,
			}

			By("creating the Argo CD namespace")
			err := k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: argoCDNamespace},
			})
			if !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating the custom resource for the Kind CCLInfraPlane")
			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{
						Name: secretName,
					},
					Domain: domain,
				},
			})).To(Succeed())
		})

		AfterEach(func() {
			By("Cleanup every CCLInfraPlane, running the finalizer of each")
			// envtest runs no garbage collector, so an object survives until the
			// controller drops its finalizer. Without this the next spec's Create
			// fails with AlreadyExists.
			for _, ns := range []string{namespace, argoCDNamespace} {
				var list cclusternvidiacomv1alpha1.CCLInfraPlaneList
				Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
				for i := range list.Items {
					deleteAndFinalize(client.ObjectKeyFromObject(&list.Items[i]))
				}
			}

			By("Cleanup the source and mirrored Secrets")
			for _, key := range []types.NamespacedName{secretNamespacedName, mirrorNamespacedName} {
				secret := &corev1.Secret{}
				if err := k8sClient.Get(ctx, key, secret); err == nil {
					Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
				} else {
					Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
				}
			}

			By("Cleanup the CCLClusterConfig a spec left behind")
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &cclusternvidiacomv1alpha1.CCLClusterConfig{
				ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
			}))).To(Succeed())

			By("Cleanup any AppProject a spec left behind")
			var projects argocdv1alpha1.AppProjectList
			Expect(k8sClient.List(ctx, &projects, client.InNamespace(argoCDNamespace))).To(Succeed())
			for i := range projects.Items {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &projects.Items[i]))).To(Succeed())
			}
		})

		It("should report SecretNotFound and mirror nothing when the source Secret is missing", func() {
			infraPlane := settle()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonSecretNotFound))

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}))).To(BeTrue())
		})

		// The project comes before the Secret: it exists for as long as any CCLInfraPlane
		// does, credentials or not, so a CCLComponentSet in the namespace always has one.
		It("should create the namespace's AppProject even before the credentials Secret exists", func() {
			settle()

			project := getProject(projectNamespacedName)

			By("confining the namespace's Applications to the catalog and its own clusters")
			Expect(project.Spec.SourceRepos).To(Equal([]string{repoURL}))
			Expect(project.Spec.SourceNamespaces).To(Equal([]string{namespace}))
			Expect(project.Spec.Destinations).To(Equal([]argocdv1alpha1.ApplicationDestination{
				{Server: "*", Namespace: "*"},
			}))
			Expect(project.Spec.PermitOnlyProjectScopedClusters).To(BeTrue())
			Expect(project.Spec.ClusterResourceWhitelist).To(Equal([]metav1.GroupKind{
				{Group: "*", Kind: "*"},
			}))

			By("stamping it as the operator's, for this namespace")
			Expect(project.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))
			Expect(project.Annotations).To(HaveKeyWithValue(annotationInfraPlaneNamespace, namespace))
		})

		It("should restore the AppProject after it drifts", func() {
			settle()

			By("loosening the AppProject by hand")
			project := getProject(projectNamespacedName)
			project.Spec.SourceRepos = []string{"*"}
			project.Spec.PermitOnlyProjectScopedClusters = false
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			reconcileAndGet()

			restored := getProject(projectNamespacedName)
			Expect(restored.Spec.SourceRepos).To(Equal([]string{repoURL}))
			Expect(restored.Spec.PermitOnlyProjectScopedClusters).To(BeTrue())
		})

		// sourceRepos is modelled and namespaceResourceBlacklist is not, so only the
		// first is visible to the comparison that decides whether to write. The write it
		// triggers replaces the whole spec, taking the second with it, and leaves the
		// status Argo CD owns in place - which a whole-object update would delete, since
		// the operator's AppProject type has no status at all.
		It("should replace the AppProject spec while preserving Argo CD status", func() {
			settle()
			project := getProject(projectNamespacedName)
			Expect(k8sClient.Patch(ctx, project, client.RawPatch(types.MergePatchType, []byte(`{
				"spec":{"sourceRepos":["*"],"namespaceResourceBlacklist":[{"group":"*","kind":"*"}]},
				"status":{"jwtTokensByRole":{"reader":{"items":[{"id":"test-id","iat":1}]}}}
			}`)))).To(Succeed())

			reconcileAndGet()
			raw := &unstructured.Unstructured{}
			raw.SetGroupVersionKind(argocdv1alpha1.GroupVersion.WithKind("AppProject"))
			Expect(k8sClient.Get(ctx, projectNamespacedName, raw)).To(Succeed())
			_, found, err := unstructured.NestedFieldNoCopy(raw.Object, "spec", "namespaceResourceBlacklist")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(getProject(projectNamespacedName).Spec.SourceRepos).To(Equal([]string{repoURL}))
			tokens, found, err := unstructured.NestedSlice(raw.Object, "status", "jwtTokensByRole", "reader", "items")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(tokens).To(Equal([]any{map[string]any{"id": "test-id", "iat": int64(1)}}))

			version := raw.GetResourceVersion()
			reconcileAndGet()
			Expect(getProject(projectNamespacedName).ResourceVersion).To(Equal(version))
		})

		// Argo CD refuses an AppProject that lists the Argo CD namespace itself in
		// sourceNamespaces, and Applications there may use any project anyway.
		It("should omit sourceNamespaces for a CCLInfraPlane in the Argo CD namespace", func() {
			key := types.NamespacedName{Name: "in-argocd", Namespace: argoCDNamespace}
			createInfraPlane(key)
			reconcileKey(key)
			reconcileKey(key)

			project := getProject(types.NamespacedName{Name: argoCDNamespace, Namespace: argoCDNamespace})
			Expect(project.Spec.SourceNamespaces).To(BeEmpty())
			Expect(project.Spec.SourceRepos).To(Equal([]string{repoURL}))
		})

		It("should report ProjectConflict for an AppProject claimed by another namespace", func() {
			createSecret(credentials)

			By("planting an AppProject stamped for a different namespace")
			Expect(k8sClient.Create(ctx, &argocdv1alpha1.AppProject{
				ObjectMeta: metav1.ObjectMeta{
					Name:        namespace,
					Namespace:   argoCDNamespace,
					Annotations: map[string]string{annotationInfraPlaneNamespace: "other-namespace"},
				},
				Spec: argocdv1alpha1.AppProjectSpec{SourceRepos: []string{"someone-elses"}},
			})).To(Succeed())

			infraPlane := settle()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonProjectConflict))

			By("leaving the foreign AppProject untouched and mirroring nothing")
			Expect(getProject(projectNamespacedName).Spec.SourceRepos).To(Equal([]string{"someone-elses"}))
			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}))).To(BeTrue())
		})

		It("should clone the Secret into the Argo CD namespace", func() {
			createSecret(credentials)

			infraPlane := settle()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(reasonClusterSecretCreated))
			Expect(infraPlane.Status.ObservedGeneration).To(Equal(infraPlane.Generation))

			source := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretNamespacedName, source)).To(Succeed())

			mirror := getMirror()
			Expect(mirror.Type).To(Equal(source.Type))

			By("overwriting the Argo CD cluster name with the namespace-prefixed name")
			Expect(mirror.Data).To(HaveKeyWithValue("name", []byte(mirrorName)))

			By("scoping the cluster to the namespace's AppProject")
			Expect(mirror.Data).To(HaveKeyWithValue("project", []byte(namespace)))

			By("cloning every other key verbatim")
			for key, value := range source.Data {
				if key == "name" {
					continue
				}
				Expect(mirror.Data).To(HaveKeyWithValue(key, value))
			}
			Expect(mirror.Data).To(HaveLen(len(source.Data) + 1))

			Expect(mirror.Labels).To(HaveKeyWithValue(argoCDSecretTypeLabel, argoCDSecretTypeCluster))
			Expect(mirror.Annotations).To(HaveKeyWithValue(annotationInfraPlaneName, resourceName))
			Expect(mirror.Annotations).To(HaveKeyWithValue(annotationInfraPlaneNamespace, namespace))
		})

		It("should restore the mirrored Secret after it drifts", func() {
			createSecret(credentials)
			settle()

			By("corrupting the mirrored Secret")
			mirror := getMirror()
			mirror.Data["config"] = []byte("tampered")
			delete(mirror.Labels, argoCDSecretTypeLabel)
			Expect(k8sClient.Update(ctx, mirror)).To(Succeed())

			reconcileAndGet()

			source := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, secretNamespacedName, source)).To(Succeed())

			restored := getMirror()
			Expect(restored.Data).To(HaveKeyWithValue("config", source.Data["config"]))
			Expect(restored.Data).To(HaveKeyWithValue("name", []byte(mirrorName)))
			Expect(restored.Labels).To(HaveKeyWithValue(argoCDSecretTypeLabel, argoCDSecretTypeCluster))
		})

		It("should refuse to overwrite a Secret owned by another CCLInfraPlane", func() {
			createSecret(credentials)

			By("planting a mirrored Secret that belongs to a CCLInfraPlane in another namespace")
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mirrorName,
					Namespace: argoCDNamespace,
					Annotations: map[string]string{
						annotationInfraPlaneName:      resourceName,
						annotationInfraPlaneNamespace: "other-namespace",
					},
				},
				StringData: map[string]string{"config": "someone-elses"},
			})).To(Succeed())

			infraPlane := settle()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonClusterSecretConflict))

			Expect(getMirror().Data).To(HaveKeyWithValue("config", []byte("someone-elses")))
		})

		It("should delete the mirrored Secret and the AppProject when the CCLInfraPlane is deleted", func() {
			createSecret(credentials)

			infraPlane := settle()
			Expect(controllerutil.ContainsFinalizer(infraPlane, finalizerName)).To(BeTrue())
			Expect(projectExists(projectNamespacedName)).To(BeTrue())

			Expect(k8sClient.Delete(ctx, infraPlane)).To(Succeed())
			reconcileKey(typeNamespacedName)

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}))).To(BeTrue())
			Expect(projectExists(projectNamespacedName)).To(BeFalse())
			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, typeNamespacedName, &cclusternvidiacomv1alpha1.CCLInfraPlane{}))).To(BeTrue())
		})

		// The project is shared: it belongs to the namespace, and goes only with the
		// last CCLInfraPlane out.
		It("should keep the AppProject while another CCLInfraPlane remains in the namespace", func() {
			other := types.NamespacedName{Name: "other-plane", Namespace: namespace}
			createInfraPlane(other)
			settle()
			reconcileKey(other)
			reconcileKey(other)

			By("deleting the first plane")
			deleteAndFinalize(typeNamespacedName)
			Expect(projectExists(projectNamespacedName)).To(BeTrue())

			By("deleting the last plane")
			deleteAndFinalize(other)
			Expect(projectExists(projectNamespacedName)).To(BeFalse())
		})

		It("should leave an AppProject claimed by another namespace in place on delete", func() {
			settle()

			By("re-stamping the AppProject for a different namespace")
			project := getProject(projectNamespacedName)
			project.Annotations[annotationInfraPlaneNamespace] = "other-namespace"
			Expect(k8sClient.Update(ctx, project)).To(Succeed())

			deleteAndFinalize(typeNamespacedName)
			Expect(projectExists(projectNamespacedName)).To(BeTrue())
		})

		// The Secret watch is what makes the controller react to credentials changing
		// and to the mirror being tampered with. A bug here is silent: the operator
		// simply stops reconciling, and nothing else in the suite notices.
		It("should enqueue the CCLInfraPlane a mirrored Secret was created for", func() {
			requests := reconciler.infraPlanesForSecret(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: argoCDNamespace,
					Annotations: map[string]string{
						annotationInfraPlaneName:      resourceName,
						annotationInfraPlaneNamespace: namespace,
					},
				},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: typeNamespacedName}))
		})

		It("should enqueue the CCLInfraPlanes that reference a source Secret", func() {
			requests := reconciler.infraPlanesForSecret(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
			})
			Expect(requests).To(ConsistOf(reconcile.Request{NamespacedName: typeNamespacedName}))

			By("ignoring a Secret no CCLInfraPlane references")
			Expect(reconciler.infraPlanesForSecret(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: namespace},
			})).To(BeEmpty())
		})

		// The fall-through: an un-annotated Secret in the Argo CD namespace is not a
		// mirror, so it is looked up by referrer instead. This is the branch a
		// refactor is most likely to break by returning early.
		It("should not enqueue anything for an unannotated Secret in the Argo CD namespace", func() {
			Expect(reconciler.infraPlanesForSecret(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "argocd-server-tls", Namespace: argoCDNamespace},
			})).To(BeEmpty())
		})

		// The AppProject watch is what repairs a project deleted or loosened by hand.
		It("should enqueue every CCLInfraPlane in the namespace an AppProject is named after", func() {
			other := types.NamespacedName{Name: "other-plane", Namespace: namespace}
			createInfraPlane(other)

			requests := reconciler.infraPlanesForProject(ctx, &argocdv1alpha1.AppProject{
				ObjectMeta: metav1.ObjectMeta{Name: namespace, Namespace: argoCDNamespace},
			})
			Expect(requests).To(ConsistOf(
				reconcile.Request{NamespacedName: typeNamespacedName},
				reconcile.Request{NamespacedName: other},
			))

			By("ignoring an AppProject outside the Argo CD namespace")
			Expect(reconciler.infraPlanesForProject(ctx, &argocdv1alpha1.AppProject{
				ObjectMeta: metav1.ObjectMeta{Name: namespace, Namespace: "elsewhere"},
			})).To(BeEmpty())

			By("ignoring an AppProject named after a namespace with no CCLInfraPlanes")
			Expect(reconciler.infraPlanesForProject(ctx, &argocdv1alpha1.AppProject{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-namespace", Namespace: argoCDNamespace},
			})).To(BeEmpty())
		})

		// reconcileResult reconciles the CCLInfraPlane, asserts no error is returned, and
		// returns the result, for the specs about when the controller looks again.
		reconcileResult := func() reconcile.Result {
			GinkgoHelper()

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			return result
		}

		// expectClusterConfigApplied asserts the ClusterConfigApplied condition's status
		// and reason, and that it was computed from the current generation.
		expectClusterConfigApplied := func(
			infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
			status metav1.ConditionStatus,
			reason string,
		) {
			GinkgoHelper()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeClusterConfigApplied)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(status))
			Expect(condition.Reason).To(Equal(reason))
			Expect(condition.ObservedGeneration).To(Equal(infraPlane.Generation))
		}

		// foreignDomain is the domain of a CCLClusterConfig the CCLInfraPlane under test did
		// not create.
		const foreignDomain = "other.example.com"

		// createForeignClusterConfig creates a CCLClusterConfig on the "infra-plane cluster"
		// that the CCLInfraPlane under test did not create, carrying annotations.
		createForeignClusterConfig := func(annotations map[string]string) {
			GinkgoHelper()

			Expect(k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLClusterConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:        cclusternvidiacomv1alpha1.CCLClusterConfigName,
					Annotations: annotations,
				},
				Spec: cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: foreignDomain},
			})).To(Succeed())
		}

		It("should reject a CCLInfraPlane without a domain", func() {
			err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLInfraPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "no-domain", Namespace: namespace},
				Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: secretName},
				},
			})
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		})

		It("should reject a domain that is not a DNS name of two labels", func() {
			for _, invalid := range []string{"localhost", "Upper.example.com", "a..b", "-a.example.com"} {
				err := k8sClient.Create(ctx, &cclusternvidiacomv1alpha1.CCLInfraPlane{
					ObjectMeta: metav1.ObjectMeta{Name: "bad-domain", Namespace: namespace},
					Spec: cclusternvidiacomv1alpha1.CCLInfraPlaneSpec{
						ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalObjectReference{Name: secretName},
						Domain:                invalid,
					},
				})
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "domain %q: got %v", invalid, err)
			}
		})

		It("should reject a change to the domain", func() {
			infraPlane := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, infraPlane)).To(Succeed())
			infraPlane.Spec.Domain = "gpu-b.example.com"
			err := k8sClient.Update(ctx, infraPlane)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
			Expect(err.Error()).To(ContainSubstring("domain is immutable"))
		})

		It("should write the CCLClusterConfig with the domain on the infra-plane cluster", func() {
			createSecret(credentials)

			infraPlane := settle()

			expectClusterConfigApplied(infraPlane, metav1.ConditionTrue, reasonClusterConfigApplied)
			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))

			config := getClusterConfig()
			Expect(config.Spec.Domain).To(Equal(domain))
			Expect(config.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))
			Expect(config.Annotations).To(HaveKeyWithValue(annotationInfraPlaneName, resourceName))
			Expect(config.Annotations).To(HaveKeyWithValue(annotationInfraPlaneNamespace, namespace))

			By("resyncing periodically, since nothing on the infra-plane cluster is watched")
			Expect(reconcileResult().RequeueAfter).To(Equal(resyncPeriod))
		})

		It("should restore a CCLClusterConfig whose domain drifted, keeping its status", func() {
			createSecret(credentials)
			settle()

			By("the infra-plane operator writing status, and someone editing the domain")
			config := getClusterConfig()
			apimeta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
				Type: "Verified", Status: metav1.ConditionTrue, Reason: "ComponentsVerified",
			})
			Expect(k8sClient.Status().Update(ctx, config)).To(Succeed())
			config = getClusterConfig()
			config.Spec.Domain = "drifted.example.com"
			Expect(k8sClient.Update(ctx, config)).To(Succeed())

			reconcileAndGet()

			restored := getClusterConfig()
			Expect(restored.Spec.Domain).To(Equal(domain))
			Expect(apimeta.IsStatusConditionTrue(restored.Status.Conditions, "Verified")).To(BeTrue())
		})

		It("should restore the domain when the infra-plane operator writes status meanwhile", func() {
			createSecret(credentials)
			settle()
			config := getClusterConfig()
			config.Spec.Domain = "drifted.example.com"
			Expect(k8sClient.Update(ctx, config)).To(Succeed())

			By("writing status between the operator's read and its write, bumping the resourceVersion")
			watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			remoteClient = interceptor.NewClient(watchClient, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					written := &cclusternvidiacomv1alpha1.CCLClusterConfig{}
					if err := c.Get(ctx, key, written); err != nil {
						return err
					}
					apimeta.SetStatusCondition(&written.Status.Conditions, metav1.Condition{
						Type: "Verified", Status: metav1.ConditionTrue, Reason: "ComponentsVerified",
					})
					return c.Status().Update(ctx, written)
				},
			})

			infraPlane := reconcileAndGet()

			expectClusterConfigApplied(infraPlane, metav1.ConditionTrue, reasonClusterConfigApplied)
			restored := getClusterConfig()
			Expect(restored.Spec.Domain).To(Equal(domain))
			Expect(apimeta.IsStatusConditionTrue(restored.Status.Conditions, "Verified")).To(BeTrue())
		})

		It("should re-create a CCLClusterConfig deleted from the infra-plane cluster", func() {
			createSecret(credentials)
			settle()

			Expect(k8sClient.Delete(ctx, getClusterConfig())).To(Succeed())
			reconcileAndGet()

			Expect(getClusterConfig().Spec.Domain).To(Equal(domain))
		})

		It("should report Conflict for a CCLClusterConfig created by hand, and leave it in place", func() {
			createSecret(credentials)
			createForeignClusterConfig(nil)

			infraPlane := settle()

			expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterConfigConflict)
			config := getClusterConfig()
			Expect(config.Spec.Domain).To(Equal(foreignDomain))
			Expect(config.Annotations).NotTo(HaveKey(annotationInfraPlaneName))

			By("polling for it to be deleted, since nothing on the infra-plane cluster is watched")
			Expect(reconcileResult().RequeueAfter).To(Equal(clusterConfigRetryInterval))

			By("leaving it in place when the CCLInfraPlane is deleted")
			deleteAndFinalize(typeNamespacedName)
			Expect(getClusterConfig().Spec.Domain).To(Equal(foreignDomain))
		})

		It("should write the CCLClusterConfig once a conflicting one is deleted", func() {
			createSecret(credentials)
			createForeignClusterConfig(nil)
			expectClusterConfigApplied(settle(), metav1.ConditionFalse, reasonClusterConfigConflict)

			Expect(k8sClient.Delete(ctx, getClusterConfig())).To(Succeed())
			infraPlane := reconcileAndGet()

			expectClusterConfigApplied(infraPlane, metav1.ConditionTrue, reasonClusterConfigApplied)
			config := getClusterConfig()
			Expect(config.Spec.Domain).To(Equal(domain))
			Expect(config.Annotations).To(HaveKeyWithValue(annotationInfraPlaneName, resourceName))
		})

		It("should report Conflict and leave a CCLClusterConfig owned by another CCLInfraPlane alone", func() {
			createSecret(credentials)
			createForeignClusterConfig(map[string]string{
				annotationInfraPlaneName:      "other-plane",
				annotationInfraPlaneNamespace: "other-namespace",
			})

			infraPlane := settle()

			expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterConfigConflict)
			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeClusterConfigApplied)
			Expect(condition.Message).To(ContainSubstring("other-namespace/other-plane"))
			Expect(getClusterConfig().Spec.Domain).To(Equal(foreignDomain))
			Expect(reconcileResult().RequeueAfter).To(Equal(clusterConfigRetryInterval))

			By("still registering the cluster with Argo CD")
			Expect(apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable)).To(BeTrue())

			By("leaving it in place when the CCLInfraPlane is deleted")
			deleteAndFinalize(typeNamespacedName)
			Expect(getClusterConfig().Spec.Domain).To(Equal(foreignDomain))
		})

		It("should report CRDMissing when the infra-plane cluster does not serve CCLClusterConfig", func() {
			createSecret(credentials)

			By("handing out a client whose RESTMapper knows no kinds")
			var err error
			remoteClient, err = client.New(cfg, client.Options{
				Scheme: k8sClient.Scheme(),
				Mapper: apimeta.NewDefaultRESTMapper(nil),
			})
			Expect(err).NotTo(HaveOccurred())

			reconcileAndGet()
			Expect(reconcileResult().RequeueAfter).To(Equal(clusterConfigRetryInterval))
			infraPlane := reconcileAndGet()

			expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterConfigCRDMissing)
			Expect(apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable)).To(BeTrue())
			Expect(clusterConfigExists()).To(BeFalse())
		})

		It("should report ClusterUnreachable and retry when the infra-plane cluster refuses the connection", func() {
			createSecret(credentials)

			By("handing out a client for an address nothing listens on")
			var err error
			remoteClient, err = client.New(&rest.Config{
				Host:    "https://127.0.0.1:1",
				Timeout: remote.RequestTimeout,
			}, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())

			reconcileAndGet()
			Expect(reconcileResult().RequeueAfter).To(Equal(clusterConfigRetryInterval))
			infraPlane := reconcileAndGet()

			expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterUnreachable)
			Expect(apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable)).To(BeTrue())

			By("still deleting the CCLInfraPlane")
			deleteAndFinalize(typeNamespacedName)
		})

		It("should report AccessDenied and retry when the infra-plane cluster refuses the credentials", func() {
			createSecret(credentials)
			watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())

			for _, refusal := range []error{
				apierrors.NewUnauthorized("token expired"),
				apierrors.NewForbidden(schema.GroupResource{
					Group: cclusternvidiacomv1alpha1.GroupVersion.Group, Resource: "cclclusterconfigs",
				}, cclusternvidiacomv1alpha1.CCLClusterConfigName, errors.New("no RBAC")),
			} {
				By("handing out a client whose every read is refused with " + refusal.Error())
				remoteClient = interceptor.NewClient(watchClient, interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return refusal
					},
				})

				reconcileAndGet()
				Expect(reconcileResult().RequeueAfter).To(Equal(clusterConfigRetryInterval))
				infraPlane := reconcileAndGet()

				expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterAccessDenied)
				Expect(apimeta.IsStatusConditionTrue(infraPlane.Status.Conditions, conditionTypeAvailable)).To(BeTrue())
			}
		})

		It("should report Conflict when another CCLInfraPlane creates the CCLClusterConfig first", func() {
			createSecret(credentials)

			By("creating another plane's CCLClusterConfig between the operator's read and its create")
			watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			remoteClient = interceptor.NewClient(watchClient, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*cclusternvidiacomv1alpha1.CCLClusterConfig); ok && !clusterConfigExists() {
						createForeignClusterConfig(map[string]string{
							annotationInfraPlaneName:      "other-plane",
							annotationInfraPlaneNamespace: "other-namespace",
						})
					}
					return c.Create(ctx, obj, opts...)
				},
			})

			infraPlane := settle()

			expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonClusterConfigConflict)
			config := getClusterConfig()
			Expect(config.Spec.Domain).To(Equal(foreignDomain))
			Expect(config.Annotations).To(HaveKeyWithValue(annotationInfraPlaneName, "other-plane"))
		})

		It("should mark ClusterConfigApplied Unknown once the plane is no longer Available", func() {
			createSecret(credentials)
			expectClusterConfigApplied(settle(), metav1.ConditionTrue, reasonClusterConfigApplied)

			By("deleting the credentials Secret")
			Expect(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
			})).To(Succeed())
			Expect(reconcileResult()).To(Equal(reconcile.Result{}))
			infraPlane := reconcileAndGet()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal(reasonSecretNotFound))
			expectClusterConfigApplied(infraPlane, metav1.ConditionUnknown, reasonClusterConfigNotAvailable)
		})

		// These go through the real SecretClientFactory, the one the operator runs with.
		Context("with the operator's ClientFactory", func() {
			BeforeEach(func() {
				reconciler.RemoteClients = remote.SecretClientFactory{Local: cfg, Scheme: k8sClient.Scheme()}
			})

			It("should write the CCLClusterConfig into its own cluster for a Secret without a server", func() {
				createSecret(map[string]string{"config": `{"tlsClientConfig":{"insecure":false}}`})

				infraPlane := settle()

				expectClusterConfigApplied(infraPlane, metav1.ConditionTrue, reasonClusterConfigApplied)
				Expect(getClusterConfig().Spec.Domain).To(Equal(domain))

				By("deleting it with the CCLInfraPlane")
				deleteAndFinalize(typeNamespacedName)
				Expect(clusterConfigExists()).To(BeFalse())
			})

			It("should report UnsupportedCredentials for an exec plugin", func() {
				createSecret(map[string]string{
					"server": serverURL,
					"config": `{"execProviderConfig":{"command":"gke-gcloud-auth-plugin"}}`,
				})

				infraPlane := settle()

				expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonUnsupportedCredentials)
				Expect(reconcileResult().RequeueAfter).To(BeZero())
			})

			It("should report InvalidCredentials for a config that is not JSON", func() {
				createSecret(map[string]string{"server": serverURL, "config": "{not json"})

				infraPlane := settle()

				expectClusterConfigApplied(infraPlane, metav1.ConditionFalse, reasonInvalidCredentials)
				condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeClusterConfigApplied)
				Expect(condition.Message).NotTo(ContainSubstring("not json"))
			})
		})

		It("should delete the CCLClusterConfig before the mirrored Secret when the CCLInfraPlane is deleted", func() {
			createSecret(credentials)
			settle()
			Expect(clusterConfigExists()).To(BeTrue())

			By("recording what still exists when the infra-plane cluster is asked to delete it")
			var mirrorPresent bool
			watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			remoteClient = interceptor.NewClient(watchClient, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					mirrorPresent = k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}) == nil
					return c.Delete(ctx, obj, opts...)
				},
			})

			deleteAndFinalize(typeNamespacedName)

			Expect(clusterConfigExists()).To(BeFalse())
			Expect(mirrorPresent).To(BeTrue(), "the mirrored Secret was deleted first")
			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}))).To(BeTrue())
		})

		It("should delete the CCLClusterConfig with the mirrored Secret once the source Secret is gone", func() {
			createSecret(credentials)
			settle()
			Expect(clusterConfigExists()).To(BeTrue())

			By("deleting the source Secret before the finalizer runs, as `ccluster unregister` does")
			var reachedWith []string
			reconciler.RemoteClients = remote.ClientFactoryFunc(func(secret *corev1.Secret) (client.Client, error) {
				reachedWith = append(reachedWith, secret.Namespace+"/"+secret.Name)
				return remoteClient, nil
			})
			Expect(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
			})).To(Succeed())

			deleteAndFinalize(typeNamespacedName)

			Expect(clusterConfigExists()).To(BeFalse())
			Expect(reachedWith).To(Equal([]string{mirrorNamespacedName.String()}))
		})

	})
})
