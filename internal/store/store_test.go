package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOpenRejectsMismatchedSchemaVersion(t *testing.T) {
	t.Setenv("TERNAL_DATA_SCHEMA_VERSION", "2")
	if _, err := Open(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "must be 1") {
		t.Fatalf("mismatched schema version was accepted: %v", err)
	}
}

func TestRhizaPersistsHostsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.CreateHost(ctx, NewHost{Name: "persistent", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", SSHPort: 22}, "system")
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

func TestRhizaRestoresIntoEmptyCacheFromObjectStore(t *testing.T) {
	ctx := context.Background()
	objectStore := t.TempDir()
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "empty-cache-recovery")
	t.Setenv("TERNAL_DATA_NODE_ID", "node-1")
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", "filesystem")
	t.Setenv("TERNAL_OBJECT_STORE_DIR", objectStore)
	t.Setenv("TERNAL_OBJECT_STORE_PREFIX", "clusters/empty-cache-recovery")
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "test-anchor")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	binding := storageBindingFromEnv()
	record := binding
	record.Token = uuid.NewString()
	record.Bootstrap = true
	anchor := &memoryAnchor{record: record}
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return anchor }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.CreateHost(ctx, NewHost{Name: "object-store", EndpointID: strings.Repeat("b", 64), SSHUser: "ops", SSHPort: 22}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	loaded, err := restored.GetHost(ctx, host.ID)
	if err != nil || loaded == nil || loaded.Name != host.Name {
		t.Fatalf("object-store restored host = %#v, err=%v", loaded, err)
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

func TestSessionRevocationCleanupRetainsRecentlyExpired(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	recentlyExpired := nowUnix() - int64((sessionRevocationRetention/2)/time.Second)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO revoked_sessions (cookie_hash, expires_at) VALUES ('recently-expired', ?)`, recentlyExpired); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "current-session", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_sessions WHERE cookie_hash = 'recently-expired'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Fatal("recently expired revocation was removed before the clock-skew grace elapsed")
	}
	if got := sessionRevocationCleanupCutoff(1000); got != 1000-int64(sessionRevocationRetention/time.Second) {
		t.Fatalf("cleanup cutoff = %d", got)
	}
}

func TestPolicyPrincipalPersists(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	created, err := s.CreatePolicy(ctx, NewPolicy{Name: "support", Principal: "role=support", HostSelector: "*", SSHUsers: []string{"ops"}}, "system")
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
	if !hasAuditEvent(events, "access.approved", hostID, "user-1") {
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
	if !hasAuditEvent(events, "relay.grant.created", hostID, "user-1") {
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
	token, err := s.CreateManufacturingToken(t.Context(), "", &expires, "system")
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

func hasAuditEvent(events []AuditEvent, action, resourceID, userID string) bool {
	for _, event := range events {
		if event.Action == action && event.ResourceID == resourceID && event.UserID == userID {
			return true
		}
	}
	return false
}

func TestAdminAuditEventsRequireActorAndSkipNoops(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const actor = "admin@example.com"
	if err := s.RecordPolicyDenied(ctx, "access.ssh.denied", "host-1", ""); err == nil {
		t.Fatal("empty policy-denial audit actor accepted")
	}
	hostInput := NewHost{Name: "audit-host", EndpointID: strings.Repeat("b", 64), SSHUser: "ops", SSHPort: 22}
	if _, err := s.CreateHost(ctx, hostInput, ""); err == nil {
		t.Fatal("empty audit actor accepted")
	}
	host, err := s.CreateHost(ctx, hostInput, actor)
	if err != nil {
		t.Fatal(err)
	}
	hostInput.Status = "unknown"
	if err := s.UpdateHost(ctx, host.ID, hostInput, actor); err != nil {
		t.Fatal(err)
	}
	hostInput.Name = "audit-host-updated"
	if err := s.UpdateHost(ctx, host.ID, hostInput, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost(ctx, "missing", actor); err != nil {
		t.Fatal(err)
	}
	policy, err := s.CreatePolicy(ctx, NewPolicy{Name: "audit-policy", Principal: "role=ops", HostSelector: "*", SSHUsers: []string{"ops"}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	updatedPolicy := NewPolicy{Name: "audit-policy", Principal: "role=ops", HostSelector: "env=prod", SSHUsers: []string{"ops"}}
	if err := s.UpdatePolicy(ctx, policy.ID, updatedPolicy, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePolicy(ctx, policy.ID, actor); err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateSSHKey(ctx, "other-user", "ssh-ed25519 test", "SHA256:test")
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.DeleteSSHKeyForUser(ctx, key.ID, actor, true); err != nil || !deleted {
		t.Fatalf("admin key delete = %v, %v", deleted, err)
	}
	if deleted, err := s.DeleteSSHKeyForUser(ctx, key.ID, actor, true); err != nil || deleted {
		t.Fatalf("missing admin key delete = %v, %v", deleted, err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	if _, err := s.CreateManufacturingToken(ctx, "", &expires, actor); err != nil {
		t.Fatal(err)
	}
	batch, token, err := s.CreateManufacturingBatch(ctx, "audit-batch", "AUDIT", expires, 1, actor)
	if err != nil || token == "" {
		t.Fatalf("batch = %#v token=%q err=%v", batch, token, err)
	}
	if _, _, err := s.CreateManufacturingBatch(ctx, "audit-batch", "OTHER", expires, 1, actor); err == nil {
		t.Fatal("duplicate batch accepted")
	}
	if err := s.CloseManufacturingBatch(ctx, batch.ID, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseManufacturingBatch(ctx, batch.ID, actor); err != nil {
		t.Fatal(err)
	}
	deviceHostID := createActiveTestHost(t, s)
	device, err := s.GetDeviceByHostID(ctx, deviceHostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, device.ID, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, device.ID, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHost(ctx, host.ID, actor); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListAuditEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ action, id string }{
		{"host.created", host.ID}, {"host.updated", host.ID}, {"host.deleted", host.ID},
		{"policy.created", policy.ID}, {"policy.updated", policy.ID}, {"policy.deleted", policy.ID},
		{"ssh_key.deleted", key.ID}, {"manufacturing.token.created", ""},
		{"manufacturing.batch.created", batch.ID}, {"manufacturing.batch.closed", batch.ID},
		{"device.revoked", device.ID},
	} {
		if check.id == "" {
			found := false
			for _, event := range events {
				found = found || (event.Action == check.action && event.UserID == actor)
			}
			if !found {
				t.Fatalf("missing %s audit event: %#v", check.action, events)
			}
		} else if !hasAuditEvent(events, check.action, check.id, actor) {
			t.Fatalf("missing %s/%s audit event: %#v", check.action, check.id, events)
		}
	}
	for _, event := range events {
		if event.UserID == actor && (event.Action == "host.updated" || event.Action == "manufacturing.batch.closed" || event.Action == "device.revoked") && len(event.After) != 0 {
			t.Fatalf("audit event leaked payload: %#v", event)
		}
	}
	counts := map[string]int{}
	for _, event := range events {
		if event.UserID == actor {
			counts[event.Action]++
		}
	}
	for _, action := range []string{"host.created", "host.updated", "host.deleted", "policy.created", "policy.updated", "policy.deleted", "ssh_key.deleted", "manufacturing.token.created", "manufacturing.batch.created", "manufacturing.batch.closed", "device.revoked"} {
		if counts[action] != 1 {
			t.Fatalf("%s audit count = %d, want 1", action, counts[action])
		}
	}
}

func TestRecordPolicyDeniedPropagatesDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPolicyDenied(ctx, "access.ssh.denied", "host-1", "operator@example.com"); err == nil {
		t.Fatal("database audit failure was swallowed")
	}
	events, err := s.ListAuditEvents(ctx, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after failed audit = %#v, err=%v", events, err)
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
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "system")
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
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "system")
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

	if err := s.DeleteDevice(ctx, device.ID, "system"); err != nil {
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
	if _, err := s.CreateAccessGrant(ctx, "request-same-key", "user-1", hostID, "ops", time.Now().Add(time.Minute).Unix(), ""); err != nil {
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
	if err != nil || !installed["request-1"] || installed["request-same-key"] {
		t.Fatalf("old snapshot acknowledgement over-marked identical-key grant = %#v, err=%v", grants, err)
	}
	keys, snapshotGrants, err = s.AuthorizedKeysSnapshotForHost(ctx, hostID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	sameDigest := hashSecret(strings.Join(keys, "\n") + "\n")
	sameGeneration, err := s.AuthorizedKeysGeneration(ctx, hostID, "ops", sameDigest, snapshotGrants)
	if err != nil {
		t.Fatal(err)
	}
	if sameDigest != digest || sameGeneration <= generation {
		t.Fatalf("identical-key grant snapshot digest/generation = %s/%d, want %s/>%d", sameDigest, sameGeneration, digest, generation)
	}
	if err := s.AcknowledgeAuthorizedKeys(ctx, hostID, "ops", generation, digest); err == nil {
		t.Fatal("snapshot acknowledgement with stale grant set was accepted")
	}
	generation = sameGeneration
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
	grants, err = s.ListAccessGrants(ctx)
	installed = map[string]bool{}
	for _, grant := range grants {
		installed[grant.RequestID] = grant.KeyInstalled
	}
	if err != nil || !installed["request-1"] || !installed["request-same-key"] || installed["request-2"] {
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
	if err != nil || len(grants) != 4 || !installed["request-1"] || !installed["request-same-key"] || !installed["request-2"] || installed["request-3"] {
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
	_, token, err := s.CreateManufacturingBatch(ctx, "greenfield", "TEST", time.Now().Add(time.Hour).Unix(), 1, "system")
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

func TestEnrollDeviceRollsBackCredentialConsumptionOnInsertFailure(t *testing.T) {
	t.Run("one-time token", func(t *testing.T) {
		ctx := t.Context()
		s, err := Open(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		conflict, err := s.CreateHost(ctx, NewHost{Name: "DUPLICATE", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", SSHPort: 22}, "system")
		if err != nil {
			t.Fatal(err)
		}
		token, err := s.CreateManufacturingToken(ctx, "", nil, "system")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("b", 64), "DUPLICATE", "test", "SHA256:"+strings.Repeat("A", 43), "device-key", "ops", 22, nil); err == nil {
			t.Fatal("enrollment with duplicate host name succeeded")
		}
		if available, err := s.GetManufacturingToken(ctx, token.Token); err != nil || available == nil {
			t.Fatalf("manufacturing token was consumed: %#v, err=%v", available, err)
		}
		if err := s.DeleteHost(ctx, conflict.ID, "system"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("b", 64), "DUPLICATE", "test", "SHA256:"+strings.Repeat("A", 43), "device-key", "ops", 22, nil); err != nil {
			t.Fatalf("token was not reusable after rollback: %v", err)
		}
	})

	t.Run("batch slot", func(t *testing.T) {
		ctx := t.Context()
		s, err := Open(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		conflict, err := s.CreateHost(ctx, NewHost{Name: "DUPLICATE-000001", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", SSHPort: 22}, "system")
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := s.CreateManufacturingBatch(ctx, "rollback", "DUPLICATE", time.Now().Add(time.Hour).Unix(), 2, "system")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnrollDevice(ctx, token, strings.Repeat("b", 64), "", "test", "SHA256:"+strings.Repeat("A", 43), "device-key", "ops", 22, nil); err == nil {
			t.Fatal("enrollment with duplicate host name succeeded")
		}
		batches, err := s.ListManufacturingBatches(ctx)
		if err != nil || len(batches) != 1 || batches[0].UsedCount != 0 || batches[0].Status != "open" {
			t.Fatalf("manufacturing batch changed after rollback: %#v, err=%v", batches, err)
		}
		if err := s.DeleteHost(ctx, conflict.ID, "system"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnrollDevice(ctx, token, strings.Repeat("b", 64), "", "test", "SHA256:"+strings.Repeat("A", 43), "device-key", "ops", 22, nil); err != nil {
			t.Fatalf("batch slot was not reusable after rollback: %v", err)
		}
	})
}
