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
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

// errApplicationSetConflict reports that the ApplicationSet name is already taken by
// another CCLInfraPlane, for the same reason errClusterSecretConflict exists.
var errApplicationSetConflict = errors.New("ApplicationSet belongs to another CCLInfraPlane")

// The one component every registered infra-plane cluster gets. The name and namespace
// match catalog/cluster_base/__init__.py's ArgoRollouts entry, so the two catalogs
// describe the same deployment.
const (
	// argoRolloutsName ends up in the generated Application's name, so it has to be
	// DNS-1123.
	argoRolloutsName = "argo-rollouts"
	// argoRolloutsChartPath is the chart's directory, relative to the repository root.
	argoRolloutsChartPath = "catalogs/cluster_base/argo_rollouts"
	// argoRolloutsNamespace is where the chart is installed on the target cluster.
	argoRolloutsNamespace = "argo-rollouts"
)

// reconcileApplicationSet creates or updates the ApplicationSet that installs
// argo-rollouts onto the cluster infraPlane registered.
//
// It must run after the cluster Secret has been mirrored: the ApplicationSet addresses its
// destination by cluster name, which is what that Secret's "name" key defines.
func (r *CCLInfraPlaneReconciler) reconcileApplicationSet(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) (controllerutil.OperationResult, error) {
	appSet := &argocdv1alpha1.ApplicationSet{
		ObjectMeta: metav1.ObjectMeta{
			// Named after the CCLInfraPlane, like the mirrored cluster Secret. The
			// two do not collide: they are different kinds.
			Name:      infraPlane.Name,
			Namespace: r.ArgoCDNamespace,
		},
	}

	return controllerutil.CreateOrUpdate(ctx, r.Client, appSet, func() error {
		if !infraPlaneOwner.ownedBy(appSet, infraPlane) {
			return errApplicationSetConflict
		}
		infraPlaneOwner.stamp(appSet, infraPlane)

		// The spec is owned outright: it is rebuilt from scratch on every reconcile, so
		// edits made by hand are reverted.
		appSet.Spec = r.applicationSetSpec(infraPlane)
		return nil
	})
}

// applicationSetSpec builds the ApplicationSet's spec: a single-element list generator
// feeding an Application template that installs the argo-rollouts chart.
//
// A generator with one literal element is not much of a generator. It is here because
// there is exactly one component today; a second one becomes a second element, which is
// why the whole thing is an ApplicationSet rather than a bare Application.
func (r *CCLInfraPlaneReconciler) applicationSetSpec(
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) argocdv1alpha1.ApplicationSetSpec {
	return argocdv1alpha1.ApplicationSetSpec{
		GoTemplate: true,
		Generators: []argocdv1alpha1.ApplicationSetGenerator{{
			List: &argocdv1alpha1.ListGenerator{
				Elements: []argocdv1alpha1.GeneratorElement{{Name: argoRolloutsName}},
			},
		}},
		Template: argocdv1alpha1.ApplicationSetTemplate{
			ApplicationSetTemplateMeta: argocdv1alpha1.ApplicationSetTemplateMeta{
				// The only placeholder: one Application per generated element, so its
				// name has to vary with the element.
				Name: infraPlane.Name + "-{{.name}}",
			},
			Spec: argocdv1alpha1.ApplicationSpec{
				Project: "default",
				Source: argocdv1alpha1.ApplicationSource{
					RepoURL:        r.CatalogRepoURL,
					TargetRevision: r.CatalogRevision,
					Path:           argoRolloutsChartPath,
				},
				Destination: argocdv1alpha1.ApplicationDestination{
					// By name, not by server: this is the name the mirrored cluster
					// Secret carries, which the operator forces to the CCLInfraPlane's.
					Name:      infraPlane.Name,
					Namespace: argoRolloutsNamespace,
				},
				SyncPolicy: &argocdv1alpha1.SyncPolicy{
					Automated: &argocdv1alpha1.SyncPolicyAutomated{
						Prune:    true,
						SelfHeal: true,
					},
					SyncOptions: []string{"CreateNamespace=true"},
				},
			},
		},
	}
}

// deleteApplicationSet removes the ApplicationSet created for infraPlane, unless it belongs
// to a different one. Deleting it takes the Applications it generated with it, and with
// them argo-rollouts on the infra-plane cluster.
func (r *CCLInfraPlaneReconciler) deleteApplicationSet(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	log := logf.FromContext(ctx)

	appSet := &argocdv1alpha1.ApplicationSet{}
	key := client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: infraPlane.Name}
	switch err := r.Get(ctx, key, appSet); {
	case apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err):
		// Already gone, or Argo CD was never installed. Nothing to clean up.
		return nil
	case err != nil:
		return err
	case !infraPlaneOwner.ownedBy(appSet, infraPlane):
		log.Info("Left ApplicationSet owned by another CCLInfraPlane in place",
			"applicationSet", infraPlane.Name, "namespace", r.ArgoCDNamespace)
		return nil
	}

	if err := r.Delete(ctx, appSet); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.Info("Deleted Argo CD ApplicationSet", "applicationSet", infraPlane.Name,
		"namespace", r.ArgoCDNamespace)
	return nil
}

// applicationSetCondition turns the outcome of reconcileApplicationSet into the
// ComponentsApplied condition. A missing CRD and a name collision are both reported rather
// than returned: neither is fixed by retrying, and both are resolved by an event the
// controller already watches for.
func (r *CCLInfraPlaneReconciler) applicationSetCondition(
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	err error,
) (status metav1.ConditionStatus, reason, message string, retryErr error) {
	name := infraPlane.Name

	switch {
	case err == nil:
		return metav1.ConditionTrue, reasonApplicationSetCreated, fmt.Sprintf(
			"Applied ApplicationSet %q in namespace %q", name, r.ArgoCDNamespace), nil
	case apimeta.IsNoMatchError(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first. Not an error worth crash-looping over.
		return metav1.ConditionFalse, reasonApplicationSetCRDMissing, fmt.Sprintf(
			"Argo CD ApplicationSet CRD not installed: %v", err), nil
	case errors.Is(err, errApplicationSetConflict):
		return metav1.ConditionFalse, reasonApplicationSetConflict, fmt.Sprintf(
			"ApplicationSet %q in namespace %q belongs to another CCLInfraPlane",
			name, r.ArgoCDNamespace), nil
	default:
		return metav1.ConditionFalse, reasonApplicationSetFailed, err.Error(), err
	}
}
