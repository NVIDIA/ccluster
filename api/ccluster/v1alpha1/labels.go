/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

// Labels that catalog charts put on the objects they render, for the operators to find.
const (
	// LabelLoadBalancer marks a Service of type LoadBalancer for the infra-plane operator
	// to report in CCLClusterConfig's status.loadBalancers, under the label's value. A
	// chart opts a LoadBalancer in by labelling its Service; the value must be unique in
	// the cluster.
	LabelLoadBalancer = "ccluster.nvidia.com/loadbalancer"

	// LoadBalancerIngress is the LabelLoadBalancer value of the cluster's ingress, the
	// ingress_nginx controller's Service: the address the cluster's DNS points at.
	LoadBalancerIngress = "ingress"
)
