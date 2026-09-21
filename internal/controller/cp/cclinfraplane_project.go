/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
)

// errProjectConflict reports that the AppProject for a namespace is claimed by a
// different namespace. Unreachable through the operator's own naming - the project is
// named after the namespace - so it means someone re-stamped the annotation by hand.
var errProjectConflict = errors.New("AppProject belongs to another namespace")

// argoCDClusterName is the name a CCLInfraPlane's cluster is registered under in Argo CD,
// and therefore the name of its mirrored Secret and what Applications put in
// destination.name. It is prefixed with the namespace because Argo CD's cluster names
// are global, and two namespaces should each be able to have a plane called "prod".
func argoCDClusterName(namespace, name string) string {
	return namespace + "-" + name
}

// projectName is the Argo CD AppProject a ccluster namespace's clusters and Applications
// belong to. It is the namespace's own name, which puts the CCLInfraPlanes of the
// "default" namespace in Argo CD's built-in "default" project - adopted and rewritten
// like any other unclaimed object, see namespaceOwner.
func projectName(namespace string) string {
	return namespace
}

// projectSpec is the policy every namespace's AppProject carries. It confines that
// namespace's Applications to the catalog repository and to clusters registered from the
// same namespace, and confines the Applications themselves to that namespace.
func (r *CCLInfraPlaneReconciler) projectSpec(namespace string) argocdv1alpha1.AppProjectSpec {
	spec := argocdv1alpha1.AppProjectSpec{
		// The catalog, and nothing else. Whoever can create an Application in the
		// namespace can otherwise deploy an arbitrary repository onto its clusters.
		SourceRepos: []string{r.CatalogRepoURL},
		// Any cluster, any namespace on it - but with PermitOnlyProjectScopedClusters
		// "any cluster" means any cluster whose Secret names this project, which the
		// operator only ever writes for this namespace's CCLInfraPlanes.
		Destinations: []argocdv1alpha1.ApplicationDestination{
			{Server: "*", Namespace: "*"},
		},
		PermitOnlyProjectScopedClusters: true,
		// The catalog's charts install CRDs, ClusterRoles and namespaces; a project
		// with an empty whitelist rejects all of them.
		ClusterResourceWhitelist: []metav1.GroupKind{{Group: "*", Kind: "*"}},
	}

	// Argo CD forbids listing its own namespace here - it is the privileged one - and
	// an Application there may reference any project regardless. Everything else needs
	// naming explicitly before Argo CD will accept an Application from it.
	if namespace != r.ArgoCDNamespace {
		spec.SourceNamespaces = []string{namespace}
	}
	return spec
}

// createOrPatchProject writes the AppProject for infraPlane's namespace.
//
// Retried on a stale read like createOrPatchApplication. An AppProject has no status
// subresource either, and Argo CD keeps JWT token records in its status.
func (r *CCLInfraPlaneReconciler) createOrPatchProject(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) (controllerutil.OperationResult, error) {
	result := controllerutil.OperationResultNone

	err := retry.OnError(retry.DefaultBackoff, staleRead, func() error {
		project := &argocdv1alpha1.AppProject{
			ObjectMeta: metav1.ObjectMeta{
				Name:      projectName(infraPlane.Namespace),
				Namespace: r.ArgoCDNamespace,
			},
		}

		if err := r.Get(ctx, client.ObjectKeyFromObject(project), project); client.IgnoreNotFound(err) != nil {
			return err
		}
		before := project.DeepCopy()
		if !projectOwner.ownedBy(project, infraPlane.Namespace) {
			return errProjectConflict
		}
		projectOwner.stamp(project, infraPlane.Namespace)
		project.Spec = r.projectSpec(infraPlane.Namespace)

		var err error
		result, err = createOrPatchArgoCDObject(ctx, r.Client, before, project, project.Spec)
		return err
	})
	return result, err
}

// deleteProjectIfUnused deletes the namespace's AppProject when infraPlane is the last
// CCLInfraPlane in its namespace that is not itself being deleted.
//
// Terminating planes are not counted. If they were, two planes deleted together would
// each see the other and neither would delete the project; counting only live ones means
// both try, and the second delete finds nothing, which is fine.
func (r *CCLInfraPlaneReconciler) deleteProjectIfUnused(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	log := logf.FromContext(ctx)
	name := projectName(infraPlane.Namespace)

	var list cclusternvidiacomv1alpha1.CCLInfraPlaneList
	if err := r.List(ctx, &list, client.InNamespace(infraPlane.Namespace)); err != nil {
		return err
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.Name != infraPlane.Name && other.DeletionTimestamp.IsZero() {
			log.V(1).Info("Kept Argo CD AppProject, other CCLInfraPlanes remain",
				"project", name, "projectNamespace", r.ArgoCDNamespace)
			return nil
		}
	}

	var project argocdv1alpha1.AppProject
	key := client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: name}
	switch err := r.Get(ctx, key, &project); {
	case apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err):
		// Already gone, or never creatable; nothing to clean up.
		return nil
	case err != nil:
		return err
	case !projectOwner.ownedBy(&project, infraPlane.Namespace):
		log.Info("Left AppProject claimed by another namespace in place",
			"project", name, "projectNamespace", r.ArgoCDNamespace)
		return nil
	}

	if err := r.Delete(ctx, &project); client.IgnoreNotFound(err) != nil {
		return err
	}
	log.Info("Deleted Argo CD AppProject", "project", name, "projectNamespace", r.ArgoCDNamespace)
	return nil
}

// infraPlanesForProject maps an AppProject to every CCLInfraPlane in the namespace it is
// named after, so that drift on, or deletion of, a project is repaired by the planes that
// need it. It goes by name rather than by annotation so that a project whose annotation
// was stripped is still repaired rather than orphaned.
func (r *CCLInfraPlaneReconciler) infraPlanesForProject(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() != r.ArgoCDNamespace {
		return nil
	}

	var list cclusternvidiacomv1alpha1.CCLInfraPlaneList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetName())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CCLInfraPlanes for AppProject",
			"name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&list.Items[i]),
		})
	}
	return requests
}
