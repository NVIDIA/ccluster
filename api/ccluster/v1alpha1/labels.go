/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

// Labels that catalog charts put on the objects they render, for the operators to find.
const (
	// LabelLoadBalancer marks a Service of type LoadBalancer for the infra-plane operator
	// to report in CCLClusterConfig's status.loadBalancers, under the label's value. A
	// chart opts a LoadBalancer in by labelling its Service. The value is the chart's to
	// choose and means nothing to the operators; it must be unique in the cluster.
	LabelLoadBalancer = "ccluster.nvidia.com/loadbalancer"
)
