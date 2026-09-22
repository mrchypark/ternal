package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
)

// legacyAnchorServer is a ConfigMap endpoint that enforces the one admission
// rule the format migration exists for: a record applied with the legacy
// eight-key shape may only grow into the composite representation through a
// request that keeps every flat value and zeroes the guards.  Any other write
// that adds the keys is denied, exactly as the enforced policy denies it.
type legacyAnchorServer struct {
	data    map[string]string
	rv      int
	puts    []map[string]string
	denials int
}

func (s *legacyAnchorServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var wire configMapWire
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(s.data) == trustAnchorLegacyKeys && len(wire.Data) == trustAnchorCompositeKeys && !migrationAdmitted(s.data, wire.Data) {
			s.denials++
			http.Error(w, "trust-anchor transition is not monotonic", http.StatusUnprocessableEntity)
			return
		}
		s.puts = append(s.puts, wire.Data)
		s.data = wire.Data
		s.rv++
	}
	body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": "anchor", "namespace": "ns", "resourceVersion": fmt.Sprint(s.rv)}, "data": s.data})
	_, _ = w.Write(body)
}

func migrationAdmitted(stored, next map[string]string) bool {
	for key, value := range stored {
		if next[key] != value {
			return false
		}
	}
	return next["recoveryGeneration"] == "0" && next["recoveryTransition"] == recoveryNull && next["recoveryReceipt"] == recoveryNull
}

func newLegacyAnchorServer(t *testing.T, record trustAnchorRecord) (*legacyAnchorServer, *kubernetesTrustAnchor) {
	t.Helper()
	data := anchorData(record)
	for _, key := range []string{"recoveryGeneration", "recoveryEvidence", "recoveryTransition", "recoveryReceipt"} {
		delete(data, key)
	}
	state := &legacyAnchorServer{data: data, rv: 7}
	server := httptest.NewTLSServer(http.HandlerFunc(state.serve))
	t.Cleanup(server.Close)
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	return state, &kubernetesTrustAnchor{name: "anchor", namespace: "ns", baseURL: server.URL, tokenPath: tokenPath, client: server.Client()}
}

// TestKubernetesTrustAnchorMigratesTheLegacyFormatFirst covers the record a
// greenfield deployment applies: eight keys, no guard fields.  The next write
// this cluster makes is the reservation that opens the trust pair, and it
// changes flat values, so publishing it together with the new keys is denied.
// The adapter has to move the representation first, alone, keeping every value
// it found, and only then write what it was asked for.
func TestKubernetesTrustAnchorMigratesTheLegacyFormatFirst(t *testing.T) {
	ctx := context.Background()
	legacy := trustAnchorRecord{Format: "1", ClusterID: "cluster", StorageID: "storage", Token: uuid.NewString(), Bootstrap: true}
	state, k := newLegacyAnchorServer(t, legacy)
	if len(state.data) != trustAnchorLegacyKeys {
		t.Fatalf("fixture is not legacy: %#v", state.data)
	}
	applied := map[string]string{}
	for key, value := range state.data {
		applied[key] = value
	}

	record, rv, err := k.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := record
	pending := record.Epoch + 1
	next.PendingEpoch = &pending
	next.PendingID = uuid.NewString()
	if _, err := k.CAS(ctx, rv, next); err != nil {
		t.Fatalf("reservation after a legacy record: %v", err)
	}
	if state.denials != 0 {
		t.Fatalf("the adapter issued %d denied writes", state.denials)
	}
	if len(state.puts) != 2 {
		t.Fatalf("reservation issued %d writes, want the migration and then the reservation", len(state.puts))
	}
	migrated := state.puts[0]
	if len(migrated) != trustAnchorCompositeKeys {
		t.Fatalf("migration wrote %d keys: %#v", len(migrated), migrated)
	}
	for key, value := range applied {
		if migrated[key] != value {
			t.Fatalf("migration changed %s: %q, want %q", key, migrated[key], value)
		}
	}
	if migrated["recoveryGeneration"] != "0" || migrated["recoveryTransition"] != recoveryNull || migrated["recoveryReceipt"] != recoveryNull {
		t.Fatalf("migration invented recovery state: %#v", migrated)
	}
	reserved := state.puts[1]
	if reserved["pendingID"] != next.PendingID || reserved["pendingEpoch"] != fmt.Sprint(pending) || reserved["epoch"] != "0" {
		t.Fatalf("reservation did not follow the migration: %#v", reserved)
	}
	if state.data["pendingID"] != next.PendingID {
		t.Fatalf("final record = %#v", state.data)
	}
}

// TestKubernetesTrustAnchorLeavesACompositeRecordAlone proves the migration is
// a one-shot: a record that already carries the common representation is
// written in the single compare-and-swap the caller asked for.
func TestKubernetesTrustAnchorLeavesACompositeRecordAlone(t *testing.T) {
	ctx := context.Background()
	state, k := newLegacyAnchorServer(t, trustAnchorRecord{Format: "1", ClusterID: "cluster", StorageID: "storage", Token: uuid.NewString(), Bootstrap: true})
	record, rv, err := k.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CAS(ctx, rv, record); err != nil {
		t.Fatal(err)
	}
	state.puts = nil
	composite, rv, err := k.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := composite
	next.Bootstrap = false
	next.Epoch = 1
	next.Token = uuid.NewString()
	if _, err := k.CAS(ctx, rv, next); err != nil {
		t.Fatal(err)
	}
	if len(state.puts) != 1 {
		t.Fatalf("a composite record took %d writes, want 1", len(state.puts))
	}
	if state.puts[0]["epoch"] != "1" || state.puts[0]["bootstrap"] != "false" {
		t.Fatalf("composite write = %#v", state.puts[0])
	}
}
