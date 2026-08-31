package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestRhizaPersistsHostsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.CreateHost(ctx, NewHost{Name: "persistent", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", SSHPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.GetHost(ctx, host.ID)
	if err != nil || loaded == nil || loaded.Name != host.Name {
		t.Fatalf("persisted host = %#v, err=%v", loaded, err)
	}
}

func TestReadyPerformsLinearizableRead(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("ready store reported unavailable: %v", err)
	}
}

func TestOpenRejectsUnmarkedLegacySchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	config, err := rhizaConfigFromEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openRhiza(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE hosts (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, dir); err == nil || !strings.Contains(err.Error(), "empty greenfield data directory") {
		t.Fatalf("legacy schema was not rejected: %v", err)
	}
}

func TestIssueSSHAccessWritesDecisionGrantAndAuditAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	expiresAt := time.Now().Add(5 * time.Minute).Unix()
	if err := s.IssueSSHAccess(ctx, "user-1", "host-1", "ops", expiresAt); err != nil {
		t.Fatal(err)
	}
	requests, err := s.ListAccessRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := s.ListAccessGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Status != "approved" || requests[0].UserID != "user-1" {
		t.Fatalf("requests = %#v", requests)
	}
	if len(grants) != 1 || grants[0].RequestID != requests[0].ID || grants[0].ExpiresAt != expiresAt {
		t.Fatalf("grants = %#v", grants)
	}
	if len(events) != 1 || events[0].Action != "access.approved" || events[0].ResourceID != "host-1" || events[0].UserID != "user-1" {
		t.Fatalf("events = %#v", events)
	}
}

func TestCreateRelayAccessGrantWritesGrantAndAuditAtomically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	endpointID := strings.Repeat("a", 64)
	grant, err := s.CreateRelayAccessGrant(ctx, "host-1", endpointID, "user-1", 300)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := s.RelayEndpointAllowed(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || grant.ExpiresAt-grant.CreatedAt != 300 {
		t.Fatalf("grant = %#v, allowed = %v", grant, allowed)
	}
	if len(events) != 1 || events[0].Action != "relay.grant.created" || events[0].ResourceID != "host-1" || events[0].UserID != "user-1" {
		t.Fatalf("events = %#v", events)
	}
}

func TestRenewRelayAccessGrantReplacesPriorEndpointGrant(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	endpointID := strings.Repeat("c", 64)
	first, err := s.RenewRelayAccessGrant(ctx, "host-1", endpointID, "device:TEST-1", 300)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RenewRelayAccessGrant(ctx, "host-1", endpointID, "device:TEST-1", 300)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_access_grants WHERE host_id = ? AND client_endpoint_id = ?`, "host-1", endpointID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || first.ID == second.ID || second.ExpiresAt-second.CreatedAt != 300 {
		t.Fatalf("first=%#v second=%#v count=%d", first, second, count)
	}
}

func TestEnrollmentCreatesBoundHostAndDevice(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(ctx, "", &expires)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("a", 64), "TEST-000001", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, map[string]string{"env": "test"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.GetHost(ctx, device.HostID)
	if err != nil || host == nil || host.EndpointID != device.EndpointID || host.Name != device.SerialNumber {
		t.Fatalf("bound host = %#v, err=%v", host, err)
	}
}

func TestAuthorizedKeysGenerationIsStableAndMonotonic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	first, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("a", 64))
	if err != nil || first != 1 {
		t.Fatalf("first generation = %d, err=%v", first, err)
	}
	same, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("a", 64))
	if err != nil || same != first {
		t.Fatalf("stable generation = %d, err=%v", same, err)
	}
	next, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("b", 64))
	if err != nil || next != first+1 {
		t.Fatalf("next generation = %d, err=%v", next, err)
	}
}

func TestManufacturingBatchAssignsSerialAndClosesAtLimit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, token, err := s.CreateManufacturingBatch(ctx, "greenfield", "TEST", time.Now().Add(time.Hour).Unix(), 1)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, token, strings.Repeat("c", 64), "", "test", "SHA256:"+strings.Repeat("B", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil || device.SerialNumber != "TEST-000001" {
		t.Fatalf("device = %#v, err=%v", device, err)
	}
	if _, err := s.EnrollDevice(ctx, token, strings.Repeat("d", 64), "", "test", "SHA256:"+strings.Repeat("C", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil); err == nil {
		t.Fatal("closed manufacturing batch accepted another device")
	}
}
