/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ExternalSecrets has the External Secrets Operator create the workload's Secrets from
// references into a secret store, so secret values never enter the values, the
// CCLComponentSet or the Application. The infra plane must run ESO serving
// external-secrets.io/v1.
//
// The chart names each Secret from the store and the references, as
// <name>-env-<hash> and <name>-pull-<hash>. A new reference or version is a new
// Secret, which the pods roll onto, and the old one stays until the rollout ends. The
// templates live in catalogs/library/workload.
type ExternalSecrets struct {
	// SecretStoreRef is the store the references resolve in. Required.
	SecretStoreRef SecretStoreRef `json:"secretStoreRef"`

	// Env sets variables in the container, each from the value its reference reads.
	// Each name is also the key in the Secret, so it must be a valid variable name and
	// Secret key. It may not also be in envVars, secretEnvVars or the chart's own
	// variables.
	Env map[string]RemoteRef `json:"env,omitempty"`

	// ImagePull is a registry credential whose password comes from the store, added to
	// the pod's imagePullSecrets.
	ImagePull *ExternalImagePull `json:"imagePull,omitempty"`
}

// SecretStoreKind is the kind of an ESO store. Fields of this type carry the enum and a
// title in their jsonschema tag, as BackendProtocol does.
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
	// store reaches the pods only when a reference or version changes.
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

// Validate reports unset required values, an unknown store kind, values with neither
// env nor imagePull, and env names that are not valid variable names and Secret keys or
// that envVars or secretEnvVars also set. A nil receiver is valid.
func (e *ExternalSecrets) Validate(envVars map[string]string, secretEnvVars map[string]SecretKeyRef) error {
	if e == nil {
		return nil
	}
	errs := []error{MissingError(Missing(
		Field{"externalSecrets.secretStoreRef.name", e.SecretStoreRef.Name},
		Field{"externalSecrets.secretStoreRef.kind", string(e.SecretStoreRef.Kind)},
	))}
	if kind := e.SecretStoreRef.Kind; kind != "" && kind != SecretStore && kind != ClusterSecretStore {
		errs = append(errs, fmt.Errorf("%w: externalSecrets.secretStoreRef.kind %q is not SecretStore or ClusterSecretStore",
			ErrInvalidValue, kind))
	}
	if len(e.Env) == 0 && e.ImagePull == nil {
		errs = append(errs, MissingError([]string{"externalSecrets.env or externalSecrets.imagePull"}))
	}
	for _, name := range slices.Sorted(maps.Keys(e.Env)) {
		prefix := "externalSecrets.env." + name
		errs = append(errs, MissingError(Missing(Field{prefix + ".key", e.Env[name].Key})))
		// Both reject "." and "..", so check the Secret key rules only for a valid name.
		problems := validation.IsEnvVarName(name)
		if len(problems) == 0 {
			problems = validation.IsConfigMapKey(name)
		}
		if len(problems) > 0 {
			errs = append(errs, fmt.Errorf("%w: %s is not a valid variable name and Secret key: %s",
				ErrInvalidValue, prefix, strings.Join(problems, "; ")))
		}
		if _, ok := envVars[name]; ok {
			errs = append(errs, fmt.Errorf("%w: %s is also set in envVars", ErrInvalidValue, prefix))
		}
		if _, ok := secretEnvVars[name]; ok {
			errs = append(errs, fmt.Errorf("%w: %s is also set in secretEnvVars", ErrInvalidValue, prefix))
		}
	}
	if pull := e.ImagePull; pull != nil {
		errs = append(errs, MissingError(Missing(
			Field{"externalSecrets.imagePull.server", pull.Server},
			Field{"externalSecrets.imagePull.username", pull.Username},
			Field{"externalSecrets.imagePull.password.key", pull.Password.Key},
		)))
	}
	return errors.Join(errs...)
}

// ValidateChartEnv reports env names that are in chartEnv, the variables a chart sets
// itself, as ValidateChartEnv does for secretEnvVars. A nil receiver is valid.
func (e *ExternalSecrets) ValidateChartEnv(chartEnv []string) error {
	if e == nil {
		return nil
	}
	return setByChart("externalSecrets.env", e.Env, chartEnv)
}
