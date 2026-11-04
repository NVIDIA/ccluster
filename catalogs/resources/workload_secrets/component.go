/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package workloadsecrets describes the values of catalogs/resources/workload_secrets:
// the ExternalSecrets that have the External Secrets Operator (ESO) create one
// deployment's Secrets from references into a secret store, so secret values never pass
// through the chart's values. The chart renders up to two ExternalSecrets,
// <name>-env-<suffix> for Env and <name>-pull-<suffix> for ImagePull, each creating a
// Secret of the same name. The workload chart reads them through its secretEnvVars and
// imagePullSecretNames values.
//
// ESO fills each Secret once and never updates it, so a new reference or version needs a
// new Suffix. The caller names the component secrets-<suffix> and lists it before the
// workload, so a new suffix replaces the component: the cp operator releases the new one
// first, writes the workload only once its ExternalSecrets are Ready, and deletes the old
// one, with its Secrets, only after the workload is finished. See "Removed components go
// last, in reverse" in DESIGN.md.
//
// The infra plane must run ESO serving external-secrets.io/v1 (v0.16.0 or later) and the
// store SecretStoreRef names.
package workloadsecrets

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/catalogs"
	"github.com/NVIDIA/ccluster/catalogs/deployments"
)

// Chart is the chart directory, relative to the repository root.
const Chart = "catalogs/resources/workload_secrets"

// MaxSuffixLength is the longest Suffix. It keeps the Application name,
// <set>-secrets-<suffix>, short: Argo CD uses it as the Helm release name, which Helm
// limits to 53 characters.
const MaxSuffixLength = 16

// Values is the top-level values document for catalogs/resources/workload_secrets.
type Values struct {
	// Name is the workload chart's name value. It starts the ExternalSecret and Secret
	// names. Required.
	Name string `json:"name"`

	// Suffix ends the ExternalSecret and Secret names, and the caller names the
	// component secrets-<suffix>. A new reference or version needs a new suffix. By
	// convention it is the first 8 hex characters of a hash of the other values, so
	// returning to earlier references gives the earlier names, and the cp operator keeps
	// a component that is still waiting to be deleted. A DNS label of at most
	// MaxSuffixLength characters. Required.
	Suffix string `json:"suffix"`

	// SecretStoreRef is the store the references resolve in. Required.
	SecretStoreRef SecretStoreRef `json:"secretStoreRef"`

	// Env fills the Secret <name>-env-<suffix>, keyed by variable name. Each name is also
	// the key in the Secret, so it must be a valid Secret key. The workload sets the
	// variable through secretEnvVars.
	Env map[string]RemoteRef `json:"env,omitempty"`

	// ImagePull is the registry credential <name>-pull-<suffix>, whose password comes
	// from the store. The workload lists it in imagePullSecretNames.
	ImagePull *ExternalImagePull `json:"imagePull,omitempty"`
}

// SecretStoreKind is the kind of an ESO store. Fields of this type carry the enum and a
// title in their jsonschema tag; see deployments.BackendProtocol for why.
type SecretStoreKind string

const (
	// SecretStore is a store in the release namespace.
	SecretStore SecretStoreKind = "SecretStore"

	// ClusterSecretStore is a store every namespace can read from.
	ClusterSecretStore SecretStoreKind = "ClusterSecretStore"
)

// SecretStoreRef names an ESO store. Both fields are required.
type SecretStoreRef struct {
	// Name is the store's name.
	Name string `json:"name"`

	// Kind is SecretStore or ClusterSecretStore.
	//nolint:lll // A struct tag cannot wrap.
	Kind SecretStoreKind `json:"kind" jsonschema:"title=SecretStoreKind,enum=SecretStore,enum=ClusterSecretStore"`
}

// RemoteRef selects one value in the store, as an ExternalSecret's remoteRef does.
type RemoteRef struct {
	// The optional fields are strings with omitempty, not pointers: the chart renders
	// only the non-empty ones, so an empty value acts as an unset one.

	// Key is the secret's key in the store. Required.
	Key string `json:"key"`

	// Property selects one field of a structured secret, such as a JSON key.
	Property string `json:"property,omitempty"`

	// Version selects a version of the secret. Unset, ESO reads the version that is
	// current when it creates the Secret, and the Secret keeps it: a value rotated in the
	// store reaches the pods only with a new Suffix.
	Version string `json:"version,omitempty"`
}

// ExternalImagePull is a registry credential whose password comes from the store. ESO
// builds the dockerconfigjson when it creates the Secret. All three fields are required.
type ExternalImagePull struct {
	// Server is the registry host, as it appears in the image URL.
	Server string `json:"server"`

	// Username is the registry user.
	Username string `json:"username"`

	// Password is the registry password or token in the store.
	Password RemoteRef `json:"password"`
}

// New returns the workload_secrets chart entry, creating the ExternalSecrets in
// namespace, the workload's.
func New(
	namespace string,
	values Values,
) (cclusternvidiacomv1alpha1.ComponentEntry, error) {
	return catalogs.Entry(Chart, namespace, values)
}

// suffixPattern is a DNS-1123 label.
var suffixPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// secretKeyPattern is what a Secret's data keys may contain.
var secretKeyPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// Validate implements catalogs.Validator: the required values are set, Suffix is a DNS
// label of at most MaxSuffixLength characters, the store kind is known, and each Env
// name is a valid Secret key. Values with neither Env nor ImagePull are valid and render
// nothing.
func (v Values) Validate() error {
	errs := []error{deployments.MissingError(deployments.Missing(
		deployments.Field{Name: "name", Value: v.Name},
		deployments.Field{Name: "suffix", Value: v.Suffix},
		deployments.Field{Name: "secretStoreRef.name", Value: v.SecretStoreRef.Name},
		deployments.Field{Name: "secretStoreRef.kind", Value: string(v.SecretStoreRef.Kind)},
	))}
	if v.Suffix != "" && (len(v.Suffix) > MaxSuffixLength || !suffixPattern.MatchString(v.Suffix)) {
		errs = append(errs, fmt.Errorf("%w: suffix %q is not a DNS label of at most %d characters",
			deployments.ErrInvalidValue, v.Suffix, MaxSuffixLength))
	}
	if kind := v.SecretStoreRef.Kind; kind != "" && kind != SecretStore && kind != ClusterSecretStore {
		errs = append(errs, fmt.Errorf("%w: secretStoreRef.kind %q is not SecretStore or ClusterSecretStore",
			deployments.ErrInvalidValue, kind))
	}
	for _, name := range slices.Sorted(maps.Keys(v.Env)) {
		prefix := "env." + name
		errs = append(errs, deployments.MissingError(deployments.Missing(
			deployments.Field{Name: prefix + ".key", Value: v.Env[name].Key},
		)))
		if !secretKeyPattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("%w: %s is not a valid Secret key", deployments.ErrInvalidValue, prefix))
		}
	}
	if pull := v.ImagePull; pull != nil {
		errs = append(errs, deployments.MissingError(deployments.Missing(
			deployments.Field{Name: "imagePull.server", Value: pull.Server},
			deployments.Field{Name: "imagePull.username", Value: pull.Username},
			deployments.Field{Name: "imagePull.password.key", Value: pull.Password.Key},
		)))
	}
	return errors.Join(errs...)
}
