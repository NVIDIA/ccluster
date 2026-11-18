/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cp

// Plain table tests for every address form the operator rejects or rewrites. The
// CCLClusterConfig schema accepts them all, so the controller suite covers one end to end.

import (
	"strings"
	"testing"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	externaldnsv1alpha1 "github.com/NVIDIA/ccluster/internal/api/externaldns/v1alpha1"
)

func TestWildcardEndpointsRejectInvalidAddresses(t *testing.T) {
	const domain = "gpu-a.infra.example.com"
	valid := cclusternvidiacomv1alpha1.LoadBalancerStatus{Name: "ingress", IP: "192.0.2.10"}
	validRecord := externaldnsv1alpha1.Endpoint{
		DNSName: "*.ingress." + domain, RecordType: "A", Targets: []string{"192.0.2.10"}, RecordTTL: wildcardTTLSeconds,
	}

	for _, tc := range []struct {
		name    string
		lb      cclusternvidiacomv1alpha1.LoadBalancerStatus
		invalid string
		record  *externaldnsv1alpha1.Endpoint
	}{
		{name: "ip that is not an IP address", lb: lbStatus("lb.example.com", ""),
			invalid: `ip "lb.example.com" is not an IP address`},
		{name: "ip with a zone", lb: lbStatus("fe80::1%eth0", ""),
			invalid: `ip "fe80::1%eth0" has a zone`},
		{name: "uppercase hostname", lb: lbStatus("", "LB.example.com"),
			invalid: `hostname "LB.example.com" is not a DNS name`},
		{name: "hostname with a trailing dot", lb: lbStatus("", "lb.example.com."),
			invalid: `hostname "lb.example.com." is not a DNS name`},
		{name: "hostname that is an IP address", lb: lbStatus("", "192.0.2.20"),
			invalid: `hostname "192.0.2.20" is an IP address`},
		{name: "invalid hostname beside a valid ip", lb: lbStatus("192.0.2.20", "lb.example.com."),
			invalid: `hostname "lb.example.com." is not a DNS name`},
		{name: "IPv4-mapped IPv6 ip is published as IPv4", lb: lbStatus("::ffff:192.0.2.20", ""),
			record: &externaldnsv1alpha1.Endpoint{DNSName: "*.api." + domain, RecordType: "A",
				Targets: []string{"192.0.2.20"}, RecordTTL: wildcardTTLSeconds}},
		{name: "non-canonical IPv6 ip is published canonical", lb: lbStatus("2001:DB8:0::20", ""),
			record: &externaldnsv1alpha1.Endpoint{DNSName: "*.api." + domain, RecordType: "AAAA",
				Targets: []string{"2001:db8::20"}, RecordTTL: wildcardTTLSeconds}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoints, invalid := wildcardEndpoints(domain, &cclusternvidiacomv1alpha1.CCLClusterConfigStatus{
				LoadBalancers: []cclusternvidiacomv1alpha1.LoadBalancerStatus{tc.lb, valid},
			})

			want := []externaldnsv1alpha1.Endpoint{validRecord}
			if tc.record != nil {
				want = []externaldnsv1alpha1.Endpoint{*tc.record, validRecord}
			}
			if describeRecords(endpoints) != describeRecords(want) {
				t.Errorf("records = %s, want %s", describeRecords(endpoints), describeRecords(want))
			}
			switch {
			case tc.invalid == "" && len(invalid) > 0:
				t.Errorf("invalid = %q, want none", invalid)
			case tc.invalid != "" && (len(invalid) != 1 || !strings.Contains(invalid[0], tc.invalid)):
				t.Errorf("invalid = %q, want one containing %q", invalid, tc.invalid)
			}
		})
	}
}

func lbStatus(ip, hostname string) cclusternvidiacomv1alpha1.LoadBalancerStatus {
	return cclusternvidiacomv1alpha1.LoadBalancerStatus{Name: "api", IP: ip, Hostname: hostname}
}
