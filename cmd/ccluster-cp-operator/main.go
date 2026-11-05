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
	externaldnsv1alpha1 "github.com/NVIDIA/ccluster/internal/externaldns/v1alpha1"
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

// dnsEndpointNamespace is where the DNSEndpoint of each CCLInfraPlane is written. Set by a
// flag, for the same reason as argoCDNamespace; setupControllers replaces an empty value
// with the operator's own namespace.
var dnsEndpointNamespace string

// baseDomain is the DNS domain every CCLInfraPlane's domain is derived under:
// "<name>.<namespace>.<baseDomain>". Set by a required flag, for the same reason as
// argoCDNamespace; validateBaseDomain rejects an invalid value before the manager starts.
var baseDomain string

// maxBaseDomainLength leaves room in a 253-character domain for the "<name>.<namespace>."
// in front of the base domain, each of them a DNS label of at most 63 characters.
const maxBaseDomainLength = validation.DNS1123SubdomainMaxLength - 2*(validation.DNS1123LabelMaxLength+1)

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
	// external-dns's DNSEndpoint, which the CCLInfraPlane controller creates. Safe on a
	// cluster without external-dns for the same reason.
	utilruntime.Must(externaldnsv1alpha1.AddToScheme(scheme))
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
	flag.DurationVar(&infraPlaneResyncPeriod, "infraplane-resync-period", 5*time.Minute,
		"How often each CCLInfraPlane is reconciled again once its infra-plane cluster's "+
			"CCLClusterConfig has been written. A safety net: the operator watches that "+
			"CCLClusterConfig, so changes to it arrive as events. 0 disables it.")
	flag.StringVar(&argoCDTokenSecret, "argocd-token-secret", "ccluster-cp-argocd-token",
		"The Secret in --argocd-namespace whose \""+argoCDTokenKey+"\" key holds the token "+
			"of the Argo CD account the operator reads argocd-server's API as.")
	flag.StringVar(&dnsEndpointNamespace, "dns-endpoint-namespace", "",
		"The namespace the external-dns DNSEndpoint of each CCLInfraPlane, pointing "+
			"*.<lb>.<domain> at each of the plane's labelled LoadBalancers, is written to, which external-dns must read. "+
			"Empty means the operator's own namespace, where the cp chart installs external-dns.")
	flag.StringVar(&baseDomain, "base-domain", "",
		"Required. The DNS domain every CCLInfraPlane's domain is derived under: "+
			"<name>.<namespace>.<base-domain>. The operator writes that domain to the plane's "+
			"CCLClusterConfig, where it is also the SPIFFE trust domain, and publishes the plane's "+
			"records under it. Set it once: changing it moves every plane's domain.")

	err := manager.Run(manager.Options{
		Name:             "ccluster-cp-operator",
		LeaderElectionID: "cp.ccluster.nvidia.com",
		Scheme:           scheme,
		ValidateFlags:    func() error { return validateBaseDomain(baseDomain) },
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

	namespace, err = resolveDNSEndpointNamespace(dnsEndpointNamespace, serviceAccountNamespaceFile)
	if err != nil {
		return err
	}
	dnsEndpointNamespace = namespace

	argoCDServer, err := newArgoCDServerClient(mgr)
	if err != nil {
		return err
	}
	// Stops every infra-plane watch when the manager stops.
	remoteWatches := remote.NewRegistry(mgr.GetConfig())
	if err := mgr.Add(remoteWatches); err != nil {
		return fmt.Errorf("unable to add the infra-plane watch registry: %w", err)
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
		RemoteWatches:        remoteWatches,
		ResyncPeriod:         infraPlaneResyncPeriod,
		DNSEndpointNamespace: dnsEndpointNamespace,
		BaseDomain:           baseDomain,
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

// validateBaseDomain checks the --base-domain value: a lowercase DNS name, with or without
// dots, short enough that "<name>.<namespace>.<value>" always is one too.
func validateBaseDomain(value string) error {
	if value == "" {
		return fmt.Errorf("--base-domain is required: every CCLInfraPlane's domain is derived " +
			"under it, as <name>.<namespace>.<base-domain>")
	}
	for label := range strings.SplitSeq(value, ".") {
		if errs := validation.IsDNS1123Label(label); len(errs) > 0 {
			return fmt.Errorf("--base-domain %q is not a lowercase DNS name: label %q: %s",
				value, label, strings.Join(errs, ", "))
		}
	}
	if len(value) > maxBaseDomainLength {
		return fmt.Errorf("--base-domain %q is longer than %d characters, which leaves no room "+
			"for <name>.<namespace>. in front of it", value, maxBaseDomainLength)
	}
	return nil
}

// resolveArgoCDNamespace returns flagValue, or if it is empty the operator's own
// namespace. Outside a Pod there is none, and the namespace has to be given.
func resolveArgoCDNamespace(flagValue, namespaceFile string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	namespace, err := ownNamespace(namespaceFile)
	if err != nil {
		return "", fmt.Errorf("--argocd-namespace is unset and %w", err)
	}
	return namespace, nil
}

// resolveDNSEndpointNamespace returns flagValue, or if it is empty the operator's own
// namespace, where the cp chart installs external-dns. Outside a Pod there is none, and
// the namespace has to be given.
func resolveDNSEndpointNamespace(flagValue, namespaceFile string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	namespace, err := ownNamespace(namespaceFile)
	if err != nil {
		return "", fmt.Errorf("--dns-endpoint-namespace is unset and %w", err)
	}
	return namespace, nil
}

// ownNamespace returns the namespace in namespaceFile, the operator's own. Outside a Pod
// there is no such file.
func ownNamespace(namespaceFile string) (string, error) {
	data, err := os.ReadFile(namespaceFile)
	if err != nil {
		return "", fmt.Errorf("the operator's own namespace is unknown: %w", err)
	}
	namespace := strings.TrimSpace(string(data))
	if namespace == "" {
		return "", fmt.Errorf("%s is empty", namespaceFile)
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
