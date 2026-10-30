// Package operatoridentity applies the recovery operator's generation identity
// to Ternal's own configuration variables.
//
// The upstream controller rewrites the identity variables of the target
// container when it starts a recovered generation, and Ternal has to run that
// generation, so the rewrite has to reach Ternal's configuration. This package
// is the only place in Ternal where the upstream variable names exist: it maps
// them onto TERNAL_* names at process start, before anything reads
// configuration. Operators of Ternal never set them; the values are derived
// from Ternal's own settings and only differ during a recovery transition.
// scripts/check-public-config.sh exempts this file.
package operatoridentity

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mrchypark/rhiza"
)

// pairs holds the operator-managed variable and the Ternal configuration
// variable it carries: exactly what the controller rewrites to start a recovered
// generation, except the token map folded into the credentials below.
var pairs = [][2]string{
	{"RHIZA_CLUSTER_ID", "TERNAL_DATA_CLUSTER_ID"},
	{"RHIZA_CLUSTER_MEMBERS", "TERNAL_DATA_CLUSTER_MEMBERS"},
	{"RHIZA_ADMIN_TOKEN", "TERNAL_DATA_ADMIN_TOKEN"},
	{"RHIZA_OBJSTORE_DURABILITY", "TERNAL_OBJECT_STORE_DURABILITY"},
}

// credentialMember is one TERNAL_DATA_CLUSTER_MEMBERS entry: the public member identity plus its private token.
type credentialMember struct {
	rhiza.Member
	Token string `json:"token"`
}

// Apply is a no-op outside explicit, non-Local HA. In HA it copies present
// operator-managed variables, then folds in the recovered peer token map.
// Empty operator values never clear Ternal's own; an unadaptable map fails closed.
func Apply() error {
	if os.Getenv("TERNAL_DATA_LOCAL") == "1" || os.Getenv("TERNAL_DATA_MULTI_NODE") != "1" {
		return nil
	}
	for _, pair := range pairs {
		if value := os.Getenv(pair[0]); value != "" {
			_ = os.Setenv(pair[1], value)
		}
	}
	return applyPeerTokens()
}

// applyPeerTokens joins the operator's node-ID token map onto the membership it
// rewrote: one pod template cannot carry per-replica Secrets, so the map is the
// only place a recovered voter's token exists. Errors name nodes, never tokens.
func applyPeerTokens() error {
	raw := os.Getenv("RHIZA_PEER_TOKENS")
	if raw == "" {
		return nil
	}
	var tokens map[string]string
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return fmt.Errorf("operator peer token map is not a node-ID to token object")
	}
	if len(tokens) == 0 {
		return fmt.Errorf("operator peer token map is empty")
	}
	membersRaw := os.Getenv("TERNAL_DATA_CLUSTER_MEMBERS")
	if membersRaw == "" {
		return fmt.Errorf("operator peer token map has no membership to join")
	}
	var members []credentialMember
	if err := json.Unmarshal([]byte(membersRaw), &members); err != nil {
		return fmt.Errorf("operator membership is not a member list")
	}
	if len(members) == 0 {
		return fmt.Errorf("operator peer token map has no member entries to join")
	}
	joined := make(map[string]struct{}, len(members))
	for i := range members {
		id := string(members[i].ID)
		if id == "" {
			return fmt.Errorf("operator membership contains an empty node ID")
		}
		token, found := tokens[id]
		if !found || token == "" {
			return fmt.Errorf("operator peer token map has no entry for node %s", id)
		}
		members[i].Token = token
		joined[id] = struct{}{}
	}
	for id := range tokens {
		if _, found := joined[id]; !found {
			return fmt.Errorf("operator peer token map has no member entry for node %s", id)
		}
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return fmt.Errorf("encode adapted membership: %w", err)
	}
	_ = os.Setenv("TERNAL_DATA_CLUSTER_MEMBERS", string(encoded))
	return nil
}
