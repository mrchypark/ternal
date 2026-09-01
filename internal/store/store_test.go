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

func TestSessionRevocationPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "signed-session", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	revoked, err := s.SessionRevoked(ctx, "signed-session")
	if err != nil || !revoked {
		t.Fatalf("revoked = %v, err = %v", revoked, err)
	}
}

func TestSessionRevocationCleanupIsBounded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.db.ExecContext(ctx, `
		WITH RECURSIVE expired(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM expired WHERE n < 101)
		INSERT INTO revoked_sessions (cookie_hash, expires_at) SELECT printf('expired-%d', n), 0 FROM expired
	`); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "current-session", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_sessions WHERE expires_at = 0`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expired revocations remaining = %d, want 1", expired)
	}
	revoked, err := s.SessionRevoked(ctx, "current-session")
	if err != nil || !revoked {
		t.Fatalf("current session revoked = %v, err = %v", revoked, err)
	}
}

func TestPolicyPrincipalPersists(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	created, err := s.CreatePolicy(ctx, NewPolicy{Name: "support", Principal: "role=support", HostSelector: "*", SSHUsers: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := s.ListPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].ID != created.ID || policies[0].Principal != "role=support" {
		t.Fatalf("policies = %#v", policies)
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
	hostID := createActiveTestHost(t, s)

	expiresAt := time.Now().Add(5 * time.Minute).Unix()
	if err := s.IssueSSHAccess(ctx, "user-1", hostID, "ops", expiresAt); err != nil {
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
	if len(events) != 1 || events[0].Action != "access.approved" || events[0].ResourceID != hostID || events[0].UserID != "user-1" {
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
	hostID := createActiveTestHost(t, s)

	endpointID := strings.Repeat("a", 64)
	grant, err := s.CreateRelayAccessGrant(ctx, hostID, endpointID, "user-1", 300)
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
	if len(events) != 1 || events[0].Action != "relay.grant.created" || events[0].ResourceID != hostID || events[0].UserID != "user-1" {
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
	hostID := createActiveTestHost(t, s)

	endpointID := strings.Repeat("c", 64)
	first, err := s.RenewRelayAccessGrant(ctx, hostID, endpointID, "device:TEST-1", 300)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RenewRelayAccessGrant(ctx, hostID, endpointID, "device:TEST-1", 300)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_access_grants WHERE host_id = ? AND client_endpoint_id = ?`, hostID, endpointID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || first.ID == second.ID || second.ExpiresAt-second.CreatedAt != 300 {
		t.Fatalf("first=%#v second=%#v count=%d", first, second, count)
	}
}

func createActiveTestHost(t *testing.T, s *Store) string {
	t.Helper()
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(t.Context(), "", &expires)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(t.Context(), token.Token, strings.Repeat("9", 64), "TEST-ACTIVE", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	return device.HostID
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

func TestDeleteDeviceAtomicallyRevokesAccessAndRelayGrants(t *testing.T) {
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
	device, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("e", 64), "TEST-REVOKE", "test", "SHA256:"+strings.Repeat("D", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEndpointDiscovery(ctx, device.HostID, []string{"127.0.0.1:1234"}, []string{"https://relay.example"}); err != nil {
		t.Fatal(err)
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D test"
	if _, err := s.CreateSSHKey(ctx, "user-1", key, "SHA256:test"); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueSSHAccess(ctx, "user-1", device.HostID, "ops", time.Now().Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	clientEndpointID := strings.Repeat("f", 64)
	if _, err := s.CreateRelayAccessGrant(ctx, device.HostID, clientEndpointID, "user-1", 300); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	revoked, err := s.GetDeviceByHostID(ctx, device.HostID)
	if err != nil || revoked == nil || revoked.State != "revoked" {
		t.Fatalf("revoked device = %#v, err=%v", revoked, err)
	}
	host, err := s.GetHost(ctx, device.HostID)
	if err != nil || host == nil || host.Status != "revoked" {
		t.Fatalf("revoked host = %#v, err=%v", host, err)
	}
	if discovery, err := s.GetEndpointDiscovery(ctx, device.HostID); err != nil || discovery != nil {
		t.Fatalf("revoked discovery = %#v, err=%v", discovery, err)
	}
	if allowed, err := s.RelayEndpointAllowed(ctx, clientEndpointID); err != nil || allowed {
		t.Fatalf("revoked relay admission = %v, err=%v", allowed, err)
	}
	if keys, err := s.AuthorizedKeysForHost(ctx, device.HostID, "ops"); err != nil || len(keys) != 0 {
		t.Fatalf("revoked authorized keys = %#v, err=%v", keys, err)
	}
	if err := s.IssueSSHAccess(ctx, "user-1", device.HostID, "ops", time.Now().Add(5*time.Minute).Unix()); err == nil {
		t.Fatal("revoked host received a new SSH grant")
	}
	if _, err := s.CreateRelayAccessGrant(ctx, device.HostID, clientEndpointID, "user-1", 300); err == nil {
		t.Fatal("revoked host received a new relay grant")
	}
	if _, err := s.RenewRelayAccessGrant(ctx, device.HostID, clientEndpointID, "device:"+device.SerialNumber, 300); err == nil {
		t.Fatal("revoked host renewed a relay grant")
	}
	if err := s.TouchDevice(ctx, device.ID, device.HostID, device.EndpointID, device.SSHHostKeyFingerprint, "healthy", []string{"127.0.0.1:1234"}, []string{"https://relay.example"}); err == nil {
		t.Fatal("revoked device heartbeat was accepted")
	}
	host, err = s.GetHost(ctx, device.HostID)
	if err != nil || host == nil || host.Status != "revoked" {
		t.Fatalf("revoked heartbeat changed host = %#v, err=%v", host, err)
	}
}

func TestAuthorizedKeysGenerationIsStableAndMonotonic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	first, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("a", 64), nil)
	if err != nil || first != 1 {
		t.Fatalf("first generation = %d, err=%v", first, err)
	}
	same, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("a", 64), nil)
	if err != nil || same != first {
		t.Fatalf("stable generation = %d, err=%v", same, err)
	}
	next, err := s.AuthorizedKeysGeneration(ctx, "host-1", "ops", strings.Repeat("b", 64), nil)
	if err != nil || next != first+1 {
		t.Fatalf("next generation = %d, err=%v", next, err)
	}
}

func TestAuthorizedKeysAcknowledgementRequiresExactSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	hostID := createActiveTestHost(t, s)
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D test"
	if _, err := s.CreateSSHKey(ctx, "user-1", key, "SHA256:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessGrant(ctx, "request-1", "user-1", hostID, "ops", time.Now().Add(time.Minute).Unix(), ""); err != nil {
		t.Fatal(err)
	}
	keys, snapshotGrants, err := s.AuthorizedKeysSnapshotForHost(ctx, hostID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	digest := hashSecret(strings.Join(keys, "\n") + "\n")
	generation, err := s.AuthorizedKeysGeneration(ctx, hostID, "ops", digest, snapshotGrants)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAuthorizedKeys(ctx, hostID, "ops", generation, strings.Repeat("b", 64)); err == nil {
		t.Fatal("mismatched authorized_keys acknowledgement was accepted")
	}
	secondKey := key + " second"
	if _, err := s.CreateSSHKey(ctx, "user-2", secondKey, "SHA256:second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessGrant(ctx, "request-2", "user-2", hostID, "ops", time.Now().Add(time.Minute).Unix(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAuthorizedKeys(ctx, hostID, "ops", generation, digest); err != nil {
		t.Fatal(err)
	}
	grants, err := s.ListAccessGrants(ctx)
	installed := map[string]bool{}
	for _, grant := range grants {
		installed[grant.RequestID] = grant.KeyInstalled
	}
	if err != nil || !installed["request-1"] || installed["request-2"] {
		t.Fatalf("old snapshot acknowledgement over-marked grants = %#v, err=%v", grants, err)
	}
	keys, snapshotGrants, err = s.AuthorizedKeysSnapshotForHost(ctx, hostID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	nextDigest := hashSecret(strings.Join(keys, "\n") + "\n")
	if _, err := s.AuthorizedKeysGeneration(ctx, hostID, "ops", nextDigest, snapshotGrants); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAuthorizedKeys(ctx, hostID, "ops", generation, digest); err == nil {
		t.Fatal("stale authorized_keys acknowledgement was accepted")
	}
	generation, digest = generation+1, nextDigest
	if _, err := s.CreateAccessGrant(ctx, "request-3", "user-without-key", hostID, "ops", time.Now().Add(time.Minute).Unix(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAuthorizedKeys(ctx, hostID, "ops", generation, digest); err != nil {
		t.Fatal(err)
	}
	grants, err = s.ListAccessGrants(ctx)
	installed = map[string]bool{}
	for _, grant := range grants {
		installed[grant.RequestID] = grant.KeyInstalled
	}
	if err != nil || len(grants) != 3 || !installed["request-1"] || !installed["request-2"] || installed["request-3"] {
		t.Fatalf("acknowledged grants = %#v, err=%v", grants, err)
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
