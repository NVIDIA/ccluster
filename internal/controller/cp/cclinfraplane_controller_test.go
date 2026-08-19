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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

var _ = Describe("CCLInfraPlane Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName    = "test-resource"
			secretName      = "test-resource-credentials"
			namespace       = "default"
			argoCDNamespace = "argocd"
			serverURL       = "https://infra.example.com:6443"
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
			Name:      resourceName,
			Namespace: argoCDNamespace,
		}

		// Built in BeforeEach: k8sClient does not exist until BeforeSuite has run.
		var reconciler *CCLInfraPlaneReconciler

		// reconcileAndGet reconciles the CCLInfraPlane, asserts no error is returned,
		// and returns the refreshed object.
		reconcileAndGet := func() *cclusternvidiacomv1alpha1.CCLInfraPlane {
			GinkgoHelper()

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

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
			reconciler = &CCLInfraPlaneReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				ArgoCDNamespace: argoCDNamespace,
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
					ClusterCredentialsRef: cclusternvidiacomv1alpha1.LocalSecretReference{
						Name: secretName,
					},
				},
			})).To(Succeed())
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance CCLInfraPlane")
			resource := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

				// envtest runs no garbage collector, so the object survives until the
				// controller drops its finalizer. Without this the next spec's Create
				// fails with AlreadyExists.
				_, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
				Eventually(func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, typeNamespacedName, resource))
				}).Should(BeTrue())
			} else {
				Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
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

			By("overwriting the Argo CD cluster name with the CCLInfraPlane's name")
			Expect(mirror.Data).To(HaveKeyWithValue("name", []byte(resourceName)))

			By("cloning every other key verbatim")
			for key, value := range source.Data {
				if key == "name" {
					continue
				}
				Expect(mirror.Data).To(HaveKeyWithValue(key, value))
			}
			Expect(mirror.Data).To(HaveLen(len(source.Data)))

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
			Expect(restored.Data).To(HaveKeyWithValue("name", []byte(resourceName)))
			Expect(restored.Labels).To(HaveKeyWithValue(argoCDSecretTypeLabel, argoCDSecretTypeCluster))
		})

		It("should refuse to overwrite a Secret owned by another CCLInfraPlane", func() {
			createSecret(credentials)

			By("planting a mirrored Secret that belongs to a CCLInfraPlane in another namespace")
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
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

		It("should delete the mirrored Secret when the CCLInfraPlane is deleted", func() {
			createSecret(credentials)

			infraPlane := settle()
			Expect(controllerutil.ContainsFinalizer(infraPlane, finalizerName)).To(BeTrue())

			Expect(k8sClient.Delete(ctx, infraPlane)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, mirrorNamespacedName, &corev1.Secret{}))).To(BeTrue())
			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, typeNamespacedName, &cclusternvidiacomv1alpha1.CCLInfraPlane{}))).To(BeTrue())
		})

	})
})
