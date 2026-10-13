package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type memberPublicKey [ed25519.PublicKeySize]byte

func (key memberPublicKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(base64.StdEncoding.EncodeToString(key[:]))
}

func (key *memberPublicKey) UnmarshalJSON(data []byte) error {
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("public key must be a base64 string")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != len(key) {
		return fmt.Errorf("public key must be %d base64-encoded bytes", len(key))
	}
	copy(key[:], decoded)
	return nil
}

// memberCredential is one TERNAL_DATA_CLUSTER_MEMBERS entry: public identity plus its private token.
type memberCredential struct {
	ID          string          `json:"node_id"`
	URL         string          `json:"url"`
	PeerURL     string          `json:"peer_url,omitempty"`
	LogURL      string          `json:"log_url"`
	PublicKey   memberPublicKey `json:"public_key"`
	WALIdentity string          `json:"wal_identity,omitempty"`
	Token       string          `json:"token"`
}

func peerPublicKey(clusterID, nodeID, token string) memberPublicKey {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("rhiza-peer-certificate\x00"))
	_, _ = mac.Write([]byte(clusterID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nodeID))
	privateKey := ed25519.NewKeyFromSeed(mac.Sum(nil))
	var publicKey memberPublicKey
	copy(publicKey[:], privateKey.Public().(ed25519.PublicKey))
	return publicKey
}

// prepareMembers derives every public key the operator requires from the
// credential that already owns it, so preparation never invents a key; errors name nodes, never tokens.
func prepareMembers(clusterID string, in io.Reader, out io.Writer) error {
	if clusterID == "" {
		return fmt.Errorf("cluster ID is required")
	}
	var members []memberCredential
	decoder := json.NewDecoder(in)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&members); err != nil {
		return fmt.Errorf("read membership credentials: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("read membership credentials: trailing JSON")
	}
	if len(members) == 0 {
		return fmt.Errorf("membership requires at least one member")
	}
	seen := make(map[string]struct{}, len(members))
	tokenSeen := make(map[string]struct{}, len(members))
	for i := range members {
		id := members[i].ID
		if id == "" {
			return fmt.Errorf("membership contains an empty node ID")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("membership contains duplicate node ID %s", id)
		}
		seen[id] = struct{}{}
		// Same contract rhizaConfigFromEnv enforces at startup, checked here so
		// a bad Secret is refused before it is written.
		if len(members[i].Token) < 32 {
			return fmt.Errorf("member %s token must contain at least 32 bytes", id)
		}
		if _, duplicate := tokenSeen[members[i].Token]; duplicate {
			return fmt.Errorf("membership tokens must be distinct")
		}
		tokenSeen[members[i].Token] = struct{}{}
		derived := peerPublicKey(clusterID, id, members[i].Token)
		if members[i].PublicKey != (memberPublicKey{}) && members[i].PublicKey != derived {
			return fmt.Errorf("member %s public key does not match its credential", id)
		}
		members[i].PublicKey = derived
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return fmt.Errorf("write membership credentials: %w", err)
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}
