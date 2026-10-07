/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs/components"
)

// Condition types and reasons reported on CCLClusterConfig.status.
const (
	// conditionTypeVerified reports whether every cluster_base component passed its
	// Verifier against the spec.
	conditionTypeVerified = "Verified"

	reasonComponentsVerified    = "ComponentsVerified"
	reasonComponentsNotVerified = "ComponentsNotVerified"
)

// Re-verification intervals. Verifiers read state this operator does not write, so
// there is no event of its own to wait for; a periodic pass is what notices a component
// arriving or breaking. It is the one place in ccluster that polls.
const (
	// recheckWhileFailing is the interval while any component fails: the cluster is
	// being set up and the operator is what tells the user how far along it is.
	recheckWhileFailing = time.Minute
	// recheckWhenVerified is the interval once everything passes.
	recheckWhenVerified = 5 * time.Minute
)

// CCLClusterConfigReconciler reconciles a CCLClusterConfig object
type CCLClusterConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Components is the catalog registry the Verifiers are taken from, normally
	// components.ClusterBase. Entries without a Verifier are skipped. Injectable so tests can
	// drive the controller with a registry of their own.
	Components []components.Component
}

// RBAC is what the controller itself needs. A Verifier that reads a kind adds a marker
// for it here; controller-gen only reads markers in this package. Everything stays
// read-only: the operator writes nothing but CCLClusterConfig status.
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclclusterconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclclusterconfigs/status,verbs=get;update;patch

// Reconcile runs every cluster_base component's Verifier against the CCLClusterConfig and
// records the outcome as its Verified condition.
//
// The operator reads the cluster and writes status; it changes nothing else. Whether
// the components were installed by the control plane through a CCLComponentSet or by a
// user with helm is invisible to it - the Verifiers look at live objects, never at Argo
// CD or Helm release state - so the same check holds in both cases. A failing Verifier
// is not an error: it is the answer, and it is reported rather than retried.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *CCLClusterConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var config cclusternvidiacomv1alpha1.CCLClusterConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if apierrors.IsNotFound(err) {
			// Nothing to verify against; the operator is idle until the singleton exists.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	total, failing := r.verify(ctx, config.Spec)

	status, reason, message := verifiedCondition(total, failing)
	previous := apimeta.FindStatusCondition(config.Status.Conditions, conditionTypeVerified)
	if previous == nil || previous.Status != status || previous.Message != message {
		log.Info("Verified cluster_base components", "verified", total-len(failing),
			"total", total, "failing", failing)
	}

	if err := r.setVerified(ctx, &config, status, reason, message); err != nil {
		return ctrl.Result{}, err
	}

	if len(failing) > 0 {
		return ctrl.Result{RequeueAfter: recheckWhileFailing}, nil
	}
	return ctrl.Result{RequeueAfter: recheckWhenVerified}, nil
}

// verify runs every registered Verifier against spec and returns how many there were
// and the charts whose Verifier reported false, in catalog order.
//
// The Verifiers run concurrently. Each is an independent set of reads through the
// manager's cached client, which is safe for concurrent use, so the pass takes as long
// as the slowest check rather than the sum of all of them. Results are collected by
// position so the failing list, and with it the condition message, is deterministic
// whatever order the goroutines finish in.
func (r *CCLClusterConfigReconciler) verify(
	ctx context.Context,
	spec cclusternvidiacomv1alpha1.CCLClusterConfigSpec,
) (total int, failing []string) {
	verified := make([]bool, len(r.Components))
	var wg sync.WaitGroup
	for i, component := range r.Components {
		if component.Verifier == nil {
			continue
		}
		total++
		wg.Go(func() {
			verified[i] = component.Verifier.Verify(ctx, r, spec)
		})
	}
	wg.Wait()

	for i, component := range r.Components {
		if component.Verifier != nil && !verified[i] {
			failing = append(failing, component.Chart)
		}
	}
	return total, failing
}

// verifiedCondition turns the verification outcome into the Verified condition.
func verifiedCondition(total int, failing []string) (metav1.ConditionStatus, string, string) {
	if len(failing) == 0 {
		return metav1.ConditionTrue, reasonComponentsVerified, fmt.Sprintf(
			"All %d cluster_base components are installed and configured", total)
	}
	return metav1.ConditionFalse, reasonComponentsNotVerified, fmt.Sprintf(
		"%d of %d cluster_base components verified; failing: %s",
		total-len(failing), total, strings.Join(failing, ", "))
}

// setVerified sets the Verified condition and the observed generation, then writes the
// status subresource.
//
// A plain merge patch, without an optimistic lock: this controller is the only writer of
// this status and nothing acts on Verified, so a racing write has nothing to clobber, and
// a result computed for an older spec carries that spec's observedGeneration, so it reads
// as stale until the next pass replaces it. NotFound is ignored: the object was deleted
// while it was being verified.
func (r *CCLClusterConfigReconciler) setVerified(
	ctx context.Context,
	config *cclusternvidiacomv1alpha1.CCLClusterConfig,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	base := config.DeepCopy()

	apimeta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               conditionTypeVerified,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: config.Generation,
	})
	config.Status.ObservedGeneration = config.Generation

	return client.IgnoreNotFound(r.Status().Patch(ctx, config, client.MergeFrom(base)))
}

// SetupWithManager sets up the controller with the Manager.
//
// Only the CCLClusterConfig is watched. Verifiers read state the operator does not
// write, and the kinds they will read are installed by the very components being
// verified - a watch on a kind the API server does not serve keeps a controller from
// ever starting - so re-verification is periodic (Reconcile's RequeueAfter) rather than
// event-driven.
func (r *CCLClusterConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLClusterConfig{}).
		Named("cclclusterconfig").
		Complete(r)
}
