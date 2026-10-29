package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
	"github.com/mrchypark/rhiza/pkg/checkpoint"
	"github.com/mrchypark/rhiza/pkg/network"
	"github.com/mrchypark/rhiza/pkg/quepaxa"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/thanos-io/objstore"
	"github.com/thanos-io/objstore/providers/filesystem"
)

func localTestEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"TERNAL_DATA_ADMIN_TOKEN", "TERNAL_DATA_BIND_ADDR", "TERNAL_DATA_PEER_ADDR", "TERNAL_DATA_CLUSTER_MEMBERS", "TERNAL_DATA_EXPECTED_MEMBER_IDS", "TERNAL_OPERATOR_BIND", "TERNAL_DATA_ENABLE_RECONFIGURATION", "TERNAL_DATA_LEARNER", "TERNAL_OBJECT_STORE_DIR", "TERNAL_OBJECT_STORE_DURABILITY", "TERNAL_OBJECT_STORE_ENDPOINT", "TERNAL_OBJECT_STORE_INSECURE"}, localRejectedUpstreamEnv...) {
		t.Setenv(name, "")
	}
	for name, value := range map[string]string{"TERNAL_DATA_LOCAL": "1", "TERNAL_DATA_MULTI_NODE": "0", "TERNAL_REQUIRE_DATA_ADMIN_TOKEN": "0", "TERNAL_OBJECT_STORE_PROVIDER": "s3", "TERNAL_OBJECT_STORE_BUCKET": "test", "TERNAL_OBJECT_STORE_PREFIX": "new-generation", "TERNAL_DATA_CLUSTER_ID": "local-test", "TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK": "1"} {
		t.Setenv(name, value)
	}
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_SCOPE", uuid.NewString())
}

type unavailableLocalGuardAnchor struct{ trustAnchorBackend }

func (unavailableLocalGuardAnchor) Get(context.Context) (trustAnchorRecord, string, error) {
	return trustAnchorRecord{}, "", errors.New("fixture authority unavailable")
}

func TestLocalReverseModeGuardFailsClosedBeforeMutation(t *testing.T) {
	localTestEnv(t)
	t.Setenv("TERNAL_DATA_LOCAL", "0")
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "")
	dir := filepath.Join(t.TempDir(), "never-created")
	c := rhiza.Config{DataDir: dir, ObjStoreProvider: "s3", ObjStoreBucket: "test", ObjStorePrefix: "p", ClusterID: "c"}
	if err := prepareLocalOpen(context.Background(), c); err == nil || !strings.Contains(err.Error(), "CONFIGMAP") {
		t.Fatalf("constructor failure ignored: %v", err)
	}
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "guard")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return unavailableLocalGuardAnchor{} }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
	if err := prepareLocalOpen(context.Background(), c); err == nil || !strings.Contains(err.Error(), "fixture authority unavailable") {
		t.Fatalf("authority Get failure ignored: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("guard mutated DataDir: %v", err)
	}
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", "")
	t.Setenv("TERNAL_OBJECT_STORE_BUCKET", "")
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "")
	if err := prepareLocalOpen(context.Background(), rhiza.Config{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("unconfigured legacy path changed: %v", err)
	}
}

func TestLocalReceiptScopeAndSingletonBinding(t *testing.T) {
	localTestEnv(t)
	c := rhiza.Config{NodeID: "node-1"}
	binding := recovery.LocalBinding{Bootstrap: quepaxa.Cluster{Members: []quepaxa.Member{{ID: "node-1"}}}}
	if err := validateLocalBinding(c, binding); err != nil {
		t.Fatal(err)
	}
	binding.Bootstrap.Members[0].ID = "other"
	if err := validateLocalBinding(c, binding); err == nil {
		t.Fatal("wrong singleton accepted")
	}
	binding.Bootstrap.Members = append(binding.Bootstrap.Members, quepaxa.Member{ID: "node-1"})
	if err := validateLocalBinding(c, binding); err == nil {
		t.Fatal("multiple members accepted")
	}
	a := &trustAnchor{name: "protected", namespace: "test"}
	r := localPreflightReceipt{Scope: os.Getenv("TERNAL_LOCAL_PREFLIGHT_SCOPE"), AnchorName: a.name, AnchorNamespace: a.namespace}
	if err := validateLocalReceiptScope(r, a); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*localPreflightReceipt)
	}{
		{"pod", func(r *localPreflightReceipt) { r.Scope = uuid.NewString() }},
		{"anchor-name", func(r *localPreflightReceipt) { r.AnchorName = "other" }},
		{"anchor-namespace", func(r *localPreflightReceipt) { r.AnchorNamespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := r
			tc.change(&changed)
			if err := validateLocalReceiptScope(changed, a); err == nil {
				t.Fatal("scope/authority mismatch accepted")
			}
		})
	}
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_SCOPE", "")
	if err := validateLocalReceiptScope(r, a); err == nil {
		t.Fatal("missing explicit scope accepted")
	}
}

func TestLocalRejectsLegacyRecoveryAnchorBeforeConstruction(t *testing.T) {
	t.Setenv("TERNAL_DATA_LOCAL", "1")
	t.Setenv("TERNAL_RECOVERY_ANCHOR_ID", "")
	if _, err := OpenAnchorService(context.Background()); err == nil || !strings.Contains(err.Error(), "does not support Local") {
		t.Fatalf("Local anchor startup = %v", err)
	}
	if _, err := NewAnchorCoordinator("", nil, nil, recoveryStorage{}); err == nil || !strings.Contains(err.Error(), "does not support Local") {
		t.Fatalf("Local anchor coordinator = %v", err)
	}
	t.Setenv("TERNAL_DATA_LOCAL", "0")
	if _, err := OpenAnchorService(context.Background()); err == nil || !strings.Contains(err.Error(), "TERNAL_RECOVERY_ANCHOR_ID is required") {
		t.Fatalf("legacy anchor configuration path changed: %v", err)
	}
}

// This focused test uses an SDK-seeded filesystem fixture only to exercise
// Ternal's public checkpoint-download helper. It is not a Ternal filesystem
// backend or S3/GCS preflight/restart qualification.
func TestLocalSeededCheckpointDownloadsWithoutCacheDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := rhiza.Config{Local: true, ClusterID: "checkpoint-fixture", NodeID: "node-1", DataDir: t.TempDir(), ObjStoreProvider: "filesystem", ObjStoreDir: t.TempDir(), ObjStorePrefix: "fixture", ObjStoreDurability: rhiza.ObjectStoreDurabilityBeforeAck, CheckpointInterval: 20 * time.Millisecond}
	db, err := openRhiza(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if err := db.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	// Seed once with one stable idempotency key. Periodic Local maintenance can
	// reject admission temporarily; only its explicit ErrNotReady is retryable.
	request := rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: []rhiza.SQLStatement{{SQL: `CREATE TABLE preflight_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`}, {SQL: `INSERT INTO preflight_fixture VALUES (1,'seeded')`}}}
	var seedErr error
	if err := waitUntilReady(ctx, func() bool {
		_, seedErr = db.db.Execute(ctx, request)
		if seedErr == nil {
			return true
		}
		if !errors.Is(seedErr, network.ErrNotReady) {
			t.Fatalf("seed Local fixture: %v", seedErr)
		}
		return false
	}); err != nil {
		t.Fatalf("seed Local fixture: %v (last admission: %v)", err, seedErr)
	}
	bucket, err := filesystem.NewBucket(c.ObjStoreDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bucket.Close(); err != nil {
			t.Error(err)
		}
	})
	var snapshot *recovery.LocalSnapshot
	var lastErr error
	if err := waitUntilReady(ctx, func() bool {
		a, err := recovery.OpenLocalArchive(ctx, readOnlyLocalBucket{bucket}, localNamespace(c))
		lastErr = err
		if err != nil {
			return false
		}
		snapshot = a.Snapshot()
		return snapshot != nil
	}); err != nil {
		t.Fatalf("SDK fixture checkpoint: %v (last archive read: %v)", err, lastErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	// Reproduce the rejected caller contract before checking the fixed helper.
	badWork := t.TempDir()
	if err := os.Mkdir(filepath.Join(badWork, "files"), 0700); err != nil {
		t.Fatal(err)
	}
	bad := checkpoint.NewManager(readOnlyLocalBucket{bucket}, localNamespace(c), filepath.Join(badWork, "missing-cache"), snapshot.Seal.ConfigID)
	root, err := bad.OpenRoot(ctx, uint64(snapshot.Seal.Index), snapshot.PortableRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.DownloadAndVerifyRootFiles(ctx, root, filepath.Join(badWork, "files")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cache did not reproduce original caller error: %v", err)
	} else {
		t.Logf("original caller error reproduced: %v", err)
	}
	work := t.TempDir()
	if err := downloadLocalRoot(ctx, bucket, localNamespace(c), snapshot, work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "files", "sqlite.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "cache")); !os.IsNotExist(err) {
		t.Fatalf("unexpected verified cache: %v", err)
	}
}

func TestLocalConfigIsExplicitAndNetworkFree(t *testing.T) {
	localTestEnv(t)
	c, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Local || c.BindAddr != "" || c.PeerAddr != "" || c.AdminToken != "" || c.PeerToken != "" || len(c.Members) != 0 || c.Learner != nil || c.EnableReconfiguration || c.ObjStoreDurability != rhiza.ObjectStoreDurabilityBeforeAck {
		t.Fatalf("unsafe Local config: %+v", c)
	}
	for _, name := range append([]string{"TERNAL_DATA_ADMIN_TOKEN", "TERNAL_DATA_BIND_ADDR", "TERNAL_DATA_PEER_ADDR", "TERNAL_DATA_CLUSTER_MEMBERS", "TERNAL_DATA_EXPECTED_MEMBER_IDS", "TERNAL_OPERATOR_BIND", "TERNAL_DATA_MULTI_NODE", "TERNAL_REQUIRE_DATA_ADMIN_TOKEN"}, localRejectedUpstreamEnv...) {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "1")
			if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
				t.Fatal("conflicting configuration accepted")
			}
		})
	}
	for _, tc := range []struct{ name, value string }{
		{"TERNAL_OBJECT_STORE_PROVIDER", "filesystem"}, {"TERNAL_OBJECT_STORE_PROVIDER", "az"}, {"TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK", ""}, {"TERNAL_DATA_CLUSTER_ID", "a/b"}, {"TERNAL_OBJECT_STORE_PREFIX", "a/../b"}, {"TERNAL_OBJECT_STORE_DURABILITY", "async"}, {"TERNAL_DATA_LOCAL", "yes"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
				t.Fatal("unsafe Local setting accepted")
			}
		})
	}
}

func TestLocalStorageIdentityIsDomainSeparatedAndLegacyUnchanged(t *testing.T) {
	tuple := []string{"gcs", "", "bucket", "prefix", "cluster"}
	encoded, _ := json.Marshal(tuple)
	want := sha256.Sum256(encoded)
	legacy := TrustAnchorStorageID(tuple[0], tuple[1], tuple[2], tuple[3], tuple[4])
	if legacy != hex.EncodeToString(want[:]) {
		t.Fatal("legacy identity changed")
	}
	base := TrustAnchorStorageID("gcs", "", "bucket", path.Join("prefix", "cluster", "local/v1"), "cluster")
	localHash := sha256.Sum256([]byte("ternal-local-v1\x00" + base))
	local := LocalTrustAnchorStorageID("gcs", "", "bucket", "prefix", "cluster")
	if local != hex.EncodeToString(localHash[:]) || local == legacy || local == base {
		t.Fatal("Local identity is not domain separated")
	}
	if local == LocalTrustAnchorStorageID("gcs", "", "bucket", "other", "cluster") || local == LocalTrustAnchorStorageID("gcs", "", "bucket", "prefix", "other") {
		t.Fatal("different Local roots alias")
	}
}

func TestLocalDataDirGuardPreservesUnknownDataAndRefusesRollback(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "user-data")
	if err := os.WriteFile(name, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	c := rhiza.Config{Local: true, DataDir: dir, ObjStoreProvider: "s3", ObjStoreBucket: "test", ObjStorePrefix: "fresh", ClusterID: "c"}
	if _, err := checkLocalDataDir(c); err == nil {
		t.Fatal("unknown nonempty directory accepted")
	}
	if data, err := os.ReadFile(name); err != nil || string(data) != "preserve" {
		t.Fatalf("data changed: %q %v", data, err)
	}
	c.Local = false
	if _, err := checkLocalDataDir(c); err != nil {
		t.Fatalf("legacy unknown data was newly rejected: %v", err)
	}
	c.Local = true
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err := checkLocalDataDir(c); err != nil {
		t.Fatal(err)
	}
	marker := localDataMode{Version: 1, Mode: "local", StorageID: localStorageID(c), Binding: recovery.LocalBinding{StoreUUID: uuid.NewString()}}
	if err := writeLocalJSON(filepath.Join(dir, localModeMarker), marker); err != nil {
		t.Fatal(err)
	}
	if got, err := checkLocalDataDir(c); err != nil || !reflect.DeepEqual(got, &marker) {
		t.Fatalf("marker = %+v, %v", got, err)
	}
	c.Local = false
	if _, err := checkLocalDataDir(c); err == nil {
		t.Fatal("reverse flag rollback accepted")
	}
	c.Local = true
	c.ObjStorePrefix = "other"
	if _, err := checkLocalDataDir(c); err == nil {
		t.Fatal("archive reuse accepted")
	}
}

func TestLocalArchiveModeGuardAndWriteDenial(t *testing.T) {
	ctx := context.Background()
	b := objstore.NewInMemBucket()
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	ro := readOnlyLocalBucket{b}
	if ro.Upload(ctx, "not-written", strings.NewReader("x")) == nil || ro.Delete(ctx, "not-deleted") == nil || len(b.Objects()) != 0 {
		t.Fatal("readonly bucket mutated")
	}
	c := rhiza.Config{Local: true, ObjStorePrefix: "fresh", ClusterID: "c"}
	if _, _, err := readLocalArchive(ctx, b, c); err == nil || !strings.Contains(err.Error(), "manual SDK seeding") || len(b.Objects()) != 0 {
		t.Fatalf("empty archive was initialized: %v", err)
	}
	key := path.Join(localNamespace(c), "evidence")
	if err := b.Upload(ctx, key, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := checkArchiveMode(ctx, b, c); err != nil {
		t.Fatal(err)
	}
	c.Local = false
	if err := checkArchiveMode(ctx, b, c); err == nil {
		t.Fatal("Local archive accepted in legacy mode after DataDir loss")
	}
	c.Local = true
	if err := b.Upload(ctx, path.Join(c.ObjStorePrefix, c.ClusterID, "voter-evidence"), strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := checkArchiveMode(ctx, b, c); err == nil {
		t.Fatal("legacy archive accepted for Local conversion")
	}
}

func TestLocalReceiptIsBoundedStrictAndAtomic(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "receipt.json")
	want := localPreflightReceipt{Version: 1, StorageID: "test", NodeID: "node"}
	if err := writeLocalJSON(name, want); err != nil {
		t.Fatal(err)
	}
	var got localPreflightReceipt
	if err := readLocalJSON(name, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt=%+v %v", got, err)
	}
	for _, invalid := range [][]byte{[]byte(`{"version":1,"unknown":true}`), []byte(`{} {}`), bytes.Repeat([]byte("x"), maxLocalReceipt+1)} {
		if err := os.WriteFile(name, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if err := readLocalJSON(name, &got); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), name); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalJSON(name, want); err == nil {
		t.Fatal("receipt symlink accepted")
	}
	if err := readLocalJSON(name, &got); err == nil {
		t.Fatal("receipt symlink read")
	}
}

func TestLocalReceiptAllowsAppRestartAfterHistoryAdvances(t *testing.T) {
	c := rhiza.Config{Local: true, ObjStoreProvider: "s3", ObjStoreBucket: "b", ObjStorePrefix: "p", ClusterID: "c", NodeID: "n"}
	old := localArchiveView{Binding: recovery.LocalBinding{StoreUUID: uuid.NewString()}, Tip: 5, Snapshot: &recovery.LocalSnapshot{}}
	r := localPreflightReceipt{Version: 1, StorageID: localStorageID(c), NodeID: c.NodeID, View: old}
	current := old
	current.Tip = 50
	current.PrefixHash[0] = 1
	current.Snapshot = &recovery.LocalSnapshot{Descriptor: json.RawMessage(`{}`)}
	if err := validateLocalReceipt(c, r, current); err != nil {
		t.Fatalf("legitimate app restart rejected: %v", err)
	}
	current.Binding.StoreUUID = uuid.NewString()
	if err := validateLocalReceipt(c, r, current); err == nil {
		t.Fatal("different binding accepted")
	}
	current = old
	c.NodeID = "other"
	if err := validateLocalReceipt(c, r, current); err == nil {
		t.Fatal("different node configuration accepted")
	}
}

func TestLocalPreflightPathsAreSeparateAndNoDataDirCreation(t *testing.T) {
	localTestEnv(t)
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	data := filepath.Join(root, "data")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_DATA_DIR", data)
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", stage)
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE", filepath.Join(stage, "preflight.json"))
	if _, _, err := localPreflightPaths(data); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "")
	if err := LocalPreflightFromEnv(context.Background()); err == nil {
		t.Fatal("missing trusted authority accepted")
	}
	if err := LocalRestoreFromEnv(context.Background()); err == nil {
		t.Fatal("restore accepted missing trusted authority")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("init touched DataDir: %v", err)
	}
	if entries, err := os.ReadDir(stage); err != nil || len(entries) != 0 {
		t.Fatalf("failed init left staging artifacts: %v %v", entries, err)
	}
	if _, _, err := localPreflightPaths(stage); err == nil {
		t.Fatal("stage/DataDir overlap accepted")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(stage, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := localPreflightPaths(filepath.Join(alias, "data")); err == nil {
		t.Fatal("resolved overlap accepted")
	}
}

func TestLocalTrustPreflightRejectsPendingAndMismatchedBinding(t *testing.T) {
	localTestEnv(t)
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "test")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	r := storageBindingFromEnv()
	r.Token = uuid.NewString()
	r.Bootstrap = true
	b := &memoryAnchor{record: r}
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return b }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
	if _, err := localTrustBeforeOpen(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending := int64(1)
	b.record.PendingEpoch = &pending
	b.record.PendingID = uuid.NewString()
	if _, err := localTrustBeforeOpen(context.Background()); err == nil {
		t.Fatal("pending authority accepted")
	}
	b.record = r
	b.record.StorageID = TrustAnchorStorageID("s3", "", "test", "new-generation", "local-test")
	if _, err := localTrustBeforeOpen(context.Background()); err == nil {
		t.Fatal("legacy authority accepted")
	}
}
