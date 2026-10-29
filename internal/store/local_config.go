package store

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/mrchypark/rhiza"
)

// Local never consumes these upstream settings; every nonempty value is rejected.
var localRejectedUpstreamEnv = []string{
	"RHIZA_CLUSTER_ID",
	"RHIZA_NODE_ID",
	"RHIZA_CLUSTER_MEMBERS",
	"RHIZA_PEER_TOKENS",
	"RHIZA_ENABLE_RECONFIGURATION",
	"RHIZA_LEARNER",
	"RHIZA_ADMIN_TOKEN",
	"RHIZA_OBJSTORE_DURABILITY",
}

func localConfigFromEnv(dataDir string) (rhiza.Config, error) {
	for _, name := range append([]string{
		"TERNAL_DATA_ADMIN_TOKEN", "TERNAL_DATA_BIND_ADDR", "TERNAL_DATA_PEER_ADDR",
		"TERNAL_DATA_CLUSTER_MEMBERS", "TERNAL_DATA_EXPECTED_MEMBER_IDS", "TERNAL_OPERATOR_BIND",
		"TERNAL_DATA_ENABLE_RECONFIGURATION", "TERNAL_DATA_LEARNER",
		"TERNAL_OBJECT_STORE_DIR",
	}, localRejectedUpstreamEnv...) {
		if os.Getenv(name) != "" {
			return rhiza.Config{}, fmt.Errorf("%s is forbidden in Local mode", name)
		}
	}
	for _, name := range []string{"TERNAL_DATA_MULTI_NODE", "TERNAL_REQUIRE_DATA_ADMIN_TOKEN"} {
		if value := os.Getenv(name); value != "" && value != "0" {
			return rhiza.Config{}, fmt.Errorf("%s must be 0 in Local mode", name)
		}
	}
	provider := os.Getenv("TERNAL_OBJECT_STORE_PROVIDER")
	if provider != "s3" && provider != "gcs" {
		return rhiza.Config{}, fmt.Errorf("Ternal Local mode requires an s3 or gcs archive")
	}
	if os.Getenv("TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK") != "1" {
		return rhiza.Config{}, fmt.Errorf("TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK=1 is required; the acknowledgement is not fencing proof")
	}
	if durability := os.Getenv("TERNAL_OBJECT_STORE_DURABILITY"); durability != "" && durability != "before-ack" {
		return rhiza.Config{}, fmt.Errorf("Ternal Local mode requires before-ack durability")
	}
	clusterID := os.Getenv("TERNAL_DATA_CLUSTER_ID")
	if clusterID == "" || clusterID == "." || clusterID == ".." || strings.ContainsAny(clusterID, "/\\\x00\r\n") {
		return rhiza.Config{}, fmt.Errorf("Local mode requires an explicit single-component TERNAL_DATA_CLUSTER_ID")
	}
	prefix := os.Getenv("TERNAL_OBJECT_STORE_PREFIX")
	if prefix == "" || path.IsAbs(prefix) || path.Clean(prefix) != prefix || prefix == "." || prefix == ".." || strings.HasPrefix(prefix, "../") || strings.ContainsAny(prefix, "\\\x00\r\n") {
		return rhiza.Config{}, fmt.Errorf("Local mode requires a canonical relative TERNAL_OBJECT_STORE_PREFIX")
	}
	if os.Getenv("TERNAL_OBJECT_STORE_BUCKET") == "" {
		return rhiza.Config{}, fmt.Errorf("TERNAL_OBJECT_STORE_BUCKET is required in Local mode")
	}
	if provider == "gcs" && (os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT") != "" || os.Getenv("TERNAL_OBJECT_STORE_INSECURE") == "true") {
		return rhiza.Config{}, fmt.Errorf("gcs Local mode does not accept endpoint or insecure overrides")
	}
	syncInterval, err := durationEnv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", time.Minute)
	if err != nil {
		return rhiza.Config{}, err
	}
	batchDelay, err := durationEnv("TERNAL_OBJECT_STORE_BATCH_DELAY", 2*time.Millisecond)
	if err != nil {
		return rhiza.Config{}, err
	}
	checkpointInterval, err := durationEnv("TERNAL_DATA_CHECKPOINT_INTERVAL", 15*time.Minute)
	if err != nil {
		return rhiza.Config{}, err
	}
	return rhiza.Config{
		Local: true, ClusterID: clusterID, NodeID: envOr("TERNAL_DATA_NODE_ID", "node-1"), DataDir: dataDir,
		ObjStoreProvider: provider, ObjStoreEndpoint: os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT"),
		ObjStoreBucket: os.Getenv("TERNAL_OBJECT_STORE_BUCKET"), ObjStorePrefix: prefix,
		ObjStoreRegion: os.Getenv("TERNAL_OBJECT_STORE_REGION"), ObjStoreInsecure: os.Getenv("TERNAL_OBJECT_STORE_INSECURE") == "true",
		ObjStoreAccessKey: os.Getenv("TERNAL_OBJECT_STORE_ACCESS_KEY"), ObjStoreSecretKey: os.Getenv("TERNAL_OBJECT_STORE_SECRET_KEY"), ObjStoreSessionToken: os.Getenv("TERNAL_OBJECT_STORE_SESSION_TOKEN"),
		ObjStoreDurability: rhiza.ObjectStoreDurabilityBeforeAck, ObjStoreSyncInterval: syncInterval,
		ObjStoreBatchDelay: batchDelay, CheckpointInterval: checkpointInterval,
	}, nil
}
