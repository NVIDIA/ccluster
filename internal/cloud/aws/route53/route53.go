/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package route53 manages the Route53 hosted zone of each infra-plane cluster's domain:
// it creates the zone, delegates it from a parent zone the control-plane operator is
// given, keeps the zone's wildcard record pointing at the cluster's ingress, and deletes
// all of it again.
package route53

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The tags every hosted zone this package creates carries, so that the zones and the
// CCLInfraPlanes they belong to can be told apart in the AWS console and billing.
const (
	TagManagedBy      = "ccluster.nvidia.com/managed-by"
	TagManagedByValue = "ccluster-cp-operator"

	tagInfraPlaneName      = "ccluster.nvidia.com/infraplane-name"
	tagInfraPlaneNamespace = "ccluster.nvidia.com/infraplane-namespace"
)

// delegationTTL is the TTL of the NS record delegating a zone from the parent zone.
const delegationTTL = 300

// wildcardTTL is the TTL of the wildcard record, short so that resolvers follow a
// LoadBalancer whose address changes quickly.
const wildcardTTL = 60

// maxChangesPerBatch bounds a ChangeResourceRecordSets call while purging a zone, well
// under Route53's limit of 1,000 records per request.
const maxChangesPerBatch = 100

// ErrOutsideParentZone reports a domain that is not a subdomain of the parent zone's, so
// it cannot be delegated from it.
var ErrOutsideParentZone = errors.New("domain is not under the parent hosted zone")

// Owner identifies the CCLInfraPlane a hosted zone is created for. UID is what the zone
// is found by: it is the zone's CallerReference, which Route53 never changes.
type Owner struct {
	UID       string
	Namespace string
	Name      string
}

// Zone is a hosted zone and the name servers its delegation points at.
type Zone struct {
	// ID is the hosted zone's ID, without the "/hostedzone/" prefix.
	ID string
	// NameServers are lowercase, without a trailing dot, and sorted.
	NameServers []string
}

// Record is the value of a record set holding a single value, such as the wildcard
// record.
type Record struct {
	// Type is "A" for an IP address, or "CNAME" for a host name.
	Type string
	// Value is the IP address or the host name.
	Value string
}

// ZoneManager creates and deletes the hosted zone of an infra-plane cluster's domain, and
// the wildcard record in it. EnsureZone and DeleteZone take the zone ID last recorded for
// owner, empty if none, which spares a lookup by name; a wrong one is ignored.
type ZoneManager interface {
	// EnsureZone creates the hosted zone for domain if owner has none, and makes the
	// parent zone delegate domain to it. It returns ErrOutsideParentZone, wrapped, for a
	// domain that is not under the parent zone's.
	EnsureZone(ctx context.Context, domain string, owner Owner, knownID string) (Zone, error)

	// DeleteZone deletes owner's hosted zone for domain: first the parent zone's
	// delegation, so nothing resolves through the zone any more, then every record left
	// in it, then the zone. A zone that does not exist is not an error.
	DeleteZone(ctx context.Context, domain string, owner Owner, knownID string) error

	// SetWildcard makes "*.<domain>" in the hosted zone zoneID hold record alone, with a
	// short TTL, or deletes it if record is nil, and reports whether it changed it. A
	// change of type, A to CNAME or back, is made in one atomic batch, so the name never
	// stops resolving.
	SetWildcard(ctx context.Context, zoneID, domain string, record *Record) (bool, error)
}

// API is the part of the Route53 client Manager uses.
type API interface {
	GetHostedZone(context.Context, *sdk.GetHostedZoneInput, ...func(*sdk.Options)) (*sdk.GetHostedZoneOutput, error)
	ListHostedZonesByName(context.Context, *sdk.ListHostedZonesByNameInput, ...func(*sdk.Options)) (*sdk.ListHostedZonesByNameOutput, error)
	CreateHostedZone(context.Context, *sdk.CreateHostedZoneInput, ...func(*sdk.Options)) (*sdk.CreateHostedZoneOutput, error)
	DeleteHostedZone(context.Context, *sdk.DeleteHostedZoneInput, ...func(*sdk.Options)) (*sdk.DeleteHostedZoneOutput, error)
	ChangeTagsForResource(context.Context, *sdk.ChangeTagsForResourceInput, ...func(*sdk.Options)) (*sdk.ChangeTagsForResourceOutput, error)
	ListResourceRecordSets(context.Context, *sdk.ListResourceRecordSetsInput, ...func(*sdk.Options)) (*sdk.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(context.Context, *sdk.ChangeResourceRecordSetsInput, ...func(*sdk.Options)) (*sdk.ChangeResourceRecordSetsOutput, error)
}

// Manager is the Route53 ZoneManager. Each zone is a public hosted zone, delegated from
// the parent zone by an NS record.
type Manager struct {
	api          API
	parentZoneID string

	// parentDomain is read from Route53 on first use and kept, so a parent zone that
	// cannot be read is reported on the CCLInfraPlanes rather than failing startup.
	mu           sync.Mutex
	parentDomain string
}

var _ ZoneManager = (*Manager)(nil)

// New returns a Manager that delegates from the hosted zone parentZoneID.
func New(api API, parentZoneID string) *Manager {
	return &Manager{api: api, parentZoneID: zoneID(parentZoneID)}
}

// NewFromConfig returns a Manager on a Route53 client built from cfg.
func NewFromConfig(cfg aws.Config, parentZoneID string) *Manager {
	return New(sdk.NewFromConfig(cfg), parentZoneID)
}

// EnsureZone implements ZoneManager.
func (m *Manager) EnsureZone(ctx context.Context, domain string, owner Owner, knownID string) (Zone, error) {
	log := logf.FromContext(ctx)
	domain = normalize(domain)

	parent, err := m.checkDomain(ctx, domain)
	if err != nil {
		return Zone{}, err
	}

	zone, viaKnownID, err := m.findZone(ctx, domain, owner, knownID)
	if err != nil {
		return Zone{}, err
	}
	if zone == nil {
		if zone, err = m.createZone(ctx, domain, owner); err != nil {
			return Zone{}, err
		}
		log.Info("Created Route53 hosted zone", "domain", domain, "zoneID", zone.ID)
	}
	if !viaKnownID {
		// The zone was created, or found without having been recorded: either way a
		// previous attempt may have stopped before tagging it. Adding tags is idempotent.
		if err := m.tagZone(ctx, zone.ID, owner); err != nil {
			return Zone{}, err
		}
	}

	changed, err := m.ensureDelegation(ctx, domain, zone.NameServers)
	if err != nil {
		return Zone{}, err
	}
	if changed {
		log.Info("Delegated Route53 hosted zone from parent zone", "domain", domain,
			"zoneID", zone.ID, "parentZoneID", m.parentZoneID, "parentDomain", parent)
	}
	return *zone, nil
}

// DeleteZone implements ZoneManager.
func (m *Manager) DeleteZone(ctx context.Context, domain string, owner Owner, knownID string) error {
	log := logf.FromContext(ctx)
	domain = normalize(domain)

	if _, err := m.checkDomain(ctx, domain); err != nil {
		if errors.Is(err, ErrOutsideParentZone) {
			// No zone was ever created for it.
			return nil
		}
		return err
	}

	zone, _, err := m.findZone(ctx, domain, owner, knownID)
	if err != nil || zone == nil {
		return err
	}

	deleted, err := m.deleteDelegation(ctx, domain, zone.NameServers)
	if err != nil {
		return err
	}
	if deleted {
		log.Info("Deleted Route53 delegation from parent zone", "domain", domain,
			"zoneID", zone.ID, "parentZoneID", m.parentZoneID)
	}

	if err := m.purge(ctx, domain, zone.ID); err != nil {
		return err
	}

	switch _, err = m.api.DeleteHostedZone(ctx, &sdk.DeleteHostedZoneInput{Id: aws.String(zone.ID)}); {
	case isNoSuchHostedZone(err):
		return nil
	case err != nil:
		return fmt.Errorf("deleting hosted zone %s for %s: %w", zone.ID, domain, err)
	}
	log.Info("Deleted Route53 hosted zone", "domain", domain, "zoneID", zone.ID)
	return nil
}

// SetWildcard implements ZoneManager.
func (m *Manager) SetWildcard(ctx context.Context, id, domain string, record *Record) (bool, error) {
	id = zoneID(id)
	name := "*." + normalize(domain)

	var desired *types.ResourceRecordSet
	if record != nil {
		desired = &types.ResourceRecordSet{
			Name:            aws.String(name),
			Type:            types.RRType(record.Type),
			TTL:             aws.Int64(wildcardTTL),
			ResourceRecords: []types.ResourceRecord{{Value: aws.String(record.Value)}},
		}
	}

	// Every A or CNAME record set at the name that is not the desired one goes, and the
	// desired one is created unless it is already there, all in one batch.
	current, err := m.recordSets(ctx, id, name)
	if err != nil {
		return false, err
	}
	var changes []types.Change
	found := false
	for _, rrs := range current {
		if rrs.Type != types.RRTypeA && rrs.Type != types.RRTypeCname {
			continue
		}
		if desired != nil && sameRecordSet(rrs, *desired) {
			found = true
			continue
		}
		changes = append(changes, types.Change{Action: types.ChangeActionDelete, ResourceRecordSet: &rrs})
	}
	if desired != nil && !found {
		changes = append(changes, types.Change{Action: types.ChangeActionCreate, ResourceRecordSet: desired})
	}
	if len(changes) == 0 {
		return false, nil
	}
	if err := m.changeRecords(ctx, id, changes); err != nil {
		return false, fmt.Errorf("setting %s in hosted zone %s: %w", name, id, err)
	}
	return true, nil
}

// recordSets returns the zone's record sets named name.
func (m *Manager) recordSets(ctx context.Context, id, name string) ([]types.ResourceRecordSet, error) {
	out, err := m.api.ListResourceRecordSets(ctx, &sdk.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(id),
		StartRecordName: aws.String(name),
		// A name holds at most a handful of types; those of other names stop the scan.
		MaxItems: aws.Int32(10),
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s in hosted zone %s: %w", name, id, err)
	}
	var sets []types.ResourceRecordSet
	for _, rrs := range out.ResourceRecordSets {
		if normalize(aws.ToString(rrs.Name)) == name {
			sets = append(sets, rrs)
		}
	}
	return sets, nil
}

// sameRecordSet reports whether two simple record sets hold the same type, TTL and values.
func sameRecordSet(a, b types.ResourceRecordSet) bool {
	return a.Type == b.Type && aws.ToInt64(a.TTL) == aws.ToInt64(b.TTL) && a.AliasTarget == nil &&
		slices.Equal(recordValues(&a), recordValues(&b))
}

// checkDomain returns the parent zone's domain, or ErrOutsideParentZone if domain is not
// strictly under it.
func (m *Manager) checkDomain(ctx context.Context, domain string) (string, error) {
	parent, err := m.parent(ctx)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(domain, "."+parent) {
		return "", fmt.Errorf("%w: %q is not a subdomain of %q, the domain of hosted zone %s",
			ErrOutsideParentZone, domain, parent, m.parentZoneID)
	}
	return parent, nil
}

// parent returns the parent zone's domain, reading it from Route53 the first time.
func (m *Manager) parent(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.parentDomain != "" {
		return m.parentDomain, nil
	}
	out, err := m.api.GetHostedZone(ctx, &sdk.GetHostedZoneInput{Id: aws.String(m.parentZoneID)})
	if err != nil {
		return "", fmt.Errorf("reading parent hosted zone %s: %w", m.parentZoneID, err)
	}
	m.parentDomain = normalize(aws.ToString(out.HostedZone.Name))
	return m.parentDomain, nil
}

// findZone returns owner's hosted zone for domain, or nil if there is none, and whether
// it was knownID. A zone is owner's when its CallerReference is owner's UID, which is
// what createZone sets.
func (m *Manager) findZone(
	ctx context.Context,
	domain string,
	owner Owner,
	knownID string,
) (*Zone, bool, error) {
	if knownID != "" {
		zone, err := m.getZone(ctx, zoneID(knownID), domain, owner)
		if err != nil || zone != nil {
			return zone, zone != nil, err
		}
	}

	input := &sdk.ListHostedZonesByNameInput{DNSName: aws.String(domain)}
	for {
		out, err := m.api.ListHostedZonesByName(ctx, input)
		if err != nil {
			return nil, false, fmt.Errorf("listing hosted zones named %s: %w", domain, err)
		}
		// Zones are listed in name order starting at domain, so the first with another
		// name ends the search.
		for _, hz := range out.HostedZones {
			if normalize(aws.ToString(hz.Name)) != domain {
				return nil, false, nil
			}
			if ownedBy(hz, owner) {
				zone, err := m.getZone(ctx, zoneID(aws.ToString(hz.Id)), domain, owner)
				return zone, false, err
			}
		}
		if !out.IsTruncated {
			return nil, false, nil
		}
		input.DNSName = out.NextDNSName
		input.HostedZoneId = out.NextHostedZoneId
	}
}

// getZone reads the hosted zone id, and returns it if it is owner's zone for domain, or
// nil if it is not, or does not exist.
func (m *Manager) getZone(ctx context.Context, id, domain string, owner Owner) (*Zone, error) {
	out, err := m.api.GetHostedZone(ctx, &sdk.GetHostedZoneInput{Id: aws.String(id)})
	if isNoSuchHostedZone(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading hosted zone %s: %w", id, err)
	}
	if normalize(aws.ToString(out.HostedZone.Name)) != domain || !ownedBy(*out.HostedZone, owner) {
		return nil, nil
	}
	return &Zone{ID: id, NameServers: nameServers(out.DelegationSet)}, nil
}

// createZone creates owner's hosted zone for domain.
//
// The CallerReference is owner's UID, so a request whose response was lost cannot create
// a second zone. Route53 also refuses a CallerReference it has seen before, even for a
// zone since deleted by hand; when that refusal comes with no zone of owner's to be found,
// the request is repeated with a unique suffix, which ownedBy also accepts.
func (m *Manager) createZone(ctx context.Context, domain string, owner Owner) (*Zone, error) {
	input := &sdk.CreateHostedZoneInput{
		Name:            aws.String(domain),
		CallerReference: aws.String(owner.UID),
		HostedZoneConfig: &types.HostedZoneConfig{
			Comment: aws.String(fmt.Sprintf("ccluster CCLInfraPlane %s/%s", owner.Namespace, owner.Name)),
		},
	}
	out, err := m.api.CreateHostedZone(ctx, input)
	if isHostedZoneAlreadyExists(err) {
		zone, _, findErr := m.findZone(ctx, domain, owner, "")
		if findErr != nil || zone != nil {
			return zone, findErr
		}
		input.CallerReference = aws.String(owner.UID + "-" + strconv.FormatInt(time.Now().UnixNano(), 36))
		out, err = m.api.CreateHostedZone(ctx, input)
	}
	if err != nil {
		return nil, fmt.Errorf("creating hosted zone %s: %w", domain, err)
	}
	return &Zone{ID: zoneID(aws.ToString(out.HostedZone.Id)), NameServers: nameServers(out.DelegationSet)}, nil
}

// tagZone adds the tags marking the zone as the operator's, and naming its owner.
func (m *Manager) tagZone(ctx context.Context, id string, owner Owner) error {
	_, err := m.api.ChangeTagsForResource(ctx, &sdk.ChangeTagsForResourceInput{
		ResourceId:   aws.String(id),
		ResourceType: types.TagResourceTypeHostedzone,
		AddTags: []types.Tag{
			{Key: aws.String(TagManagedBy), Value: aws.String(TagManagedByValue)},
			{Key: aws.String(tagInfraPlaneNamespace), Value: aws.String(owner.Namespace)},
			{Key: aws.String(tagInfraPlaneName), Value: aws.String(owner.Name)},
		},
	})
	if err != nil {
		return fmt.Errorf("tagging hosted zone %s: %w", id, err)
	}
	return nil
}

// ensureDelegation makes the parent zone's NS record for domain point at nameServers,
// and reports whether it had to change it.
func (m *Manager) ensureDelegation(ctx context.Context, domain string, nameServers []string) (bool, error) {
	current, err := m.delegation(ctx, domain)
	if err != nil {
		return false, err
	}
	if current != nil && slices.Equal(recordValues(current), nameServers) {
		return false, nil
	}

	records := make([]types.ResourceRecord, 0, len(nameServers))
	for _, ns := range nameServers {
		records = append(records, types.ResourceRecord{Value: aws.String(ns)})
	}
	err = m.changeRecords(ctx, m.parentZoneID, []types.Change{{
		Action: types.ChangeActionUpsert,
		ResourceRecordSet: &types.ResourceRecordSet{
			Name:            aws.String(domain),
			Type:            types.RRTypeNs,
			TTL:             aws.Int64(delegationTTL),
			ResourceRecords: records,
		},
	}})
	if err != nil {
		return false, fmt.Errorf("delegating %s from hosted zone %s: %w", domain, m.parentZoneID, err)
	}
	return true, nil
}

// deleteDelegation deletes the parent zone's NS record for domain if it points at
// nameServers, and reports whether it did. A delegation to any other name servers is not
// this zone's, and is left alone.
func (m *Manager) deleteDelegation(ctx context.Context, domain string, nameServers []string) (bool, error) {
	current, err := m.delegation(ctx, domain)
	if err != nil || current == nil || !slices.Equal(recordValues(current), nameServers) {
		return false, err
	}
	err = m.changeRecords(ctx, m.parentZoneID, []types.Change{{
		Action:            types.ChangeActionDelete,
		ResourceRecordSet: current,
	}})
	if err != nil {
		return false, fmt.Errorf("deleting the delegation of %s from hosted zone %s: %w", domain, m.parentZoneID, err)
	}
	return true, nil
}

// delegation returns the parent zone's NS record set for domain, or nil if it has none.
func (m *Manager) delegation(ctx context.Context, domain string) (*types.ResourceRecordSet, error) {
	out, err := m.api.ListResourceRecordSets(ctx, &sdk.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(m.parentZoneID),
		StartRecordName: aws.String(domain),
		StartRecordType: types.RRTypeNs,
		MaxItems:        aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("reading the delegation of %s from hosted zone %s: %w", domain, m.parentZoneID, err)
	}
	if len(out.ResourceRecordSets) == 0 {
		return nil, nil
	}
	rrs := out.ResourceRecordSets[0]
	if normalize(aws.ToString(rrs.Name)) != domain || rrs.Type != types.RRTypeNs {
		return nil, nil
	}
	return &rrs, nil
}

// purge deletes every record set in the zone but the SOA and NS records at its apex,
// which Route53 manages and deletes with the zone.
func (m *Manager) purge(ctx context.Context, domain, id string) error {
	var changes []types.Change
	input := &sdk.ListResourceRecordSetsInput{HostedZoneId: aws.String(id)}
	for {
		out, err := m.api.ListResourceRecordSets(ctx, input)
		if isNoSuchHostedZone(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("listing the records of hosted zone %s: %w", id, err)
		}
		for _, rrs := range out.ResourceRecordSets {
			apex := normalize(aws.ToString(rrs.Name)) == domain
			if apex && (rrs.Type == types.RRTypeSoa || rrs.Type == types.RRTypeNs) {
				continue
			}
			changes = append(changes, types.Change{Action: types.ChangeActionDelete, ResourceRecordSet: &rrs})
		}
		if !out.IsTruncated {
			break
		}
		input.StartRecordName = out.NextRecordName
		input.StartRecordType = out.NextRecordType
		input.StartRecordIdentifier = out.NextRecordIdentifier
	}

	for batch := range slices.Chunk(changes, maxChangesPerBatch) {
		if err := m.changeRecords(ctx, id, batch); err != nil {
			return fmt.Errorf("deleting the records of hosted zone %s: %w", id, err)
		}
	}
	if len(changes) > 0 {
		logf.FromContext(ctx).Info("Deleted records left in Route53 hosted zone",
			"domain", domain, "zoneID", id, "count", len(changes))
	}
	return nil
}

// changeRecords applies changes to the zone id.
func (m *Manager) changeRecords(ctx context.Context, id string, changes []types.Change) error {
	_, err := m.api.ChangeResourceRecordSets(ctx, &sdk.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(id),
		ChangeBatch:  &types.ChangeBatch{Changes: changes},
	})
	return err
}

// ownedBy reports whether hz was created for owner: whether its CallerReference is
// owner's UID, or that UID with the suffix createZone adds on a retry.
func ownedBy(hz types.HostedZone, owner Owner) bool {
	ref := aws.ToString(hz.CallerReference)
	return owner.UID != "" && (ref == owner.UID || strings.HasPrefix(ref, owner.UID+"-"))
}

// nameServers returns a delegation set's name servers in the form Zone holds them.
func nameServers(set *types.DelegationSet) []string {
	if set == nil {
		return nil
	}
	servers := make([]string, 0, len(set.NameServers))
	for _, ns := range set.NameServers {
		servers = append(servers, normalize(ns))
	}
	slices.Sort(servers)
	return servers
}

// recordValues returns a record set's values in the form Zone holds name servers.
func recordValues(rrs *types.ResourceRecordSet) []string {
	values := make([]string, 0, len(rrs.ResourceRecords))
	for _, rr := range rrs.ResourceRecords {
		values = append(values, normalize(aws.ToString(rr.Value)))
	}
	slices.Sort(values)
	return values
}

// normalize lowercases a DNS name, drops its trailing dot, which Route53 returns and a
// CCLInfraPlane's domain never has, and turns the escaped wildcard Route53 returns back
// into "*".
func normalize(name string) string {
	name = strings.ReplaceAll(name, `\052`, "*")
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// zoneID strips the "/hostedzone/" prefix Route53 returns zone IDs with.
func zoneID(id string) string {
	return strings.TrimPrefix(id, "/hostedzone/")
}

func isNoSuchHostedZone(err error) bool {
	var target *types.NoSuchHostedZone
	return errors.As(err, &target)
}

func isHostedZoneAlreadyExists(err error) bool {
	var target *types.HostedZoneAlreadyExists
	return errors.As(err, &target)
}
