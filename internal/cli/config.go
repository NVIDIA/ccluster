/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// configEnv names the config file init writes. Unset, it is ccluster/config.yaml under
// $XDG_CONFIG_HOME, or under ~/.config.
const configEnv = "CCLUSTER_CONFIG"

// config is the config file's content. Only the control plane's kubeconfig context is
// kept; credentials stay in the kubeconfig.
type config struct {
	ControlPlane *controlPlaneConfig `json:"controlPlane,omitempty"`
}

type controlPlaneConfig struct {
	Context string `json:"context"`
}

func configPath() (string, error) {
	if explicit := os.Getenv(configEnv); explicit != "" {
		return explicit, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the config file: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ccluster", "config.yaml"), nil
}

// loadControlPlane returns the control plane's kubeconfig context saved by init.
func loadControlPlane() (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("config file %s could not be read: %w", path, err)
	}
	// A file of the wrong shape, such as a list, is as good as an empty one.
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("config file %s is not valid YAML: %w", path, err)
	}
	var cfg config
	if _, ok := raw.(map[string]any); ok {
		_ = yaml.Unmarshal(data, &cfg)
	}
	if cfg.ControlPlane == nil || cfg.ControlPlane.Context == "" {
		return "", errors.New("no control plane is configured; run ccluster init first")
	}
	return cfg.ControlPlane.Context, nil
}

// saveControlPlane writes context to the config file, replacing what was there, and
// returns the file's path.
func saveControlPlane(context string) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	data, err := yaml.Marshal(config{ControlPlane: &controlPlaneConfig{Context: context}})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("config file %s could not be written: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("config file %s could not be written: %w", path, err)
	}
	return path, nil
}

// restConfig loads the client config of a kubeconfig context, from $KUBECONFIG or
// ~/.kube/config as kubectl does.
func restConfig(context string) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig could not be loaded: %w", err)
	}
	return cfg, nil
}

// apiError turns a failed API request into a one-line error with the server's message.
// Other errors are returned as they are.
func apiError(err error) error {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return fmt.Errorf("API request failed (%d): %s", status.Status().Code, status.Status().Message)
	}
	return err
}
