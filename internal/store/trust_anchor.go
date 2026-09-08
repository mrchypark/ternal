package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	if record.Format != "1" || record.ClusterID != a.binding.ClusterID || record.StorageID != a.binding.StorageID || record.Epoch < 0 || !validUUID(record.Token) {
		return fmt.Errorf("invalid or mismatched trust anchor")
	}
	if record.PendingEpoch != nil {
		if *record.PendingEpoch != record.Epoch+1 || !validUUID(record.PendingID) {
			return fmt.Errorf("invalid pending trust anchor")
		}
	} else if record.PendingID != "" {
		return fmt.Errorf("invalid pending trust anchor")
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

func anchorData(record trustAnchorRecord) map[string]string {
	data := map[string]string{"format": record.Format, "clusterID": record.ClusterID, "storageID": record.StorageID, "epoch": strconv.FormatInt(record.Epoch, 10), "token": record.Token, "pendingEpoch": "", "pendingID": record.PendingID, "bootstrap": strconv.FormatBool(record.Bootstrap)}
	if record.PendingEpoch != nil {
		data["pendingEpoch"] = strconv.FormatInt(*record.PendingEpoch, 10)
	}
	return data
}

func parseAnchorData(data map[string]string) (trustAnchorRecord, error) {
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
	return record, nil
}

func parseAnchorEpoch(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != raw {
		return 0, fmt.Errorf("not canonical decimal")
	}
	return value, nil
}
