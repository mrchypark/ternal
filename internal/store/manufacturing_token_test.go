package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBatchLinkedManufacturingTokensAreOneUseAndConsumeQuota(t *testing.T) {
	ctx := context.Background()
	s, _ := anchoredStore(t)
	batch, _, err := s.CreateManufacturingBatch(ctx, "linked", "LINK", time.Now().Add(time.Hour).Unix(), 2, "actor")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateManufacturingToken(ctx, batch.ID, nil, "actor")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateManufacturingToken(ctx, batch.ID, nil, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollDevice(ctx, first.Token, strings.Repeat("a", 64), "LINK-000999", "model", "fingerprint", "key-wrong", "ops", 22, nil); err == nil {
		t.Fatal("caller-supplied mismatched serial was accepted")
	}
	firstDevice, err := s.EnrollDevice(ctx, first.Token, strings.Repeat("a", 64), "", "model", "fingerprint", "key-1", "ops", 22, nil)
	if err != nil || firstDevice.SerialNumber != "LINK-000001" {
		t.Fatalf("first enrollment = %#v, %v", firstDevice, err)
	}
	if _, err := s.EnrollDevice(ctx, first.Token, strings.Repeat("b", 64), "", "model", "fingerprint", "key-replay", "ops", 22, nil); err == nil {
		t.Fatal("replayed linked token was accepted")
	}
	secondDevice, err := s.EnrollDevice(ctx, second.Token, strings.Repeat("c", 64), "", "model", "fingerprint", "key-2", "ops", 22, nil)
	if err != nil || secondDevice.SerialNumber != "LINK-000002" {
		t.Fatalf("second enrollment = %#v, %v", secondDevice, err)
	}
	batches, err := s.ListManufacturingBatches(ctx)
	if err != nil || len(batches) != 1 || batches[0].UsedCount != 2 || batches[0].Status != "closed" {
		t.Fatalf("batch after two linked tokens = %#v, %v", batches, err)
	}
}

func TestBatchLinkedTokenIssuanceAndFailureAreFailClosed(t *testing.T) {
	ctx := context.Background()
	s, _ := anchoredStore(t)
	if _, err := s.CreateManufacturingToken(ctx, "missing", nil, "actor"); err == nil {
		t.Fatal("nonexistent batch accepted")
	}
	batch, _, err := s.CreateManufacturingBatch(ctx, "closed", "CLOSED", time.Now().Add(time.Hour).Unix(), 2, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseManufacturingBatch(ctx, batch.ID, "actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateManufacturingToken(ctx, batch.ID, nil, "actor"); err == nil {
		t.Fatal("closed batch accepted")
	}
	open, _, err := s.CreateManufacturingBatch(ctx, "expired", "EXPIRED", time.Now().Add(time.Hour).Unix(), 2, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE manufacturing_batches SET expires_at=0 WHERE id=?`, open.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateManufacturingToken(ctx, open.ID, nil, "actor"); err == nil {
		t.Fatal("expired batch accepted")
	}
	valid, _, err := s.CreateManufacturingBatch(ctx, "token-expiry", "TOKEN", time.Now().Add(time.Hour).Unix(), 1, "actor")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := s.CreateManufacturingToken(ctx, valid.ID, nil, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE manufacturing_tokens SET expires_at=0 WHERE id=?`, linked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollDevice(ctx, linked.Token, strings.Repeat("a", 64), "", "model", "fingerprint", "key", "ops", 22, nil); err == nil {
		t.Fatal("expired linked token enrolled")
	}
}

func TestBatchLinkedTokenRollbackAndNoBatchFallback(t *testing.T) {
	ctx := context.Background()
	s, _ := anchoredStore(t)
	batch, directToken, err := s.CreateManufacturingBatch(ctx, "rollback", "ROLL", time.Now().Add(time.Hour).Unix(), 2, "actor")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := s.CreateManufacturingToken(ctx, batch.ID, nil, "actor")
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := s.CreateHost(ctx, NewHost{Name: "ROLL-000001", EndpointID: strings.Repeat("d", 64), SSHUser: "ops"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollDevice(ctx, linked.Token, strings.Repeat("e", 64), "", "model", "fingerprint", "key", "ops", 22, nil); err == nil {
		t.Fatal("duplicate host enrollment succeeded")
	}
	if available, err := s.GetManufacturingToken(ctx, linked.Token); err != nil || available == nil {
		t.Fatalf("failed enrollment consumed linked token: %#v, %v", available, err)
	}
	batches, err := s.ListManufacturingBatches(ctx)
	if err != nil || batches[0].UsedCount != 0 {
		t.Fatalf("failed enrollment consumed batch quota: %#v, %v", batches, err)
	}
	if err := s.DeleteHost(ctx, conflict.ID, "actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollDevice(ctx, linked.Token, strings.Repeat("e", 64), "", "model", "fingerprint", "key", "ops", 22, nil); err != nil {
		t.Fatalf("linked token did not recover after rollback: %v", err)
	}
	// A legacy record wins over a same-hash direct credential and must not fall
	// through to the direct batch when its declared batch is unavailable.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO manufacturing_tokens (id,token_hash,batch_id,expires_at,created_at) VALUES (?,?,?,?,?)`, uuid.NewString(), hashSecret(directToken), "missing", nil, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollDevice(ctx, directToken, strings.Repeat("f", 64), "", "model", "fingerprint", "key", "ops", 22, nil); err == nil {
		t.Fatal("wrong batch token fell back to direct credential")
	}
}
