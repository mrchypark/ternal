package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
)

// flippableAnchor lets a test decide the outcome of one compare-and-swap, which
// is how a competing writer or a lost response is simulated deterministically.
type flippableAnchor struct {
	inner    trustAnchorBackend
	gate     chan struct{}
	failNext bool
}

func newFlippableAnchor(inner trustAnchorBackend) *flippableAnchor {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &flippableAnchor{inner: inner, gate: gate}
}

func (a *flippableAnchor) Get(ctx context.Context) (trustAnchorRecord, string, error) {
	return a.inner.Get(ctx)
}

func (a *flippableAnchor) CAS(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	<-a.gate
	fail := a.failNext
	a.gate <- struct{}{}
	if fail {
		return "", errors.New("injected compare-and-swap failure")
	}
	return a.inner.CAS(ctx, rv, next)
}

func (a *flippableAnchor) setFailNext(fail bool) {
	<-a.gate
	a.failNext = fail
	a.gate <- struct{}{}
}

func hex64() string { return strings.Repeat("a", 64) }

func recoveryRequest(anchorID string) recoveryanchor.Request {
	return recoveryanchor.Request{
		AnchorID:         anchorID,
		OperationID:      uuid.NewString(),
		StatefulSetUID:   uuid.NewString(),
		SourceClusterID:  "ternal",
		TargetClusterID:  "rhiza-r-target",
		SourcePrefix:     "root/ternal",
		TargetPrefix:     "root/rhiza-r-target",
		SourceMembership: "membership",
		FenceHash:        hex64(),
		TargetAnchorHash: hex64(),
		Fork:             recovery.ForkResult{Tip: 9, PrefixHash: hex64(), ManifestHash: hex64()},
		TargetMembership: recovery.MembershipRecord{Version: 1, Cluster: "rhiza-r-target", Membership: "membership", Durability: "async"},
	}
}

func anchorRecord(t *testing.T, backend trustAnchorBackend) trustAnchorRecord {
	t.Helper()
	record, _, err := backend.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func currentRV(t *testing.T, backend trustAnchorBackend) string {
	t.Helper()
	_, rv, err := backend.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rv
}

// testCoordinator accepts the frozen evidence as its proof, so a commit can only
// publish the state the transition froze.
func testCoordinator(t *testing.T, backend trustAnchorBackend, proof []byte) *recoveryanchor.Coordinator {
	t.Helper()
	return &recoveryanchor.Coordinator{
		Backend:        &recoveryAnchorBackend{anchor: backend, id: "anchor"},
		EvidenceFormat: trustAnchorEvidenceFormat,
		Verifier: func(_ context.Context, req recoveryanchor.Request, _ recoveryanchor.Record) (recoveryanchor.Binding, []byte, error) {
			return recoveryanchor.Binding{ClusterID: req.TargetClusterID, StorageID: "target-storage"}, proof, nil
		},
	}
}

// TestApplicationAndRecoveryReservationsExcludeEachOther drives both orders of
// the two reservations over one record, then proves the recovered voter serves
// again through the ordinary fenced path with its recovery state intact.
func TestApplicationAndRecoveryReservationsExcludeEachOther(t *testing.T) {
	ctx := context.Background()

	t.Run("pending application write blocks recovery", func(t *testing.T) {
		s, _, backend := unpublishedAnchoredStore(t)
		s.db.enableTrust(anchorFor(t, backend))
		if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err != nil {
			t.Fatal(err)
		}
		before := anchorRecord(t, backend)
		if before.Epoch != 1 || before.PendingEpoch != nil {
			t.Fatalf("application write did not advance the pair: %#v", before)
		}
		next := before.Epoch + 1
		pending := before
		pending.PendingEpoch = &next
		pending.PendingID = uuid.NewString()
		if _, err := backend.CAS(ctx, currentRV(t, backend), pending); err != nil {
			t.Fatal(err)
		}
		// Settlement has to be able to read its own unresolved reservation.
		read, _, err := (&recoveryAnchorBackend{anchor: backend, id: "anchor"}).Read(ctx, "anchor")
		if err != nil || !read.PendingWrite {
			t.Fatalf("pending write was unreadable: %#v %v", read, err)
		}
		if _, err := testCoordinator(t, backend, before.evidence().bytes()).Activate(ctx, recoveryRequest("anchor")); err == nil {
			t.Fatal("recovery reserved over a pending application write")
		}
		after := anchorRecord(t, backend)
		if after.Transition != nil || after.Generation != 0 || after.PendingID != pending.PendingID || after.Epoch != before.Epoch {
			t.Fatalf("refused activation changed the record: %#v", after)
		}
		// Clearing the reservation restores ordinary serving.
		if _, err := backend.CAS(ctx, currentRV(t, backend), before); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.QueryContext(ctx, `SELECT 1`); err != nil {
			t.Fatalf("cleared reservation did not restore serving: %v", err)
		}
	})

	t.Run("frozen recovery transition blocks the application", func(t *testing.T) {
		s, anchor, backend := unpublishedAnchoredStore(t)
		s.db.enableTrust(anchor)
		source := anchorRecord(t, backend)
		flippable := newFlippableAnchor(backend)
		anchor.backend = flippable
		coordinator := testCoordinator(t, flippable, source.evidence().bytes())
		request := recoveryRequest("anchor")
		// A freeze published by one attempt has to keep the application closed
		// even when the attempt that published it never came back.
		frozenRecord := source
		frozenRecord.Transition = &recoveryanchor.Transition{
			Request:    request,
			Source:     recoveryanchor.Binding{ClusterID: source.ClusterID, StorageID: source.StorageID},
			Generation: source.Generation,
			Evidence:   source.evidence().bytes(),
		}
		if err := (&recoveryAnchorBackend{anchor: flippable, id: "anchor"}).CAS(ctx, "anchor", currentRV(t, flippable), recoveryRecord(frozenRecord)); err != nil {
			t.Fatal(err)
		}
		frozen := anchorRecord(t, flippable)
		if frozen.Transition == nil || frozen.Generation != 0 || frozen.Transition.Request.OperationID != request.OperationID {
			t.Fatalf("reserve did not freeze the request: %#v", frozen.Transition)
		}
		if err := anchor.validate(frozen); err == nil {
			t.Fatal("frozen transition was accepted for serving")
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err == nil {
			t.Fatal("application wrote through a frozen recovery transition")
		}
		if _, err := s.db.QueryContext(ctx, `SELECT 1`); err == nil {
			t.Fatal("application read through a frozen recovery transition")
		}
		if err := s.db.verifyDBTrust(ctx, frozen.Epoch, frozen.Token); err != nil {
			t.Fatalf("frozen transition moved the application pair: %v", err)
		}

		receipt, err := coordinator.Activate(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		// The same operation retried after a lost response returns its own
		// committed receipt instead of advancing a second generation.
		replayed, err := coordinator.Activate(ctx, request)
		if err != nil || replayed != receipt {
			t.Fatalf("retried activation = %#v, %v; want %#v", replayed, err, receipt)
		}
		if receipt.Generation != 1 || receipt.Target.ClusterID != request.TargetClusterID {
			t.Fatalf("receipt = %#v", receipt)
		}
		committed := anchorRecord(t, flippable)
		if committed.Transition != nil || committed.Generation != 1 || committed.ClusterID != request.TargetClusterID || committed.Epoch != frozen.Epoch || committed.Token != frozen.Token || committed.Bootstrap != frozen.Bootstrap {
			t.Fatalf("commit = %#v", committed)
		}
		if committed.Receipt == nil || committed.Receipt.OperationID != request.OperationID {
			t.Fatalf("commit receipt = %#v", committed.Receipt)
		}
		// The recovered voter adopts the target binding and serves again.
		anchor.binding.ClusterID = request.TargetClusterID
		anchor.binding.StorageID = receipt.Target.StorageID
		if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (id,user_id,public_key,fingerprint,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), "u", "key", "fp", 1); err != nil {
			t.Fatalf("recovered voter could not serve: %v", err)
		}
		advanced := anchorRecord(t, flippable)
		if advanced.Epoch != frozen.Epoch+1 || advanced.Generation != 1 || advanced.Receipt == nil || advanced.Receipt.OperationID != request.OperationID {
			t.Fatalf("ordinary write after recovery = %#v", advanced)
		}
	})
}

// anchorFor rebuilds the binding a backend already holds, which is how the
// serving wrapper is published without repeating the deployment recipe.
func anchorFor(t *testing.T, backend trustAnchorBackend) *trustAnchor {
	t.Helper()
	record := anchorRecord(t, backend)
	record.Epoch = 0
	record.Token = ""
	record.PendingEpoch = nil
	record.PendingID = ""
	record.Bootstrap = false
	record.Generation = 0
	record.Transition = nil
	record.Receipt = nil
	return &trustAnchor{backend: backend, binding: record}
}
