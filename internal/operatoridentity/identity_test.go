package operatoridentity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mrchypark/rhiza"
)

func TestApplyAdoptsOperatorManifestIdentity(t *testing.T) {
	// The controller rewrites exactly these when it starts a recovered
	// generation; Ternal must run that generation.
	for _, pair := range pairs {
		t.Setenv(pair[0], "operator-"+pair[1])
		t.Setenv(pair[1], "source-identity")
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "operator-"+pair[1] {
			t.Fatalf("%s = %q, want the operator-managed value", pair[1], got)
		}
	}
}

func TestApplyIsNoopWithoutOperator(t *testing.T) {
	for _, pair := range pairs {
		// Unset is what an unmanaged pod has; Apply treats empty the same way.
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], "ternals-own-value")
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "ternals-own-value" {
			t.Fatalf("%s = %q, want Ternal's own value untouched", pair[1], got)
		}
	}
}

func TestApplyNeverClearsWithEmptyOperatorValues(t *testing.T) {
	// An empty operator variable must not erase the configured identity: the
	// object-store durability and cluster ID decide which generation is served.
	for _, pair := range pairs {
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], "ternals-own-value")
	}
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, pair := range pairs {
		if got := os.Getenv(pair[1]); got != "ternals-own-value" {
			t.Fatalf("%s = %q, want Ternal's own value preserved", pair[1], got)
		}
	}
}

// recoveredTokens are the fresh per-node peer tokens the controller writes for
// a target generation: one entry per voter, selected by node ID.
func recoveredTokens() map[string]string {
	return map[string]string{
		"ternal-0": strings.Repeat("0", 43),
		"ternal-1": strings.Repeat("1", 43),
		"ternal-2": strings.Repeat("2", 43),
	}
}

// recoveredMembership is the public-only member list the controller rewrote
// into the target generation: no token fields, public keys derived for the
// target cluster, voter endpoints preserved.
func recoveredMembership(t *testing.T, clusterID string, tokens map[string]string) []rhiza.Member {
	t.Helper()
	members := make([]rhiza.Member, 0, len(tokens))
	for _, id := range []string{"ternal-0", "ternal-1", "ternal-2"} {
		host := id + ".ternal-data.default.svc.cluster.local"
		members = append(members, rhiza.Member{
			ID:          rhiza.NodeID(id),
			URL:         "quic://" + host + ":9090",
			PeerURL:     "quic://" + host + ":9090",
			LogURL:      "quic://" + host + ":9090",
			PublicKey:   rhiza.PeerPublicKey(clusterID, id, tokens[id]),
			WALIdentity: "wal-" + id,
		})
	}
	return members
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestApplyAdaptsRecoveredTokenMapIntoMemberCredentials(t *testing.T) {
	for _, pair := range pairs {
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], os.Getenv(pair[1]))
	}
	const clusterID = "rhiza-r-target"
	tokens := recoveredTokens()
	t.Setenv("RHIZA_CLUSTER_ID", clusterID)
	t.Setenv("RHIZA_CLUSTER_MEMBERS", mustJSON(t, recoveredMembership(t, clusterID, tokens)))
	t.Setenv("RHIZA_PEER_TOKENS", mustJSON(t, tokens))
	t.Setenv("RHIZA_ADMIN_TOKEN", strings.Repeat("a", 43))
	// Ternal still holds the source generation's credential-shaped membership
	// until the bridge folds the recovered token map onto the rewritten one.
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", `[{"node_id":"stale","token":"stale-source-credential"}]`)

	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var adapted []credentialMember
	if err := json.Unmarshal([]byte(os.Getenv("TERNAL_DATA_CLUSTER_MEMBERS")), &adapted); err != nil {
		t.Fatalf("adapted membership is not Ternal's credential shape: %v", err)
	}
	if len(adapted) != 3 {
		t.Fatalf("adapted members = %d, want 3", len(adapted))
	}
	source := recoveredMembership(t, clusterID, tokens)
	for i, member := range adapted {
		id := string(member.ID)
		if member.Token != tokens[id] {
			t.Fatalf("member %s token is not its recovered token", id)
		}
		// Every public and endpoint identity field survives the fold.
		if member.Member != source[i] {
			t.Fatalf("member %s identity changed: got %+v want %+v", id, member.Member, source[i])
		}
	}
	if got := os.Getenv("TERNAL_DATA_CLUSTER_ID"); got != clusterID {
		t.Fatalf("%s = %q, want the recovered cluster ID", "TERNAL_DATA_CLUSTER_ID", got)
	}
}

func TestApplyKeepsSourceCredentialsWithoutRecoveredTokenMap(t *testing.T) {
	for _, pair := range pairs {
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], os.Getenv(pair[1]))
	}
	source := `[{"node_id":"ternal-0","token":"source-credential-0000000000000000000000000"}]`
	t.Setenv("RHIZA_CLUSTER_MEMBERS", "")
	t.Setenv("RHIZA_PEER_TOKENS", "")
	t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", source)
	if err := Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := os.Getenv("TERNAL_DATA_CLUSTER_MEMBERS"); got != source {
		t.Fatalf("membership = %q, want Ternal's own credentials untouched", got)
	}
}

func TestApplyRejectsUnadaptableRecoveredTokenMaps(t *testing.T) {
	for _, pair := range pairs {
		t.Setenv(pair[0], "")
		t.Setenv(pair[1], os.Getenv(pair[1]))
	}
	const clusterID = "rhiza-r-target"
	tokens := recoveredTokens()
	members := mustJSON(t, recoveredMembership(t, clusterID, tokens))
	tokenMap := mustJSON(t, tokens)
	const stale = `[{"node_id":"stale","token":"stale-source-credential"}]`
	for name, tc := range map[string]struct {
		members string
		tokens  string
		own     string
	}{
		"malformed map": {members: members, tokens: `{"ternal-0":`, own: stale},
		"empty map":     {members: members, tokens: `{}`, own: stale},
		"map without member entry": {
			members: members,
			tokens:  mustJSON(t, map[string]string{"ternal-0": tokens["ternal-0"], "ternal-1": tokens["ternal-1"]}),
			own:     stale,
		},
		"member without map entry": {
			members: members,
			tokens: mustJSON(t, map[string]string{
				"ternal-0": tokens["ternal-0"],
				"ternal-1": tokens["ternal-1"],
				"other-2":  tokens["ternal-2"],
			}),
			own: stale,
		},
		"map without membership": {members: "", tokens: tokenMap, own: ""},
		"empty membership list":  {members: "", tokens: tokenMap, own: `[]`},
		"malformed membership":   {members: "", tokens: tokenMap, own: `[{"node_id":`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RHIZA_CLUSTER_MEMBERS", tc.members)
			t.Setenv("RHIZA_PEER_TOKENS", tc.tokens)
			t.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", tc.own)
			err := Apply()
			if err == nil {
				t.Fatal("unadaptable recovered token map was accepted")
			}
			for _, token := range tokens {
				if strings.Contains(err.Error(), token) {
					t.Fatalf("error exposes a token value: %v", err)
				}
			}
			if strings.Contains(err.Error(), "stale-source-credential") {
				t.Fatalf("error exposes a credential: %v", err)
			}
		})
	}
}
