package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mrchypark/rhiza/pkg/recoveryanchor"
)

// trustAnchor is deliberately independent of Rhiza's object store.  A restored
// object-store snapshot therefore cannot also restore this monotonic record.
type trustAnchorRecord struct {
	Format       string
	ClusterID    string
	StorageID    string
	Epoch        int64
	Token        string
	PendingEpoch *int64
	PendingID    string
	Bootstrap    bool
	// The recovery fields are the common representation shared with the
	// anchor service.  They advance in the same ConfigMap compare-and-swap as
	// the flat fields above and are never written on their own.
	Generation uint64
	Transition *recoveryanchor.Transition
	Receipt    *recoveryanchor.Receipt
}

type trustAnchorBackend interface {
	Get(context.Context) (trustAnchorRecord, string, error)
	CAS(context.Context, string, trustAnchorRecord) (string, error)
}

type trustAnchor struct {
	backend trustAnchorBackend
	binding trustAnchorRecord
}

func storageBindingFromEnv() trustAnchorRecord {
	// Credentials intentionally are not part of the binding.
	storageID := TrustAnchorStorageID(os.Getenv("TERNAL_OBJECT_STORE_PROVIDER"), os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT"), os.Getenv("TERNAL_OBJECT_STORE_BUCKET"), os.Getenv("TERNAL_OBJECT_STORE_PREFIX"), envOr("TERNAL_DATA_CLUSTER_ID", "ternal"))
	return trustAnchorRecord{Format: "1", ClusterID: envOr("TERNAL_DATA_CLUSTER_ID", "ternal"), StorageID: storageID}
}

// TrustAnchorStorageID is the stable, credential-free binding written by the
// deployment controller.  JSON is used instead of a delimiter to make tuples
// unambiguous across provider names and user supplied prefixes.
func TrustAnchorStorageID(provider, endpoint, bucket, prefix, clusterID string) string {
	payload, _ := json.Marshal([]string{provider, endpoint, bucket, prefix, clusterID})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (a *trustAnchor) get(ctx context.Context) (trustAnchorRecord, string, error) {
	record, rv, err := a.backend.Get(ctx)
	if err != nil {
		return trustAnchorRecord{}, "", fmt.Errorf("read trust anchor: %w", err)
	}
	if err := a.validate(record); err != nil {
		return trustAnchorRecord{}, "", err
	}
	return record, rv, nil
}

func (a *trustAnchor) cas(ctx context.Context, rv string, next trustAnchorRecord) (string, error) {
	if err := a.validate(next); err != nil {
		return "", err
	}
	updated, err := a.backend.CAS(ctx, rv, next)
	if err != nil {
		return "", fmt.Errorf("update trust anchor: %w", err)
	}
	return updated, nil
}

func (a *trustAnchor) validate(record trustAnchorRecord) error {
	if err := record.validateStructure(); err != nil {
		return err
	}
	if record.ClusterID != a.binding.ClusterID || record.StorageID != a.binding.StorageID {
		return fmt.Errorf("invalid or mismatched trust anchor")
	}
	// A frozen recovery transition means the binding is about to change under
	// this node.  Letting the application write through it would advance the
	// very evidence the transition froze, so every Ternal path stays closed
	// until the anchor service commits or clears the transition.
	if record.Transition != nil {
		return fmt.Errorf("trust anchor has a pending recovery transition")
	}
	return nil
}

// validateStructure is the binding-free half of validate.  The anchor service
// adopts records whose binding legitimately changed, so it cannot check the
// deployment binding; everything else still has to hold.
func (r trustAnchorRecord) validateStructure() error {
	if r.Format != "1" || r.ClusterID == "" || r.StorageID == "" || r.Epoch < 0 || !validUUID(r.Token) {
		return fmt.Errorf("invalid trust anchor")
	}
	if r.PendingEpoch != nil {
		if *r.PendingEpoch != r.Epoch+1 || !validUUID(r.PendingID) {
			return fmt.Errorf("invalid pending trust anchor")
		}
	} else if r.PendingID != "" {
		return fmt.Errorf("invalid pending trust anchor")
	}
	if r.Transition != nil {
		if r.Transition.Generation != r.Generation || r.Transition.Request.AnchorID == "" {
			return fmt.Errorf("invalid recovery transition")
		}
	} else if r.Receipt != nil && r.Receipt.Generation != r.Generation {
		return fmt.Errorf("invalid recovery receipt")
	}
	return nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func requireTrustAnchor() bool {
	// A configured backend is durable state even when a deployment accidentally
	// omits the older require-object-store switch.
	return os.Getenv("TERNAL_DATA_REQUIRE_OBJECT_STORE") == "true" ||
		os.Getenv("TERNAL_OBJECT_STORE_PROVIDER") != "" || os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT") != "" ||
		os.Getenv("TERNAL_OBJECT_STORE_BUCKET") != "" || os.Getenv("TERNAL_OBJECT_STORE_DIR") != ""
}

func trustAnchorFromEnv() (*trustAnchor, error) {
	if !requireTrustAnchor() {
		return nil, nil
	}
	name := os.Getenv("TERNAL_TRUST_ANCHOR_CONFIGMAP")
	if name == "" {
		return nil, fmt.Errorf("TERNAL_TRUST_ANCHOR_CONFIGMAP is required with object-store data")
	}
	namespace := os.Getenv("TERNAL_TRUST_ANCHOR_NAMESPACE")
	if namespace == "" {
		data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			return nil, fmt.Errorf("read trust-anchor namespace: %w", err)
		}
		namespace = strings.TrimSpace(string(data))
	}
	if namespace == "" {
		return nil, fmt.Errorf("trust-anchor namespace is required")
	}
	return &trustAnchor{backend: kubernetesAnchorBackend(name, namespace), binding: storageBindingFromEnv()}, nil
}

const (
	// trustAnchorEvidenceFormat names the Ternal migration envelope.  It retains
	// the legacy flat state and carries the lifecycle fields that the flat
	// projection cannot express.
	trustAnchorEvidenceFormat = "ternal-trust-anchor/v1"
	trustAnchorLegacyKeys     = 8
	trustAnchorCompositeKeys  = 12
)

// anchorDataKeys is the exact whitelist of ConfigMap keys.  The shape is
// compared exactly, so a stray key is a representation error and never
// silently ignored state.
var anchorDataKeys = map[string]bool{
	"format": true, "clusterID": true, "storageID": true, "epoch": true,
	"token": true, "pendingEpoch": true, "pendingID": true, "bootstrap": true,
	"recoveryGeneration": true, "recoveryEvidence": true, "recoveryTransition": true, "recoveryReceipt": true,
}

// trustAnchorEvidence is the canonical envelope frozen as the anchor evidence.
// Every encoding is byte-for-byte canonical so the anchor service and Ternal
// compare proofs without ambiguity.
type trustAnchorEvidence struct {
	Format       string `json:"format"`
	Epoch        int64  `json:"epoch"`
	Token        string `json:"token"`
	PendingEpoch *int64 `json:"pending_epoch"`
	PendingID    string `json:"pending_id"`
	Bootstrap    bool   `json:"bootstrap"`
}

func (r trustAnchorRecord) evidence() trustAnchorEvidence {
	return trustAnchorEvidence{Format: r.Format, Epoch: r.Epoch, Token: r.Token, PendingEpoch: r.PendingEpoch, PendingID: r.PendingID, Bootstrap: r.Bootstrap}
}

func (e trustAnchorEvidence) bytes() []byte {
	data, _ := json.Marshal(e)
	return data
}

// agrees reports whether the envelope and the flat projection describe the same
// application state.  Disagreement is an error, never a repair opportunity.
func (e trustAnchorEvidence) agrees(record trustAnchorRecord) bool {
	if e.Format != record.Format || e.Epoch != record.Epoch || e.Token != record.Token || e.PendingID != record.PendingID || e.Bootstrap != record.Bootstrap {
		return false
	}
	if e.PendingEpoch == nil || record.PendingEpoch == nil {
		return e.PendingEpoch == nil && record.PendingEpoch == nil
	}
	return *e.PendingEpoch == *record.PendingEpoch
}

func parseTrustAnchorEvidence(raw []byte) (trustAnchorEvidence, error) {
	var evidence trustAnchorEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return trustAnchorEvidence{}, fmt.Errorf("parse trust-anchor evidence")
	}
	if string(evidence.bytes()) != string(raw) {
		return trustAnchorEvidence{}, fmt.Errorf("non-canonical trust-anchor evidence")
	}
	return evidence, nil
}

func anchorDataShape(data map[string]string) bool {
	for key := range data {
		if !anchorDataKeys[key] {
			return false
		}
	}
	return len(data) == trustAnchorLegacyKeys || len(data) == trustAnchorCompositeKeys
}

func anchorData(record trustAnchorRecord) map[string]string {
	data := map[string]string{"format": record.Format, "clusterID": record.ClusterID, "storageID": record.StorageID, "epoch": strconv.FormatInt(record.Epoch, 10), "token": record.Token, "pendingEpoch": "", "pendingID": record.PendingID, "bootstrap": strconv.FormatBool(record.Bootstrap)}
	if record.PendingEpoch != nil {
		data["pendingEpoch"] = strconv.FormatInt(*record.PendingEpoch, 10)
	}
	// The recovery projections are written in the same compare-and-swap as the
	// flat fields, so no reader can observe the two representations apart.
	data["recoveryGeneration"] = strconv.FormatUint(record.Generation, 10)
	data["recoveryEvidence"] = base64.StdEncoding.EncodeToString(record.evidence().bytes())
	data["recoveryTransition"] = recoveryProjection(record.Transition)
	data["recoveryReceipt"] = recoveryProjection(record.Receipt)
	return data
}

func parseAnchorData(data map[string]string) (trustAnchorRecord, error) {
	if !anchorDataShape(data) {
		return trustAnchorRecord{}, fmt.Errorf("invalid trust-anchor data shape")
	}
	epoch, err := parseAnchorEpoch(data["epoch"])
	if err != nil {
		return trustAnchorRecord{}, fmt.Errorf("parse trust-anchor epoch")
	}
	if data["bootstrap"] != "true" && data["bootstrap"] != "false" {
		return trustAnchorRecord{}, fmt.Errorf("parse trust-anchor bootstrap")
	}
	bootstrap := data["bootstrap"] == "true"
	record := trustAnchorRecord{Format: data["format"], ClusterID: data["clusterID"], StorageID: data["storageID"], Epoch: epoch, Token: data["token"], PendingID: data["pendingID"], Bootstrap: bootstrap}
	if raw := data["pendingEpoch"]; raw != "" {
		pending, err := parseAnchorEpoch(raw)
		if err != nil {
			return trustAnchorRecord{}, fmt.Errorf("parse trust-anchor pending epoch")
		}
		record.PendingEpoch = &pending
	}
	if len(data) == trustAnchorLegacyKeys {
		// Pre-migration representation.  Recovery metadata stays zero until the
		// next compare-and-swap initializes it, and nothing here re-derives it.
		return record, nil
	}
	raw, err := base64.StdEncoding.DecodeString(data["recoveryEvidence"])
	if err != nil {
		return trustAnchorRecord{}, fmt.Errorf("parse trust-anchor evidence")
	}
	evidence, err := parseTrustAnchorEvidence(raw)
	if err != nil {
		return trustAnchorRecord{}, err
	}
	if !evidence.agrees(record) {
		return trustAnchorRecord{}, fmt.Errorf("trust-anchor representations disagree")
	}
	if record.Generation, err = parseAnchorGeneration(data["recoveryGeneration"]); err != nil {
		return trustAnchorRecord{}, err
	}
	if record.Transition, err = parseRecoveryProjection[recoveryanchor.Transition](data["recoveryTransition"]); err != nil {
		return trustAnchorRecord{}, err
	}
	if record.Receipt, err = parseRecoveryProjection[recoveryanchor.Receipt](data["recoveryReceipt"]); err != nil {
		return trustAnchorRecord{}, err
	}
	if err := record.validateStructure(); err != nil {
		return trustAnchorRecord{}, err
	}
	return record, nil
}

func parseAnchorGeneration(raw string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("parse trust-anchor recovery generation")
	}
	return value, nil
}

func parseAnchorEpoch(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf("not canonical decimal")
	}
	return value, nil
}
