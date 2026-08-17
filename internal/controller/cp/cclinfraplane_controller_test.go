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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
)

var _ = Describe("CCLInfraPlane Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
			secretName   = "test-resource-credentials"
			namespace    = "default"
			serverURL    = "https://infra.example.com:6443"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: namespace,
		}
		secretNamespacedName := types.NamespacedName{
			Name:      secretName,
			Namespace: namespace,
		}

		// reconcileAndGet reconciles the CCLInfraPlane, asserts no error is returned,
		// and returns the refreshed object.
		reconcileAndGet := func() *cclusternvidiacomv1alpha1.CCLInfraPlane {
			GinkgoHelper()

			controllerReconciler := &CCLInfraPlaneReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			refreshed := &cclusternvidiacomv1alpha1.CCLInfraPlane{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, refreshed)).To(Succeed())
			return refreshed
		}

		// createSecret creates the referenced Secret with the given keys.
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

		BeforeEach(func() {
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
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Cleanup the credentials Secret if the spec created one")
			secret := &corev1.Secret{}
			err := k8sClient.Get(ctx, secretNamespacedName, secret)
			if err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			} else {
				Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			}
		})

		It("should report SecretNotFound when the referenced Secret is missing", func() {
			infraPlane := reconcileAndGet()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonSecretNotFound))
		})

		It("should report SecretInvalid when a required key is missing", func() {
			createSecret(map[string]string{
				cclusternvidiacomv1alpha1.CredentialsKeyServer:      serverURL,
				cclusternvidiacomv1alpha1.CredentialsKeyBearerToken: "fake-token",
			})

			infraPlane := reconcileAndGet()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(reasonSecretInvalid))
			Expect(condition.Message).To(ContainSubstring(cclusternvidiacomv1alpha1.CredentialsKeyCACert))
		})

		It("should report CredentialsResolved when the Secret is complete", func() {
			createSecret(map[string]string{
				cclusternvidiacomv1alpha1.CredentialsKeyServer:      serverURL,
				cclusternvidiacomv1alpha1.CredentialsKeyBearerToken: "fake-token",
				cclusternvidiacomv1alpha1.CredentialsKeyCACert:      "fake-ca",
			})

			infraPlane := reconcileAndGet()

			condition := apimeta.FindStatusCondition(infraPlane.Status.Conditions, conditionTypeAvailable)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal(reasonCredentialsResolved))
			Expect(infraPlane.Status.ObservedGeneration).To(Equal(infraPlane.Generation))
		})
	})
})
