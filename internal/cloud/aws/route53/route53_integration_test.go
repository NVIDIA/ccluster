//go:build route53integration

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package route53

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// TestManagerAgainstARoute53API runs Manager against the Route53 API at
// $ROUTE53_TEST_ENDPOINT, such as moto's:
//
//	docker run --rm -d -p 5000:5000 motoserver/moto
//	ROUTE53_TEST_ENDPOINT=http://localhost:5000 \
//	  go test -tags route53integration -run TestManagerAgainstARoute53API ./internal/cloud/aws/route53/
//
// It checks what the in-memory fake cannot: the SDK's requests and errors as a real API
// answers them. Never point it at real AWS credentials.
func TestManagerAgainstARoute53API(t *testing.T) {
	endpoint := os.Getenv("ROUTE53_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("ROUTE53_TEST_ENDPOINT is not set")
	}
	ctx := context.Background()
	api := sdk.NewFromConfig(aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(endpoint),
	})

	// Unique per run, since the API's state outlives the test.
	parent := strings.ToLower(rand.Text()[:8]) + ".example.com"
	domain := "gpu-a." + parent
	owner := Owner{UID: rand.Text(), Namespace: "team-a", Name: "gpu-a"}

	created, err := api.CreateHostedZone(ctx, &sdk.CreateHostedZoneInput{
		Name: aws.String(parent), CallerReference: aws.String(rand.Text()),
	})
	if err != nil {
		t.Fatal(err)
	}
	parentID := zoneID(aws.ToString(created.HostedZone.Id))
	m := New(api, parentID)

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(zone.NameServers) == 0 {
		t.Fatal("the zone has no name servers")
	}
	if got := delegationValues(ctx, t, api, parentID, domain); !slices.Equal(got, zone.NameServers) {
		t.Errorf("delegation = %v, want %v", got, zone.NameServers)
	}

	for _, knownID := range []string{zone.ID, ""} {
		again, err := m.EnsureZone(ctx, domain, owner, knownID)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != zone.ID {
			t.Errorf("EnsureZone(knownID %q) = %s, want %s", knownID, again.ID, zone.ID)
		}
	}

	tags, err := api.ListTagsForResource(ctx, &sdk.ListTagsForResourceInput{
		ResourceId: aws.String(zone.ID), ResourceType: types.TagResourceTypeHostedzone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tags.ResourceTagSet.Tags, func(tag types.Tag) bool {
		return aws.ToString(tag.Key) == TagManagedBy && aws.ToString(tag.Value) == TagManagedByValue
	}) {
		t.Errorf("tags = %v, want %s", tags.ResourceTagSet.Tags, TagManagedBy)
	}

	for _, record := range []*Record{
		{Type: "A", Value: "192.0.2.10"},
		{Type: "A", Value: "192.0.2.20"},
		{Type: "CNAME", Value: "lb-1.elb.example.net"},
	} {
		if _, err := m.SetWildcard(ctx, zone.ID, domain, record); err != nil {
			t.Fatalf("SetWildcard(%v): %v", *record, err)
		}
		changed, err := m.SetWildcard(ctx, zone.ID, domain, record)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Errorf("SetWildcard(%v) again changed the record", *record)
		}
		got, err := m.recordSets(ctx, zone.ID, "*."+domain)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || string(got[0].Type) != record.Type ||
			!slices.Equal(recordValues(&got[0]), []string{record.Value}) {
			t.Errorf("wildcard = %+v, want %v", got, *record)
		}
	}

	// A record added by hand, which DeleteZone has to purge along with the wildcard.
	_, err = api.ChangeResourceRecordSets(ctx, &sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zone.ID),
		ChangeBatch: &types.ChangeBatch{Changes: []types.Change{
			{Action: types.ChangeActionCreate, ResourceRecordSet: &types.ResourceRecordSet{
				Name: aws.String("_acme-challenge." + domain), Type: types.RRTypeTxt, TTL: aws.Int64(300),
				ResourceRecords: []types.ResourceRecord{{Value: aws.String(`"token"`)}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.DeleteZone(ctx, domain, owner, zone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetHostedZone(ctx, &sdk.GetHostedZoneInput{Id: aws.String(zone.ID)}); !isNoSuchHostedZone(err) {
		t.Errorf("GetHostedZone after DeleteZone: %v, want NoSuchHostedZone", err)
	}
	if got := delegationValues(ctx, t, api, parentID, domain); got != nil {
		t.Errorf("delegation = %v after DeleteZone, want none", got)
	}
	if err := m.DeleteZone(ctx, domain, owner, zone.ID); err != nil {
		t.Errorf("DeleteZone of a deleted zone: %v", err)
	}

	if _, err := api.DeleteHostedZone(ctx, &sdk.DeleteHostedZoneInput{Id: aws.String(parentID)}); err != nil {
		t.Logf("leaving parent zone %s: %v", parentID, err)
	}
}

func delegationValues(ctx context.Context, t *testing.T, api *sdk.Client, parentID, domain string) []string {
	t.Helper()
	out, err := api.ListResourceRecordSets(ctx, &sdk.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(parentID),
	})
	if err != nil {
		t.Fatal(fmt.Errorf("listing the parent zone: %w", err))
	}
	for _, rrs := range out.ResourceRecordSets {
		if normalize(aws.ToString(rrs.Name)) == domain && rrs.Type == types.RRTypeNs {
			return recordValues(&rrs)
		}
	}
	return nil
}
