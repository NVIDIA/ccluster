/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package server

import (
	"net/http"

	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

// ClientSource returns the clientset a request reads Kubernetes with. Whatever
// credentials it carries decide, through Kubernetes RBAC, what the request may see; the
// server adds no access control of its own.
type ClientSource interface {
	Client(r *http.Request) (versioned.Interface, error)
}

// LocalSource serves every request with one clientset, built from the kubeconfig context
// of whoever runs ccluster server on their workstation.
type LocalSource struct {
	Clientset versioned.Interface
}

// Client returns the source's clientset.
func (s LocalSource) Client(*http.Request) (versioned.Interface, error) {
	return s.Clientset, nil
}
