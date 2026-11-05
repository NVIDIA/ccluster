/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/ccluster/pkg/cclclient"
)

// What register creates on the infra plane. Argo CD deploys charts that install CRDs,
// namespaces and other cluster-scoped objects, so its credentials are cluster-admin, the
// same rights `argocd cluster add` grants its argocd-manager ServiceAccount.
const (
	serviceAccountNamespace = "kube-system"
	serviceAccountName      = "ccluster-manager"
	tokenSecretName         = "ccluster-manager-token"
	clusterRoleBindingName  = "ccluster-manager"
	clusterRoleName         = "cluster-admin"
)

// How long register waits for the token controller to fill in the token Secret. Tests
// shorten both.
var (
	tokenTimeout      = 30 * time.Second
	tokenPollInterval = time.Second
)

type registerOptions struct {
	namespace  string
	infraPlane string
	domain     string
	server     string
}

func newRegisterCommand() *cobra.Command {
	var opts registerOptions
	cmd := &cobra.Command{
		Use:   "register NAME --namespace NAMESPACE --context CONTEXT --domain DOMAIN",
		Short: "Register a cluster with the control plane.",
		Long: `Register a cluster with the control plane.

The control plane is the one saved by ccluster init. On the infra plane, named by
--context, creates the ServiceAccount kube-system/ccluster-manager, binds it to
cluster-admin, and reads its long-lived token. On the control plane, applies the Secret
NAME-credentials carrying that token and the cluster CA, and the CCLInfraPlane NAME
pointing at it with the given domain. The control-plane operator then writes the domain
to the infra plane's CCLClusterConfig. Re-running reuses the same token.

Pass --server when the kubeconfig's address is not reachable from the control plane, for
example a kind cluster, whose kubeconfig points at 127.0.0.1.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runRegister(cmd.Context(), cmd.OutOrStdout(), args[0], opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&opts.namespace, "namespace", "n", "",
		"Control-plane namespace for the CCLInfraPlane and its credentials Secret; created if it does not exist.")
	flags.StringVar(&opts.infraPlane, "context", "",
		"Kubeconfig context of the infra-plane cluster to register.")
	flags.StringVar(&opts.domain, "domain", "",
		"The infra-plane cluster's DNS domain, written to its CCLClusterConfig. Cannot be changed once registered.")
	flags.StringVar(&opts.server, "server", "",
		"Infra-plane API server URL that Argo CD in the control plane connects to. "+
			"Defaults to the infra-plane context's server in the kubeconfig.")
	for _, name := range []string{"namespace", "context", "domain"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runRegister(ctx context.Context, out io.Writer, name string, opts registerOptions) error {
	controlPlane, err := loadControlPlane()
	if err != nil {
		return err
	}
	infraConfig, err := restConfig(opts.infraPlane)
	if err != nil {
		return err
	}
	controlConfig, err := restConfig(controlPlane)
	if err != nil {
		return err
	}

	infra, err := kubernetes.NewForConfig(infraConfig)
	if err != nil {
		return err
	}
	token, caCert, err := clusterAdminToken(ctx, infra)
	if err != nil {
		return apiError(err)
	}
	server := opts.server
	if server == "" {
		server = infraConfig.Host
	}

	client, err := cclclient.NewForConfig(opts.namespace, controlConfig)
	if err != nil {
		return err
	}
	if err := client.EnsureNamespace(ctx); err != nil {
		return apiError(err)
	}
	plane, err := client.ApplyInfraPlane(ctx, cclclient.InfraPlaneOptions{
		Name:        name,
		Domain:      opts.domain,
		Server:      server,
		BearerToken: token,
		CACert:      caCert,
	})
	if err != nil {
		return apiError(err)
	}
	_, _ = fmt.Fprintf(out, "Registered CCLInfraPlane %s/%s for %s\n",
		plane.Namespace, plane.Name, server)
	return nil
}

// clusterAdminToken creates the cluster-admin ServiceAccount and its token Secret, and
// returns the token and the cluster's CA, PEM, as the token controller copies it into the
// Secret.
//
// Each object that already exists is kept, so a second registration of the same cluster
// returns the same token.
func clusterAdminToken(ctx context.Context, kube kubernetes.Interface) (string, []byte, error) {
	if _, err := kube.CoreV1().ServiceAccounts(serviceAccountNamespace).Create(ctx,
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name: serviceAccountName, Namespace: serviceAccountNamespace,
		}}, metav1.CreateOptions{}); ignoreAlreadyExists(err) != nil {
		return "", nil, err
	}
	if _, err := kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterRoleBindingName},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      serviceAccountName,
			Namespace: serviceAccountNamespace,
		}},
	}, metav1.CreateOptions{}); ignoreAlreadyExists(err) != nil {
		return "", nil, err
	}
	// ServiceAccounts no longer get a token Secret of their own, but the token controller
	// still fills in one created with this type and annotation.
	secrets := kube.CoreV1().Secrets(serviceAccountNamespace)
	if _, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        tokenSecretName,
			Namespace:   serviceAccountNamespace,
			Annotations: map[string]string{corev1.ServiceAccountNameKey: serviceAccountName},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}, metav1.CreateOptions{}); ignoreAlreadyExists(err) != nil {
		return "", nil, err
	}

	var token, caCert []byte
	// Each Get runs under ctx, not the poll's deadline: a request the deadline cuts short,
	// or that client-go's rate limiter refuses to start because it would pass the
	// deadline, fails with an error of its own rather than the poll's timeout.
	err := wait.PollUntilContextTimeout(ctx, tokenPollInterval, tokenTimeout, true,
		func(context.Context) (bool, error) {
			secret, err := secrets.Get(ctx, tokenSecretName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			token = secret.Data[corev1.ServiceAccountTokenKey]
			caCert = secret.Data[corev1.ServiceAccountRootCAKey]
			return len(token) > 0 && len(caCert) > 0, nil
		})
	if wait.Interrupted(err) && ctx.Err() == nil {
		return "", nil, fmt.Errorf("secret %s/%s was not given a token within %s",
			serviceAccountNamespace, tokenSecretName, tokenTimeout)
	}
	if err != nil {
		return "", nil, err
	}
	return string(token), caCert, nil
}

func ignoreAlreadyExists(err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
