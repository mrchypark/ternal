package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
)

func legacyAnchorData(record trustAnchorRecord) map[string]string {
	data := anchorData(record)
	for key := range data {
		if !anchorDataKeys[key] || key == "recoveryGeneration" || key == "recoveryEvidence" || key == "recoveryTransition" || key == "recoveryReceipt" {
			delete(data, key)
		}
	}
	return data
}

func mustBase64(t *testing.T, raw string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCompositeAnchorMigratesLegacyShapeWithoutResettingState(t *testing.T) {
	pending := int64(5)
	record := trustAnchorRecord{Format: "1", ClusterID: "c", StorageID: "s", Epoch: 4, Token: uuid.NewString(), PendingEpoch: &pending, PendingID: uuid.NewString()}
	legacy := legacyAnchorData(record)
	if len(legacy) != trustAnchorLegacyKeys {
		t.Fatalf("legacy fixture has %d keys: %#v", len(legacy), legacy)
	}
	migratedRecord, err := parseAnchorData(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if migratedRecord.Epoch != 4 || migratedRecord.Token != record.Token || migratedRecord.Bootstrap || migratedRecord.PendingEpoch == nil || *migratedRecord.PendingEpoch != pending || migratedRecord.PendingID != record.PendingID {
		t.Fatalf("legacy read altered state: %#v", migratedRecord)
	}
	if migratedRecord.Generation != 0 || migratedRecord.Transition != nil || migratedRecord.Receipt != nil {
		t.Fatalf("legacy read invented recovery state: %#v", migratedRecord)
	}

	migrated := anchorData(migratedRecord)
	if len(migrated) != trustAnchorCompositeKeys {
		t.Fatalf("migrated shape has %d keys: %#v", len(migrated), migrated)
	}
	for key, value := range legacy {
		if migrated[key] != value {
			t.Fatalf("migration changed %s: %q -> %q", key, value, migrated[key])
		}
	}
	round, err := parseAnchorData(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if round.Epoch != migratedRecord.Epoch || round.Token != migratedRecord.Token || round.PendingID != migratedRecord.PendingID || round.Bootstrap != migratedRecord.Bootstrap || round.Generation != 0 {
		t.Fatalf("composite round trip = %#v", round)
	}
	envelope, err := parseTrustAnchorEvidence(mustBase64(t, migrated["recoveryEvidence"]))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Format != round.Format || !envelope.agrees(round) {
		t.Fatalf("envelope = %#v", envelope)
	}
	if migrated["recoveryTransition"] != recoveryNull || migrated["recoveryReceipt"] != recoveryNull {
		t.Fatalf("empty recovery projections = %q, %q", migrated["recoveryTransition"], migrated["recoveryReceipt"])
	}
}

func TestCompositeAnchorRejectsDisagreeingRepresentations(t *testing.T) {
	transition := recoveryanchor.Transition{
		Request:    recoveryanchor.Request{AnchorID: "anchor", OperationID: "op", SourceClusterID: "c", TargetClusterID: "t"},
		Source:     recoveryanchor.Binding{ClusterID: "c", StorageID: "s"},
		Generation: 2,
		Evidence:   []byte("epoch-pair"),
	}
	receipt := recoveryanchor.Receipt{AnchorID: "anchor", OperationID: "op", RequestHash: "hash", Target: recoveryanchor.Binding{ClusterID: "t", StorageID: "s2"}, Generation: 3}
	record := trustAnchorRecord{Format: "1", ClusterID: "c", StorageID: "s", Epoch: 4, Token: uuid.NewString(), Generation: 2, Transition: &transition, Receipt: &receipt}
	cases := map[string]func(map[string]string){
		"epoch disagrees with envelope": func(data map[string]string) {
			envelope := trustAnchorEvidence{Format: "1", Epoch: 9, Token: data["token"]}
			data["recoveryEvidence"] = base64.StdEncoding.EncodeToString(envelope.bytes())
		},
		"pending disagrees with envelope": func(data map[string]string) {
			pending := int64(5)
			envelope := trustAnchorEvidence{Format: "1", Epoch: 4, Token: data["token"], PendingEpoch: &pending, PendingID: uuid.NewString()}
			data["recoveryEvidence"] = base64.StdEncoding.EncodeToString(envelope.bytes())
		},
		"bootstrap disagrees with envelope": func(data map[string]string) {
			envelope := trustAnchorEvidence{Format: "1", Epoch: 4, Token: data["token"], Bootstrap: true}
			data["recoveryEvidence"] = base64.StdEncoding.EncodeToString(envelope.bytes())
		},
		"non-canonical generation":             func(data map[string]string) { data["recoveryGeneration"] = "02" },
		"generation disagrees with transition": func(data map[string]string) { data["recoveryGeneration"] = "3" },
		"transition with unknown field": func(data map[string]string) {
			data["recoveryTransition"] = "{\"request\":{},\"source\":{},\"generation\":2,\"evidence\":null,\"extra\":true}"
		},
		"stray key": func(data map[string]string) { data["anchor.json"] = "{}" },
		"non-canonical evidence": func(data map[string]string) {
			data["recoveryEvidence"] = base64.StdEncoding.EncodeToString([]byte(" " + string(mustJSON(t, record.evidence()))))
		},
		"non-canonical transition": func(data map[string]string) { data["recoveryTransition"] = " " + recoveryProjection(&transition) },
		"eleven keys":              func(data map[string]string) { delete(data, "recoveryReceipt") },
		"receipt disagrees with generation": func(data map[string]string) {
			data["recoveryGeneration"] = "9"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			data := anchorData(record)
			mutate(data)
			if _, err := parseAnchorData(data); err == nil {
				t.Fatalf("disagreeing representation accepted: %#v", data)
			}
		})
	}
}

func TestCompositeAnchorRoundTripsRecoveryFields(t *testing.T) {
	transition := recoveryanchor.Transition{
		Request:    recoveryanchor.Request{AnchorID: "anchor", OperationID: "op", SourceClusterID: "c", TargetClusterID: "t"},
		Source:     recoveryanchor.Binding{ClusterID: "c", StorageID: "s"},
		Generation: 7,
		Evidence:   []byte("epoch-pair"),
	}
	record := trustAnchorRecord{Format: "1", ClusterID: "t", StorageID: "s2", Epoch: 4, Token: uuid.NewString(), Generation: 7, Transition: &transition}
	parsed, err := parseAnchorData(anchorData(record))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Generation != 7 || parsed.Transition == nil || parsed.Transition.Request.OperationID != "op" || parsed.Transition.Generation != 7 {
		t.Fatalf("transition round trip = %#v", parsed.Transition)
	}
	common := recoveryRecord(parsed)
	if common.Version != recoveryanchor.Version1 || common.EvidenceFormat != trustAnchorEvidenceFormat || common.Binding.ClusterID != "t" || common.PendingWrite {
		t.Fatalf("common record = %#v", common)
	}
	back, err := anchorRecordFromRecovery(common)
	if err != nil {
		t.Fatal(err)
	}
	if back.ClusterID != parsed.ClusterID || back.StorageID != parsed.StorageID || back.Epoch != parsed.Epoch || back.Token != parsed.Token || back.Generation != parsed.Generation || back.Transition == nil {
		t.Fatalf("inverse projection = %#v", back)
	}
	committed := common
	committed.Binding = recoveryanchor.Binding{ClusterID: "u", StorageID: "s3"}
	committed.Generation = 8
	committed.Transition = nil
	committed.LastTransition = &recoveryanchor.Receipt{AnchorID: "anchor", OperationID: "op", RequestHash: "hash", Target: committed.Binding, Generation: 8}
	next, err := anchorRecordFromRecovery(committed)
	if err != nil {
		t.Fatal(err)
	}
	if next.ClusterID != "u" || next.Epoch != 4 || next.Token != parsed.Token || next.Generation != 8 || next.Receipt == nil || next.Transition != nil {
		t.Fatalf("commit projection = %#v", next)
	}
	if err := next.validateStructure(); err != nil {
		t.Fatal(err)
	}
	pendingWrite := common
	pendingWrite.PendingWrite = true
	if _, err := anchorRecordFromRecovery(pendingWrite); err == nil {
		t.Fatal("pending flag without a reserved pair accepted")
	}
}

func TestRecoveryAnchorBackendKeepsConfigMapIdentityAndCAS(t *testing.T) {
	record := trustAnchorRecord{Format: "1", ClusterID: "c", StorageID: "s", Epoch: 3, Token: uuid.NewString()}
	state := configMapWire{APIVersion: "v1", Kind: "ConfigMap", Data: anchorData(record), Metadata: map[string]json.RawMessage{}}
	state.setMetadataString("resourceVersion", "7")
	state.setMetadataString("name", "ternal-anchor")
	state.setMetadataString("namespace", "ns")
	state.Metadata["labels"] = json.RawMessage("{\"ternal.dev/trustguard-anchor\":\"true\"}")
	state.Metadata["annotations"] = json.RawMessage("{\"operator.example/keep\":\"yes\"}")
	var puts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			var wire configMapWire
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Error(err)
			}
			if len(wire.Data) != trustAnchorCompositeKeys {
				t.Errorf("PUT wrote %d keys: %#v", len(wire.Data), wire.Data)
			}
			if string(wire.Metadata["labels"]) != "{\"ternal.dev/trustguard-anchor\":\"true\"}" || string(wire.Metadata["annotations"]) != "{\"operator.example/keep\":\"yes\"}" {
				t.Error("PUT removed admission-controlled metadata")
			}
			state = wire
			state.setMetadataString("resourceVersion", "8")
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer server.Close()
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &recoveryAnchorBackend{anchor: &kubernetesTrustAnchor{name: "ternal-anchor", namespace: "ns", baseURL: server.URL, tokenPath: tokenPath, client: server.Client()}, id: "anchor"}
	got, rv, err := backend.Read(context.Background(), "anchor")
	if err != nil || rv != "7" || got.Generation != 0 || got.Binding.ClusterID != "c" {
		t.Fatalf("read = %#v rv=%q err=%v", got, rv, err)
	}
	if _, _, err := backend.Read(context.Background(), "other"); err == nil {
		t.Fatal("unknown anchor id accepted")
	}
	got.PendingWrite = true
	if err := backend.CAS(context.Background(), "anchor", rv, got); err == nil {
		t.Fatal("pending write without a reserved pair accepted")
	}
	got.PendingWrite = false
	got.Generation = 1
	got.LastTransition = &recoveryanchor.Receipt{AnchorID: "anchor", OperationID: "op", RequestHash: "hash", Target: got.Binding, Generation: 1}
	if err := backend.CAS(context.Background(), "anchor", rv, got); err != nil {
		t.Fatal(err)
	}
	if err := backend.CAS(context.Background(), "anchor", rv, got); err == nil {
		t.Fatal("stale resource version accepted")
	}
	if puts != 1 {
		t.Fatalf("writes = %d", puts)
	}
	final, _, err := backend.Read(context.Background(), "anchor")
	if err != nil || final.Generation != 1 || final.LastTransition == nil || final.Binding.ClusterID != "c" || len(final.Evidence) == 0 {
		t.Fatalf("final = %#v err=%v", final, err)
	}
}

func TestCompositeAnchorEvidenceIsStableAndStructureIsChecked(t *testing.T) {
	record := trustAnchorRecord{Format: "1", ClusterID: "c", StorageID: "s", Epoch: 2, Token: uuid.NewString()}
	first := anchorData(record)["recoveryEvidence"]
	if first != anchorData(record)["recoveryEvidence"] {
		t.Fatal("evidence encoding is not deterministic")
	}
	if _, err := parseTrustAnchorEvidence(mustBase64(t, first)); err != nil {
		t.Fatal(err)
	}
	if err := record.validateStructure(); err != nil {
		t.Fatal(err)
	}
	broken := record
	broken.Transition = &recoveryanchor.Transition{Request: recoveryanchor.Request{AnchorID: "a"}, Generation: 9}
	if err := broken.validateStructure(); err == nil {
		t.Fatal("transition generation mismatch accepted")
	}
	orphan := record
	orphan.Generation = 4
	if err := orphan.validateStructure(); err != nil {
		t.Fatal(err)
	}
	orphan.Receipt = &recoveryanchor.Receipt{AnchorID: "a", OperationID: "op", Generation: 3}
	if err := orphan.validateStructure(); err == nil {
		t.Fatal("receipt generation mismatch accepted")
	}
}
