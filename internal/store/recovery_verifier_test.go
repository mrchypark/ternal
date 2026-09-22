package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza/pkg/checkpoint"
	"github.com/mrchypark/rhiza/pkg/materializer"
	"github.com/mrchypark/rhiza/pkg/qlog"
	"github.com/mrchypark/rhiza/pkg/quepaxa"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
	"github.com/thanos-io/objstore"
)

// recoveryFixture builds a real source generation: a certified checkpoint of a
// trust_state pair, forked into a target prefix under the configured root.  The
// verifier is then checked against the archive the voters actually produce
// rather than against a hand-written object.
func recoveryFixture(t *testing.T) (objstore.Bucket, recoveryStorage, recoveryanchor.Request, trustAnchorRecord) {
	t.Helper()
	ctx := context.Background()
	bucket := objstore.NewInMemBucket()
	storage := recoveryStorage{Provider: "s3", Endpoint: "minio.ternal-operator-e2e.svc:9000", Bucket: "ternal-e2e", Prefix: "clusters/ternal-e2e-a1"}
	sourceID, targetID := "ternal-e2e-a1", "rhiza-r-recovered"
	sourcePrefix, targetPrefix := storage.prefix(sourceID), storage.prefix(targetID)
	sourceMembers := []quepaxa.Member{{ID: "a1", Token: "a1-token"}}
	wal, err := qlog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	core, err := quepaxa.New(quepaxa.Config{NodeID: "a1", Cluster: quepaxa.Cluster{ConfigID: 1, Members: sourceMembers}, WAL: wal})
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	for _, statement := range []string{
		`CREATE TABLE trust_state (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL, token TEXT NOT NULL)`,
		`INSERT INTO trust_state (id, epoch, token) VALUES (1, 3, '` + token + `')`,
	} {
		if _, _, err := core.Propose(ctx, []byte(statement)); err != nil {
			t.Fatal(err)
		}
	}
	state, err := materializer.Open(filepath.Join(t.TempDir(), "sqlite.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	for slot := quepaxa.Slot(1); slot <= core.Tip(); slot++ {
		decision, ok := core.CertifiedValue(slot)
		if !ok {
			t.Fatalf("missing decision at slot %d", slot)
		}
		if err := state.ApplyBatch(ctx, []quepaxa.DecidedValue{decision}); err != nil {
			t.Fatal(err)
		}
	}
	claim := publishCheckpoint(t, ctx, bucket, state, core, sourcePrefix)
	archive := recovery.NewManager(bucket, sourcePrefix, 1)
	defer archive.Close()
	if err := archive.SyncThrough(ctx, core, core.Tip()); err != nil {
		t.Fatal(err)
	}
	sealed, ok, err := core.LatestCheckpointSeal()
	if err != nil || !ok {
		t.Fatalf("latest checkpoint seal: %v", err)
	}
	base, ok := core.CertifiedValue(sealed.DecisionSlot)
	if !ok {
		t.Fatal("missing seal decision")
	}
	if err := archive.TrimThrough(ctx, sealed, base); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.NewManager(bucket, sourcePrefix, "", 1).ReleasePublisherClaim(ctx, claim); err != nil {
		t.Fatal(err)
	}
	opID := uuid.NewString()
	if err := recovery.Seal(ctx, bucket, sourcePrefix, opID); err != nil {
		t.Fatal(err)
	}
	targetMembers := []quepaxa.Member{{ID: "a2", Token: "a2-token"}}
	targetMembership := recovery.NewMembershipRecord(targetID, targetMembers, "async")
	result, err := recovery.Fork(ctx, bucket, recovery.ForkOptions{
		SourcePrefix: sourcePrefix, TargetPrefix: targetPrefix,
		SourceBootstrap: quepaxa.Cluster{ConfigID: 1, Members: sourceMembers},
		TargetMembers:   targetMembers, TargetMembership: targetMembership, OperationID: opID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, anchorHash, err := recovery.ReadGenerationAnchor(ctx, bucket, targetPrefix)
	if err != nil {
		t.Fatal(err)
	}
	record := trustAnchorRecord{Format: "1", ClusterID: sourceID, StorageID: storage.identity(sourceID), Epoch: 3, Token: token}
	request := recoveryanchor.Request{
		AnchorID: "ternal-anchor", OperationID: opID, StatefulSetUID: uuid.NewString(),
		SourceClusterID: sourceID, TargetClusterID: targetID,
		SourcePrefix: sourcePrefix, TargetPrefix: targetPrefix,
		SourceMembership: recovery.NewMembershipRecord(sourceID, sourceMembers, "async").Membership,
		FenceHash:        hex64(), TargetAnchorHash: hex.EncodeToString(anchorHash[:]),
		Fork: result, TargetMembership: targetMembership,
	}
	return bucket, storage, request, record
}

// publishCheckpoint certifies the materialized state and records the seal the
// archive requires before a generation can be forked.
func publishCheckpoint(t *testing.T, ctx context.Context, bucket objstore.Bucket, state *materializer.Materializer, core *quepaxa.Core, prefix string) *checkpoint.PublisherClaim {
	t.Helper()
	manager := checkpoint.NewManager(bucket, prefix, "", 1)
	claim, err := manager.AcquirePublisherClaim(ctx, prefix, 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	files, index, cleanup, err := state.CheckpointFilesAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	sources := make([]checkpoint.Source, 0, len(files))
	for _, file := range files {
		sources = append(sources, checkpoint.Source{Role: string(file.Role), Path: file.Path})
	}
	root, err := manager.CreateFiles(ctx, claim, sources, index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BindPublisherClaim(ctx, claim, index, root.RootHash, time.Minute); err != nil {
		t.Fatal(err)
	}
	prefixHash, ok := core.PrefixHash(core.Tip())
	if !ok {
		t.Fatal("missing prefix hash")
	}
	next, following, err := core.CheckpointLeaderOrders(core.Tip())
	if err != nil {
		t.Fatal(err)
	}
	core.SetCheckpointValidator(func(context.Context, quepaxa.CheckpointSeal) error { return nil })
	seal := quepaxa.CheckpointSeal{ConfigID: 1, Index: core.Tip(), RootHash: root.RootHash, StateHash: root.Hash, PrefixHash: prefixHash, NextLeaderOrder: next, FollowingLeaderOrder: following}
	if err := core.PrepareCheckpoint(ctx, seal); err != nil {
		t.Fatal(err)
	}
	encoded, err := quepaxa.EncodeCheckpointSeal(seal)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := core.Propose(ctx, encoded); err != nil {
		t.Fatal(err)
	}
	return claim
}

// coordinatorFor wires the real verifier to the record adapter over an
// in-memory anchor, which is the same composition the anchor service runs.
func coordinatorFor(bucket objstore.Bucket, storage recoveryStorage, record trustAnchorRecord) (*recoveryanchor.Coordinator, *countingAnchor) {
	backend := &countingAnchor{record: record, rv: 1}
	return &recoveryanchor.Coordinator{
		Backend:        &recoveryAnchorBackend{anchor: backend, id: "ternal-anchor"},
		EvidenceFormat: trustAnchorEvidenceFormat,
		Verifier:       recoveryVerifier{bucket: bucket, storage: storage}.verify,
	}, backend
}

// TestRecoveryActivationCommitsOnlyProvenEvidence is the whole contract in one
// place: a target that really carries the frozen application pair commits once,
// and every mismatch leaves the frozen transition exactly where it was so a
// corrected retry can still settle it.
func TestRecoveryActivationCommitsOnlyProvenEvidence(t *testing.T) {
	ctx := context.Background()
	bucket, storage, request, record := recoveryFixture(t)
	frozen := record.evidence().bytes()

	t.Run("matching target commits once", func(t *testing.T) {
		coordinator, backend := coordinatorFor(bucket, storage, record)
		receipt, err := coordinator.Activate(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		committed := backend.record
		if receipt.Generation != 1 || receipt.Target.ClusterID != request.TargetClusterID || receipt.Target.StorageID != storage.identity(request.TargetClusterID) {
			t.Fatalf("receipt = %#v", receipt)
		}
		if committed.Generation != 1 || committed.Transition != nil || committed.Receipt == nil || committed.ClusterID != request.TargetClusterID || committed.StorageID != storage.identity(request.TargetClusterID) {
			t.Fatalf("committed record = %#v", committed)
		}
		if !bytes.Equal(committed.evidence().bytes(), frozen) {
			t.Fatalf("activation rewrote the application evidence: %s", committed.evidence().bytes())
		}
		// The proof the coordinator accepted is the pair read back out of the
		// recovered archive, so a replayed request settles on the same state.
		replayed, err := coordinator.Activate(ctx, request)
		if err != nil || replayed != receipt {
			t.Fatalf("replayed activation = %#v %v", replayed, err)
		}
	})

	cases := map[string]func(*recoveryanchor.Request, *trustAnchorRecord){
		"frozen pair does not match the recovered pair": func(_ *recoveryanchor.Request, rec *trustAnchorRecord) {
			rec.Epoch++
		},
		"anchor hash is not the archived anchor": func(req *recoveryanchor.Request, _ *trustAnchorRecord) {
			req.TargetAnchorHash = hex64()
		},
		"target membership differs from the archived registration": func(req *recoveryanchor.Request, _ *trustAnchorRecord) {
			req.TargetMembership = recovery.NewMembershipRecord(req.TargetClusterID, []quepaxa.Member{{ID: "other", Token: "other"}}, "async")
		},
		"fork result differs from the archived fork": func(req *recoveryanchor.Request, _ *trustAnchorRecord) {
			req.Fork.Tip++
		},
		"operation ID differs from the archived operation": func(req *recoveryanchor.Request, _ *trustAnchorRecord) {
			req.OperationID = uuid.NewString()
		},
		"source cluster differs from the anchored binding": func(req *recoveryanchor.Request, _ *trustAnchorRecord) {
			req.SourceClusterID = "ternal-e2e-a2"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			attempt := request
			before := record
			mutate(&attempt, &before)
			coordinator, backend := coordinatorFor(bucket, storage, before)
			if _, err := coordinator.Activate(ctx, attempt); err == nil {
				t.Fatal("activation committed without proven evidence")
			}
			refused := backend.record
			// Refusal may leave the request frozen, but it must never commit,
			// never clear the reservation, and never rewrite the anchored pair.
			if refused.Generation != 0 || refused.Receipt != nil || refused.ClusterID != before.ClusterID || refused.StorageID != before.StorageID || !bytes.Equal(refused.evidence().bytes(), before.evidence().bytes()) {
				t.Fatalf("refusal changed the anchored state: %#v", refused)
			}
			if refused.Transition != nil && (refused.Transition.Generation != before.Generation || !bytes.Equal(refused.Transition.Evidence, before.evidence().bytes())) {
				t.Fatalf("refusal corrupted the frozen transition: %#v", refused.Transition)
			}
		})
	}

	// A reserved transition belongs to the request that froze it: another
	// operation cannot settle it, and the anchor keeps failing closed until
	// the original request proves its target.
	reserved := request
	reserved.OperationID = uuid.NewString()
	coordinator, backend := coordinatorFor(bucket, storage, record)
	if _, err := coordinator.Activate(ctx, reserved); err == nil {
		t.Fatal("activation committed without proven evidence")
	}
	if _, err := coordinator.Activate(ctx, request); err == nil {
		t.Fatal("a different operation settled a reserved transition")
	}
	if backend.record.Generation != 0 || backend.record.Receipt != nil {
		t.Fatalf("reserved transition committed: %#v", backend.record)
	}
}

// TestRecoveryVerifierRejectsUnsettledOrForeignRequests covers the checks that
// run before the archive is opened, plus the two that must never be satisfied
// by the record itself: an unsettled application pair and a proof echoed from
// the frozen bytes instead of the recovered database.
func TestRecoveryVerifierRejectsUnsettledOrForeignRequests(t *testing.T) {
	ctx := context.Background()
	bucket, storage, request, record := recoveryFixture(t)
	verifier := recoveryVerifier{bucket: bucket, storage: storage}
	good := recoveryRecord(record)

	binding, proof, err := verifier.verify(ctx, request, good)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ClusterID != request.TargetClusterID || binding.StorageID != storage.identity(request.TargetClusterID) {
		t.Fatalf("target binding = %#v", binding)
	}
	if !bytes.Equal(proof, record.evidence().bytes()) {
		t.Fatalf("proof = %s, want the frozen pair", proof)
	}

	cases := map[string]func(*recoveryanchor.Request, *recoveryanchor.Record){
		"application write still pending": func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) { rec.PendingWrite = true },
		"evidence still carries a pending epoch": func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) {
			pending := int64(4)
			rec.Evidence = trustAnchorEvidence{Format: "1", Epoch: 3, Token: record.Token, PendingEpoch: &pending, PendingID: uuid.NewString()}.bytes()
		},
		"bootstrap was never consumed": func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) {
			rec.Evidence = trustAnchorEvidence{Format: "1", Epoch: 3, Token: record.Token, Bootstrap: true}.bytes()
		},
		"record format is not this integration's": func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) { rec.EvidenceFormat = "sql-epoch-token/v1" },
		"binding names another cluster":           func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) { rec.Binding.ClusterID = "ternal-e2e-a2" },
		"binding names another storage recipe": func(_ *recoveryanchor.Request, rec *recoveryanchor.Record) {
			rec.Binding.StorageID = storage.identity("ternal-e2e-a2")
		},
		"source prefix is outside the configured root": func(req *recoveryanchor.Request, _ *recoveryanchor.Record) {
			req.SourcePrefix = storage.prefix("ternal-e2e-a2")
		},
		"target prefix is double prefixed": func(req *recoveryanchor.Request, _ *recoveryanchor.Record) {
			req.TargetPrefix = path.Join(req.TargetPrefix, request.TargetClusterID)
		},
		"target prefix is outside the configured root": func(req *recoveryanchor.Request, _ *recoveryanchor.Record) {
			req.TargetPrefix = path.Join("elsewhere", request.TargetClusterID)
		},
		"anchor hash is not a digest": func(req *recoveryanchor.Request, _ *recoveryanchor.Record) { req.TargetAnchorHash = "not-a-digest" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			attempt, mutated := request, good
			mutate(&attempt, &mutated)
			if _, _, err := verifier.verify(ctx, attempt, mutated); err == nil {
				t.Fatal("verifier accepted a request it cannot prove")
			}
		})
	}

	// The proof is rebuilt from the recovered pair: an anchor record that
	// claims a different epoch cannot borrow the archive's proof.
	claimed := good
	claimed.Evidence = trustAnchorEvidence{Format: "1", Epoch: 4, Token: record.Token}.bytes()
	_, rebuilt, err := verifier.verify(ctx, request, claimed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rebuilt, claimed.Evidence) {
		t.Fatal("verifier echoed the frozen evidence instead of reading the recovered pair")
	}
}

// TestRecoveryStorageKeepsTheConfiguredRoot pins the prefix semantics the
// operator and the anchor service have to agree on: one root, one generation
// per recovery, and never a root derived from the generation being recovered.
func TestRecoveryStorageKeepsTheConfiguredRoot(t *testing.T) {
	storage := recoveryStorage{Provider: "s3", Endpoint: "minio:9000", Bucket: "ternal-e2e", Prefix: "clusters/ternal-e2e-a1"}
	for _, generation := range []struct{ cluster, prefix string }{
		{"ternal-e2e-a1", "clusters/ternal-e2e-a1/ternal-e2e-a1"},
		{"rhiza-r-first", "clusters/ternal-e2e-a1/rhiza-r-first"},
		{"rhiza-r-second", "clusters/ternal-e2e-a1/rhiza-r-second"},
	} {
		if got := storage.prefix(generation.cluster); got != generation.prefix {
			t.Fatalf("prefix(%q) = %q, want %q", generation.cluster, got, generation.prefix)
		}
		if got, want := storage.identity(generation.cluster), TrustAnchorStorageID("s3", "minio:9000", "ternal-e2e", "clusters/ternal-e2e-a1", generation.cluster); got != want {
			t.Fatalf("identity(%q) = %q, want %q", generation.cluster, got, want)
		}
	}

	// A provider this integration cannot verify against is an error, never a
	// silent fall back to local storage.
	for _, unsupported := range []recoveryStorage{{Provider: "gcs", Bucket: "b"}, {Provider: ""}, {Provider: "s3"}} {
		if _, err := unsupported.bucket(); err == nil {
			t.Fatalf("built a bucket for %#v", unsupported)
		}
	}
	if _, err := (recoveryStorage{Provider: "filesystem", Dir: t.TempDir()}).bucket(); err != nil {
		t.Fatal(err)
	}
}
