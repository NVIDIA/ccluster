/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package route53

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

const (
	parentDomain = "planes.example.com"
	domain       = "gpu-a.planes.example.com"
)

var owner = Owner{UID: "4f6c1f9e-0b8a-4c55-9d43-0d6c1d8f2a11", Namespace: "team-a", Name: "gpu-a"}

func TestEnsureZoneCreatesTagsAndDelegates(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)

	zone, err := m.EnsureZone(context.Background(), domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}

	created := fake.zone(t, zone.ID)
	if got := aws.ToString(created.hz.CallerReference); got != owner.UID {
		t.Errorf("CallerReference = %q, want the owner's UID", got)
	}
	if got := aws.ToString(created.hz.Name); got != domain+"." {
		t.Errorf("zone name = %q, want %q", got, domain+".")
	}
	if want := map[string]string{
		TagManagedBy:           TagManagedByValue,
		tagInfraPlaneNamespace: "team-a",
		tagInfraPlaneName:      "gpu-a",
	}; !maps.Equal(created.tags, want) {
		t.Errorf("tags = %v, want %v", created.tags, want)
	}
	if !slices.IsSorted(zone.NameServers) || len(zone.NameServers) != 4 {
		t.Errorf("name servers = %v, want the zone's four, sorted", zone.NameServers)
	}
	for _, ns := range zone.NameServers {
		if strings.HasSuffix(ns, ".") {
			t.Errorf("name server %q keeps its trailing dot", ns)
		}
	}

	delegation := fake.delegation(t)
	if delegation == nil {
		t.Fatal("the parent zone has no NS record for the domain")
	}
	if got := recordValues(delegation); !slices.Equal(got, zone.NameServers) {
		t.Errorf("delegation = %v, want %v", got, zone.NameServers)
	}
	if got := aws.ToInt64(delegation.TTL); got != delegationTTL {
		t.Errorf("delegation TTL = %d, want %d", got, delegationTTL)
	}
}

func TestEnsureZoneIsIdempotent(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("with the recorded zone", func(t *testing.T) {
		fake.calls = nil
		again, err := m.EnsureZone(ctx, domain, owner, zone.ID)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != zone.ID {
			t.Errorf("zone = %s, want %s", again.ID, zone.ID)
		}
		// The parent's domain is cached; the zone is read by ID and the delegation
		// checked, and nothing is written.
		if want := []string{"GetHostedZone", "ListResourceRecordSets"}; !slices.Equal(fake.calls, want) {
			t.Errorf("calls = %v, want %v", fake.calls, want)
		}
	})

	t.Run("without it, as after a lost status write", func(t *testing.T) {
		fake.calls = nil
		again, err := m.EnsureZone(ctx, domain, owner, "")
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != zone.ID {
			t.Errorf("zone = %s, want %s", again.ID, zone.ID)
		}
		if slices.Contains(fake.calls, "CreateHostedZone") || slices.Contains(fake.calls, "ChangeResourceRecordSets") {
			t.Errorf("calls = %v, want no zone created and no record changed", fake.calls)
		}
		if !slices.Contains(fake.calls, "ChangeTagsForResource") {
			t.Errorf("calls = %v, want the zone tagged again", fake.calls)
		}
	})

	t.Run("with a recorded ID that is not the owner's zone", func(t *testing.T) {
		other := fake.addZone(domain, "someone-else")
		again, err := m.EnsureZone(ctx, domain, owner, other)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != zone.ID {
			t.Errorf("zone = %s, want %s", again.ID, zone.ID)
		}
	})

	if n := fake.zonesNamed(domain); n != 2 {
		t.Errorf("%d zones named %s, want the owner's and the other one", n, domain)
	}
}

func TestEnsureZoneSkipsZonesOfTheSameNameOwnedByOthers(t *testing.T) {
	fake := newFake(t)
	fake.pageSize = 1
	for i := range 3 {
		fake.addZone(domain, fmt.Sprintf("someone-else-%d", i))
	}
	m := New(fake, fake.parentID)

	zone, err := m.EnsureZone(context.Background(), domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(fake.zone(t, zone.ID).hz.CallerReference); got != owner.UID {
		t.Errorf("zone %s has CallerReference %q, want a new zone of the owner's", zone.ID, got)
	}

	// Listed one zone per page, the owner's is found past the others.
	again, err := m.EnsureZone(context.Background(), domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != zone.ID {
		t.Errorf("zone = %s, want %s", again.ID, zone.ID)
	}
}

func TestEnsureZoneRetriesAUsedCallerReference(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}

	// Deleted by hand: Route53 still remembers the CallerReference.
	delete(fake.zones, zone.ID)

	recreated, err := m.EnsureZone(ctx, domain, owner, zone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID == zone.ID {
		t.Fatal("the deleted zone was returned")
	}
	if ref := aws.ToString(fake.zone(t, recreated.ID).hz.CallerReference); !strings.HasPrefix(ref, owner.UID+"-") {
		t.Errorf("CallerReference = %q, want the UID with a suffix", ref)
	}
	if got := recordValues(fake.delegation(t)); !slices.Equal(got, recreated.NameServers) {
		t.Errorf("delegation = %v, want the new zone's %v", got, recreated.NameServers)
	}

	again, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != recreated.ID {
		t.Errorf("zone = %s, want the recreated %s", again.ID, recreated.ID)
	}
}

func TestEnsureZoneRepairsTheDelegation(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	fake.setRecord(fake.parentID, types.ResourceRecordSet{
		Name: aws.String(domain + "."), Type: types.RRTypeNs, TTL: aws.Int64(60),
		ResourceRecords: []types.ResourceRecord{{Value: aws.String("ns.elsewhere.example.")}},
	})

	if _, err := m.EnsureZone(ctx, domain, owner, zone.ID); err != nil {
		t.Fatal(err)
	}
	if got := recordValues(fake.delegation(t)); !slices.Equal(got, zone.NameServers) {
		t.Errorf("delegation = %v, want %v", got, zone.NameServers)
	}
}

func TestEnsureZoneRejectsADomainOutsideTheParent(t *testing.T) {
	for _, d := range []string{"gpu-a.example.org", parentDomain, "xplanes.example.com", "a.xplanes.example.com"} {
		t.Run(d, func(t *testing.T) {
			fake := newFake(t)
			m := New(fake, fake.parentID)

			_, err := m.EnsureZone(context.Background(), d, owner, "")
			if !errors.Is(err, ErrOutsideParentZone) {
				t.Fatalf("error = %v, want ErrOutsideParentZone", err)
			}
			if slices.Contains(fake.calls, "CreateHostedZone") {
				t.Error("a zone was created")
			}
		})
	}
}

func TestEnsureZoneNormalizesTheDomain(t *testing.T) {
	fake := newFake(t)
	m := New(fake, "/hostedzone/"+fake.parentID)

	zone, err := m.EnsureZone(context.Background(), "GPU-A.planes.example.com.", owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(fake.zone(t, zone.ID).hz.Name); got != domain+"." {
		t.Errorf("zone name = %q, want %q", got, domain+".")
	}
}

func TestEnsureZoneRereadsAParentItCouldNotRead(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	fake.fail["GetHostedZone"] = errors.New("throttled")

	if _, err := m.EnsureZone(context.Background(), domain, owner, ""); err == nil {
		t.Fatal("want an error while the parent zone cannot be read")
	}

	delete(fake.fail, "GetHostedZone")
	if _, err := m.EnsureZone(context.Background(), domain, owner, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteZone(t *testing.T) {
	fake := newFake(t)
	fake.pageSize = 2
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	// The wildcard, and records added by hand.
	if _, err := m.SetWildcard(ctx, zone.ID, domain, &Record{Type: "A", Value: "192.0.2.10"}); err != nil {
		t.Fatal(err)
	}
	fake.setRecord(zone.ID, types.ResourceRecordSet{
		Name: aws.String("_acme-challenge." + domain + "."), Type: types.RRTypeTxt, TTL: aws.Int64(300),
		ResourceRecords: []types.ResourceRecord{{Value: aws.String(`"token"`)}},
	})
	fake.setRecord(zone.ID, types.ResourceRecordSet{
		Name: aws.String("ssh." + domain + "."), Type: types.RRTypeA, TTL: aws.Int64(60),
		ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.11")}},
	})
	fake.calls = nil

	if err := m.DeleteZone(ctx, domain, owner, zone.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := fake.zones[zone.ID]; ok {
		t.Error("the zone still exists")
	}
	if fake.delegation(t) != nil {
		t.Error("the parent zone still delegates the domain")
	}

	// The delegation goes first, so nothing resolves through the zone while it is
	// emptied, and the zone last.
	var changes []string
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "ChangeResourceRecordSets") || call == "DeleteHostedZone" {
			changes = append(changes, call)
		}
	}
	if want := []string{
		"ChangeResourceRecordSets " + fake.parentID,
		"ChangeResourceRecordSets " + zone.ID,
		"DeleteHostedZone",
	}; !slices.Equal(changes, want) {
		t.Errorf("changes = %v, want %v", changes, want)
	}
}

func TestDeleteZoneWithoutTheRecordedID(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteZone(ctx, domain, owner, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.zones[zone.ID]; ok {
		t.Error("the zone still exists")
	}
}

func TestDeleteZoneLeavesOthersAlone(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	t.Run("no zone", func(t *testing.T) {
		other := fake.addZone(domain, "someone-else")
		fake.calls = nil
		if err := m.DeleteZone(ctx, domain, owner, ""); err != nil {
			t.Fatal(err)
		}
		if _, ok := fake.zones[other]; !ok {
			t.Error("another owner's zone was deleted")
		}
		if slices.ContainsFunc(fake.calls, func(c string) bool { return strings.HasPrefix(c, "Change") }) {
			t.Errorf("calls = %v, want nothing changed", fake.calls)
		}
	})

	t.Run("a delegation to other name servers", func(t *testing.T) {
		zone, err := m.EnsureZone(ctx, domain, owner, "")
		if err != nil {
			t.Fatal(err)
		}
		fake.setRecord(fake.parentID, types.ResourceRecordSet{
			Name: aws.String(domain + "."), Type: types.RRTypeNs, TTL: aws.Int64(60),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String("ns.elsewhere.example.")}},
		})
		if err := m.DeleteZone(ctx, domain, owner, zone.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := fake.zones[zone.ID]; ok {
			t.Error("the zone still exists")
		}
		if fake.delegation(t) == nil {
			t.Error("a delegation to other name servers was deleted")
		}
	})

	t.Run("a domain outside the parent", func(t *testing.T) {
		if err := m.DeleteZone(ctx, "gpu-a.example.org", owner, ""); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeleteZoneReturnsFailures(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	fake.fail["DeleteHostedZone"] = errors.New("access denied")

	if err := m.DeleteZone(ctx, domain, owner, zone.ID); err == nil {
		t.Fatal("want the DeleteHostedZone error")
	}

	delete(fake.fail, "DeleteHostedZone")
	if err := m.DeleteZone(ctx, domain, owner, zone.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.zones[zone.ID]; ok {
		t.Error("the zone still exists")
	}
}

func TestSetWildcard(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	// Neighbours the wildcard must never touch.
	neighbours := []types.ResourceRecordSet{
		{Name: aws.String("*.sub." + domain), Type: types.RRTypeA, TTL: aws.Int64(300),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String("198.51.100.1")}}},
		{Name: aws.String("app." + domain), Type: types.RRTypeA, TTL: aws.Int64(300),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String("198.51.100.2")}}},
	}
	for _, rrs := range neighbours {
		fake.setRecord(zone.ID, rrs)
	}

	// set calls SetWildcard and checks what it reported and what the zone now holds at
	// the wildcard: want, or nothing if want is nil.
	set := func(t *testing.T, record *Record, wantChanged bool) {
		t.Helper()
		fake.calls = nil
		changed, err := m.SetWildcard(ctx, "/hostedzone/"+zone.ID, domain, record)
		if err != nil {
			t.Fatal(err)
		}
		if changed != wantChanged {
			t.Errorf("changed = %v, want %v", changed, wantChanged)
		}
		if writes := countPrefix(fake.calls, "ChangeResourceRecordSets"); writes > 1 || (writes == 1) != wantChanged {
			t.Errorf("calls = %v, want one change batch only if something changed", fake.calls)
		}

		var got []string
		for _, rrs := range fake.zone(t, zone.ID).records {
			if aws.ToString(rrs.Name) == `\052.`+domain+"." {
				got = append(got, fmt.Sprintf("%s %d %v", rrs.Type, aws.ToInt64(rrs.TTL), recordValues(&rrs)))
			}
		}
		var want []string
		if record != nil {
			want = []string{fmt.Sprintf("%s %d [%s]", record.Type, wildcardTTL, record.Value)}
		}
		if !slices.Equal(got, want) {
			t.Errorf("wildcard = %v, want %v", got, want)
		}
		for _, n := range neighbours {
			if !slices.ContainsFunc(fake.zone(t, zone.ID).records, func(r types.ResourceRecordSet) bool {
				return sameKey(r, n) && slices.Equal(recordValues(&r), recordValues(&n))
			}) {
				t.Errorf("%s was changed", aws.ToString(n.Name))
			}
		}
	}

	t.Run("creates an A record", func(t *testing.T) { set(t, &Record{Type: "A", Value: "192.0.2.10"}, true) })
	t.Run("leaves it alone", func(t *testing.T) { set(t, &Record{Type: "A", Value: "192.0.2.10"}, false) })
	t.Run("follows a new IP", func(t *testing.T) { set(t, &Record{Type: "A", Value: "192.0.2.20"}, true) })
	t.Run("switches to a CNAME in one batch", func(t *testing.T) {
		set(t, &Record{Type: "CNAME", Value: "lb-1.elb.example.net"}, true)
	})
	t.Run("leaves the CNAME alone", func(t *testing.T) {
		set(t, &Record{Type: "CNAME", Value: "lb-1.elb.example.net"}, false)
	})
	t.Run("switches back to an A record", func(t *testing.T) { set(t, &Record{Type: "A", Value: "192.0.2.30"}, true) })
	t.Run("deletes it", func(t *testing.T) { set(t, nil, true) })
	t.Run("deletes nothing when there is none", func(t *testing.T) { set(t, nil, false) })

	t.Run("restores a TTL changed by hand", func(t *testing.T) {
		set(t, &Record{Type: "A", Value: "192.0.2.10"}, true)
		fake.setRecord(zone.ID, types.ResourceRecordSet{
			Name: aws.String("*." + domain), Type: types.RRTypeA, TTL: aws.Int64(3600),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String("192.0.2.10")}},
		})
		set(t, &Record{Type: "A", Value: "192.0.2.10"}, true)
	})
}

func TestSetWildcardReturnsFailures(t *testing.T) {
	fake := newFake(t)
	m := New(fake, fake.parentID)
	ctx := context.Background()

	zone, err := m.EnsureZone(ctx, domain, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	fake.fail["ChangeResourceRecordSets"] = errors.New("throttled")

	if _, err := m.SetWildcard(ctx, zone.ID, domain, &Record{Type: "A", Value: "192.0.2.10"}); err == nil {
		t.Fatal("want the ChangeResourceRecordSets error")
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// fakeRoute53 is an in-memory Route53, faithful in what Manager depends on: CallerReference
// uniqueness, name ordering, pagination, and refusing to delete a zone with records in it.
type fakeRoute53 struct {
	t        *testing.T
	parentID string
	zones    map[string]*fakeZone
	// usedRefs holds every CallerReference ever accepted, deleted zones' included.
	usedRefs map[string]bool
	nextID   int
	// pageSize bounds every list call. Zero means Route53's default of 100.
	pageSize int32
	// fail makes the named operation return the error.
	fail  map[string]error
	calls []string
}

type fakeZone struct {
	hz      types.HostedZone
	servers []string
	records []types.ResourceRecordSet
	tags    map[string]string
}

func newFake(t *testing.T) *fakeRoute53 {
	f := &fakeRoute53{t: t, zones: map[string]*fakeZone{}, usedRefs: map[string]bool{}, fail: map[string]error{}}
	f.parentID = f.addZone(parentDomain, "parent")
	return f
}

// addZone creates a zone directly, and returns its ID.
func (f *fakeRoute53) addZone(name, ref string) string {
	f.nextID++
	id := fmt.Sprintf("Z%04d", f.nextID)
	servers := make([]string, 4)
	for i := range servers {
		// Out of order, as Route53 returns them.
		servers[i] = fmt.Sprintf("ns-%d%d.awsdns-%s.net", 3-i, f.nextID, id)
	}
	zone := &fakeZone{
		hz: types.HostedZone{
			Id:              aws.String("/hostedzone/" + id),
			Name:            aws.String(name + "."),
			CallerReference: aws.String(ref),
		},
		servers: servers,
		tags:    map[string]string{},
	}
	nsRecords := make([]types.ResourceRecord, 0, len(servers))
	for _, ns := range servers {
		nsRecords = append(nsRecords, types.ResourceRecord{Value: aws.String(ns + ".")})
	}
	zone.records = []types.ResourceRecordSet{
		{Name: aws.String(name + "."), Type: types.RRTypeNs, TTL: aws.Int64(172800), ResourceRecords: nsRecords},
		{Name: aws.String(name + "."), Type: types.RRTypeSoa, TTL: aws.Int64(900),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String(servers[0] + ". hostmaster. 1 7200 900 1209600 86400")}}},
	}
	f.zones[id] = zone
	f.usedRefs[ref] = true
	return id
}

func (f *fakeRoute53) zone(t *testing.T, id string) *fakeZone {
	t.Helper()
	zone, ok := f.zones[id]
	if !ok {
		t.Fatalf("no zone %s", id)
	}
	return zone
}

func (f *fakeRoute53) zonesNamed(name string) int {
	n := 0
	for _, zone := range f.zones {
		if aws.ToString(zone.hz.Name) == name+"." {
			n++
		}
	}
	return n
}

// delegation returns the parent zone's NS record set for domain, or nil.
func (f *fakeRoute53) delegation(t *testing.T) *types.ResourceRecordSet {
	t.Helper()
	for _, rrs := range f.zone(t, f.parentID).records {
		if aws.ToString(rrs.Name) == domain+"." && rrs.Type == types.RRTypeNs {
			return &rrs
		}
	}
	return nil
}

func (f *fakeRoute53) setRecord(id string, rrs types.ResourceRecordSet) {
	rrs = fqdn(rrs)
	zone := f.zones[id]
	zone.records = slices.DeleteFunc(zone.records, func(r types.ResourceRecordSet) bool {
		return sameKey(r, rrs)
	})
	zone.records = append(zone.records, rrs)
	sortRecords(zone.records)
}

func (f *fakeRoute53) call(op string, args ...string) error {
	f.calls = append(f.calls, strings.Join(append([]string{op}, args...), " "))
	return f.fail[op]
}

func (f *fakeRoute53) limit(maxItems *int32) int {
	if maxItems != nil {
		return int(*maxItems)
	}
	if f.pageSize > 0 {
		return int(f.pageSize)
	}
	return 100
}

func (f *fakeRoute53) GetHostedZone(_ context.Context, in *sdk.GetHostedZoneInput, _ ...func(*sdk.Options)) (*sdk.GetHostedZoneOutput, error) {
	if err := f.call("GetHostedZone"); err != nil {
		return nil, err
	}
	zone, ok := f.zones[zoneID(aws.ToString(in.Id))]
	if !ok {
		return nil, &types.NoSuchHostedZone{Message: aws.String("no such zone")}
	}
	hz := zone.hz
	return &sdk.GetHostedZoneOutput{HostedZone: &hz, DelegationSet: &types.DelegationSet{NameServers: slices.Clone(zone.servers)}}, nil
}

func (f *fakeRoute53) ListHostedZonesByName(_ context.Context, in *sdk.ListHostedZonesByNameInput, _ ...func(*sdk.Options)) (*sdk.ListHostedZonesByNameOutput, error) {
	if err := f.call("ListHostedZonesByName"); err != nil {
		return nil, err
	}
	all := make([]types.HostedZone, 0, len(f.zones))
	for _, zone := range f.zones {
		all = append(all, zone.hz)
	}
	key := func(hz types.HostedZone) string {
		return normalize(aws.ToString(hz.Name)) + "\x00" + zoneID(aws.ToString(hz.Id))
	}
	slices.SortFunc(all, func(a, b types.HostedZone) int { return cmp.Compare(key(a), key(b)) })

	start := normalize(aws.ToString(in.DNSName))
	if in.HostedZoneId != nil {
		start += "\x00" + zoneID(aws.ToString(in.HostedZoneId))
	}
	i, _ := slices.BinarySearchFunc(all, start, func(hz types.HostedZone, s string) int { return cmp.Compare(key(hz), s) })
	all = all[i:]

	out := &sdk.ListHostedZonesByNameOutput{}
	if n := f.limit(in.MaxItems); len(all) > n {
		out.IsTruncated = true
		out.NextDNSName = all[n].Name
		out.NextHostedZoneId = aws.String(zoneID(aws.ToString(all[n].Id)))
		all = all[:n]
	}
	out.HostedZones = all
	return out, nil
}

func (f *fakeRoute53) CreateHostedZone(_ context.Context, in *sdk.CreateHostedZoneInput, _ ...func(*sdk.Options)) (*sdk.CreateHostedZoneOutput, error) {
	if err := f.call("CreateHostedZone"); err != nil {
		return nil, err
	}
	ref := aws.ToString(in.CallerReference)
	if f.usedRefs[ref] {
		return nil, &types.HostedZoneAlreadyExists{Message: aws.String("caller reference already used")}
	}
	id := f.addZone(normalize(aws.ToString(in.Name)), ref)
	zone := f.zones[id]
	hz := zone.hz
	return &sdk.CreateHostedZoneOutput{HostedZone: &hz, DelegationSet: &types.DelegationSet{NameServers: slices.Clone(zone.servers)}}, nil
}

func (f *fakeRoute53) DeleteHostedZone(_ context.Context, in *sdk.DeleteHostedZoneInput, _ ...func(*sdk.Options)) (*sdk.DeleteHostedZoneOutput, error) {
	if err := f.call("DeleteHostedZone"); err != nil {
		return nil, err
	}
	id := zoneID(aws.ToString(in.Id))
	zone, ok := f.zones[id]
	if !ok {
		return nil, &types.NoSuchHostedZone{Message: aws.String("no such zone")}
	}
	if len(zone.records) > 2 {
		return nil, &types.HostedZoneNotEmpty{Message: aws.String("zone has records")}
	}
	delete(f.zones, id)
	return &sdk.DeleteHostedZoneOutput{}, nil
}

func (f *fakeRoute53) ChangeTagsForResource(_ context.Context, in *sdk.ChangeTagsForResourceInput, _ ...func(*sdk.Options)) (*sdk.ChangeTagsForResourceOutput, error) {
	if err := f.call("ChangeTagsForResource"); err != nil {
		return nil, err
	}
	zone, ok := f.zones[aws.ToString(in.ResourceId)]
	if !ok || in.ResourceType != types.TagResourceTypeHostedzone {
		return nil, &types.NoSuchHostedZone{Message: aws.String("no such zone")}
	}
	for _, tag := range in.AddTags {
		zone.tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return &sdk.ChangeTagsForResourceOutput{}, nil
}

func (f *fakeRoute53) ListResourceRecordSets(_ context.Context, in *sdk.ListResourceRecordSetsInput, _ ...func(*sdk.Options)) (*sdk.ListResourceRecordSetsOutput, error) {
	if err := f.call("ListResourceRecordSets"); err != nil {
		return nil, err
	}
	zone, ok := f.zones[aws.ToString(in.HostedZoneId)]
	if !ok {
		return nil, &types.NoSuchHostedZone{Message: aws.String("no such zone")}
	}
	records := zone.records
	if in.StartRecordName != nil {
		start := recordKey(aws.ToString(in.StartRecordName), in.StartRecordType)
		i := slices.IndexFunc(records, func(r types.ResourceRecordSet) bool {
			return recordKey(aws.ToString(r.Name), r.Type) >= start
		})
		if i < 0 {
			i = len(records)
		}
		records = records[i:]
	}

	out := &sdk.ListResourceRecordSetsOutput{}
	if n := f.limit(in.MaxItems); len(records) > n {
		out.IsTruncated = true
		out.NextRecordName = records[n].Name
		out.NextRecordType = records[n].Type
		records = records[:n]
	}
	out.ResourceRecordSets = slices.Clone(records)
	return out, nil
}

// ChangeResourceRecordSets applies a batch all or nothing, in order, refusing what Route53
// refuses: creating a record set that exists, deleting one that does not match exactly,
// and a CNAME sharing its name with any other record set.
func (f *fakeRoute53) ChangeResourceRecordSets(_ context.Context, in *sdk.ChangeResourceRecordSetsInput, _ ...func(*sdk.Options)) (*sdk.ChangeResourceRecordSetsOutput, error) {
	id := aws.ToString(in.HostedZoneId)
	if err := f.call("ChangeResourceRecordSets", id); err != nil {
		return nil, err
	}
	zone, ok := f.zones[id]
	if !ok {
		return nil, &types.NoSuchHostedZone{Message: aws.String("no such zone")}
	}
	invalid := func(msg string) error { return &types.InvalidChangeBatch{Message: aws.String(msg)} }

	records := slices.Clone(zone.records)
	for _, change := range in.ChangeBatch.Changes {
		rrs := *change.ResourceRecordSet
		existing := slices.IndexFunc(records, func(r types.ResourceRecordSet) bool { return sameKey(r, rrs) })
		switch change.Action {
		case types.ChangeActionCreate, types.ChangeActionUpsert:
			if existing >= 0 && change.Action == types.ChangeActionCreate {
				return nil, invalid("record set already exists")
			}
			if existing >= 0 {
				records = slices.Delete(records, existing, existing+1)
			}
			for _, r := range records {
				sameName := normalize(aws.ToString(r.Name)) == normalize(aws.ToString(rrs.Name))
				if sameName && (r.Type == types.RRTypeCname || rrs.Type == types.RRTypeCname) {
					return nil, invalid("a CNAME cannot share its name with another record set")
				}
			}
			records = append(records, fqdn(rrs))
		case types.ChangeActionDelete:
			if existing < 0 || !slices.Equal(recordValues(&records[existing]), recordValues(&rrs)) ||
				aws.ToInt64(records[existing].TTL) != aws.ToInt64(rrs.TTL) {
				return nil, invalid("record set not found")
			}
			records = slices.Delete(records, existing, existing+1)
		default:
			f.t.Fatalf("unexpected change action %s", change.Action)
		}
	}
	sortRecords(records)
	zone.records = records
	return &sdk.ChangeResourceRecordSetsOutput{}, nil
}

// fqdn returns rrs named as Route53 returns names: fully qualified, with a wildcard
// escaped.
func fqdn(rrs types.ResourceRecordSet) types.ResourceRecordSet {
	name := strings.TrimSuffix(aws.ToString(rrs.Name), ".") + "."
	if strings.HasPrefix(name, "*.") {
		name = `\052` + name[1:]
	}
	rrs.Name = aws.String(name)
	return rrs
}

func recordKey(name string, rrType types.RRType) string {
	return normalize(name) + "\x00" + string(rrType)
}

func sameKey(a, b types.ResourceRecordSet) bool {
	return recordKey(aws.ToString(a.Name), a.Type) == recordKey(aws.ToString(b.Name), b.Type)
}

func sortRecords(records []types.ResourceRecordSet) {
	slices.SortFunc(records, func(a, b types.ResourceRecordSet) int {
		return cmp.Compare(recordKey(aws.ToString(a.Name), a.Type), recordKey(aws.ToString(b.Name), b.Type))
	})
}
