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

// withStartupBudget shortens the foreground settlement window so a test can
// reach the deadline path without waiting the production budget out.
func withStartupBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	previous := trustStartupBudget
	trustStartupBudget = budget
	t.Cleanup(func() { trustStartupBudget = previous })
}

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

// snapshot is the race-free view a concurrent check needs: the anchor service
// answers requests on its own goroutines while the test reads the record.
func (c *countingAnchor) snapshot() (trustAnchorRecord, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.record, c.rv
}

func (c *countingAnchor) setBootstrap(bootstrap bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record.Bootstrap = bootstrap
}

// lostResponseAnchor applies a reservation and then reports the call as
// failed, which is the window a compare-and-swap response is lost in: the
// anchor holds the reservation while the caller never learned its outcome.
type lostResponseAnchor struct {
	trustAnchorBackend
	mu       sync.Mutex
	lost     bool
	proposed string
}

func (a *lostResponseAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	updated, err := a.trustAnchorBackend.CAS(ctx, rv, next)
	if err != nil || next.PendingEpoch == nil {
		return updated, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lost {
		return updated, nil
	}
	a.lost = true
	a.proposed = next.PendingID
	return "", fmt.Errorf("the reservation response was lost")
}

func (a *lostResponseAnchor) proposedReservation() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.proposed
}

// delayedCASAnchor retains the first reservation it is asked to publish and
// reports that call as failed without applying it.  The anchor keeps showing
// the predecessor to the next read, and the retained write lands only when
// the retry reaches the compare-and-swap: the schedule where a retry reads the
// anchor before the attempt it is retrying has become visible.
type delayedCASAnchor struct {
	trustAnchorBackend
	mu        sync.Mutex
	held      bool
	heldRec   trustAnchorRecord
	heldRV    string
	applied   bool
	published int
	proposed  []string
}

func (a *delayedCASAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	a.mu.Lock()
	if next.PendingEpoch != nil && !a.held {
		a.held = true
		a.heldRec = next
		a.heldRV = rv
		a.proposed = append(a.proposed, next.PendingID)
		a.mu.Unlock()
		return "", fmt.Errorf("the reservation outcome is unknown")
	}
	if next.PendingEpoch != nil && !a.applied {
		// The retained attempt lands now, exactly as the retry reaches the
		// compare-and-swap.  The retry read the anchor before this
		// application, so it competes with the write it is retrying.
		a.applied = true
		a.proposed = append(a.proposed, next.PendingID)
		held, heldRV := a.heldRec, a.heldRV
		a.mu.Unlock()
		if _, err := a.trustAnchorBackend.CAS(ctx, heldRV, held); err != nil {
			return "", err
		}
		a.mu.Lock()
		a.published++
		a.mu.Unlock()
		return a.trustAnchorBackend.CAS(ctx, rv, next)
	}
	a.mu.Unlock()
	updated, err := a.trustAnchorBackend.CAS(ctx, rv, next)
	if err == nil && next.PendingEpoch != nil {
		a.mu.Lock()
		a.published++
		a.mu.Unlock()
	}
	return updated, err
}

func (a *delayedCASAnchor) proposals() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.proposed...)
}

func (a *delayedCASAnchor) reservationCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.published
}

// pendingID reports the reservation the anchor currently carries.
func (a *delayedCASAnchor) pendingID() string {
	record, _, err := a.Get(context.Background())
	if err != nil {
		return fmt.Sprintf("unreadable: %v", err)
	}
	return record.PendingID
}

// trustAnchorEnv points the store at a shared object store with a trust
// anchor, which is the HA configuration the bootstrap deadlock was reported
// for.
func trustAnchorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "trust-startup")
	t.Setenv("TERNAL_DATA_NODE_ID", "node-1")
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", "filesystem")
	t.Setenv("TERNAL_OBJECT_STORE_DIR", t.TempDir())
	t.Setenv("TERNAL_OBJECT_STORE_PREFIX", "clusters/trust-startup")
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "test-anchor")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
}

// useAnchorBackend makes every store opened by this test read the given anchor.
func useAnchorBackend(t *testing.T, backend trustAnchorBackend) {
	t.Helper()
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return backend }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
}

// objectStoreEnv is trustAnchorEnv plus the anchor backend the store will use.
func objectStoreEnv(t *testing.T, conflicts int) *countingAnchor {
	t.Helper()
	trustAnchorEnv(t)
	backend := &countingAnchor{record: bootstrapAnchorRecord(), conflicts: conflicts}
	useAnchorBackend(t, backend)
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
	anchor := s.db.trust.Load()
	base, pending := pendingFence(t, backend)
	peer := &countingAnchor{record: pending, rv: 1}
	anchor.backend = peer
	commitTrustTail(t, s, pending, base)

	if err := s.settleTrustAnchor(ctx, anchor, &trustStartup{}); err != nil {
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
	anchor := s.db.trust.Load()
	base, pending := pendingFence(t, backend)
	peer := &countingAnchor{record: pending, rv: 1}
	anchor.backend = peer

	shortCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	err := s.settleTrustAnchor(shortCtx, anchor, &trustStartup{})
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
	if err := s.settleTrustAnchor(ctx, anchor, &trustStartup{}); err != nil {
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
	anchor := s.db.trust.Load()
	base, pending := pendingFence(t, backend)
	owned := &countingAnchor{record: pending, rv: 1}
	anchor.backend = owned
	commitTrustTail(t, s, pending, base)

	state := trustStartup{attempted: pending.PendingID, reserved: true, pendingID: pending.PendingID}
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
	anchor := s.db.trust.Load()
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

// TestTrustActivationNeverServesTheUnfencedPath covers publication of the
// anchor.  A deployment that configured a fence makes the store closed to
// unfenced operations for its whole life, and activation publishes the anchor
// without relaxing that: a request that read the anchor before publication
// observed exactly the pre-activation state, and letting it fall through to the
// raw path would commit a mutation with no trust transition.  A restored
// snapshot could then keep the same epoch and token while omitting that
// mutation, which is the failure the anchor exists to catch.
func TestTrustActivationNeverServesTheUnfencedPath(t *testing.T) {
	ctx := context.Background()
	s, anchor, backend := unpublishedAnchoredStore(t)
	s.db.requireTrust()
	// Activation publishes the anchor, exactly as background settlement does
	// once the pair is established here.
	s.db.enableTrust(anchor)

	// A request that read the anchor before that publication observed an empty
	// anchor on a store whose deployment configured one.  Whatever it does
	// next, it may not be served unfenced: the requirement belongs to the
	// deployment and has to outlive publication.
	s.db.trust.Store(nil)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err == nil {
		t.Fatal("a configured store served an unfenced mutation")
	}
	if _, err := s.db.QueryContext(ctx, `SELECT 1`); err == nil {
		t.Fatal("a configured store served an unfenced read")
	}
	s.db.trust.Store(anchor)

	base, _, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if base.PendingEpoch != nil || base.Epoch != 0 || !base.Bootstrap {
		t.Fatalf("activation mutated the anchor: %#v", base)
	}
	if err := s.db.verifyDBTrust(ctx, base.Epoch, base.Token); err != nil {
		t.Fatal(err)
	}
}

// TestOpenKeepsReservationOwnershipAcrossTheForegroundDeadline covers the
// reservation this process proposed and the anchor now carries: the foreground
// attempt times out before the tail is submitted, and the background
// continuation has to recognize the fence as its own instead of waiting on a
// peer operation that will never appear.  It also covers the lost CAS
// response: the anchor holds the reservation while this process never learned
// that the compare-and-swap was applied.
func TestOpenKeepsReservationOwnershipAcrossTheForegroundDeadline(t *testing.T) {
	ctx := context.Background()
	withStartupBudget(t, 300*time.Millisecond)
	trustAnchorEnv(t)
	backend := &lostResponseAnchor{trustAnchorBackend: &countingAnchor{record: bootstrapAnchorRecord()}}
	useAnchorBackend(t, backend)

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("voter failed to start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := s.Ready(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("voter never settled the reservation it proposed")
		}
		time.Sleep(25 * time.Millisecond)
	}
	proposed := backend.proposedReservation()
	if proposed == "" {
		t.Fatal("no reservation was proposed")
	}
	final, _, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingEpoch != nil || final.Epoch != 1 || final.Token != proposed || final.Bootstrap {
		t.Fatalf("anchor after the deadline = %#v, want the proposed reservation %s finalized", final, proposed)
	}
	if err := s.db.verifyDBTrust(ctx, final.Epoch, final.Token); err != nil {
		t.Fatal(err)
	}
}

// TestOpenRecognizesItsOwnReservationWhenTheRetryOutrunsTheFirstAttempt covers
// the retry that reads the anchor before the attempt it is retrying has been
// applied.  The first reservation is retained by the backend and reported as
// an unknown outcome while the anchor still shows the predecessor; the retry
// then competes with that delayed write.  The retry has to keep proposing the
// same reservation identity, because that identity is the only thing that
// proves the fence the delayed attempt published is this Open's own.  Minting
// a fresh one leaves the applied reservation stranded: no peer submitted its
// trust tail, this Open cannot claim it, and readiness never arrives.
func TestOpenRecognizesItsOwnReservationWhenTheRetryOutrunsTheFirstAttempt(t *testing.T) {
	ctx := context.Background()
	withStartupBudget(t, 300*time.Millisecond)
	trustAnchorEnv(t)
	backend := &delayedCASAnchor{trustAnchorBackend: &countingAnchor{record: bootstrapAnchorRecord()}}
	useAnchorBackend(t, backend)

	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("voter failed to start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := s.Ready(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("voter never settled the reservation it proposed; anchor holds pending %q, this Open proposed %v", backend.pendingID(), backend.proposals())
		}
		time.Sleep(25 * time.Millisecond)
	}

	proposals := backend.proposals()
	if len(proposals) < 2 {
		t.Fatalf("expected the delayed attempt and its retry to both propose, got %v", proposals)
	}
	for _, proposed := range proposals {
		if proposed != proposals[0] {
			t.Fatalf("retry replaced the reservation identity: %v", proposals)
		}
	}
	if got := backend.reservationCount(); got != 1 {
		t.Fatalf("anchor published %d reservations, want 1", got)
	}
	final, _, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingEpoch != nil || final.Epoch != 1 || final.Token != proposals[0] || final.Bootstrap {
		t.Fatalf("anchor after the delayed compare-and-swap = %#v, want the proposed reservation %s finalized", final, proposals[0])
	}
	if err := s.db.verifyDBTrust(ctx, final.Epoch, final.Token); err != nil {
		t.Fatal(err)
	}
}

// TestTrustStartupReservesOnlyUnderTheBootstrapItObserved covers the starter
// racing a peer that consumes Bootstrap: this node reads an unmigrated database
// under a bootstrap anchor, the peer then establishes the pair, and this node
// re-reads the anchor while reserving.  The reservation may only be taken
// under the record it decided on, so the peer's finalized anchor makes the
// compare-and-swap conflict instead of the reservation being taken again on an
// anchor that is already established.
func TestTrustStartupReservesOnlyUnderTheBootstrapItObserved(t *testing.T) {
	ctx := context.Background()
	s, _, backend := unpublishedAnchoredStore(t)
	observed, _, err := backend.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.Bootstrap {
		t.Fatal("the observed anchor is not a bootstrap anchor")
	}
	// The peer establishes the pair in between: it consumes Bootstrap,
	// advances the epoch, and its trust tail replicates into this database.
	peerRecord := observed
	peerRecord.Epoch = 1
	peerRecord.Token = uuid.NewString()
	peerRecord.Bootstrap = false
	peer := &countingAnchor{record: peerRecord, rv: 2}
	if _, err := s.db.execRaw(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: `UPDATE trust_state SET epoch=?, token=? WHERE id=1`, Args: []any{peerRecord.Epoch, peerRecord.Token}}); err != nil {
		t.Fatal(err)
	}
	anchor := &trustAnchor{backend: peer, binding: storageBindingFromEnv()}

	// The reservation is decided on the record this node read.  Handing that
	// stale record to the reservation has to leave the peer's finalized anchor
	// alone: re-reading it here is what used to turn a consumed bootstrap into
	// a second fence and put the shared anchor back into pending state.
	if _, err := s.beginMigrationFence(ctx, anchor, observed, "1", &trustStartup{}); err == nil {
		t.Fatal("reserved a second fence under a bootstrap the peer had consumed")
	}
	if got := peer.pendingCount(); got != 0 {
		t.Fatalf("starter reserved %d fences after the peer consumed bootstrap", got)
	}
	final, _, err := peer.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.PendingEpoch != nil || final.Epoch != peerRecord.Epoch || final.Token != peerRecord.Token || final.Bootstrap {
		t.Fatalf("peer's finalized anchor was rewritten: %#v", final)
	}
}
