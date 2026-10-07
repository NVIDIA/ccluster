/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/components"
)

// switchVerifier is a catalogs.Verifier the specs flip by hand, and which records the
// spec it was handed, so the controller's contract - run every Verifier against the
// CCLClusterConfig, report the outcome - is tested without seventeen real charts.
type switchVerifier struct {
	ready   atomic.Bool
	lastCfg atomic.Pointer[cclusternvidiacomv1alpha1.CCLClusterConfigSpec]
}

func (v *switchVerifier) Verify(
	_ context.Context,
	_ client.Reader,
	cfg cclusternvidiacomv1alpha1.CCLClusterConfigSpec,
) bool {
	v.lastCfg.Store(&cfg)
	return v.ready.Load()
}

var _ = Describe("CCLClusterConfig Controller", func() {
	const (
		chartA = "catalogs/cluster_base/a"
		chartB = "catalogs/cluster_base/b"
		domain = "gpu-a.infra.example.com"
	)

	request := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	}

	var (
		reconciler *CCLClusterConfigReconciler
		a, b       *switchVerifier
	)

	verified := func() *metav1.Condition {
		var config cclusternvidiacomv1alpha1.CCLClusterConfig
		Expect(k8sClient.Get(ctx, request.NamespacedName, &config)).To(Succeed())
		return apimeta.FindStatusCondition(config.Status.Conditions, conditionTypeVerified)
	}

	BeforeEach(func() {
		a, b = &switchVerifier{}, &switchVerifier{}
		reconciler = &CCLClusterConfigReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Components: []components.Component{
				{Chart: chartA, Verifier: a},
				{Chart: chartB, Verifier: b},
				// A tenant chart: no Verifier, and therefore not counted.
				{Chart: "catalogs/deployments/c", Verifier: nil},
			},
		}

		config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
			Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: domain},
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
	})

	AfterEach(func() {
		config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, config))).To(Succeed())
	})

	It("reports Verified=False naming every failing chart, and rechecks soon", func() {
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(recheckWhileFailing))

		condition := verified()
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonComponentsNotVerified))
		Expect(condition.Message).To(ContainSubstring("0 of 2"))
		Expect(condition.Message).To(ContainSubstring(chartA))
		Expect(condition.Message).To(ContainSubstring(chartB))
	})

	It("hands every Verifier the spec it verifies against", func() {
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(a.lastCfg.Load()).NotTo(BeNil())
		Expect(a.lastCfg.Load().Domain).To(Equal(domain))
		Expect(b.lastCfg.Load().Domain).To(Equal(domain))
	})

	It("drops a chart from the message once it verifies, and reports True when all do", func() {
		a.ready.Store(true)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		condition := verified()
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Message).To(ContainSubstring("1 of 2"))
		Expect(condition.Message).NotTo(ContainSubstring(chartA))
		Expect(condition.Message).To(ContainSubstring(chartB))

		b.ready.Store(true)
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(recheckWhenVerified))

		condition = verified()
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(condition.Reason).To(Equal(reasonComponentsVerified))
		Expect(condition.ObservedGeneration).To(Equal(int64(1)))
	})

	It("goes back to False when a component stops verifying", func() {
		a.ready.Store(true)
		b.ready.Store(true)
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(verified().Status).To(Equal(metav1.ConditionTrue))

		b.ready.Store(false)
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(verified().Status).To(Equal(metav1.ConditionFalse))
		Expect(verified().Message).To(ContainSubstring(chartB))
	})

	It("re-verifies against a changed spec and records the new generation", func() {
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		var config cclusternvidiacomv1alpha1.CCLClusterConfig
		Expect(k8sClient.Get(ctx, request.NamespacedName, &config)).To(Succeed())
		config.Spec.Domain = "gpu-b.infra.example.com"
		Expect(k8sClient.Update(ctx, &config)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		Expect(a.lastCfg.Load().Domain).To(Equal("gpu-b.infra.example.com"))
		Expect(k8sClient.Get(ctx, request.NamespacedName, &config)).To(Succeed())
		Expect(config.Status.ObservedGeneration).To(Equal(config.Generation))
		Expect(verified().ObservedGeneration).To(Equal(config.Generation))
	})

	It("runs the Verifiers concurrently", func() {
		// Each Verifier blocks until every other one has been called. A sequential
		// controller would never get there; the timeout on ctx fails the spec instead
		// of hanging the suite.
		var arrived sync.WaitGroup
		arrived.Add(2)
		met := make(chan struct{})
		go func() {
			arrived.Wait()
			close(met)
		}()
		rendezvous := &meetingVerifier{arrived: &arrived, met: met}
		reconciler.Components = []components.Component{
			{Chart: chartA, Verifier: rendezvous},
			{Chart: chartB, Verifier: rendezvous},
		}

		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := reconciler.Reconcile(timeout, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(verified().Status).To(Equal(metav1.ConditionTrue))
	})

	It("is idle when the singleton does not exist", func() {
		config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
		}
		Expect(k8sClient.Delete(ctx, config)).To(Succeed())

		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(a.lastCfg.Load()).To(BeNil(), "nothing should be verified without a spec")
	})

	It("rejects any name but \"cluster\" and an empty domain at admission", func() {
		other := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "other"},
			Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: domain},
		}
		err := k8sClient.Create(ctx, other)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected the CEL singleton rule to reject %q", other.Name)

		var config cclusternvidiacomv1alpha1.CCLClusterConfig
		Expect(k8sClient.Get(ctx, request.NamespacedName, &config)).To(Succeed())
		config.Spec.Domain = ""
		err = k8sClient.Update(ctx, &config)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an empty domain to be rejected")
	})
})

// meetingVerifier passes only once every expected call to Verify is in flight at the
// same time: each call checks in on arrived and then waits for met, which the spec
// closes when the last one has checked in.
type meetingVerifier struct {
	arrived *sync.WaitGroup
	met     <-chan struct{}
}

func (v *meetingVerifier) Verify(ctx context.Context, _ client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) bool {
	v.arrived.Done()
	select {
	case <-v.met:
		return true
	case <-ctx.Done():
		return false
	}
}

// Compile-time check that the test doubles are what the controller consumes.
var (
	_ catalogs.Verifier = &switchVerifier{}
	_ catalogs.Verifier = &meetingVerifier{}
)
