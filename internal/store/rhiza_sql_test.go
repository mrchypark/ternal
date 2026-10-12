package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
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

func TestRhizaConfigProjectsPublicIdentityAndOwnPeerToken(t *testing.T) {
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "ternal-ha")
	t.Setenv("TERNAL_DATA_NODE_ID", "ternal-1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", "admin-token-00000000000000000000")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", `[
		{"node_id":"ternal-0","token":"00000000000000000000000000000000"},
		{"node_id":"ternal-1","token":"11111111111111111111111111111111"},
		{"node_id":"ternal-2","token":"22222222222222222222222222222222"}
	]`)

	config, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("HA membership rejected: %v", err)
	}
	if config.PeerToken != "11111111111111111111111111111111" {
		t.Fatalf("peer token is not this node's token: %q", config.PeerToken)
	}
	if len(config.Members) != 3 {
		t.Fatalf("members=%d, want 3", len(config.Members))
	}
	// A replicated membership is public identity only: every key must be the
	// official derivation of that member's own token, and no secret may remain.
	for i, member := range config.Members {
		token := []string{"00000000000000000000000000000000", "11111111111111111111111111111111", "22222222222222222222222222222222"}[i]
		if member.PublicKey != rhiza.PeerPublicKey("ternal-ha", string(member.ID), token) {
			t.Fatalf("member %s public key is not derived from its token", member.ID)
		}
		encoded, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(token)) {
			t.Fatalf("member %s still exposes its private token: %s", member.ID, encoded)
		}
	}
}

// haTokens are distinct voter credentials of at least 32 bytes, none of them
// the admin token below.
func haTokens() map[string]string {
	return map[string]string{
		"ternal-0": strings.Repeat("0", 43),
		"ternal-1": strings.Repeat("1", 43),
		"ternal-2": strings.Repeat("2", 43),
	}
}

const haAdminToken = "admin-token-00000000000000000000"

// haCredentials builds the member list an HA deployment stores under
// TERNAL_DATA_CLUSTER_MEMBERS, with each public key the official derivation
// for clusterID and that member's own token.
func haCredentials(t *testing.T, clusterID string, tokens map[string]string) []memberCredential {
	t.Helper()
	members := make([]memberCredential, 0, len(tokens))
	for _, id := range []string{"ternal-0", "ternal-1", "ternal-2"} {
		host := id + ".ternal-data.default.svc.cluster.local"
		members = append(members, memberCredential{
			Member: rhiza.Member{
				ID:        rhiza.NodeID(id),
				URL:       "quic://" + host + ":9090",
				PeerURL:   "quic://" + host + ":9090",
				LogURL:    "quic://" + host + ":9090",
				PublicKey: rhiza.PeerPublicKey(clusterID, id, tokens[id]),
			},
			Token: tokens[id],
		})
	}
	return members
}

func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestRhizaConfigAcceptsPreparedSourceCredentials proves the membership a
// deployment prepares is the public member list the recovery operator reads
// from the same Secret key: it decodes as upstream rhiza.Member with a
// non-zero, correctly derived key and parseable voter endpoints, and Ternal
// still derives its own peer token from it.
func TestRhizaConfigAcceptsPreparedSourceCredentials(t *testing.T) {
	const clusterID = "ternal-ha"
	tokens := haTokens()
	raw := encodeJSON(t, haCredentials(t, clusterID, tokens))

	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_CLUSTER_ID", clusterID)
	t.Setenv("TERNAL_DATA_NODE_ID", "ternal-1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", haAdminToken)
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", raw)

	config, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("prepared HA credentials rejected: %v", err)
	}
	if config.PeerToken != tokens["ternal-1"] {
		t.Fatalf("peer token is not this node's token: %q", config.PeerToken)
	}

	// The operator parses this value with a plain json.Unmarshal into
	// rhiza.Member and then refuses an empty key, a duplicate ID, or an
	// endpoint that does not parse, so all three are checked here.
	var public []rhiza.Member
	if err := json.Unmarshal([]byte(raw), &public); err != nil {
		t.Fatalf("membership is not a public member list: %v", err)
	}
	if len(public) != 3 {
		t.Fatalf("public members = %d, want 3", len(public))
	}
	seen := map[string]bool{}
	for _, member := range public {
		id := string(member.ID)
		if seen[id] {
			t.Fatalf("duplicate member ID %s", id)
		}
		seen[id] = true
		if member.PublicKey == (rhiza.PublicKey{}) {
			t.Fatalf("member %s has no public key", id)
		}
		if member.PublicKey != rhiza.PeerPublicKey(clusterID, id, tokens[id]) {
			t.Fatalf("member %s public key is not the derivation of its own credential", id)
		}
		for _, endpoint := range []string{member.URL, member.PeerURL, member.LogURL} {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Host == "" {
				t.Fatalf("member %s endpoint %q does not parse: %v", id, endpoint, err)
			}
		}
	}
}

// The operator bridge supplies recovered credentials; the config must preserve
// their target identity and keep private tokens out of replicated membership.
func TestRhizaConfigAcceptsRecoveredCredentials(t *testing.T) {
	const sourceCluster = "ternal-ha"
	const targetCluster = "rhiza-r-0123456789abcdef"
	tokens := haTokens()

	// The controller rewrites membership with public keys for the target
	// cluster and no token fields at all.
	var recovered []memberCredential
	for _, member := range haCredentials(t, sourceCluster, tokens) {
		member.PublicKey = rhiza.PeerPublicKey(targetCluster, string(member.ID), tokens[string(member.ID)])
		member.WALIdentity = "wal-" + string(member.ID)
		recovered = append(recovered, member)
	}

	t.Setenv("TERNAL_DATA_CLUSTER_ID", targetCluster)
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", haAdminToken)
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "async")
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_NODE_ID", "ternal-1")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", encodeJSON(t, recovered))

	config, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("recovered membership rejected: %v", err)
	}
	if config.ClusterID != targetCluster {
		t.Fatalf("cluster ID = %q, want %q", config.ClusterID, targetCluster)
	}
	if config.PeerToken != tokens["ternal-1"] {
		t.Fatalf("peer token is not this node's recovered token: %q", config.PeerToken)
	}
	if len(config.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(config.Members))
	}
	for _, member := range config.Members {
		id := string(member.ID)
		if member.PublicKey != rhiza.PeerPublicKey(targetCluster, id, tokens[id]) {
			t.Fatalf("member %s public key is not derived for the recovered cluster", id)
		}
		if member.WALIdentity != "wal-"+id {
			t.Fatalf("member %s WAL identity was not preserved: %q", id, member.WALIdentity)
		}
		wantHost := id + ".ternal-data.default.svc.cluster.local:9090"
		if !strings.HasSuffix(member.URL, wantHost) || !strings.HasSuffix(member.PeerURL, wantHost) || !strings.HasSuffix(member.LogURL, wantHost) {
			t.Fatalf("member %s endpoints changed: %q %q %q", id, member.URL, member.PeerURL, member.LogURL)
		}
	}
	// Replicated membership stays public: no recovered token may reach it.
	encoded, err := json.Marshal(config.Members)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if bytes.Contains(encoded, []byte(token)) {
			t.Fatalf("replicated membership exposes a token: %s", encoded)
		}
	}
}

func TestRhizaConfigRejectsPublicKeyFromAnotherCredential(t *testing.T) {
	const clusterID = "ternal-ha"
	tokens := haTokens()
	t.Setenv("TERNAL_DATA_MULTI_NODE", "1")
	t.Setenv("TERNAL_DATA_CLUSTER_ID", clusterID)
	t.Setenv("TERNAL_DATA_NODE_ID", "ternal-1")
	t.Setenv("TERNAL_DATA_ADMIN_TOKEN", haAdminToken)
	for name, foreign := range map[string]rhiza.PublicKey{
		"another credential": rhiza.PeerPublicKey(clusterID, "ternal-0", haTokens()["ternal-0"]+"x"),
		"another cluster":    rhiza.PeerPublicKey("other-cluster", "ternal-0", tokens["ternal-0"]),
	} {
		t.Run(name, func(t *testing.T) {
			members := haCredentials(t, clusterID, tokens)
			members[0].PublicKey = foreign
			t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", encodeJSON(t, members))
			_, err := rhizaConfigFromEnv(t.TempDir())
			if err == nil {
				t.Fatal("public key from another credential was accepted")
			}
			if !strings.Contains(err.Error(), "public key") {
				t.Fatalf("error does not report the mismatch: %v", err)
			}
			for _, token := range tokens {
				if strings.Contains(err.Error(), token) {
					t.Fatalf("error exposes a token value: %v", err)
				}
			}
		})
	}
}
