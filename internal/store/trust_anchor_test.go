package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
)

type memoryAnchor struct {
	mu     sync.Mutex
	record trustAnchorRecord
	rv     int64
}

type cancelOnPendingAnchor struct {
	trustAnchorBackend
	cancel func()
	once   sync.Once
}

func (a *cancelOnPendingAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	updated, err := a.trustAnchorBackend.CAS(ctx, rv, next)
	if err == nil && next.PendingEpoch != nil {
		a.once.Do(a.cancel)
	}
	return updated, err
}

type blockingPendingAnchor struct {
	trustAnchorBackend
	pending chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (a *blockingPendingAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	updated, err := a.trustAnchorBackend.CAS(ctx, rv, next)
	if err == nil && next.PendingEpoch != nil {
		a.once.Do(func() { close(a.pending) })
		<-a.release
	}
	return updated, err
}

func (m *memoryAnchor) Get(context.Context) (trustAnchorRecord, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.record, fmt.Sprint(m.rv), nil
}
func (m *memoryAnchor) CAS(_ context.Context, rv string, next trustAnchorRecord) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rv != fmt.Sprint(m.rv) {
		return "", fmt.Errorf("conflict")
	}
	m.record = next
	m.rv++
	return fmt.Sprint(m.rv), nil
}

func anchoredStore(t *testing.T) (*Store, *memoryAnchor) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	binding := storageBindingFromEnv()
	record := binding
	record.Epoch = 0
	record.Token = uuid.NewString()
	backend := &memoryAnchor{record: record}
	anchor := &trustAnchor{backend: backend, binding: binding}
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: []rhiza.SQLStatement{{SQL: `CREATE TABLE trust_state (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL, token TEXT NOT NULL)`}, {SQL: `INSERT INTO trust_state (id,epoch,token) VALUES (1,?,?)`, Args: []any{record.Epoch, record.Token}}}}); err != nil {
		t.Fatal(err)
	}
	s.db.enableTrust(anchor)
	return s, backend
}

func TestTrustAnchorFencesWritesAndRejectsRestoredPair(t *testing.T) {
	ctx := context.Background()
	s, anchor := anchoredStore(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err != nil {
		t.Fatal(err)
	}
	current, _, _ := anchor.Get(ctx)
	if current.Epoch != 1 || current.PendingEpoch != nil || current.Bootstrap {
		t.Fatalf("anchor after write = %#v", current)
	}
	if err := s.db.verifyDBTrust(ctx, current.Epoch, current.Token); err != nil {
		t.Fatal(err)
	}
	// Simulate a complete old data snapshot restored while the independent
	// anchor remains advanced.
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: `UPDATE trust_state SET epoch=0, token=? WHERE id=1`, Args: []any{uuid.NewString()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.QueryContext(ctx, `SELECT 1`); err == nil {
		t.Fatal("read accepted a database pair below external anchor")
	}
	if err := s.Ready(ctx); err == nil {
		t.Fatal("ready accepted a database pair below external anchor")
	}
}

func TestTrustAnchorFencedWriteSurvivesCallerCancellation(t *testing.T) {
	s, backend := anchoredStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.db.trust.backend = &cancelOnPendingAnchor{trustAnchorBackend: backend, cancel: cancel}
	defer cancel()

	if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err != nil {
		t.Fatalf("fenced write after caller cancellation: %v", err)
	}
	current, _, err := backend.Get(context.Background())
	if err != nil || current.PendingEpoch != nil || current.Epoch != 1 {
		t.Fatalf("canceled write left anchor unresolved: epoch=%d pending=%t err=%v", current.Epoch, current.PendingEpoch != nil, err)
	}
	if _, err := s.db.QueryContext(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("read after canceled write: %v", err)
	}
}

func TestTrustAnchorReadWaitsForLocalFencedWrite(t *testing.T) {
	s, backend := anchoredStore(t)
	pending := make(chan struct{})
	release := make(chan struct{})
	s.db.trust.backend = &blockingPendingAnchor{trustAnchorBackend: backend, pending: pending, release: release}

	writeDone := make(chan error, 1)
	go func() {
		_, err := s.db.ExecContext(context.Background(), `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1)
		writeDone <- err
	}()
	<-pending
	readDone := make(chan error, 1)
	go func() {
		_, err := s.db.QueryContext(context.Background(), `SELECT 1`)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		t.Fatalf("read observed local pending fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("fenced write: %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("read after fenced write: %v", err)
	}
}

func TestTrustAnchorRejectsInvalidPlanBeforePending(t *testing.T) {
	ctx := context.Background()
	s, anchor := anchoredStore(t)
	before, _, _ := anchor.Get(ctx)
	if _, err := s.db.ExecTransactionResult(ctx); err == nil {
		t.Fatal("empty transaction accepted")
	}
	if _, err := s.db.execute(ctx, rhiza.ExecuteRequest{Statements: []rhiza.SQLStatement{{SQL: `SELECT 1`, OutputRefs: []rhiza.SQLStatementOutputRef{{ArgIndex: 0, StatementIndex: 7}}}}}, 1); err == nil {
		t.Fatal("empty SQL accepted")
	}
	after, _, _ := anchor.Get(ctx)
	if after.Epoch != before.Epoch || after.PendingID != "" {
		t.Fatalf("invalid plan altered anchor: %#v", after)
	}
}

func TestTrustAnchorPreservesOriginalStatementProjection(t *testing.T) {
	ctx := context.Background()
	s, _ := anchoredStore(t)
	response, err := s.db.ExecTransactionResult(ctx, rhiza.SQLStatement{SQL: `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?) RETURNING id`, Args: []any{uuid.NewString(), "u", "key", "fp", 1}, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Statements) != 1 {
		t.Fatalf("statement projection includes trust tail: %#v", response.Statements)
	}
	plain, err := s.db.ExecTransactionResult(ctx, rhiza.SQLStatement{SQL: `INSERT INTO audit_events (id,action,resource,resource_id,created_at) VALUES (?,?,?,?,?)`, Args: []any{uuid.NewString(), "a", "r", "id", 1}}, rhiza.SQLStatement{SQL: `INSERT INTO audit_events (id,action,resource,resource_id,created_at) VALUES (?,?,?,?,?)`, Args: []any{uuid.NewString(), "a", "r", "id", 1}})
	if err != nil || plain.RowsAffected != 2 {
		t.Fatalf("two-statement aggregate = %#v, %v", plain.MutationReceipt, err)
	}
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: `CREATE TABLE trust_insert_ids (id INTEGER PRIMARY KEY)`}); err != nil {
		t.Fatal(err)
	}
	insert, err := s.db.ExecContext(ctx, `INSERT INTO trust_insert_ids DEFAULT VALUES`)
	if err != nil || insert.RowsAffected != 1 || insert.LastInsertID != 1 {
		t.Fatalf("insert receipt = %#v, %v", insert.MutationReceipt, err)
	}
}

func TestTrustAnchorPreservesStoreMutationCounts(t *testing.T) {
	ctx := context.Background()
	s, _ := anchoredStore(t)
	if grant, err := s.CreateAccessGrant(ctx, "request", "user", "missing", "ops", 99, ""); err == nil || grant != nil {
		t.Fatalf("no-op grant = %#v, %v; trust tail must not make it successful", grant, err)
	}
	host, err := s.CreateHost(ctx, NewHost{Name: "count-host", EndpointID: "endpoint", SSHUser: "ops"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO devices (id,host_id,endpoint_id,ssh_host_key_fingerprint,device_public_key,state,enrolled_at) VALUES (?,?,?,?,?,'active',?)`, "device", host.ID, "endpoint", "fingerprint", "key", 1); err != nil {
		t.Fatal(err)
	}
	grant, err := s.CreateAccessGrant(ctx, "request", "user", host.ID, "ops", 99, "")
	if err != nil || grant == nil {
		t.Fatalf("one-row grant = %#v, %v", grant, err)
	}
	request, err := s.CreateAccessRequest(ctx, "user", host.ID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAccessRequest(ctx, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAccessRequest(ctx, request.ID); err == nil {
		t.Fatal("second no-op approval accepted")
	}
}

func TestTrustAnchorPendingRecoveryFailsClosedOrFinalizesExactPair(t *testing.T) {
	ctx := context.Background()
	s, backend := anchoredStore(t)
	base, rv, _ := backend.Get(ctx)
	next := base.Epoch + 1
	pending := base
	pending.Bootstrap = true
	pending.PendingEpoch = &next
	pending.PendingID = uuid.NewString()
	if _, err := backend.CAS(ctx, rv, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverPendingTrust(ctx, &trustAnchor{backend: backend, binding: storageBindingFromEnv()}, pending, "1"); err == nil {
		t.Fatal("unknown pending write was cleared")
	}
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: pending.PendingID, Statements: []rhiza.SQLStatement{{SQL: `UPDATE trust_state SET epoch=?,token=? WHERE id=1 AND epoch=? AND token=?`, Args: []any{next, pending.PendingID, base.Epoch, base.Token}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverPendingTrust(ctx, &trustAnchor{backend: backend, binding: storageBindingFromEnv()}, pending, "1"); err != nil {
		t.Fatal(err)
	}
	final, _, _ := backend.Get(ctx)
	if final.Epoch != next || final.Token != pending.PendingID || final.PendingEpoch != nil || final.Bootstrap {
		t.Fatalf("recovered final anchor = %#v", final)
	}
}

func TestTrustAnchorParsingAndBindingFailClosed(t *testing.T) {
	binding := storageBindingFromEnv()
	record := binding
	record.Epoch = 3
	record.Token = uuid.NewString()
	data := anchorData(record)
	parsed, err := parseAnchorData(data)
	if err != nil {
		t.Fatal(err)
	}
	anchor := &trustAnchor{binding: binding}
	if err := anchor.validate(parsed); err != nil {
		t.Fatal(err)
	}
	delete(data, "token")
	parsed, err = parseAnchorData(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.validate(parsed); err == nil {
		t.Fatal("missing token accepted")
	}
	if TrustAnchorStorageID("gcs", "", "bucket", "prefix", "c") == TrustAnchorStorageID("gcs", "", "bucket", "other", "c") {
		t.Fatal("storage binding omitted prefix")
	}
}

func TestKubernetesTrustAnchorUsesResourceVersionCAS(t *testing.T) {
	record := trustAnchorRecord{Format: "1", ClusterID: "c", StorageID: "s", Token: uuid.NewString()}
	state := configMapWire{APIVersion: "v1", Kind: "ConfigMap", Data: anchorData(record), Metadata: map[string]json.RawMessage{}}
	state.setMetadataString("resourceVersion", "7")
	state.setMetadataString("name", "anchor")
	state.setMetadataString("namespace", "ns")
	state.Metadata["labels"] = json.RawMessage(`{"ternal.dev/protected":"true"}`)
	state.Metadata["annotations"] = json.RawMessage(`{"operator.example/keep":"yes"}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rotating-token" {
			t.Error("missing service account bearer")
		}
		if r.Method == http.MethodPut {
			var wire configMapWire
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Error(err)
			}
			if wire.metadataString("resourceVersion") != "7" || wire.metadataString("name") != "anchor" || wire.metadataString("namespace") != "ns" {
				t.Error("PUT omitted CAS identity")
			}
			if string(wire.Metadata["labels"]) != `{"ternal.dev/protected":"true"}` || string(wire.Metadata["annotations"]) != `{"operator.example/keep":"yes"}` {
				t.Error("PUT removed protected metadata")
			}
			state = wire
			state.setMetadataString("resourceVersion", "8")
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer server.Close()
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("rotating-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	k := &kubernetesTrustAnchor{name: "anchor", namespace: "ns", baseURL: server.URL, tokenPath: tokenPath, client: server.Client()}
	got, rv, err := k.Get(context.Background())
	if err != nil || rv != "7" || got.Token != record.Token {
		t.Fatalf("get = %#v rv=%q err=%v", got, rv, err)
	}
	got.Epoch = 1
	got.Token = uuid.NewString()
	updated, err := k.CAS(context.Background(), rv, got)
	if err != nil || updated != "8" {
		t.Fatalf("cas rv=%q err=%v", updated, err)
	}
	if _, err := k.CAS(context.Background(), rv, got); err == nil {
		t.Fatal("stale resource version accepted")
	}
}
