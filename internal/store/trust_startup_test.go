package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
)

// countingAnchor records how many migration fences the store reserved and can
// fail a compare-and-swap to emulate a peer winning the race.
type countingAnchor struct {
	mu           sync.Mutex
	record       trustAnchorRecord
	rv           int64
	conflicts    int
	reservations int
}

func (c *countingAnchor) Get(context.Context) (trustAnchorRecord, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.record, fmt.Sprint(c.rv), nil
}

func (c *countingAnchor) CAS(_ context.Context, rv string, next trustAnchorRecord) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rv != fmt.Sprint(c.rv) {
		return "", fmt.Errorf("conflict")
	}
	if c.conflicts > 0 {
		c.conflicts--
		return "", fmt.Errorf("conflict")
	}
	if next.PendingEpoch != nil {
		c.reservations++
	}
	c.record = next
	c.rv++
	return fmt.Sprint(c.rv), nil
}

func (c *countingAnchor) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reservations
}

// objectStoreEnv points the store at a shared object store with a trust
// anchor, which is the HA configuration the bootstrap deadlock was reported
// for, and returns the anchor backend the store will use.
func objectStoreEnv(t *testing.T, conflicts int) *countingAnchor {
	t.Helper()
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "trust-startup")
	t.Setenv("TERNAL_DATA_NODE_ID", "node-1")
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", "filesystem")
	t.Setenv("TERNAL_OBJECT_STORE_DIR", t.TempDir())
	t.Setenv("TERNAL_OBJECT_STORE_PREFIX", "clusters/trust-startup")
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "test-anchor")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	backend := &countingAnchor{record: bootstrapAnchorRecord(), conflicts: conflicts}
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return backend }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
	return backend
}

func bootstrapAnchorRecord() trustAnchorRecord {
	record := storageBindingFromEnv()
	record.Token = uuid.NewString()
	record.Bootstrap = true
	return record
}

const trustTailUpdate = "UPDATE trust_state SET epoch=?, token=? WHERE id=1 AND epoch=? AND token=?"

// commitTrustTail writes the pending half of a fence the way the reserving
// process does, so a test can play the peer or a partial attempt.
func commitTrustTail(t *testing.T, s *Store, pending trustAnchorRecord, base trustAnchorRecord) {
	t.Helper()
	request := rhiza.ExecuteRequest{RequestID: pending.PendingID, Statements: []rhiza.SQLStatement{{SQL: trustTailUpdate, Args: []any{*pending.PendingEpoch, pending.PendingID, base.Epoch, base.Token}}}}
	if _, err := s.db.execRaw(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

// pendingFence installs a peer's reservation in the anchor.
func pendingFence(t *testing.T, backend trustAnchorBackend) (trustAnchorRecord, trustAnchorRecord) {
	t.Helper()
	ctx := context.Background()
	base, rv, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := base.Epoch + 1
	pending := base
	pending.PendingEpoch = &next
	pending.PendingID = uuid.NewString()
	if _, err := backend.CAS(ctx, rv, pending); err != nil {
		t.Fatal(err)
	}
	return base, pending
}

// TestTrustStartupBootstrapsAndFinalizesTheAnchor is the reported failure: a
// greenfield HA voter must migrate, establish the pair, and finalize its fence
// instead of exiting on an unresolved pending write.
func TestTrustStartupBootstrapsAndFinalizesTheAnchor(t *testing.T) {
	ctx := context.Background()
	backend := objectStoreEnv(t, 0)

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("greenfield HA voter failed to start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	final, _, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingEpoch != nil || final.Epoch != 1 || final.Bootstrap {
		t.Fatalf("anchor after bootstrap = %#v", final)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("bootstrapped voter is not ready: %v", err)
	}
	if err := s.requireMigrated(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.verifyDBTrust(ctx, final.Epoch, final.Token); err != nil {
		t.Fatal(err)
	}
	if got := backend.pendingCount(); got != 1 {
		t.Fatalf("bootstrap reserved %d fences, want 1", got)
	}

	// A voter with a fresh data directory joins the finalized anchor.
	joined, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("joining voter failed to start: %v", err)
	}
	t.Cleanup(func() { _ = joined.Close() })
	if err := joined.Ready(ctx); err != nil {
		t.Fatalf("joining voter is not ready: %v", err)
	}
	if err := joined.db.verifyDBTrust(ctx, final.Epoch, final.Token); err != nil {
		t.Fatal(err)
	}
	if got := backend.pendingCount(); got != 1 {
		t.Fatalf("joining voter reserved %d fences, want 1", got)
	}
}

// TestTrustStartupRetriesThroughAnchorContention covers the CAS loser: it
// retries inside the same Open and never leaves a second reservation behind.
func TestTrustStartupRetriesThroughAnchorContention(t *testing.T) {
	ctx := context.Background()
	backend := objectStoreEnv(t, 1)

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("contended voter failed to start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	final, _, _ := backend.Get(ctx)
	if final.PendingEpoch != nil || final.Epoch != 1 {
		t.Fatalf("anchor after contention = %#v", final)
	}
	if got := backend.pendingCount(); got != 1 {
		t.Fatalf("contended Open reserved %d fences, want 1", got)
	}
}

// TestTrustStartupJoinsPeerFenceWithoutReservingAnother covers the follower: a
// peer's fence is a catch-up obligation, never a reason to advance the epoch.
func TestTrustStartupJoinsPeerFenceWithoutReservingAnother(t *testing.T) {
	ctx := context.Background()
	s, backend := anchoredStore(t)
	anchor := s.db.trust
	base, pending := pendingFence(t, backend)
	peer := &countingAnchor{record: pending, rv: 1}
	anchor.backend = peer
	commitTrustTail(t, s, pending, base)

	if err := s.settleTrustAnchor(ctx, anchor); err != nil {
		t.Fatalf("follower did not converge on the peer fence: %v", err)
	}
	final, _, err := peer.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingEpoch != nil || final.Epoch != *pending.PendingEpoch || final.Token != pending.PendingID || final.Bootstrap {
		t.Fatalf("follower final anchor = %#v", final)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("follower is not ready after joining: %v", err)
	}
	if got := peer.pendingCount(); got != 0 {
		t.Fatalf("follower reserved %d fences", got)
	}
}

// TestTrustStartupWithholdsReadinessForUnknownPeerFence keeps the data node
// alive and unready instead of exiting on evidence this voter cannot prove.
func TestTrustStartupWithholdsReadinessForUnknownPeerFence(t *testing.T) {
	ctx := context.Background()
	s, backend := anchoredStore(t)
	anchor := s.db.trust
	base, pending := pendingFence(t, backend)
	peer := &countingAnchor{record: pending, rv: 1}
	anchor.backend = peer

	shortCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	err := s.settleTrustAnchor(shortCtx, anchor)
	if err == nil {
		t.Fatal("unprovable peer fence was treated as settled")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
	current, _, _ := peer.Get(ctx)
	if current.PendingEpoch == nil || current.PendingID != pending.PendingID || current.Epoch != base.Epoch {
		t.Fatalf("voter rewrote an unprovable fence: %#v", current)
	}
	if got := peer.pendingCount(); got != 0 {
		t.Fatalf("voter reserved %d fences while catching up", got)
	}
	if _, err := s.db.queryRaw(ctx, "SELECT 1"); err != nil {
		t.Fatalf("data node stopped serving while catching up: %v", err)
	}
	// The store keeps serving its data node while withholding readiness, so
	// the peer that holds the fence still has a quorum to commit with.
	s.settleMu.Lock()
	s.trustNeeded = true
	s.settleMu.Unlock()
	if err := s.Ready(ctx); err == nil {
		t.Fatal("unready voter reported readiness")
	}
	// Replication delivers the peer's committed tail.  Only then may this
	// voter finalize the fence and report readiness, exactly as Open does.
	commitTrustTail(t, s, pending, base)
	if err := s.settleTrustAnchor(ctx, anchor); err != nil {
		t.Fatalf("voter did not converge once the tail arrived: %v", err)
	}
	s.db.enableTrust(anchor)
	s.markTrustSettled()
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("settled voter is not ready: %v", err)
	}
	final, _, _ := peer.Get(ctx)
	if final.PendingEpoch != nil || final.Epoch != *pending.PendingEpoch || final.Token != pending.PendingID {
		t.Fatalf("converged peer fence = %#v", final)
	}
	if got := peer.pendingCount(); got != 0 {
		t.Fatalf("catching-up voter reserved %d fences", got)
	}
}

// TestTrustStartupRetriesItsOwnFenceWithoutReservingAgain covers the partial
// attempt: a durable tail plus a lost finalization resumes the original
// reservation instead of taking a new one.
func TestTrustStartupRetriesItsOwnFenceWithoutReservingAgain(t *testing.T) {
	ctx := context.Background()
	s, backend := anchoredStore(t)
	anchor := s.db.trust
	base, pending := pendingFence(t, backend)
	owned := &countingAnchor{record: pending, rv: 1}
	anchor.backend = owned
	commitTrustTail(t, s, pending, base)

	state := trustStartup{reserved: true, pendingID: pending.PendingID}
	if err := s.settleTrustAnchorOnce(ctx, anchor, &state); err != nil {
		t.Fatalf("resumed fence: %v", err)
	}
	final, _, _ := owned.Get(ctx)
	if final.PendingEpoch != nil || final.Epoch != *pending.PendingEpoch || final.Token != pending.PendingID {
		t.Fatalf("resumed final anchor = %#v", final)
	}
	if got := owned.pendingCount(); got != 0 {
		t.Fatalf("resume reserved %d fences, want the original one", got)
	}
}

// TestTrustFenceFinalizationRequiresTheMigrationMarker proves a fence only
// closes once the migration it was reserved for is durable here.
func TestTrustFenceFinalizationRequiresTheMigrationMarker(t *testing.T) {
	ctx := context.Background()
	s, backend := anchoredStore(t)
	anchor := s.db.trust
	base, pending := pendingFence(t, backend)
	commitTrustTail(t, s, pending, base)
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: "DROP TABLE ternal_schema"}); err != nil {
		t.Fatal(err)
	}
	if err := s.finalizeTrustFence(ctx, anchor, pending); err == nil {
		t.Fatal("fence finalized without a durable migration")
	}
	current, _, _ := backend.Get(ctx)
	if current.PendingEpoch == nil || current.PendingID != pending.PendingID {
		t.Fatalf("failed proof consumed the fence: %#v", current)
	}
}

// TestOpenKeepsUnsettledVoterAliveAndConverges is the reported deadlock: the
// voter that cannot yet prove the anchored pair must stay up and unready, and
// must become ready once the obligation is resolvable here.
func TestOpenKeepsUnsettledVoterAliveAndConverges(t *testing.T) {
	ctx := context.Background()
	backend := objectStoreEnv(t, 0)
	base, rv, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := base.Epoch + 1
	pending := base
	pending.PendingEpoch = &next
	pending.PendingID = uuid.NewString()
	if _, err := backend.CAS(ctx, rv, pending); err != nil {
		t.Fatal(err)
	}
	// The peer's reservation above is the test's own; the voter under test must
	// not add another one.
	setupReservations := backend.pendingCount()

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("voter exited instead of waiting for the fence: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Ready(ctx); err == nil {
		t.Fatal("voter reported readiness with an unprovable fence")
	}
	if _, err := s.db.queryRaw(ctx, "SELECT 1"); err != nil {
		t.Fatalf("voter stopped serving while catching up: %v", err)
	}
	// Ternal data stays closed until the pair is established here.
	if _, err := s.CreateHost(ctx, NewHost{Name: "unsettled", EndpointID: strings.Repeat("c", 64), SSHUser: "ops"}, "system"); err == nil {
		t.Fatal("unsettled voter served a fenced write")
	}

	// The obligation becomes provable here the way replication makes it: the
	// database carries the base pair and the committed tail.
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: []rhiza.SQLStatement{{SQL: "CREATE TABLE trust_state (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL, token TEXT NOT NULL)"}, {SQL: "INSERT INTO trust_state (id,epoch,token) VALUES (1,?,?)", Args: []any{base.Epoch, base.Token}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: pending.PendingID, Statements: []rhiza.SQLStatement{{SQL: trustTailUpdate, Args: []any{next, pending.PendingID, base.Epoch, base.Token}}}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := s.Ready(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("voter never converged on the resolvable fence")
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, _, _ := backend.Get(ctx)
	if final.PendingEpoch != nil || final.Epoch != next || final.Token != pending.PendingID {
		t.Fatalf("converged anchor = %#v", final)
	}
	if got := backend.pendingCount(); got != setupReservations {
		t.Fatalf("converging voter reserved %d fences, want only the peer's %d", got, setupReservations)
	}
}
