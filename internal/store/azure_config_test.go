package store

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func azureTestEnv(t *testing.T) {
	t.Helper()
	localTestEnv(t)
	for _, suffix := range []string{"STORAGE_ACCOUNT", "BLOB_ENDPOINT", "STORAGE_ACCOUNT_KEY", "CONNECTION_STRING", "TENANT_ID", "CLIENT_ID", "CLIENT_SECRET", "USER_ASSIGNED_ID", "EMULATOR"} {
		t.Setenv("TERNAL_OBJECT_STORE_AZURE_"+suffix, "")
	}
	t.Setenv("TERNAL_OBJECT_STORE_PROVIDER", "azure")
	t.Setenv("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")
	t.Setenv("TERNAL_OBJECT_STORE_BUCKET", "ternal-contract-unit")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT", "ternaltest")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", base64.StdEncoding.EncodeToString([]byte("unit-test-key")))
}

func TestAzureStorageConfigurationAndIdentity(t *testing.T) {
	azureTestEnv(t)
	config, err := rhizaConfigFromEnv(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Local || config.ObjStoreProvider != "azure" || config.ObjStoreAzureStorageAccount != "ternaltest" || config.ObjStoreEndpoint != "blob.core.windows.net" || config.ObjStoreInsecure {
		t.Fatal("Azure public SDK configuration was not projected")
	}
	storage := recoveryStorageFromEnv()
	if storage.azureErr != nil {
		t.Fatal(storage.azureErr)
	}
	anchorConfig := storage.Azure.bucketConfig(storage.Bucket)
	if anchorConfig.StorageAccountName != config.ObjStoreAzureStorageAccount || anchorConfig.ContainerName != config.ObjStoreBucket || anchorConfig.Endpoint != config.ObjStoreEndpoint || anchorConfig.StorageAccountKey != config.ObjStoreAzureStorageAccountKey || anchorConfig.StorageCreateContainer {
		t.Fatal("API and recovery/preflight account, container, endpoint or authentication differ")
	}
	want := storage.localIdentity(config.ClusterID)
	if got := localStorageID(config); got != want || storageBindingFromEnv().StorageID != want {
		t.Fatal("init/main/preflight/trust identity differs")
	}
	legacy := storage.identity(config.ClusterID)
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", base64.StdEncoding.EncodeToString([]byte("rotated-key")))
	if recoveryStorageFromEnv().identity(config.ClusterID) != legacy {
		t.Fatal("key rotation changed identity")
	}
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "AccountName=ternaltest;AccountKey="+base64.StdEncoding.EncodeToString([]byte("another-key"))+";")
	if recoveryStorageFromEnv().identity(config.ClusterID) != legacy {
		t.Fatal("equivalent connection string changed identity")
	}
	connectionConfig, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil || connectionConfig.ObjStoreEndpoint != config.ObjStoreEndpoint {
		t.Fatal("equivalent credential form changed Rhiza endpoint identity")
	}
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "")
	t.Setenv("TERNAL_DATA_LOCAL", "0")
	main, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil || main.Local || storageBindingFromEnv().StorageID != legacy {
		t.Fatal("legacy Azure projection differs")
	}
	for _, change := range []struct{ field, value string }{
		{"TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT", "otheraccount"},
		{"TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT", "https://ternaltest.blob.core.usgovcloudapi.net"},
		{"TERNAL_OBJECT_STORE_BUCKET", "other-container"},
		{"TERNAL_OBJECT_STORE_PREFIX", "other-prefix"},
	} {
		t.Run(change.field, func(t *testing.T) {
			t.Setenv(change.field, change.value)
			if got := recoveryStorageFromEnv().identity(config.ClusterID); got == "" || got == legacy {
				t.Fatal("archive relocation retained identity")
			}
		})
	}
	if storage.identity("other-cluster") == legacy || want == legacy {
		t.Fatal("cluster/Local domain separation lost")
	}
	if AzureTrustAnchorStorageID("ternaltest", "https://ternaltest.blob.core.windows.net", "b", "<>&\u2028\u2029", "c") != "19d8cb50c02bb7f1afe9a456524ab2b3805151c8e7f261791b414ea8b0764c27" {
		t.Fatal("Azure JSON escaping contract changed")
	}
}

func TestAzureAuthenticationForms(t *testing.T) {
	for _, form := range []string{"default", "key", "connection", "principal", "managed"} {
		t.Run(form, func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
			switch form {
			case "key":
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "dGVzdA==")
			case "connection":
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "AccountName=ternaltest;AccountKey=dGVzdA==;DefaultEndpointsProtocol=https;EndpointSuffix=core.windows.net")
			case "principal":
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_TENANT_ID", "tenant")
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_CLIENT_ID", "client")
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_CLIENT_SECRET", "principal-secret")
			case "managed":
				t.Setenv("TERNAL_OBJECT_STORE_AZURE_USER_ASSIGNED_ID", "managed-id")
			}
			c, err := rhizaConfigFromEnv(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if localStorageID(c) != storageBindingFromEnv().StorageID {
				t.Fatal("authentication changed storage binding")
			}
		})
	}
}

func TestAzureInvalidConfigurationRefusesBeforeMutation(t *testing.T) {
	const secret = "must-not-appear-in-errors"
	for _, tc := range []struct{ name, field, value string }{
		{"account", "STORAGE_ACCOUNT", ""},
		{"account-case", "STORAGE_ACCOUNT", "UpperCase"},
		{"trailing-slash", "BLOB_ENDPOINT", "https://ternaltest.blob.core.windows.net/"},
		{"user-info", "BLOB_ENDPOINT", "https://user:" + secret + "@ternaltest.blob.core.windows.net"},
		{"query", "BLOB_ENDPOINT", "https://ternaltest.blob.core.windows.net?sig=" + secret},
		{"fragment", "BLOB_ENDPOINT", "https://ternaltest.blob.core.windows.net#"},
		{"empty-port", "BLOB_ENDPOINT", "https://ternaltest.blob.core.windows.net:"},
		{"unrepresentable", "BLOB_ENDPOINT", "https://unrelated.example"},
		{"production-http", "BLOB_ENDPOINT", "http://127.0.0.1:1234/ternaltest"},
		{"emulator-production", "EMULATOR", "1"},
		{"partial-principal", "CLIENT_SECRET", secret},
		{"mixed-managed", "USER_ASSIGNED_ID", "managed-id"},
		{"invalid-key", "STORAGE_ACCOUNT_KEY", secret},
		{"empty-decoded-key", "STORAGE_ACCOUNT_KEY", "\n"},
		{"mixed-connection", "CONNECTION_STRING", "AccountName=ternaltest;AccountKey=" + secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_"+tc.field, tc.value)
			dir := filepath.Join(t.TempDir(), "never-created")
			_, err := Open(context.Background(), dir)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatal("invalid Azure configuration accepted or secret disclosed")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid Azure configuration mutated DataDir")
			}
		})
	}
	for _, field := range []string{"TERNAL_OBJECT_STORE_ENDPOINT", "TERNAL_OBJECT_STORE_INSECURE"} {
		t.Run(field, func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv(field, "true")
			if _, err := rhizaConfigFromEnv(t.TempDir()); err == nil {
				t.Fatal("Azure accepted generic override")
			}
		})
	}
}

func TestAzureConnectionStringTargetIsUnambiguous(t *testing.T) {
	for _, value := range []string{
		"AccountName=otheraccount;AccountKey=dGVzdA==",
		"AccountName=ternaltest;AccountName=ternaltest;AccountKey=dGVzdA==",
		"AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint=https://other.blob.core.windows.net",
		"AccountName=ternaltest;AccountKey=dGVzdA==;SharedAccessSignature=secret-sas",
		"AccountName=ternaltest;AccountKey=secret-key",
		"UseDevelopmentStorage=true",
		"AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint=https://ternaltest.blob.core.windows.net;EndpointSuffix=core.windows.net",
		"AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint=https://ternaltest.blob.core.windows.net;DefaultEndpointsProtocol=http",
		"AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint=https://ternaltest.blob.core.windows.net:65536",
	} {
		t.Run("refused", func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", value)
			if _, err := azureStorageFromEnv(); err == nil || strings.Contains(err.Error(), "secret-") {
				t.Fatal("ambiguous connection string accepted or secret disclosed")
			}
		})
	}
}

func TestAzureExplicitEndpointPortBounds(t *testing.T) {
	for _, port := range []string{"", "0", "65536", "99999999999999999999999"} {
		t.Run("reject-"+port, func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
			endpoint := "https://ternaltest.blob.core.windows.net:" + port
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT", endpoint)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint="+endpoint)
			if _, err := azureStorageFromEnv(); err == nil {
				t.Fatal("empty or out-of-range endpoint port accepted")
			}
		})
	}
	for _, port := range []string{"1", "65535"} {
		t.Run("accept-"+port, func(t *testing.T) {
			azureTestEnv(t)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
			endpoint := "https://ternaltest.blob.core.windows.net:" + port
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT", endpoint)
			t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint="+endpoint)
			if _, err := azureStorageFromEnv(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAzureRecoveryBucketNeverProvisions(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.Header().Set("x-ms-error-code", "ContainerNotFound")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>ContainerNotFound</Code></Error>`))
	}))
	t.Cleanup(server.Close)
	azureTestEnv(t)
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_EMULATOR", "1")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT", server.URL+"/ternaltest")
	t.Setenv("TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "AccountName=ternaltest;AccountKey=dGVzdA==;BlobEndpoint="+server.URL+"/ternaltest")
	storage := recoveryStorageFromEnv()
	config, err := rhizaConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if azureEndpointFromRhiza(config) != storage.Azure.BlobEndpoint || localStorageID(config) != storage.localIdentity(config.ClusterID) {
		t.Fatal("emulator native/recovery configuration targets differ")
	}
	if storage.Azure.bucketConfig(storage.Bucket).StorageCreateContainer {
		t.Fatal("Azure recovery enables provisioning")
	}
	bucket, err := storage.bucket()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bucket.Close(); err != nil {
			t.Error(err)
		}
	})
	mu.Lock()
	before := len(methods)
	mu.Unlock()
	if before != 0 {
		t.Fatal("account-key bucket construction contacted the server")
	}
	if _, err := bucket.Get(context.Background(), "missing"); err == nil {
		t.Fatal("missing container was accepted")
	}
	// A missing container must also stop the actual init path before the SDK
	// can open a writer or create its local DataDir.
	root := t.TempDir()
	dataDir, stage := filepath.Join(root, "data"), filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_DATA_DIR", dataDir)
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", stage)
	t.Setenv("TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE", filepath.Join(stage, "receipt.json"))
	t.Setenv("TERNAL_TRUST_ANCHOR_CONFIGMAP", "missing-container")
	t.Setenv("TERNAL_TRUST_ANCHOR_NAMESPACE", "test")
	record := storageBindingFromEnv()
	record.Token, record.Bootstrap = uuid.NewString(), true
	previous := kubernetesAnchorBackend
	kubernetesAnchorBackend = func(string, string) trustAnchorBackend { return &memoryAnchor{record: record} }
	t.Cleanup(func() { kubernetesAnchorBackend = previous })
	if err := LocalRestoreFromEnv(context.Background()); err == nil {
		t.Fatal("init accepted a missing container")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatal("failed init mutated DataDir")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("test did not reach actual Azure adapter")
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatal("Azure recovery attempted provisioning")
		}
	}
}

func TestExistingStorageIdentityGoldenHashes(t *testing.T) {
	for _, tc := range []struct{ provider, endpoint, legacy, local string }{
		{"s3", "minio:9000", "95d63b48f54568faf03b68fb13d8811e9e3deed4bc431c6d9ea0eccb7e247346", "c97ae539488ebee664c81f8c24ffa1cbe0eab1ba3344d63f68099a4db6b7b756"},
		{"gcs", "", "b7d6664c91841ea987bbef5d2645d01a7faded46ffeab226f9ec6346c0c83d36", "cbb395a78fd1b66f697fee236482776d81edae9e8507b5d483d09d30052c1c4a"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			c := recoveryStorage{Provider: tc.provider, Endpoint: tc.endpoint, Bucket: "bucket", Prefix: "prefix"}
			if c.identity("cluster") != tc.legacy || c.localIdentity("cluster") != tc.local {
				t.Fatal("existing storage authority identity changed")
			}
		})
	}
}
