package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
	"github.com/mrchypark/rhiza/pkg/recovery"
	"github.com/thanos-io/objstore"
)

// TestRhizaProviderContract runs only against an explicitly selected local
// emulator; an ordinary unit-test run must never discover cloud credentials.
func TestRhizaProviderContract(t *testing.T) {
	provider := os.Getenv("TERNAL_TEST_PROVIDER")
	if provider == "" {
		t.Skip("set TERNAL_TEST_PROVIDER, TERNAL_TEST_ENDPOINT and TERNAL_TEST_BUCKET for local emulator coverage")
	}
	if provider != "s3" && provider != "gcs" {
		t.Fatalf("unsupported Ternal recovery provider %q; Azure is not covered by this contract", provider)
	}
	endpoint, err := url.Parse(os.Getenv("TERNAL_TEST_ENDPOINT"))
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "" || endpoint.User != nil {
		t.Fatal("TERNAL_TEST_ENDPOINT must be an explicit http://127.0.0.1:PORT emulator")
	}
	bucketName := os.Getenv("TERNAL_TEST_BUCKET")
	if !strings.HasPrefix(bucketName, "ternal-contract-") || strings.ContainsAny(bucketName, "/\\ \t\r\n") {
		t.Fatal("TERNAL_TEST_BUCKET must be a disposable ternal-contract-* bucket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	for _, key := range []string{"TERNAL_DATA_CLUSTER_MEMBERS", "TERNAL_DATA_EXPECTED_MEMBER_IDS", "TERNAL_REQUIRE_DATA_ADMIN_TOKEN", "TERNAL_DATA_ADMIN_TOKEN", "TERNAL_DATA_SCHEMA_VERSION"} {
		t.Setenv(key, "")
	}
	t.Setenv("TERNAL_DATA_MULTI_NODE", "0")
	t.Setenv("TERNAL_DATA_LOCAL", "0")
	t.Setenv("TERNAL_DATA_BIND_ADDR", "127.0.0.1:0")
	t.Setenv("TERNAL_DATA_PEER_ADDR", "127.0.0.1:0")
	t.Setenv("TERNAL_DATA_CLUSTER_ID", "ternal-contract-"+provider)
	t.Setenv("TERNAL_DATA_NODE_ID", "node-1")
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", provider)
	t.Setenv("TERNAL_OBJECT_STORE_BUCKET", bucketName)
	t.Setenv("TERNAL_OBJECT_STORE_PREFIX", "contract/"+uuid.NewString())
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")
	t.Setenv("TERNAL_OBJECT_STORE_REGION", "")
	t.Setenv("TERNAL_OBJECT_STORE_INSECURE", "false")
	t.Setenv("TERNAL_OBJECT_STORE_ACCESS_KEY", "")
	t.Setenv("TERNAL_OBJECT_STORE_SECRET_KEY", "")
	t.Setenv("TERNAL_OBJECT_STORE_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	if provider == "s3" {
		t.Setenv("TERNAL_OBJECT_STORE_ENDPOINT", endpoint.Host)
		t.Setenv("TERNAL_OBJECT_STORE_INSECURE", "true")
		t.Setenv("TERNAL_OBJECT_STORE_REGION", "us-east-1")
		t.Setenv("TERNAL_OBJECT_STORE_ACCESS_KEY", "test")
		t.Setenv("TERNAL_OBJECT_STORE_SECRET_KEY", "test")
	} else {
		t.Setenv("TERNAL_OBJECT_STORE_ENDPOINT", "")
		t.Setenv("STORAGE_EMULATOR_HOST", endpoint.String())
		// Ternal uses ADC. Keep its token exchange local as well as its storage
		// requests; no real account or metadata endpoint is available here.
		tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"local-emulator-token","token_type":"Bearer","expires_in":3600}`)
		}))
		t.Cleanup(tokens.Close)
		credentials, err := json.Marshal(map[string]string{"type": "authorized_user", "client_id": "local", "client_secret": "local", "refresh_token": "local", "token_uri": tokens.URL})
		if err != nil {
			t.Fatal(err)
		}
		adc := filepath.Join(t.TempDir(), "emulator-adc.json")
		if err := os.WriteFile(adc, credentials, 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	}

	bucket, err := recoveryStorageFromEnv().bucket()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bucket.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Run("actual-adapter-operations", func(t *testing.T) {
		name := "contract/" + uuid.NewString()
		if _, err := bucket.Get(ctx, name); !bucket.IsObjNotFoundErr(err) {
			t.Fatalf("missing-object classification: %v", err)
		}
		if err := bucket.Upload(ctx, name, strings.NewReader("first"), objstore.WithIfNotExists()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := bucket.Delete(cleanupCtx, name); err != nil && !bucket.IsObjNotFoundErr(err) {
				t.Errorf("clean up owned object %q: %v", name, err)
			}
		})
		if err := bucket.Upload(ctx, name, strings.NewReader("duplicate"), objstore.WithIfNotExists()); !bucket.IsConditionNotMetErr(err) {
			t.Fatalf("conditional-create conflict classification: %v", err)
		}
		attributes, err := bucket.Attributes(ctx, name)
		if err != nil || attributes.Size != 5 || attributes.Version == nil {
			t.Fatalf("attributes = %#v, error = %v", attributes, err)
		}
		if err := bucket.Upload(ctx, name, strings.NewReader("second"), objstore.WithIfMatch(attributes.Version)); err != nil {
			t.Fatal(err)
		}
		if err := bucket.Upload(ctx, name, strings.NewReader("stale"), objstore.WithIfMatch(attributes.Version)); !bucket.IsConditionNotMetErr(err) {
			t.Fatalf("stale-version conflict classification: %v", err)
		}
		reader, err := bucket.Get(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || string(body) != "second" {
			t.Fatalf("get = %q, read error = %v, close error = %v", body, readErr, closeErr)
		}
		found := false
		if err := bucket.Iter(ctx, "contract/", func(entry string) error { found = found || entry == name; return nil }); err != nil || !found {
			t.Fatalf("list contains object = %v, error = %v", found, err)
		}
		if err := bucket.Delete(ctx, name); err != nil {
			t.Fatal(err)
		}
		if exists, err := bucket.Exists(ctx, name); err != nil || exists {
			t.Fatalf("exists after deletion = %v, error = %v", exists, err)
		}
	})

	// Graceful closes verify persistence and restore, not exact-ACK crash durability.
	t.Run("ternal-configured-before-ack-graceful-reopen-and-empty-cache-restore", func(t *testing.T) {
		t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "provider-contract")
		t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
		record := storageBindingFromEnv()
		record.Token, record.Bootstrap = uuid.NewString(), true
		anchor := &memoryAnchor{record: record}
		previous := kubernetesAnchorBackend
		kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return anchor }
		t.Cleanup(func() { kubernetesAnchorBackend = previous })
		dir := t.TempDir()
		config, err := rhizaConfigFromEnv(dir)
		if err != nil || config.Local || config.ObjStoreDurability != rhiza.ObjectStoreDurabilityBeforeAck {
			t.Fatalf("production configuration: Local=%v durability=%q, error=%v", config.Local, config.ObjStoreDurability, err)
		}
		current, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if current != nil {
				if err := current.Close(); err != nil {
					t.Errorf("close owned store: %v", err)
				}
			}
		})
		host, err := current.CreateHost(ctx, NewHost{Name: "provider-host", EndpointID: strings.Repeat("a", 64), SSHUser: "ops", SSHPort: 2222}, "provider-contract")
		if err != nil {
			t.Fatal(err)
		}
		if err := current.UpdateHost(ctx, host.ID, NewHost{Name: "provider-updated", EndpointID: host.EndpointID, SSHUser: "ops", SSHPort: 2222}, "provider-contract"); err != nil {
			t.Fatal(err)
		}
		for _, dataDir := range []string{dir, t.TempDir()} {
			if err := current.Close(); err != nil {
				t.Fatal(err)
			}
			current = nil
			current, err = Open(ctx, dataDir)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := current.GetHost(ctx, host.ID)
			if err != nil || loaded == nil || loaded.Name != "provider-updated" {
				t.Fatalf("restored host = %#v, error=%v", loaded, err)
			}
		}
		if err := current.DeleteHost(ctx, host.ID, "provider-contract"); err != nil {
			t.Fatal(err)
		}
		if loaded, err := current.GetHost(ctx, host.ID); err != nil || loaded != nil {
			t.Fatalf("deleted host = %#v, error=%v", loaded, err)
		}
	})

	t.Run("local-seeded-preflight-init-restore-write-reopen-and-total-loss", func(t *testing.T) {
		for _, name := range append([]string{"TERNAL_DATA_ADMIN_TOKEN", "TERNAL_DATA_BIND_ADDR", "TERNAL_DATA_PEER_ADDR", "TERNAL_DATA_CLUSTER_MEMBERS", "TERNAL_DATA_EXPECTED_MEMBER_IDS", "TERNAL_OPERATOR_BIND", "TERNAL_DATA_ENABLE_RECONFIGURATION", "TERNAL_DATA_LEARNER", "TERNAL_OBJECT_STORE_DIR"}, localRejectedUpstreamEnv...) {
			t.Setenv(name, "")
		}
		t.Setenv("TERNAL_DATA_LOCAL", "1")
		t.Setenv("TERNAL_REQUIRE_DATA_ADMIN_TOKEN", "0")
		t.Setenv("TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK", "1")
		t.Setenv("TERNAL_LOCAL_PREFLIGHT_SCOPE", uuid.NewString())
		t.Setenv("TERNAL_OBJECT_STORE_PREFIX", "local-contract/"+uuid.NewString())
		t.Setenv("TERNAL_DATA_CHECKPOINT_INTERVAL", "15m")
		t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "local-provider-contract")
		t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
		root := t.TempDir()
		dataDir, stage := filepath.Join(root, "data"), filepath.Join(root, "stage")
		if err := os.Mkdir(stage, 0700); err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(stage, "preflight.json")
		t.Setenv("TERNAL_DATA_DIR", dataDir)
		t.Setenv("TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", stage)
		t.Setenv("TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE", receiptPath)
		config, err := rhizaConfigFromEnv(dataDir)
		if err != nil || !config.Local || config.BindAddr != "" || config.PeerAddr != "" || config.AdminToken != "" || config.PeerToken != "" || len(config.Members) != 0 || config.ObjStoreDurability != rhiza.ObjectStoreDurabilityBeforeAck {
			t.Fatalf("Local configuration has networking or incorrect durability: %v", err)
		}
		record := storageBindingFromEnv()
		record.Token, record.Bootstrap = uuid.NewString(), true
		anchor := &memoryAnchor{record: record}
		previous := kubernetesAnchorBackend
		kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return anchor }
		t.Cleanup(func() { kubernetesAnchorBackend = previous })

		// Explicit manual fixture seeding uses the published SDK, not Ternal's
		// guarded startup. The seed writer is closed before preflight or main.
		seedConfig := config
		seedConfig.DataDir = filepath.Join(root, "manual-seed")
		seedConfig.CheckpointInterval = 20 * time.Millisecond
		seed, err := openRhiza(ctx, seedConfig)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if seed != nil {
				if err := seed.Close(); err != nil {
					t.Errorf("close owned manual seed: %v", err)
				}
			}
		})
		if err := seed.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.ExecContext(ctx, "CREATE TABLE provider_seed (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		// The public SDK checkpoints on a timer, not through a synchronous DB
		// method. Give the fixture a bounded publication window, then quiesce it.
		// Never poll a second archive view while the seed writer is maintaining it.
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
		seed = nil
		archive, err := recovery.OpenLocalArchive(ctx, readOnlyLocalBucket{bucket}, localNamespace(config))
		if err != nil || archive.Snapshot() == nil {
			t.Fatalf("quiescent manual seed lacks a certified checkpoint: %v", err)
		}
		archiveBytes := func() map[string]string {
			t.Helper()
			objects := map[string]string{}
			err := bucket.Iter(ctx, localNamespace(config)+"/", func(key string) error {
				reader, err := bucket.Get(ctx, key)
				if err != nil {
					return err
				}
				hash := sha256.New()
				_, readErr := io.Copy(hash, reader)
				closeErr := reader.Close()
				if readErr != nil {
					return readErr
				}
				if closeErr != nil {
					return closeErr
				}
				objects[key] = string(hash.Sum(nil))
				return nil
			}, objstore.WithRecursiveIter())
			if err != nil || len(objects) == 0 {
				t.Fatalf("snapshot owned archive contents: count=%d error=%v", len(objects), err)
			}
			return objects
		}
		before := archiveBytes()
		if err := LocalPreflightFromEnv(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, archiveBytes()) {
			t.Fatal("read-only preflight mutated archive object contents or names")
		}
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Fatalf("preflight touched main DataDir: %v", err)
		}
		receipt, err := os.ReadFile(receiptPath)
		if err != nil {
			t.Fatal(err)
		}
		if entries, err := os.ReadDir(stage); err != nil || len(entries) != 1 || entries[0].Name() != "preflight.json" {
			t.Fatalf("preflight retained download/staging artifacts: %v %v", entries, err)
		}
		if err := LocalRestoreFromEnv(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, localModeMarker)); err != nil {
			t.Fatalf("init restore did not prepare shared DataDir: %v", err)
		}
		restored, err := openRhiza(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		tables, queryErr := restored.queryRaw(ctx, "SELECT name FROM sqlite_master WHERE name IN ('hosts', 'provider_seed') ORDER BY name")
		closeErr := restored.Close()
		if queryErr != nil || closeErr != nil || tables == nil || !reflect.DeepEqual(tables.rows, [][]any{{"provider_seed"}}) {
			t.Fatalf("init lost seed data or ran application migrations: tables=%#v query=%v close=%v", tables, queryErr, closeErr)
		}
		// Main must reopen the identical restored root and storage identity.
		current, err := Open(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if current != nil {
				if err := current.Close(); err != nil {
					t.Errorf("close owned Local store: %v", err)
				}
			}
		})
		host, err := current.CreateHost(ctx, NewHost{Name: "local-provider-host", EndpointID: strings.Repeat("c", 64), SSHUser: "ops", SSHPort: 2222}, "provider-contract")
		if err != nil {
			t.Fatal(err)
		}
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		current = nil
		// An app-container restart does not rerun init. Keep the identical
		// receipt while the same writer's archived history has advanced.
		if after, err := os.ReadFile(receiptPath); err != nil || !bytes.Equal(receipt, after) {
			t.Fatalf("main modified preflight receipt: %v", err)
		}
		current, err = Open(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := current.GetHost(ctx, host.ID); err != nil || got == nil || got.Name != host.Name {
			t.Fatalf("same-receipt restart lost host: %#v %v", got, err)
		}
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		current = nil
		// Preserve the stopped root as evidence, then emulate a fresh Pod's
		// emptyDir without touching the independent archive or trust authority.
		if err := os.Rename(dataDir, dataDir+"-before-total-loss"); err != nil {
			t.Fatal(err)
		}
		if err := LocalRestoreFromEnv(ctx); err != nil {
			t.Fatal(err)
		}
		current, err = Open(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := current.Ready(ctx); err != nil {
			t.Fatalf("main was not ready after init restored a fresh DataDir: %v", err)
		}
		if got, err := current.GetHost(ctx, host.ID); err != nil || got == nil || got.Name != host.Name {
			t.Fatalf("init restore after total loss lost host: %#v %v", got, err)
		}
		if err := current.DeleteHost(ctx, host.ID, "provider-contract"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRecoveryStorageRejectsUnsupportedAzure(t *testing.T) {
	if _, err := (recoveryStorage{Provider: "azure", Bucket: "ternal-contract-unsupported"}).bucket(); err == nil || !strings.Contains(err.Error(), "unsupported recovery object store provider") {
		t.Fatalf("Azure must remain explicitly unsupported, got %v", err)
	}
}
