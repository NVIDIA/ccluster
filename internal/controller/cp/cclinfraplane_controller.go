/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	externaldnsv1alpha1 "github.com/NVIDIA/ccluster/internal/externaldns/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/remote"
)

// Metadata written onto the mirrored Secret in the Argo CD namespace.
const (
	// argoCDSecretTypeLabel is how Argo CD discovers the Secrets it should treat as
	// cluster registrations.
	argoCDSecretTypeLabel   = "argocd.argoproj.io/secret-type"
	argoCDSecretTypeCluster = "cluster"

	// argoCDClusterNameKey is the data key Argo CD shows as the cluster's name. The
	// operator sets it from the CCLInfraPlane, so one cluster cannot be registered
	// under two different names depending on who wrote the credentials Secret.
	argoCDClusterNameKey = "name"

	// argoCDClusterProjectKey is the data key that makes a cluster project-scoped: only
	// Applications in the named AppProject may deploy to it. The operator sets it to the
	// CCLInfraPlane's namespace, which is that namespace's AppProject.
	argoCDClusterProjectKey = "project"

	// Back-references to the CCLInfraPlane a mirrored Secret was created for. An
	// owner reference cannot be used: it may not cross namespaces, and the garbage
	// collector deletes objects whose owner it cannot find in their own namespace.
	// These annotations take its place, so the finalizer never deletes, and reconcile
	// never overwrites, a Secret belonging to a different CCLInfraPlane.
	annotationInfraPlaneName      = "ccluster.nvidia.com/infraplane-name"
	annotationInfraPlaneNamespace = "ccluster.nvidia.com/infraplane-namespace"

	// finalizerName guards the mirrored Secret, which nothing else would clean up.
	finalizerName = "ccluster.nvidia.com/argocd-cluster-secret"
)

// errClusterSecretConflict reports that the mirrored Secret name is already taken by
// another CCLInfraPlane. The mirror is named <namespace>-<name>, which is not
// collision-free: namespace "a-b" with plane "c" and namespace "a" with plane "b-c" both
// map onto "a-b-c" in the Argo CD namespace, so this is reachable.
var errClusterSecretConflict = errors.New("cluster Secret belongs to another CCLInfraPlane")

// CCLInfraPlaneReconciler reconciles a CCLInfraPlane object
type CCLInfraPlaneReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ArgoCDNamespace is the namespace Argo CD runs in, and therefore the namespace
	// the mirrored cluster Secrets and the AppProjects are written to.
	ArgoCDNamespace string

	// DNSEndpointNamespace is the namespace the DNSEndpoint of each CCLInfraPlane is
	// written to, from which external-dns publishes "*.<name>.<domain>" pointing at each
	// of the plane's labelled LoadBalancers: by default the operator's own, where the cp
	// chart installs external-dns. CCLInfraPlanes live elsewhere, so this namespace keeps
	// DNSEndpoints away from tenants. Whoever can write a CCLInfraPlane is still trusted
	// with its spec.domain, which names the records published for it.
	DNSEndpointNamespace string

	// CatalogRepoURL is the Git repository the chart catalog lives in. It is the one
	// source repository each AppProject permits, so that nothing an Application in a
	// ccluster namespace deploys can come from anywhere but the catalog.
	CatalogRepoURL string

	// RemoteClients builds clients for infra-plane clusters from their credentials
	// Secrets, which the operator uses to write each cluster's CCLClusterConfig.
	RemoteClients remote.ClientFactory

	// RemoteWatches watches each infra-plane cluster's CCLClusterConfig, so a change
	// there - the infra-plane operator writing status, or drift - reconciles the
	// CCLInfraPlane at once.
	RemoteWatches remote.Watcher

	// ResyncPeriod is how often a CCLInfraPlane whose CCLClusterConfig was written is
	// reconciled again anyway, a safety net behind RemoteWatches. Zero disables it.
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclinfraplanes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=appprojects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;list;watch;create;update;patch;delete

// The operator writes CCLClusterConfig with its own ServiceAccount only for a CCLInfraPlane
// that registers the operator's own cluster; a remote cluster is written to with the
// credentials Secret's token.
// +kubebuilder:rbac:groups=ccluster.nvidia.com,resources=cclclusterconfigs,verbs=get;list;watch;create;update;patch;delete

// Reconcile registers the infra-plane cluster with Argo CD, and writes its CCLClusterConfig.
//
// Two objects are written into the Argo CD namespace. First the AppProject for the
// CCLInfraPlane's namespace, which every CCLInfraPlane and CCLComponentSet in that
// namespace shares; it exists for as long as any CCLInfraPlane in the namespace does.
// Then the Secret named by clusterCredentialsRef is cloned, under the name
// <namespace>-<name>, labelled as an Argo CD cluster Secret and scoped to that project.
// Its data is otherwise copied verbatim - what it has to contain is Argo CD's business,
// not this operator's. Both are reported as the Available condition, in that order: when
// Available is true the project exists, which is what lets the CCLComponentSet controller
// write Applications into it without checking.
//
// Once the cluster is registered, the operator connects to it with the same credentials,
// watches the CCLClusterConfig singleton the infra-plane operator verifies the cluster
// against, and writes it with spec.domain. That is reported as the ClusterConfigApplied
// condition, and the singleton's status is copied into status.clusterConfig.
//
// The operator then writes a DNSEndpoint pointing "*.<name>.<domain>" at each labelled
// LoadBalancer in that copy, for external-dns to publish. That is reported as the
// DNSReady condition. A pass that gets this far writes every condition, and the copy
// when it took one, in one status patch. A plane that is not Available stops before
// the CCLClusterConfig and the DNSEndpoint: ClusterConfigApplied and DNSReady turn
// Unknown and the copy and the records stay as they were. An error that is retried
// writes no status.
//
// Deciding what runs on the registered cluster is CCLComponentSet's job. A CCLInfraPlane
// makes a cluster addressable by name; it does not put anything on it.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *CCLInfraPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var infraPlane cclusternvidiacomv1alpha1.CCLInfraPlane
	if err := r.Get(ctx, req.NamespacedName, &infraPlane); err != nil {
		if apierrors.IsNotFound(err) {
			// The controller's logger already carries the request's name and namespace.
			// Normally the finalizer has stopped the watch; this covers one stripped by
			// hand.
			log.Info("Ignored missing CCLInfraPlane")
			r.RemoteWatches.Stop(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !infraPlane.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &infraPlane)
	}

	base := infraPlane.DeepCopy()
	if controllerutil.AddFinalizer(&infraPlane, finalizerName) {
		// Return after the write: it bumps the resource version, and the next reconcile
		// works from a settled object.
		return ctrl.Result{}, client.IgnoreNotFound(
			r.Patch(ctx, &infraPlane, client.MergeFrom(base)))
	}

	op, err := r.createOrPatchProject(ctx, &infraPlane)
	switch {
	case apimeta.IsNoMatchError(err):
		// Argo CD is not installed - `make deploy-cp ARGOCD=false`, or the operator
		// started first. Not reported on the status: the Application CRD is missing in
		// the same situations, and the CCLComponentSet controller reports that. On the
		// restart that follows installing Argo CD, every CCLInfraPlane is reconciled
		// again and the project gets created then.
		log.Info("Argo CD AppProject CRD not installed, skipped AppProject",
			"project", projectName(infraPlane.Namespace))
	case errors.Is(err, errProjectConflict):
		// No requeue: only an edit elsewhere can resolve this, and the AppProject
		// watch picks that up.
		log.Info("Refused to overwrite AppProject claimed by another namespace",
			"project", projectName(infraPlane.Namespace), "projectNamespace", r.ArgoCDNamespace)
		return ctrl.Result{}, r.setUnavailable(ctx, &infraPlane,
			cclusternvidiacomv1alpha1.CCLInfraPlaneReasonProjectConflict, fmt.Sprintf(
				"AppProject %q in namespace %q belongs to another namespace",
				projectName(infraPlane.Namespace), r.ArgoCDNamespace))
	case err != nil:
		return ctrl.Result{}, err
	case op != controllerutil.OperationResultNone:
		log.Info("Applied Argo CD AppProject", "project", projectName(infraPlane.Namespace),
			"projectNamespace", r.ArgoCDNamespace, "operation", op)
	}

	secretName := infraPlane.Spec.ClusterCredentialsRef.Name
	mirrorName := argoCDClusterName(infraPlane.Namespace, infraPlane.Name)

	var source corev1.Secret
	key := client.ObjectKey{Namespace: infraPlane.Namespace, Name: secretName}
	if err := r.Get(ctx, key, &source); err != nil {
		if apierrors.IsNotFound(err) {
			// No requeue: the Secret watch re-triggers reconciliation once it appears.
			// Without credentials there is no cluster to watch.
			log.Info("Missing credentials Secret", "secret", secretName)
			r.RemoteWatches.Stop(req.NamespacedName)
			return ctrl.Result{}, r.setUnavailable(ctx, &infraPlane,
				cclusternvidiacomv1alpha1.CCLInfraPlaneReasonSecretNotFound, fmt.Sprintf("Secret %q not found", secretName))
		}
		return ctrl.Result{}, err
	}

	op, err = r.mirrorClusterSecret(ctx, &infraPlane, &source)
	if err != nil {
		if errors.Is(err, errClusterSecretConflict) {
			// No requeue: only an edit elsewhere can resolve a name collision, and the
			// Secret watch picks that up.
			log.Info("Refused to overwrite cluster Secret owned by another CCLInfraPlane",
				"secret", mirrorName, "secretNamespace", r.ArgoCDNamespace)
			return ctrl.Result{}, r.setUnavailable(ctx, &infraPlane,
				cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterSecretConflict, fmt.Sprintf(
					"Secret %q in namespace %q belongs to another CCLInfraPlane",
					mirrorName, r.ArgoCDNamespace))
		}
		return ctrl.Result{}, err
	}

	// Names and namespaces only: the credentials themselves must never reach the logs.
	if op != controllerutil.OperationResultNone {
		log.Info("Mirrored Argo CD cluster Secret", "secret", mirrorName,
			"secretNamespace", r.ArgoCDNamespace, "operation", op)
	}

	available := metav1.Condition{
		Type:   cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
		Status: metav1.ConditionTrue,
		Reason: cclusternvidiacomv1alpha1.CCLInfraPlaneReasonClusterSecretCreated,
		Message: fmt.Sprintf("Mirrored Secret %q into namespace %q",
			mirrorName, r.ArgoCDNamespace),
	}
	clusterConfigApplied, clusterConfigStatus, result := r.reconcileClusterConfig(ctx, &infraPlane, &source)

	// The address comes from the copy just taken, or else the last one: an unreachable
	// cluster does not withdraw its record.
	lastClusterConfig := clusterConfigStatus
	if lastClusterConfig == nil {
		lastClusterConfig = infraPlane.Status.ClusterConfig
	}
	// A DNS error is written into DNSReady with everything else before it is returned.
	dnsReady, dnsErr := r.reconcileDNS(ctx, &infraPlane, lastClusterConfig)
	err = r.patchStatus(ctx, &infraPlane, clusterConfigStatus,
		available, clusterConfigApplied, dnsReady)
	if dnsErr != nil {
		return ctrl.Result{}, errors.Join(dnsErr, err)
	}
	return result, err
}

// mirrorClusterSecret creates or updates the Argo CD cluster Secret for infraPlane from
// source. Only source's data and type are copied; its labels and annotations are not.
//
// Two keys are the operator's, whatever the source Secret says. "name", which Argo CD
// displays as the cluster name and Applications address it by, is always
// <namespace>-<name> - the same string the mirror itself is named. "project" scopes the
// cluster to the namespace's AppProject, so only that namespace's Applications can deploy
// to it. Everything else is cloned verbatim.
func (r *CCLInfraPlaneReconciler) mirrorClusterSecret(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	source *corev1.Secret,
) (controllerutil.OperationResult, error) {
	clusterName := argoCDClusterName(infraPlane.Namespace, infraPlane.Name)
	mirror := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: r.ArgoCDNamespace,
		},
	}

	return controllerutil.CreateOrUpdate(ctx, r.Client, mirror, func() error {
		if !infraPlaneOwner.ownedBy(mirror, infraPlane) {
			return errClusterSecretConflict
		}
		infraPlaneOwner.stamp(mirror, infraPlane)

		// The one label stamp does not set: it is what makes Argo CD treat this
		// Secret as a cluster registration rather than an ordinary Secret.
		mirror.Labels[argoCDSecretTypeLabel] = argoCDSecretTypeCluster

		mirror.Type = source.Type
		mirror.Data = make(map[string][]byte, len(source.Data)+2)
		for k, v := range source.Data {
			mirror.Data[k] = append([]byte(nil), v...)
		}
		// Written last so they win over anything the source Secret carries.
		mirror.Data[argoCDClusterNameKey] = []byte(clusterName)
		mirror.Data[argoCDClusterProjectKey] = []byte(projectName(infraPlane.Namespace))
		return nil
	})
}

// finalize deletes the plane's DNSEndpoint, the infra-plane
// cluster's CCLClusterConfig if this CCLInfraPlane created it, the mirrored Secret if it is
// still ours, and the namespace's AppProject if this was the last CCLInfraPlane using it,
// then drops the finalizer so the CCLInfraPlane can go away.
//
// The watch on the cluster stops first, so deleting the CCLClusterConfig does not
// reconcile a CCLInfraPlane that is going away. The DNSEndpoint goes next, then the
// CCLClusterConfig, while the
// credentials to reach the cluster are still registered; failing to reach the cluster
// does not block the rest.
func (r *CCLInfraPlaneReconciler) finalize(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(infraPlane, finalizerName) {
		return nil
	}

	r.RemoteWatches.Stop(client.ObjectKeyFromObject(infraPlane))
	if err := r.deleteDNSEndpoint(ctx, infraPlane); err != nil {
		return err
	}
	r.deleteClusterConfig(ctx, infraPlane)

	var mirror corev1.Secret
	mirrorName := argoCDClusterName(infraPlane.Namespace, infraPlane.Name)
	key := client.ObjectKey{Namespace: r.ArgoCDNamespace, Name: mirrorName}
	switch err := r.Get(ctx, key, &mirror); {
	case apierrors.IsNotFound(err):
		// Already gone; nothing to clean up.
	case err != nil:
		return err
	case !infraPlaneOwner.ownedBy(&mirror, infraPlane):
		log.Info("Left cluster Secret owned by another CCLInfraPlane in place",
			"secret", mirrorName, "secretNamespace", r.ArgoCDNamespace)
	default:
		if err := r.Delete(ctx, &mirror); client.IgnoreNotFound(err) != nil {
			return err
		}
		log.Info("Deleted Argo CD cluster Secret", "secret", mirrorName,
			"secretNamespace", r.ArgoCDNamespace)
	}

	if err := r.deleteProjectIfUnused(ctx, infraPlane); err != nil {
		return err
	}

	// Patched rather than updated, and NotFound ignored, for the reasons in patchStatus:
	// the deletion that brought us here may not have reached the cache yet.
	base := infraPlane.DeepCopy()
	controllerutil.RemoveFinalizer(infraPlane, finalizerName)
	return client.IgnoreNotFound(r.Patch(ctx, infraPlane, client.MergeFrom(base)))
}

// dnsNotUpdatedMessage starts the DNSReady message while Available is False, ahead of
// the message the condition had before.
const dnsNotUpdatedMessage = "the DNSEndpoint is kept but not updated while Available is " +
	"False; see Available. Last reported: "

// setUnavailable sets the Available condition, which reports on the mirrored cluster
// Secret, to False. ClusterConfigApplied becomes Unknown: the CCLClusterConfig is not
// written while the plane is unavailable, so whatever the condition last said is no
// longer being kept true. DNSReady, once set, becomes Unknown for the same reason: the
// DNSEndpoint is neither updated nor withdrawn, so its records stay published as they
// were. It keeps its reason and message, after a note that says so.
func (r *CCLInfraPlaneReconciler) setUnavailable(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	reason, message string,
) error {
	conditions := []metav1.Condition{{
		Type:    cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}, {
		Type:    cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied,
		Status:  metav1.ConditionUnknown,
		Reason:  cclusternvidiacomv1alpha1.CCLInfraPlaneReasonNotAvailable,
		Message: "the CCLClusterConfig is not checked while Available is False; see Available",
	}}
	if dnsReady := apimeta.FindStatusCondition(infraPlane.Status.Conditions, cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady); dnsReady != nil {
		dnsMessage := dnsReady.Message
		if !strings.HasPrefix(dnsMessage, dnsNotUpdatedMessage) {
			dnsMessage = dnsNotUpdatedMessage + dnsMessage
		}
		conditions = append(conditions, metav1.Condition{
			Type:    cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady,
			Status:  metav1.ConditionUnknown,
			Reason:  dnsReady.Reason,
			Message: dnsMessage,
		})
	}
	return r.patchStatus(ctx, infraPlane, nil, conditions...)
}

// patchStatus sets conditions, each stamped with the current generation, the observed
// generation and, unless it is nil, the copy of the infra-plane cluster's
// CCLClusterConfig status, then writes the status subresource in one patch.
//
// This is a merge patch, not an update, because r.Get reads through the informer cache:
// on a CCLInfraPlane that was just written the cached copy can carry a stale resource
// version, and an update would fail the optimistic-concurrency check even though the
// status it computed is correct. A patch carries no such precondition. A NotFound is
// ignored - the object was deleted while we were reconciling it, and there is no status
// left to record.
func (r *CCLInfraPlaneReconciler) patchStatus(
	ctx context.Context,
	infraPlane *cclusternvidiacomv1alpha1.CCLInfraPlane,
	clusterConfig *cclusternvidiacomv1alpha1.CCLClusterConfigStatus,
	conditions ...metav1.Condition,
) error {
	base := infraPlane.DeepCopy()

	if clusterConfig != nil {
		infraPlane.Status.ClusterConfig = clusterConfig
	}
	for _, condition := range conditions {
		condition.ObservedGeneration = infraPlane.Generation
		apimeta.SetStatusCondition(&infraPlane.Status.Conditions, condition)
	}
	infraPlane.Status.ObservedGeneration = infraPlane.Generation

	return client.IgnoreNotFound(r.Status().Patch(ctx, infraPlane, client.MergeFrom(base)))
}

// infraPlanesForSecret maps a Secret to the CCLInfraPlanes that care about it: either a
// mirrored cluster Secret, which names its CCLInfraPlane outright, or a source Secret,
// which is found by searching its own namespace for referrers.
func (r *CCLInfraPlaneReconciler) infraPlanesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() == r.ArgoCDNamespace {
		name, hasName := obj.GetAnnotations()[annotationInfraPlaneName]
		namespace, hasNamespace := obj.GetAnnotations()[annotationInfraPlaneNamespace]
		if hasName && hasNamespace {
			// Drift on, or deletion of, a mirrored Secret is repaired by reconciling
			// the CCLInfraPlane it was created for.
			return []reconcile.Request{{
				NamespacedName: client.ObjectKey{Namespace: namespace, Name: name},
			}}
		}
	}

	var list cclusternvidiacomv1alpha1.CCLInfraPlaneList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list CCLInfraPlanes for Secret",
			"name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for _, infraPlane := range list.Items {
		if infraPlane.Spec.ClusterCredentialsRef.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&infraPlane),
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
//
// The Secret watch is unfiltered, so the manager caches every Secret in the cluster. If
// that becomes costly, scope it with cache.ByObject in internal/manager.
//
// Events from the infra-plane clusters arrive through RemoteWatches' source, one for every
// cluster: watches on them come and go with the CCLInfraPlanes, which a controller's own
// watches cannot.
//
// The AppProject watch is registered only when Argo CD's CRD is already installed, for
// the reason given on CCLComponentSetReconciler.SetupWithManager: a watch on a kind the API
// server does not serve never syncs, and the controller would never start. The DNSEndpoint
// watch likewise needs external-dns's CRD.
func (r *CCLInfraPlaneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&cclusternvidiacomv1alpha1.CCLInfraPlane{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.infraPlanesForSecret)).
		WatchesRawSource(r.RemoteWatches.Source())

	if argoCDKindInstalled(mgr.GetRESTMapper(), mgr.GetLogger().WithName("cclinfraplane"), "AppProject") {
		builder = builder.Watches(&argocdv1alpha1.AppProject{},
			handler.EnqueueRequestsFromMapFunc(r.infraPlanesForProject))
	}
	if kindInstalled(mgr.GetRESTMapper(), mgr.GetLogger().WithName("cclinfraplane"),
		externaldnsv1alpha1.GroupVersion.WithKind("DNSEndpoint")) {
		builder = builder.Watches(&externaldnsv1alpha1.DNSEndpoint{},
			handler.EnqueueRequestsFromMapFunc(r.infraPlanesForDNSEndpoint))
	}

	return builder.Named("cclinfraplane").Complete(r)
}
