/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// ccluster-cp-operator runs in the control-plane cluster.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/argocd/apiclient"
	argocdv1alpha1 "github.com/NVIDIA/ccluster/internal/argocd/v1alpha1"
	"github.com/NVIDIA/ccluster/internal/controller/cp"
	"github.com/NVIDIA/ccluster/internal/manager"
	driftregistry "github.com/NVIDIA/ccluster/internal/registry/drift_registry"
	"github.com/NVIDIA/ccluster/internal/remote"
	// +kubebuilder:scaffold:imports
)

var scheme = runtime.NewScheme()

// argoCDNamespace is registered on flag.CommandLine in main and read in
// setupControllers. manager.Run owns flag.Parse, and calls SetupControllers only after
// it, so the value is settled by the time a controller reads it. setupControllers
// replaces an empty value with the operator's own namespace.
var argoCDNamespace string

// serviceAccountNamespaceFile holds the namespace of the Pod it is mounted into, beside
// the ServiceAccount token the operator authenticates with. controller-runtime reads the
// same file for the leader-election namespace.
const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// catalogRepoURL and catalogRevision locate the chart catalog the generated
// Applications deploy from. Set by flags, for the same reason as argoCDNamespace.
var (
	catalogRepoURL  string
	catalogRevision string
)

// The argocd-server Service the operator reads Application trees through, and the
// Secret holding its token, both in argoCDNamespace. Set by flags, for the same reason
// as argoCDNamespace.
var (
	argoCDServerService string
	argoCDTokenSecret   string
)

// infraPlaneResyncPeriod is how often a CCLInfraPlane is reconciled again to restore its
// infra-plane cluster's CCLClusterConfig. Set by a flag, for the same reason as
// argoCDNamespace.
var infraPlaneResyncPeriod time.Duration

// argoCDTokenKey is the key of the token Secret.
const argoCDTokenKey = "token"

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(cclusternvidiacomv1alpha1.AddToScheme(scheme))
	// Argo CD's Application and AppProject, which the two controllers create.
	// Registering them is safe on a cluster with no Argo CD: the missing CRDs surface
	// at reconcile time, from the RESTMapper, rather than at startup.
	utilruntime.Must(argocdv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	// Registered before manager.Run, which parses flag.CommandLine. This flag is
	// specific to the control-plane operator, so it does not belong in
	// internal/manager alongside the operator-agnostic ones.
	flag.StringVar(&argoCDNamespace, "argocd-namespace", "",
		"The namespace Argo CD runs in. Cluster Secrets mirrored from CCLInfraPlanes, "+
			"and the AppProject for each of their namespaces, are written there. Empty means "+
			"the operator's own namespace, where the cp chart installs Argo CD.")
	flag.StringVar(&catalogRepoURL, "catalog-repo-url", "https://github.com/NVIDIA/ccluster",
		"The Git repository holding the chart catalog. Argo CD's repo-server must be able to clone it.")
	flag.StringVar(&catalogRevision, "catalog-revision", "main",
		"The revision of the catalog repository to deploy from: a branch, tag or commit SHA.")
	flag.StringVar(&argoCDServerService, "argocd-server-service", "argocd-server",
		"argocd-server's Service in --argocd-namespace. The operator reads Application "+
			"trees through it, over HTTPS without verifying its certificate.")
	flag.DurationVar(&infraPlaneResyncPeriod, "infraplane-resync-period", 10*time.Minute,
		"How often each CCLInfraPlane is reconciled again once its infra-plane cluster's "+
			"CCLClusterConfig has been written, restoring it if it drifted. 0 disables it.")
	flag.StringVar(&argoCDTokenSecret, "argocd-token-secret", "ccluster-cp-argocd-token",
		"The Secret in --argocd-namespace whose \""+argoCDTokenKey+"\" key holds the token "+
			"of the Argo CD account the operator reads argocd-server's API as.")

	err := manager.Run(manager.Options{
		Name:             "ccluster-cp-operator",
		LeaderElectionID: "cp.ccluster.nvidia.com",
		Scheme:           scheme,
		SetupControllers: setupControllers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// setupControllers registers every controller owned by the control-plane
// operator. New controllers scaffolded with `kubebuilder create api` must be
// wired up here by hand — the CLI only knows how to inject into cmd/main.go,
// which this project does not have.
func setupControllers(mgr ctrl.Manager) error {
	namespace, err := resolveArgoCDNamespace(argoCDNamespace, serviceAccountNamespaceFile)
	if err != nil {
		return err
	}
	argoCDNamespace = namespace

	argoCDServer, err := newArgoCDServerClient(mgr)
	if err != nil {
		return err
	}
	if err := (&cp.CCLInfraPlaneReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ArgoCDNamespace: argoCDNamespace,
		CatalogRepoURL:  catalogRepoURL,
		RemoteClients: remote.SecretClientFactory{
			Local:  mgr.GetConfig(),
			Scheme: mgr.GetScheme(),
		},
		ResyncPeriod: infraPlaneResyncPeriod,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CCLInfraPlane: %w", err)
	}
	if err := (&cp.CCLComponentSetReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		CatalogRepoURL:  catalogRepoURL,
		CatalogRevision: catalogRevision,
		Drift:           driftregistry.Get(),
		ArgoCDServer:    argoCDServer,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller CCLComponentSet: %w", err)
	}
	// +kubebuilder:scaffold:builder
	return nil
}

// resolveArgoCDNamespace returns flagValue, or if it is empty the namespace in
// namespaceFile, the operator's own. Outside a Pod there is no such file, and the
// namespace has to be given.
func resolveArgoCDNamespace(flagValue, namespaceFile string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	data, err := os.ReadFile(namespaceFile)
	if err != nil {
		return "", fmt.Errorf("--argocd-namespace is unset and the operator's own namespace is unknown: %w", err)
	}
	namespace := strings.TrimSpace(string(data))
	if namespace == "" {
		return "", fmt.Errorf("--argocd-namespace is unset and %s is empty", namespaceFile)
	}
	return namespace, nil
}

// newArgoCDServerClient returns the argocd-server client the flags describe. It reads
// its token through the manager's cached client, and does not contact argocd-server, so
// an invalid Service name fails startup but an unreachable server does not.
//
// The client reaches argocd-server only through its Service, because Argo CD runs in
// the operator's own cluster and the client does not verify the server's certificate.
func newArgoCDServerClient(mgr ctrl.Manager) (*apiclient.Client, error) {
	if errs := validation.IsDNS1035Label(argoCDServerService); len(errs) > 0 {
		return nil, fmt.Errorf("invalid --argocd-server-service %q: %s",
			argoCDServerService, strings.Join(errs, "; "))
	}
	argoCDServer, err := apiclient.New(apiclient.Options{
		ServerURL: fmt.Sprintf("https://%s.%s.svc", argoCDServerService, argoCDNamespace),
		Token: apiclient.SecretKey{
			NamespacedName: types.NamespacedName{Namespace: argoCDNamespace, Name: argoCDTokenSecret},
			Key:            argoCDTokenKey,
		},
		Reader: mgr.GetClient(),
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create the argocd-server client: %w", err)
	}
	return argoCDServer, nil
}
