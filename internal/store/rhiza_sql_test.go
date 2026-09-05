package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mrchypark/rhiza"
)

func TestWaitUntilReady(t *testing.T) {
	checks := 0
	if err := waitUntilReady(context.Background(), func() bool {
		checks++
		return checks == 2
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitUntilReady(ctx, func() bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness wait returned %v", err)
	}
}

func TestWaitUntilReadableRequiresLinearizableRead(t *testing.T) {
	queries := 0
	if err := waitUntilReadable(context.Background(), func() bool { return true }, func(context.Context) error {
		queries++
		if queries == 1 {
			return errors.New("quorum unavailable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("linearizable queries = %d, want 2", queries)
	}
}

func TestRetryRhizaStartupRetriesOnlyTransientFailures(t *testing.T) {
	attempts := 0
	if err := retryRhizaStartup(context.Background(), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return rhiza.ErrQuorumUnavailable
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("transient attempts = %d, want 2", attempts)
	}

	permanent := errors.New("invalid migration")
	for name, failure := range map[string]error{
		"permanent":              permanent,
		"unknown commit":         rhiza.ErrCommitUnknown,
		"unconfirmed durability": rhiza.ErrDurabilityUnavailable,
	} {
		t.Run(name, func(t *testing.T) {
			attempts = 0
			if err := retryRhizaStartup(context.Background(), func(context.Context) error {
				attempts++
				return failure
			}); !errors.Is(err, failure) || attempts != 1 {
				t.Fatalf("error = %v after %d attempts", err, attempts)
			}
		})
	}
}

func TestRhizaConfigFailsClosedOnInvalidDurationAndWrongClusterSize(t *testing.T) {
	t.Setenv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", "invalid")
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("invalid duration accepted")
	}
	t.Setenv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", "1m")
	t.Setenv("TERNAL_DATA_CHECKPOINT_INTERVAL", "invalid")
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("invalid checkpoint interval accepted")
	}
	t.Setenv("TERNAL_DATA_CHECKPOINT_INTERVAL", "")
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", `[{"id":"only"}]`)
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("undersized multi-node cluster accepted")
	}
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", `[{"node_id":"n0"},{"node_id":"n1"},{"node_id":"n2"},{"node_id":"n3"}]`)
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
		t.Fatal("oversized multi-node cluster accepted")
	}
}

func TestRhizaConfigRequiresExplicitStorageMode(t *testing.T) {
	members := `[{"node_id":"ternal-0","token":"00000000000000000000000000000000"},{"node_id":"ternal-1","token":"11111111111111111111111111111111"},{"node_id":"ternal-2","token":"22222222222222222222222222222222"}]`
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", members)
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires TERNAL_DATA_MULTI_NODE=1") {
		t.Fatalf("implicit HA membership was accepted: %v", err)
	}
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", "")
	t.Setenv("TERNAL_DATA_EXPECTED_MEMBER_IDS", "ternal-0,ternal-1,ternal-2")
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil || !strings.Contains(err.Error(), "requires TERNAL_DATA_MULTI_NODE=1") {
		t.Fatalf("implicit expected HA membership was accepted: %v", err)
	}
	t.Setenv("TERNAL_DATA_EXPECTED_MEMBER_IDS", "")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", members)
	t.Setenv("TERNAL_DATA_MULTI_NODE", "yes")
	if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil || !strings.Contains(err.Error(), "must be 0 or 1") {
		t.Fatalf("invalid mode flag was accepted: %v", err)
	}
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", "admin-token-00000000000000000000")
	config, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil || len(config.Members) != 3 {
		t.Fatalf("explicit HA membership rejected: members=%d err=%v", len(config.Members), err)
	}
}

func TestRhizaConfigRejectsUnsafeHAMemberTokens(t *testing.T) {
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", "admin-token-00000000000000000000")
	for name, members := range map[string]string{
		"short":        `[{"node_id":"n0","token":"short"},{"node_id":"n1","token":"11111111111111111111111111111111"},{"node_id":"n2","token":"22222222222222222222222222222222"}]`,
		"reused admin": `[{"node_id":"n0","token":"admin-token-00000000000000000000"},{"node_id":"n1","token":"11111111111111111111111111111111"},{"node_id":"n2","token":"22222222222222222222222222222222"}]`,
		"duplicate":    `[{"node_id":"n0","token":"00000000000000000000000000000000"},{"node_id":"n1","token":"00000000000000000000000000000000"},{"node_id":"n2","token":"22222222222222222222222222222222"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", members)
			if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
				t.Fatal("unsafe HA member tokens accepted")
			}
		})
	}
}

func TestRhizaConfigRequiresExpectedHAMemberSet(t *testing.T) {
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", "admin-token-00000000000000000000")
	validMembers := `[{
		"node_id":"ternal-0","token":"00000000000000000000000000000000"
	},{
		"node_id":"ternal-1","token":"11111111111111111111111111111111"
	},{
		"node_id":"ternal-2","token":"22222222222222222222222222222222"
	}]`
	for name, expected := range map[string]string{
		"wrong set": "ternal-0,ternal-1,other-2",
		"duplicate": "ternal-0,ternal-0,ternal-2",
		"too short": "ternal-0,ternal-1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TERNAL_DATA_EXPECTED_MEMBER_IDS", expected)
			t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", validMembers)
			if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
				t.Fatal("invalid expected HA member set was accepted")
			}
		})
	}

	t.Setenv("TERNAL_DATA_EXPECTED_MEMBER_IDS", "ternal-0,ternal-1,ternal-2")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", validMembers)
	if _, err := rhizaConfigFromEnv(t.TempDir()); err != nil {
		t.Fatalf("expected HA member set was rejected: %v", err)
	}
}
