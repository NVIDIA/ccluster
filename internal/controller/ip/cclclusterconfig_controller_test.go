/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	verifierregistry "github.com/NVIDIA/ccluster/internal/registry/verifier_registry"
)

// errNotReady is what a switchVerifier reports while it is off.
var errNotReady = errors.New("not ready")

// switchVerifier is a check the specs flip by hand, and which records the
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
) error {
	v.lastCfg.Store(&cfg)
	if !v.ready.Load() {
		return errNotReady
	}
	return nil
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
			Verifiers: verifierRegistry(
				registration{chartA, a.Verify},
				registration{chartB, b.Verify},
			),
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
		Expect(condition.Message).To(Equal(
			"0 of 2 cluster_base components verified; failing: " +
				chartA + " (not ready); " + chartB + " (not ready)"))
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
		reconciler.Verifiers = verifierRegistry(
			registration{chartA, rendezvous.Verify},
			registration{chartB, rendezvous.Verify},
		)

		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := reconciler.Reconcile(timeout, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(verified().Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports a panicking Verifier as failing without stopping the others", func() {
		a.ready.Store(true)
		reconciler.Verifiers = verifierRegistry(
			registration{chartA, a.Verify},
			registration{chartB, panicVerifier{}.Verify},
		)

		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		condition := verified()
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Message).To(ContainSubstring("1 of 2"))
		Expect(condition.Message).To(ContainSubstring(chartB + " (verifier panicked: verifier bug)"))
		Expect(condition.Message).NotTo(ContainSubstring(chartA))
	})

	It("gives every Verifier a deadline of its own", func() {
		// Reconcile's ctx has no deadline, so one seen by the Verifier was set for it.
		// Waiting the real verifyTimeout out would make the suite slow; the deadline
		// being there is the contract.
		probe := &deadlineVerifier{}
		reconciler.Verifiers = verifierRegistry(registration{chartA, probe.Verify})

		start := time.Now()
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())

		deadline := probe.deadline.Load()
		Expect(deadline).NotTo(BeNil(), "the Verifier's ctx should carry a deadline")
		Expect(*deadline).To(BeTemporally("<=", start.Add(verifyTimeout).Add(time.Second)))
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

var _ = Describe("CCLClusterConfig Controller's LoadBalancers", func() {
	request := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
	}
	labelled := func(name string) map[string]string {
		return map[string]string{cclusternvidiacomv1alpha1.LabelLoadBalancer: name}
	}
	ingressLabels := labelled("ingress")
	upstreamLabels := map[string]string{
		"app.kubernetes.io/name":      "ingress-nginx",
		"app.kubernetes.io/component": "controller",
	}

	var (
		reconciler *CCLClusterConfigReconciler
		verifier   *switchVerifier
	)

	status := func() cclusternvidiacomv1alpha1.CCLClusterConfigStatus {
		var config cclusternvidiacomv1alpha1.CCLClusterConfig
		Expect(k8sClient.Get(ctx, request.NamespacedName, &config)).To(Succeed())
		return config.Status
	}

	verified := func() *metav1.Condition {
		return apimeta.FindStatusCondition(status().Conditions, conditionTypeVerified)
	}

	reconcileOnce := func() {
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
	}

	// service creates a Service in the default namespace and, for a LoadBalancer, sets the
	// addresses a cloud provider would.
	service := func(name string, serviceType corev1.ServiceType, labels map[string]string,
		ingress ...corev1.LoadBalancerIngress) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
			Spec: corev1.ServiceSpec{
				Type:  serviceType,
				Ports: []corev1.ServicePort{{Name: "http", Port: 80}},
			},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, svc))).To(Succeed()) })
		if len(ingress) > 0 {
			svc.Status.LoadBalancer.Ingress = ingress
			Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())
		}
		return svc
	}

	BeforeEach(func() {
		verifier = &switchVerifier{}
		verifier.ready.Store(true)
		reconciler = &CCLClusterConfigReconciler{
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			Verifiers: verifierRegistry(registration{"catalogs/cluster_base/a", verifier.Verify}),
		}
		config := &cclusternvidiacomv1alpha1.CCLClusterConfig{
			ObjectMeta: metav1.ObjectMeta{Name: cclusternvidiacomv1alpha1.CCLClusterConfigName},
			Spec:       cclusternvidiacomv1alpha1.CCLClusterConfigSpec{Domain: "gpu-a.infra.example.com"},
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, config))).To(Succeed()) })
	})

	It("reports nothing when no Service of type LoadBalancer carries the label", func() {
		service("labelled-cluster-ip", corev1.ServiceTypeClusterIP, ingressLabels)
		service("upstream-only", corev1.ServiceTypeLoadBalancer, upstreamLabels,
			corev1.LoadBalancerIngress{IP: "192.0.2.1"})
		reconcileOnce()

		Expect(status().LoadBalancers).To(BeEmpty())
		Expect(verified().Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports a LoadBalancer without an address, then its IP once assigned", func() {
		svc := service("ingress", corev1.ServiceTypeLoadBalancer, ingressLabels)
		reconcileOnce()
		Expect(status().LoadBalancers).To(Equal([]cclusternvidiacomv1alpha1.LoadBalancerStatus{
			{Name: "ingress"},
		}))

		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}}
		Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())
		reconcileOnce()

		Expect(status().LoadBalancers).To(Equal([]cclusternvidiacomv1alpha1.LoadBalancerStatus{
			{Name: "ingress", IP: "192.0.2.10"},
		}))
	})

	It("reports the first IP and the first hostname the Service lists", func() {
		service("ingress", corev1.ServiceTypeLoadBalancer, ingressLabels,
			corev1.LoadBalancerIngress{Hostname: "lb-1.elb.example.com"},
			corev1.LoadBalancerIngress{IP: "192.0.2.11"},
			corev1.LoadBalancerIngress{IP: "192.0.2.12", Hostname: "lb-2.elb.example.com"})
		reconcileOnce()

		cfg := status()
		Expect(cfg.LoadBalancer("ingress")).To(Equal(
			&cclusternvidiacomv1alpha1.LoadBalancerStatus{
				Name:     "ingress",
				IP:       "192.0.2.11",
				Hostname: "lb-1.elb.example.com",
			}))
	})

	It("reports every labelled LoadBalancer under its label value, sorted by it", func() {
		service("ssh", corev1.ServiceTypeLoadBalancer, labelled("ssh"),
			corev1.LoadBalancerIngress{IP: "192.0.2.30"})
		service("ingress", corev1.ServiceTypeLoadBalancer, ingressLabels,
			corev1.LoadBalancerIngress{IP: "192.0.2.10"})
		reconcileOnce()

		Expect(status().LoadBalancers).To(Equal([]cclusternvidiacomv1alpha1.LoadBalancerStatus{
			{Name: "ingress", IP: "192.0.2.10"},
			{Name: "ssh", IP: "192.0.2.30"},
		}))
	})

	// The status write has to remove the last entry, not just leave it out: a stale entry
	// would keep its address published from the control plane.
	DescribeTable("reports nothing again once the last labelled LoadBalancer stops counting",
		func(stop func(*corev1.Service)) {
			svc := service("ingress", corev1.ServiceTypeLoadBalancer, ingressLabels,
				corev1.LoadBalancerIngress{IP: "192.0.2.10"})
			reconcileOnce()
			Expect(status().LoadBalancers).To(HaveLen(1))

			stop(svc)
			reconcileOnce()

			Expect(status().LoadBalancers).To(BeEmpty())
			Expect(verified().Status).To(Equal(metav1.ConditionTrue))
		},
		Entry("when its Service is deleted", func(svc *corev1.Service) {
			Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
		}),
		Entry("when its label is removed", func(svc *corev1.Service) {
			patch := client.MergeFrom(svc.DeepCopy())
			delete(svc.Labels, cclusternvidiacomv1alpha1.LabelLoadBalancer)
			Expect(k8sClient.Patch(ctx, svc, patch)).To(Succeed())
		}),
		Entry("when its type changes to ClusterIP", func(svc *corev1.Service) {
			patch := client.MergeFrom(svc.DeepCopy())
			svc.Spec.Type = corev1.ServiceTypeClusterIP
			Expect(k8sClient.Patch(ctx, svc, patch)).To(Succeed())
		}),
	)

	It("leaves out a value claimed by two Services and reports the conflict in Verified", func() {
		service("ingress-a", corev1.ServiceTypeLoadBalancer, ingressLabels,
			corev1.LoadBalancerIngress{IP: "192.0.2.20"})
		service("ssh", corev1.ServiceTypeLoadBalancer, labelled("ssh"),
			corev1.LoadBalancerIngress{IP: "192.0.2.30"})
		reconcileOnce()
		Expect(status().LoadBalancers).To(HaveLen(2))
		Expect(verified().Status).To(Equal(metav1.ConditionTrue))

		service("ingress-b", corev1.ServiceTypeLoadBalancer, ingressLabels,
			corev1.LoadBalancerIngress{IP: "192.0.2.21"})
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(recheckWhileFailing))

		Expect(status().LoadBalancers).To(Equal([]cclusternvidiacomv1alpha1.LoadBalancerStatus{
			{Name: "ssh", IP: "192.0.2.30"},
		}))
		condition := verified()
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonLoadBalancerConflict))
		Expect(condition.Message).To(Equal("All 1 cluster_base components are installed and configured; " +
			`conflicting LoadBalancers: "ingress" is on default/ingress-a, default/ingress-b`))
	})

	It("reports a conflict next to failing components", func() {
		verifier.ready.Store(false)
		service("ingress-a", corev1.ServiceTypeLoadBalancer, ingressLabels)
		service("ingress-b", corev1.ServiceTypeLoadBalancer, ingressLabels)
		reconcileOnce()

		condition := verified()
		Expect(condition.Reason).To(Equal(reasonComponentsNotVerified))
		Expect(condition.Message).To(ContainSubstring("catalogs/cluster_base/a (not ready)"))
		Expect(condition.Message).To(HaveSuffix(
			`conflicting LoadBalancers: "ingress" is on default/ingress-a, default/ingress-b`))
	})

	It("passes on events only for Services carrying the label, to the singleton", func() {
		withLabel := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: labelled("anything")}}
		upstream := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: upstreamLabels}}
		passes := loadBalancerServices()
		Expect(passes.Create(event.CreateEvent{Object: withLabel})).To(BeTrue())
		Expect(passes.Create(event.CreateEvent{Object: upstream})).To(BeFalse())
		Expect(passes.Delete(event.DeleteEvent{Object: withLabel})).To(BeTrue())
		Expect(passes.Update(event.UpdateEvent{ObjectOld: upstream, ObjectNew: upstream})).To(BeFalse())
		Expect(singletonForService(ctx, withLabel)).To(ConsistOf(request))
	})

	// Removing the label is an update whose new Service no longer carries it; dropping
	// that event would leave the entry in status until the periodic pass.
	DescribeTable("passes on an update when either side carries the label",
		func(old, updated map[string]string) {
			Expect(loadBalancerServices().Update(event.UpdateEvent{
				ObjectOld: &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: old}},
				ObjectNew: &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: updated}},
			})).To(BeTrue())
		},
		Entry("when the label is added", upstreamLabels, ingressLabels),
		Entry("when the label is removed", ingressLabels, upstreamLabels),
		Entry("when the address changes under the label", ingressLabels, ingressLabels),
	)
})

// meetingVerifier passes only once every expected call to Verify is in flight at the
// same time: each call checks in on arrived and then waits for met, which the spec
// closes when the last one has checked in.
type meetingVerifier struct {
	arrived *sync.WaitGroup
	met     <-chan struct{}
}

func (v *meetingVerifier) Verify(ctx context.Context, _ client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	v.arrived.Done()
	select {
	case <-v.met:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// panicVerifier stands in for a Verifier with a bug.
type panicVerifier struct{}

func (panicVerifier) Verify(context.Context, client.Reader, cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	panic("verifier bug")
}

// deadlineVerifier records the deadline on the ctx it is handed, if any.
type deadlineVerifier struct {
	deadline atomic.Pointer[time.Time]
}

func (v *deadlineVerifier) Verify(ctx context.Context, _ client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	if deadline, ok := ctx.Deadline(); ok {
		v.deadline.Store(&deadline)
	}
	return nil
}

// registration is one chart's Verifier, in the order a test registers it.
type registration struct {
	chart  string
	verify verifierregistry.VerifyFunc
}

// verifierRegistry registers verifiers in the order given.
func verifierRegistry(registrations ...registration) *verifierregistry.Registry {
	r := &verifierregistry.Registry{}
	for _, reg := range registrations {
		r.Register(reg.chart, reg.verify)
	}
	return r
}
