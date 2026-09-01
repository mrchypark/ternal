package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
	"github.com/mrchypark/ternal/internal/core"
)

const sessionRevocationRetention = 10 * time.Minute

type Store struct {
	db   *rhizaSQL
	mu   sync.RWMutex
	path string
}

type NewHost struct {
	Name       string            `json:"name"`
	EndpointID string            `json:"endpoint_id"`
	SSHUser    string            `json:"ssh_user,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	SSHPort    uint16            `json:"ssh_port,omitempty"`
	Status     string            `json:"status,omitempty"`
	Owner      string            `json:"owner,omitempty"`
	LastSeen   *int64            `json:"last_seen,omitempty"`
}

type NewPolicy struct {
	Name         string   `json:"name"`
	Principal    string   `json:"principal"`
	HostSelector string   `json:"host_selector"`
	SSHUsers     []string `json:"ssh_users"`
	ExpiresAt    *int64   `json:"expires_at,omitempty"`
}

type SshKey struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   int64  `json:"created_at"`
}

type AccessRequest struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	HostID    string `json:"host_id"`
	SSHUser   string `json:"ssh_user"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

type AccessGrant struct {
	ID           string `json:"id"`
	RequestID    string `json:"request_id"`
	UserID       string `json:"user_id"`
	HostID       string `json:"host_id"`
	SSHUser      string `json:"ssh_user"`
	ExpiresAt    int64  `json:"expires_at"`
	EphemeralKey string `json:"ephemeral_key,omitempty"`
	KeyInstalled bool   `json:"key_installed"`
	CreatedAt    int64  `json:"created_at"`
}

type AuditEvent struct {
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource"`
	ResourceID string          `json:"resource_id"`
	UserID     string          `json:"user_id,omitempty"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	CreatedAt  int64           `json:"created_at"`
}

type ManufacturingToken struct {
	ID        string `json:"id"`
	Token     string `json:"token,omitempty"`
	BatchID   string `json:"batch_id,omitempty"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type ManufacturingBatch struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SerialPrefix string `json:"serial_prefix"`
	Status       string `json:"state"`
	ExpiresAt    int64  `json:"expires_at"`
	MaxDevices   int64  `json:"max_devices"`
	UsedCount    int64  `json:"used_count"`
	ClosedAt     *int64 `json:"closed_at,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

type Device struct {
	ID                    string `json:"id"`
	HostID                string `json:"host_id"`
	EndpointID            string `json:"endpoint_id"`
	SSHHostKeyFingerprint string `json:"ssh_host_key_fingerprint"`
	DevicePublicKey       string `json:"device_public_key"`
	State                 string `json:"state"`
	SerialNumber          string `json:"serial_number,omitempty"`
	Model                 string `json:"model,omitempty"`
	EnrolledAt            int64  `json:"enrolled_at"`
	LastSeenAt            *int64 `json:"last_seen_at,omitempty"`
}

type EndpointDiscovery struct {
	HostID          string   `json:"host_id"`
	DirectAddresses []string `json:"direct_addresses,omitempty"`
	RelayURLs       []string `json:"relay_urls,omitempty"`
	UpdatedAt       int64    `json:"updated_at"`
}

type RelayAccessGrant struct {
	ID               string `json:"id"`
	HostID           string `json:"host_id"`
	ClientEndpointID string `json:"client_endpoint_id"`
	ExpiresAt        int64  `json:"expires_at"`
	CreatedAt        int64  `json:"created_at"`
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "ternal.db")
	config, err := rhizaConfigFromEnv(dataDir)
	if err != nil {
		return nil, err
	}
	db, err := openRhiza(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, path: dbPath}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func OpenFromEnv(ctx context.Context) (*Store, error) {
	return Open(ctx, envOr("TERNAL_DATA_DIR", "ternal-data"))
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Ready verifies that the store can serve a linearizable read. This is stricter
// than process liveness and fails when the embedded data node cannot reach the
// consistency level required by API reads.
func (s *Store) Ready(ctx context.Context) error {
	row := s.db.QueryRowContext(ctx, `SELECT 1`)
	var value int
	if err := row.Scan(&value); err != nil {
		return fmt.Errorf("readiness query: %w", err)
	}
	if value != 1 {
		return fmt.Errorf("readiness query returned %d", value)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	var schemaTables, legacyTables int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ternal_schema'`,
	).Scan(&schemaTables); err != nil {
		return fmt.Errorf("inspect schema marker: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('hosts', 'policies', 'access_grants', 'devices', 'relay_access_grants')`,
	).Scan(&legacyTables); err != nil {
		return fmt.Errorf("inspect legacy schema: %w", err)
	}
	if schemaTables == 0 && legacyTables != 0 {
		return fmt.Errorf("unsupported pre-Go Ternal schema; use an empty greenfield data directory")
	}

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS ternal_schema (
			version INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS hosts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			endpoint_id TEXT NOT NULL,
			ssh_user TEXT NOT NULL DEFAULT 'root',
			tags TEXT NOT NULL DEFAULT '{}',
			ssh_port INTEGER NOT NULL DEFAULT 22,
			status TEXT NOT NULL DEFAULT 'unknown',
			owner TEXT NOT NULL DEFAULT '',
			last_seen INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS policies (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			principal TEXT NOT NULL,
			host_selector TEXT NOT NULL,
			ssh_users TEXT NOT NULL DEFAULT '[]',
			expires_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ssh_keys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			public_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS access_requests (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			host_id TEXT NOT NULL,
			ssh_user TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS access_grants (
			id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			host_id TEXT NOT NULL,
			ssh_user TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			ephemeral_key TEXT,
			key_installed INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS revoked_sessions (
			cookie_hash TEXT PRIMARY KEY,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS revoked_sessions_expires_at ON revoked_sessions (expires_at)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			action TEXT NOT NULL,
			resource TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			user_id TEXT,
			before_json TEXT,
			after_json TEXT,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS manufacturing_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			batch_id TEXT,
			expires_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS manufacturing_batches (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			serial_prefix TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'open',
			expires_at INTEGER NOT NULL,
			max_devices INTEGER NOT NULL,
			used_count INTEGER NOT NULL DEFAULT 0,
			closed_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS devices (
			id TEXT PRIMARY KEY,
			host_id TEXT NOT NULL,
			endpoint_id TEXT NOT NULL,
			ssh_host_key_fingerprint TEXT NOT NULL,
			device_public_key TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'manufactured',
			serial_number TEXT,
			model TEXT,
			enrolled_at INTEGER NOT NULL,
			last_seen_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS endpoint_discovery (
			host_id TEXT PRIMARY KEY,
			direct_addresses TEXT NOT NULL DEFAULT '[]',
			relay_urls TEXT NOT NULL DEFAULT '[]',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS relay_access_grants (
			id TEXT PRIMARY KEY,
			host_id TEXT NOT NULL,
			client_endpoint_id TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS authorized_keys_snapshots (
			host_id TEXT NOT NULL,
			ssh_user TEXT NOT NULL,
			generation INTEGER NOT NULL,
			digest TEXT NOT NULL,
			PRIMARY KEY (host_id, ssh_user)
		)`,
		`CREATE TABLE IF NOT EXISTS authorized_keys_snapshot_grants (
			host_id TEXT NOT NULL,
			ssh_user TEXT NOT NULL,
			grant_id TEXT NOT NULL,
			PRIMARY KEY (host_id, ssh_user, grant_id)
		)`,
	}
	for _, m := range migrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}
	}
	var versionCount, version int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(version), 0) FROM ternal_schema`).Scan(&versionCount, &version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if versionCount == 0 {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO ternal_schema (version) VALUES (1)`); err != nil {
			return fmt.Errorf("initialize schema version: %w", err)
		}
	} else if versionCount != 1 || version != 1 {
		return fmt.Errorf("unsupported Ternal schema version")
	}
	return nil
}

func (s *Store) RevokeSession(ctx context.Context, cookie string, expiresAt int64) error {
	if cookie == "" || expiresAt <= nowUnix() {
		return fmt.Errorf("invalid session revocation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO revoked_sessions (cookie_hash, expires_at) VALUES (?, ?)`, hashSecret(cookie), expiresAt); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM revoked_sessions WHERE cookie_hash IN (
			SELECT cookie_hash FROM revoked_sessions WHERE expires_at <= ? ORDER BY expires_at LIMIT 100
		)`, nowUnix()-int64(sessionRevocationRetention/time.Second))
	return nil
}

func (s *Store) SessionRevoked(ctx context.Context, cookie string) (bool, error) {
	if cookie == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM revoked_sessions WHERE cookie_hash = ? AND expires_at > ?`,
		hashSecret(cookie), nowUnix(),
	).Scan(&count)
	return count == 1, err
}

func newID() string {
	return uuid.New().String()
}

func nowUnix() int64 {
	return time.Now().Unix()
}

func (s *Store) CreateHost(ctx context.Context, h NewHost) (*core.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := nowUnix()
	if h.SSHUser == "" {
		h.SSHUser = "root"
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if h.Status == "" {
		h.Status = "unknown"
	}
	tagsJSON, _ := json.Marshal(h.Tags)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hosts (id, name, endpoint_id, ssh_user, tags, ssh_port, status, owner, last_seen, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, h.Name, h.EndpointID, h.SSHUser, string(tagsJSON), h.SSHPort, h.Status, h.Owner, h.LastSeen, now, now,
	)
	if err != nil {
		return nil, err
	}
	return &core.Host{
		ID: id, Name: h.Name, EndpointID: h.EndpointID, SSHUser: h.SSHUser,
		Tags: h.Tags, SSHPort: h.SSHPort, Status: h.Status, Owner: h.Owner, LastSeen: h.LastSeen,
	}, nil
}

func (s *Store) ListHosts(ctx context.Context) ([]core.Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, endpoint_id, ssh_user, tags, ssh_port, status, owner, last_seen FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []core.Host
	for rows.Next() {
		var h core.Host
		var tagsJSON string
		if err := rows.Scan(&h.ID, &h.Name, &h.EndpointID, &h.SSHUser, &tagsJSON, &h.SSHPort, &h.Status, &h.Owner, &h.LastSeen); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tagsJSON), &h.Tags)
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

func (s *Store) GetHost(ctx context.Context, id string) (*core.Host, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var h core.Host
	var tagsJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, endpoint_id, ssh_user, tags, ssh_port, status, owner, last_seen FROM hosts WHERE id = ?`, id,
	).Scan(&h.ID, &h.Name, &h.EndpointID, &h.SSHUser, &tagsJSON, &h.SSHPort, &h.Status, &h.Owner, &h.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(tagsJSON), &h.Tags)
	return &h, nil
}

func (s *Store) UpdateHost(ctx context.Context, id string, h NewHost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tagsJSON, _ := json.Marshal(h.Tags)
	_, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET name=?, endpoint_id=?, ssh_user=?, tags=?, ssh_port=?, status=?, owner=?, last_seen=?, updated_at=? WHERE id=?`,
		h.Name, h.EndpointID, h.SSHUser, string(tagsJSON), h.SSHPort, h.Status, h.Owner, h.LastSeen, nowUnix(), id,
	)
	return err
}

func (s *Store) DeleteHost(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM hosts WHERE id=?`, id)
	return err
}

func (s *Store) CreatePolicy(ctx context.Context, p NewPolicy) (*core.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := nowUnix()
	sshUsersJSON, _ := json.Marshal(p.SSHUsers)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO policies (id, name, principal, host_selector, ssh_users, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.Name, p.Principal, p.HostSelector, string(sshUsersJSON), p.ExpiresAt, now, now,
	)
	if err != nil {
		return nil, err
	}
	return &core.Policy{
		ID: id, Name: p.Name, Principal: p.Principal, HostSelector: p.HostSelector,
		SSHUsers: p.SSHUsers, ExpiresAt: p.ExpiresAt,
	}, nil
}

func (s *Store) ListPolicies(ctx context.Context) ([]core.Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, principal, host_selector, ssh_users, expires_at FROM policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []core.Policy
	for rows.Next() {
		var p core.Policy
		var sshUsersJSON string
		if err := rows.Scan(&p.ID, &p.Name, &p.Principal, &p.HostSelector, &sshUsersJSON, &p.ExpiresAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(sshUsersJSON), &p.SSHUsers)
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

func (s *Store) UpdatePolicy(ctx context.Context, id string, p NewPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sshUsersJSON, _ := json.Marshal(p.SSHUsers)
	_, err := s.db.ExecContext(ctx,
		`UPDATE policies SET name=?, principal=?, host_selector=?, ssh_users=?, expires_at=?, updated_at=? WHERE id=?`,
		p.Name, p.Principal, p.HostSelector, string(sshUsersJSON), p.ExpiresAt, nowUnix(), id,
	)
	return err
}

func (s *Store) DeletePolicy(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM policies WHERE id=?`, id)
	return err
}

func (s *Store) CreateSSHKey(ctx context.Context, userID, publicKey, fingerprint string) (*SshKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := nowUnix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (id, user_id, public_key, fingerprint, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, publicKey, fingerprint, now,
	)
	if err != nil {
		return nil, err
	}
	return &SshKey{ID: id, UserID: userID, PublicKey: publicKey, Fingerprint: fingerprint, CreatedAt: now}, nil
}

func (s *Store) ListSSHKeys(ctx context.Context, userID string) ([]SshKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, public_key, fingerprint, created_at FROM ssh_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SshKey
	for rows.Next() {
		var k SshKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.Fingerprint, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) AuthorizedKeysForHost(ctx context.Context, hostID, sshUser string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys, _, err := s.authorizedKeysSnapshotForHost(ctx, hostID, sshUser)
	return keys, err
}

func (s *Store) AuthorizedKeysSnapshotForHost(ctx context.Context, hostID, sshUser string) ([]string, []string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.authorizedKeysSnapshotForHost(ctx, hostID, sshUser)
}

func (s *Store) authorizedKeysSnapshotForHost(ctx context.Context, hostID, sshUser string) ([]string, []string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT k.public_key, g.id
		 FROM access_grants g
		 JOIN ssh_keys k ON k.user_id = g.user_id
		 JOIN hosts h ON h.id = g.host_id
		 JOIN devices d ON d.host_id = h.id
		 WHERE g.host_id = ? AND g.ssh_user = ? AND g.expires_at > ? AND h.status != 'revoked' AND d.state != 'revoked'
		 ORDER BY k.public_key, g.id`,
		hostID, sshUser, nowUnix(),
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var keys []string
	var grants []string
	seenKeys := map[string]bool{}
	seenGrants := map[string]bool{}
	for rows.Next() {
		var key, grant string
		if err := rows.Scan(&key, &grant); err != nil {
			return nil, nil, err
		}
		if !seenKeys[key] {
			seenKeys[key] = true
			keys = append(keys, key)
		}
		if !seenGrants[grant] {
			seenGrants[grant] = true
			grants = append(grants, grant)
		}
	}
	return keys, grants, rows.Err()
}

// AuthorizedKeysGeneration stores one replicated snapshot containing the
// digest, monotonic generation, and exact grants represented by the body.
func (s *Store) AuthorizedKeysGeneration(ctx context.Context, hostID, sshUser, digest string, grants []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statements := []rhiza.SQLStatement{{SQL: `INSERT INTO authorized_keys_snapshots (host_id, ssh_user, generation, digest)
		 VALUES (?, ?, 1, ?)
		 ON CONFLICT(host_id, ssh_user) DO UPDATE SET
		 generation = CASE WHEN digest <> excluded.digest THEN generation + 1 ELSE generation END,
		 digest = excluded.digest`, Args: []any{hostID, sshUser, digest}}, {
		SQL: `DELETE FROM authorized_keys_snapshot_grants WHERE host_id = ? AND ssh_user = ?`, Args: []any{hostID, sshUser},
	}, {
		SQL: `INSERT INTO authorized_keys_snapshot_grants (host_id, ssh_user, grant_id) VALUES (?, ?, '')`, Args: []any{hostID, sshUser},
	}}
	for _, grant := range grants {
		statements = append(statements, rhiza.SQLStatement{
			SQL:  `INSERT INTO authorized_keys_snapshot_grants (host_id, ssh_user, grant_id) VALUES (?, ?, ?)`,
			Args: []any{hostID, sshUser, grant},
		})
	}
	_, err := s.db.ExecTransactionResult(ctx, statements...)
	if err != nil {
		return 0, err
	}
	var generation int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT generation FROM authorized_keys_snapshots WHERE host_id = ? AND ssh_user = ? AND digest = ?`,
		hostID, sshUser, digest,
	).Scan(&generation); err != nil {
		return 0, err
	}
	return generation, nil
}

func (s *Store) AcknowledgeAuthorizedKeys(ctx context.Context, hostID, sshUser string, generation int64, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation < 1 || len(digest) != 64 {
		return fmt.Errorf("invalid authorized_keys acknowledgement")
	}
	response, err := s.db.ExecTransactionResult(ctx,
		rhiza.SQLStatement{SQL: `UPDATE authorized_keys_snapshots SET digest = digest
			WHERE host_id = ? AND ssh_user = ? AND generation = ? AND digest = ?
			AND EXISTS (
				SELECT 1 FROM authorized_keys_snapshot_grants
				WHERE host_id = ? AND ssh_user = ? AND grant_id = ''
			)`, Args: []any{hostID, sshUser, generation, digest, hostID, sshUser}},
		rhiza.SQLStatement{SQL: `UPDATE access_grants SET key_installed = 1
			WHERE id IN (
				SELECT grant_id FROM authorized_keys_snapshot_grants
				WHERE host_id = ? AND ssh_user = ?
			)
			AND host_id = ? AND ssh_user = ? AND expires_at > ?
			AND EXISTS (SELECT 1 FROM ssh_keys WHERE ssh_keys.user_id = access_grants.user_id)
			AND EXISTS (
				SELECT 1 FROM authorized_keys_snapshots
				WHERE host_id = ? AND ssh_user = ? AND generation = ? AND digest = ?
			)`, Args: []any{hostID, sshUser, hostID, sshUser, nowUnix(), hostID, sshUser, generation, digest}},
	)
	if err != nil {
		return err
	}
	if response.RowsAffected < 1 {
		return fmt.Errorf("authorized_keys acknowledgement does not match current snapshot")
	}
	return nil
}

func (s *Store) DeleteSSHKey(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id=?`, id)
	return err
}

func (s *Store) DeleteSSHKeyForUser(ctx context.Context, id, userID string, admin bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statement := `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`
	args := []any{id, userID}
	if admin {
		statement, args = `DELETE FROM ssh_keys WHERE id = ?`, []any{id}
	}
	response, err := s.db.ExecContext(ctx, statement, args...)
	return response.RowsAffected == 1, err
}

func (s *Store) CreateAccessRequest(ctx context.Context, userID, hostID, sshUser string) (*AccessRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := nowUnix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO access_requests (id, user_id, host_id, ssh_user, status, created_at) VALUES (?, ?, ?, ?, 'pending', ?)`,
		id, userID, hostID, sshUser, now,
	)
	if err != nil {
		return nil, err
	}
	return &AccessRequest{ID: id, UserID: userID, HostID: hostID, SSHUser: sshUser, Status: "pending", CreatedAt: now}, nil
}

func (s *Store) ApproveAccessRequest(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	response, err := s.db.ExecContext(ctx, `UPDATE access_requests SET status = 'approved' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return err
	}
	if response.RowsAffected != 1 {
		return fmt.Errorf("access request was not pending")
	}
	return nil
}

func (s *Store) CreateAccessGrant(ctx context.Context, requestID, userID, hostID, sshUser string, expiresAt int64, ephemeralKey string) (*AccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := nowUnix()
	response, err := s.db.ExecContext(ctx,
		`INSERT INTO access_grants (id, request_id, user_id, host_id, ssh_user, expires_at, ephemeral_key, key_installed, created_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?, 0, ?
		 WHERE EXISTS (SELECT 1 FROM hosts h JOIN devices d ON d.host_id = h.id WHERE h.id = ? AND h.status != 'revoked' AND d.state != 'revoked')`,
		id, requestID, userID, hostID, sshUser, expiresAt, ephemeralKey, now, hostID,
	)
	if err != nil {
		return nil, err
	}
	if response.RowsAffected != 1 {
		return nil, fmt.Errorf("host unavailable")
	}
	return &AccessGrant{
		ID: id, RequestID: requestID, UserID: userID, HostID: hostID,
		SSHUser: sshUser, ExpiresAt: expiresAt, EphemeralKey: ephemeralKey, CreatedAt: now,
	}, nil
}

// IssueSSHAccess records the approved request, short-lived key grant, and audit
// event in one replicated data transaction. An access grant is never visible
// without its corresponding decision record.
func (s *Store) IssueSSHAccess(ctx context.Context, userID, hostID, sshUser string, expiresAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := newID()
	grantID := newID()
	auditID := newID()
	now := nowUnix()
	after, _ := json.Marshal(map[string]any{"request_id": requestID, "grant_id": grantID, "expires_at": expiresAt})
	response, err := s.db.ExecTransactionResult(ctx,
		rhiza.SQLStatement{SQL: `INSERT INTO access_requests (id, user_id, host_id, ssh_user, status, created_at) SELECT ?, ?, ?, ?, 'approved', ? WHERE EXISTS (SELECT 1 FROM hosts h JOIN devices d ON d.host_id = h.id WHERE h.id = ? AND h.status != 'revoked' AND d.state != 'revoked')`, Args: []any{requestID, userID, hostID, sshUser, now, hostID}},
		rhiza.SQLStatement{SQL: `INSERT INTO access_grants (id, request_id, user_id, host_id, ssh_user, expires_at, ephemeral_key, key_installed, created_at) SELECT ?, ?, ?, ?, ?, ?, '', 0, ? WHERE EXISTS (SELECT 1 FROM access_requests WHERE id = ?)`, Args: []any{grantID, requestID, userID, hostID, sshUser, expiresAt, now, requestID}},
		rhiza.SQLStatement{SQL: `INSERT INTO audit_events (id, action, resource, resource_id, user_id, before_json, after_json, created_at) SELECT ?, 'access.approved', 'host', ?, ?, NULL, ?, ? WHERE EXISTS (SELECT 1 FROM access_grants WHERE id = ?)`, Args: []any{auditID, hostID, userID, string(after), now, grantID}},
	)
	if err != nil {
		return err
	}
	if response.RowsAffected != 3 {
		return fmt.Errorf("host unavailable")
	}
	return nil
}

func (s *Store) GetAccessGrant(ctx context.Context, id string) (*AccessGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var g AccessGrant
	err := s.db.QueryRowContext(ctx,
		`SELECT id, request_id, user_id, host_id, ssh_user, expires_at, ephemeral_key, key_installed, created_at FROM access_grants WHERE id = ?`, id,
	).Scan(&g.ID, &g.RequestID, &g.UserID, &g.HostID, &g.SSHUser, &g.ExpiresAt, &g.EphemeralKey, &g.KeyInstalled, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *Store) ListAccessGrants(ctx context.Context) ([]AccessGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, request_id, user_id, host_id, ssh_user, expires_at, ephemeral_key, key_installed, created_at FROM access_grants ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []AccessGrant
	for rows.Next() {
		var g AccessGrant
		if err := rows.Scan(&g.ID, &g.RequestID, &g.UserID, &g.HostID, &g.SSHUser, &g.ExpiresAt, &g.EphemeralKey, &g.KeyInstalled, &g.CreatedAt); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

func (s *Store) ListAccessRequests(ctx context.Context) ([]AccessRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, host_id, ssh_user, status, created_at FROM access_requests ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []AccessRequest
	for rows.Next() {
		var r AccessRequest
		if err := rows.Scan(&r.ID, &r.UserID, &r.HostID, &r.SSHUser, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

func (s *Store) CreateAuditEvent(ctx context.Context, action, resource, resourceID, userID string, before, after interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var beforeJSON, afterJSON []byte
	if before != nil {
		beforeJSON, _ = json.Marshal(before)
	}
	if after != nil {
		afterJSON, _ = json.Marshal(after)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, action, resource, resource_id, user_id, before_json, after_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), action, resource, resourceID, userID, string(beforeJSON), string(afterJSON), nowUnix(),
	)
	return err
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, action, resource, resource_id, user_id, before_json, after_json, created_at FROM audit_events ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var beforeStr, afterStr sql.NullString
		if err := rows.Scan(&e.ID, &e.Action, &e.Resource, &e.ResourceID, &e.UserID, &beforeStr, &afterStr, &e.CreatedAt); err != nil {
			return nil, err
		}
		if beforeStr.Valid {
			e.Before = json.RawMessage(beforeStr.String)
		}
		if afterStr.Valid {
			e.After = json.RawMessage(afterStr.String)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) UpdateEndpointDiscovery(ctx context.Context, hostID string, directAddresses, relayURLs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	addrsJSON, _ := json.Marshal(directAddresses)
	relaysJSON, _ := json.Marshal(relayURLs)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO endpoint_discovery (host_id, direct_addresses, relay_urls, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(host_id) DO UPDATE SET direct_addresses=excluded.direct_addresses, relay_urls=excluded.relay_urls, updated_at=excluded.updated_at`,
		hostID, string(addrsJSON), string(relaysJSON), nowUnix(),
	)
	return err
}

func (s *Store) GetEndpointDiscovery(ctx context.Context, hostID string) (*EndpointDiscovery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var d EndpointDiscovery
	var addrsJSON, relaysJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT d.host_id, d.direct_addresses, d.relay_urls, d.updated_at
		 FROM endpoint_discovery d
		 JOIN hosts h ON h.id = d.host_id
		 JOIN devices device ON device.host_id = h.id
		 WHERE d.host_id = ? AND h.status != 'revoked' AND device.state != 'revoked'`, hostID,
	).Scan(&d.HostID, &addrsJSON, &relaysJSON, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(addrsJSON), &d.DirectAddresses)
	json.Unmarshal([]byte(relaysJSON), &d.RelayURLs)
	return &d, nil
}

func (s *Store) CreateRelayAccessGrant(ctx context.Context, hostID, clientEndpointID, userID string, ttlSeconds int64) (*RelayAccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	auditID := newID()
	now := nowUnix()
	after, _ := json.Marshal(map[string]any{"grant_id": id, "expires_at": now + ttlSeconds})
	response, err := s.db.ExecTransactionResult(ctx,
		rhiza.SQLStatement{SQL: `INSERT INTO relay_access_grants (id, host_id, client_endpoint_id, expires_at, created_at) SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM hosts h JOIN devices d ON d.host_id = h.id WHERE h.id = ? AND h.status != 'revoked' AND d.state != 'revoked')`, Args: []any{id, hostID, clientEndpointID, now + ttlSeconds, now, hostID}},
		rhiza.SQLStatement{SQL: `INSERT INTO audit_events (id, action, resource, resource_id, user_id, before_json, after_json, created_at) SELECT ?, 'relay.grant.created', 'host', ?, ?, NULL, ?, ? WHERE EXISTS (SELECT 1 FROM relay_access_grants WHERE id = ?)`, Args: []any{auditID, hostID, userID, string(after), now, id}},
	)
	if err != nil {
		return nil, err
	}
	if response.RowsAffected != 2 {
		return nil, fmt.Errorf("host unavailable")
	}
	return &RelayAccessGrant{ID: id, HostID: hostID, ClientEndpointID: clientEndpointID, ExpiresAt: now + ttlSeconds, CreatedAt: now}, nil
}

func (s *Store) RenewRelayAccessGrant(ctx context.Context, hostID, endpointID, actorID string, ttlSeconds int64) (*RelayAccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	auditID := newID()
	now := nowUnix()
	after, _ := json.Marshal(map[string]any{"grant_id": id, "expires_at": now + ttlSeconds})
	response, err := s.db.ExecTransactionResult(ctx,
		rhiza.SQLStatement{SQL: `DELETE FROM relay_access_grants WHERE host_id = ? AND client_endpoint_id = ? AND EXISTS (SELECT 1 FROM hosts h JOIN devices d ON d.host_id = h.id WHERE h.id = ? AND h.status != 'revoked' AND d.state != 'revoked')`, Args: []any{hostID, endpointID, hostID}},
		rhiza.SQLStatement{SQL: `INSERT INTO relay_access_grants (id, host_id, client_endpoint_id, expires_at, created_at) SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM hosts h JOIN devices d ON d.host_id = h.id WHERE h.id = ? AND h.status != 'revoked' AND d.state != 'revoked')`, Args: []any{id, hostID, endpointID, now + ttlSeconds, now, hostID}},
		rhiza.SQLStatement{SQL: `INSERT INTO audit_events (id, action, resource, resource_id, user_id, before_json, after_json, created_at) SELECT ?, 'relay.grant.renewed', 'host', ?, ?, NULL, ?, ? WHERE EXISTS (SELECT 1 FROM relay_access_grants WHERE id = ?)`, Args: []any{auditID, hostID, actorID, string(after), now, id}},
	)
	if err != nil {
		return nil, err
	}
	if response.RowsAffected < 2 {
		return nil, fmt.Errorf("host unavailable")
	}
	return &RelayAccessGrant{ID: id, HostID: hostID, ClientEndpointID: endpointID, ExpiresAt: now + ttlSeconds, CreatedAt: now}, nil
}

func (s *Store) RelayEndpointAllowed(ctx context.Context, endpointID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM relay_access_grants g
		 JOIN hosts h ON h.id = g.host_id
		 JOIN devices d ON d.host_id = h.id
		 WHERE g.client_endpoint_id = ? AND g.expires_at > ? AND h.status != 'revoked' AND d.state != 'revoked'`,
		endpointID, nowUnix(),
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) CreateManufacturingToken(ctx context.Context, batchID string, expiresAt *int64) (*ManufacturingToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	tokenHash := hashSecret(token)
	now := nowUnix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO manufacturing_tokens (id, token_hash, batch_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, tokenHash, batchID, expiresAt, now,
	)
	if err != nil {
		return nil, err
	}
	return &ManufacturingToken{ID: id, Token: token, BatchID: batchID, ExpiresAt: expiresAt, CreatedAt: now}, nil
}

func (s *Store) ListManufacturingTokens(ctx context.Context) ([]ManufacturingToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch_id, expires_at, created_at FROM manufacturing_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []ManufacturingToken
	for rows.Next() {
		var t ManufacturingToken
		if err := rows.Scan(&t.ID, &t.BatchID, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *Store) GetManufacturingToken(ctx context.Context, token string) (*ManufacturingToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var t ManufacturingToken
	err := s.db.QueryRowContext(ctx,
		`SELECT id, batch_id, expires_at, created_at FROM manufacturing_tokens WHERE token_hash = ?`, hashSecret(token),
	).Scan(&t.ID, &t.BatchID, &t.ExpiresAt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) CreateManufacturingBatch(ctx context.Context, name, serialPrefix string, expiresAt, maxDevices int64) (*ManufacturingBatch, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || serialPrefix == "" || expiresAt <= nowUnix() || maxDevices < 1 || maxDevices > 10000 {
		return nil, "", fmt.Errorf("invalid manufacturing batch")
	}
	id := newID()
	now := nowUnix()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO manufacturing_batches (id, name, serial_prefix, token_hash, status, expires_at, max_devices, used_count, created_at) VALUES (?, ?, ?, ?, 'open', ?, ?, 0, ?)`,
		id, name, serialPrefix, hashSecret(token), expiresAt, maxDevices, now,
	)
	if err != nil {
		return nil, "", err
	}
	return &ManufacturingBatch{ID: id, Name: name, SerialPrefix: serialPrefix, Status: "open", ExpiresAt: expiresAt, MaxDevices: maxDevices, CreatedAt: now}, token, nil
}

func (s *Store) ListManufacturingBatches(ctx context.Context) ([]ManufacturingBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, serial_prefix, status, expires_at, max_devices, used_count, closed_at, created_at FROM manufacturing_batches ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []ManufacturingBatch
	for rows.Next() {
		var b ManufacturingBatch
		if err := rows.Scan(&b.ID, &b.Name, &b.SerialPrefix, &b.Status, &b.ExpiresAt, &b.MaxDevices, &b.UsedCount, &b.ClosedAt, &b.CreatedAt); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

func (s *Store) CloseManufacturingBatch(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUnix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE manufacturing_batches SET status='closed', closed_at=? WHERE id=? AND status='open'`,
		now, id,
	)
	return err
}

func (s *Store) EnrollDevice(ctx context.Context, token, endpointID, serialNumber, model, fingerprint, devicePublicKey, sshUser string, sshPort uint16, tags map[string]string) (*Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt, err := s.getManufacturingTokenByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	batch, err := s.getManufacturingBatchByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if mt == nil && batch == nil {
		return nil, fmt.Errorf("invalid manufacturing token")
	}
	if mt != nil && mt.ExpiresAt != nil && *mt.ExpiresAt < nowUnix() {
		return nil, fmt.Errorf("manufacturing token expired")
	}
	if devicePublicKey == "" {
		return nil, fmt.Errorf("device public key is required")
	}
	if batch != nil {
		if batch.Status != "open" || batch.ExpiresAt <= nowUnix() || batch.UsedCount >= batch.MaxDevices {
			return nil, fmt.Errorf("manufacturing batch is closed")
		}
		expectedSerial := fmt.Sprintf("%s-%06d", batch.SerialPrefix, batch.UsedCount+1)
		if serialNumber != "" && serialNumber != expectedSerial {
			return nil, fmt.Errorf("serial does not match manufacturing batch")
		}
		serialNumber = expectedSerial
		now := nowUnix()
		reserved, err := s.db.ExecContext(ctx,
			`UPDATE manufacturing_batches SET used_count = used_count + 1, status = CASE WHEN used_count + 1 >= max_devices THEN 'closed' ELSE status END, closed_at = CASE WHEN used_count + 1 >= max_devices THEN ? ELSE closed_at END WHERE id = ? AND status = 'open' AND expires_at > ? AND used_count = ? AND used_count < max_devices`,
			now, batch.ID, now, batch.UsedCount,
		)
		if err != nil || reserved.RowsAffected != 1 {
			return nil, fmt.Errorf("manufacturing reservation conflicted")
		}
	} else {
		if serialNumber == "" {
			return nil, fmt.Errorf("serial number is required")
		}
		spent, err := s.db.ExecContext(ctx, `DELETE FROM manufacturing_tokens WHERE id = ?`, mt.ID)
		if err != nil || spent.RowsAffected != 1 {
			return nil, fmt.Errorf("manufacturing token is no longer available")
		}
	}
	id := newID()
	hostID := newID()
	now := nowUnix()
	if sshUser == "" {
		sshUser = "root"
	}
	if sshPort == 0 {
		sshPort = 22
	}
	tagsJSON, _ := json.Marshal(tags)
	if _, err = s.db.ExecContext(ctx,
		`INSERT INTO hosts (id, name, endpoint_id, ssh_user, tags, ssh_port, status, owner, last_seen, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'manufactured', 'manufacturing', NULL, ?, ?)`,
		hostID, serialNumber, endpointID, sshUser, string(tagsJSON), sshPort, now, now,
	); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO devices (id, host_id, endpoint_id, ssh_host_key_fingerprint, device_public_key, state, serial_number, model, enrolled_at, last_seen_at) VALUES (?, ?, ?, ?, ?, 'manufactured', ?, ?, ?, NULL)`,
		id, hostID, endpointID, fingerprint, devicePublicKey, serialNumber, model, now,
	)
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, hostID)
		return nil, err
	}
	return &Device{ID: id, HostID: hostID, EndpointID: endpointID, SSHHostKeyFingerprint: fingerprint, DevicePublicKey: devicePublicKey, State: "manufactured", SerialNumber: serialNumber, Model: model, EnrolledAt: now}, nil
}

func (s *Store) getManufacturingBatchByToken(ctx context.Context, token string) (*ManufacturingBatch, error) {
	var b ManufacturingBatch
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, serial_prefix, status, expires_at, max_devices, used_count, closed_at, created_at FROM manufacturing_batches WHERE token_hash = ?`,
		hashSecret(token),
	).Scan(&b.ID, &b.Name, &b.SerialPrefix, &b.Status, &b.ExpiresAt, &b.MaxDevices, &b.UsedCount, &b.ClosedAt, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) getManufacturingTokenByToken(ctx context.Context, token string) (*ManufacturingToken, error) {
	var t ManufacturingToken
	err := s.db.QueryRowContext(ctx,
		`SELECT id, batch_id, expires_at, created_at FROM manufacturing_tokens WHERE token_hash = ?`, hashSecret(token),
	).Scan(&t.ID, &t.BatchID, &t.ExpiresAt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func hashSecret(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, host_id, endpoint_id, ssh_host_key_fingerprint, device_public_key, state, serial_number, model, enrolled_at, last_seen_at FROM devices ORDER BY enrolled_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.HostID, &d.EndpointID, &d.SSHHostKeyFingerprint, &d.DevicePublicKey, &d.State, &d.SerialNumber, &d.Model, &d.EnrolledAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Store) GetDeviceByHostID(ctx context.Context, hostID string) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var d Device
	err := s.db.QueryRowContext(ctx,
		`SELECT id, host_id, endpoint_id, ssh_host_key_fingerprint, device_public_key, state, serial_number, model, enrolled_at, last_seen_at FROM devices WHERE host_id = ? ORDER BY enrolled_at DESC LIMIT 1`,
		hostID,
	).Scan(&d.ID, &d.HostID, &d.EndpointID, &d.SSHHostKeyFingerprint, &d.DevicePublicKey, &d.State, &d.SerialNumber, &d.Model, &d.EnrolledAt, &d.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) GetDeviceBySerial(ctx context.Context, serial string) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var d Device
	err := s.db.QueryRowContext(ctx,
		`SELECT id, host_id, endpoint_id, ssh_host_key_fingerprint, device_public_key, state, serial_number, model, enrolled_at, last_seen_at FROM devices WHERE serial_number = ?`, serial,
	).Scan(&d.ID, &d.HostID, &d.EndpointID, &d.SSHHostKeyFingerprint, &d.DevicePublicKey, &d.State, &d.SerialNumber, &d.Model, &d.EnrolledAt, &d.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) TouchDevice(ctx context.Context, id, hostID, endpointID, fingerprint, status string, directAddresses, relayURLs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUnix()
	addrsJSON, _ := json.Marshal(directAddresses)
	relaysJSON, _ := json.Marshal(relayURLs)
	response, err := s.db.ExecTransactionResult(ctx,
		rhiza.SQLStatement{SQL: `UPDATE devices SET last_seen_at = ? WHERE id = ? AND host_id = ? AND endpoint_id = ? AND ssh_host_key_fingerprint = ? AND state != 'revoked'`, Args: []any{now, id, hostID, endpointID, fingerprint}},
		rhiza.SQLStatement{SQL: `UPDATE hosts SET status = ?, last_seen = ?, updated_at = ? WHERE id = ? AND EXISTS (SELECT 1 FROM devices WHERE id = ? AND host_id = ? AND state != 'revoked')`, Args: []any{status, now, now, hostID, id, hostID}},
		rhiza.SQLStatement{SQL: `INSERT INTO endpoint_discovery (host_id, direct_addresses, relay_urls, updated_at) SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE id = ? AND host_id = ? AND state != 'revoked') ON CONFLICT(host_id) DO UPDATE SET direct_addresses=excluded.direct_addresses, relay_urls=excluded.relay_urls, updated_at=excluded.updated_at`, Args: []any{hostID, string(addrsJSON), string(relaysJSON), now, id, hostID}},
	)
	if err != nil {
		return err
	}
	if response.RowsAffected != 3 {
		return fmt.Errorf("device identity mismatch or device revoked")
	}
	return nil
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hostID string
	if err := s.db.QueryRowContext(ctx, `SELECT host_id FROM devices WHERE id = ?`, id).Scan(&hostID); err != nil {
		return err
	}
	now := nowUnix()
	return s.db.ExecTransaction(ctx,
		rhiza.SQLStatement{SQL: `UPDATE devices SET state = 'revoked', last_seen_at = ? WHERE id = ?`, Args: []any{now, id}},
		rhiza.SQLStatement{SQL: `UPDATE hosts SET status = 'revoked', last_seen = ?, updated_at = ? WHERE id = ?`, Args: []any{now, now, hostID}},
		rhiza.SQLStatement{SQL: `UPDATE access_grants SET expires_at = ? WHERE host_id = ? AND expires_at > ?`, Args: []any{now, hostID, now}},
		rhiza.SQLStatement{SQL: `DELETE FROM relay_access_grants WHERE host_id = ?`, Args: []any{hostID}},
	)
}
