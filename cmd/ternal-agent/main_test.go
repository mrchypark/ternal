package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/deviceauth"
)

func TestRoostArgsPreserveManagedAndCustomRoutes(t *testing.T) {
	args := roostArgs(config{SSHPort: 2222, RelayURLs: []string{"https://managed.example"}, ExtraRelayURLs: []string{"https://extra.example"}})
	got := strings.Join(args, " ")
	want := "roost --ssh-port 2222 --relay-url https://managed.example --extra-relay-url https://extra.example"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestAuthorizedKeysStateRejectsInvalidAndLockSerializes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/authorized_keys"
	unlock, err := acquireSyncLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireSyncLock(path); err == nil {
		t.Fatal("concurrent synchronization lock was accepted")
	}
	unlock()
	state := authorizedKeysState{Generation: 2, SHA256: strings.Repeat("a", 64)}
	encoded, _ := json.Marshal(state)
	if err := atomicWrite(path+".ternal-state", encoded, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readAuthorizedKeysState(path + ".ternal-state")
	if err != nil || got.Generation != 2 || got.SHA256 != state.SHA256 {
		t.Fatalf("state = %#v, err=%v", got, err)
	}
	if err := atomicWrite(path+".ternal-state", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorizedKeysState(path + ".ternal-state"); err == nil {
		t.Fatal("invalid synchronization state accepted")
	}
}

func TestAuthorizedKeysAreValidatedAndWrittenWithStrictMode(t *testing.T) {
	body := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D test\n")
	if err := validateAuthorizedKeys(body); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/authorized_keys"
	if err := atomicWrite(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := validateAuthorizedKeys([]byte("command=evil ssh-ed25519 invalid\n")); err == nil {
		t.Fatal("invalid authorized_keys content accepted")
	}
}

func TestConfigRejectsRemoteCleartextAPI(t *testing.T) {
	t.Setenv("TERNAL_API_URL", "http://ternal.example")
	t.Setenv("TERNAL_TRANSPORT_BIN", os.Args[0])
	if _, err := loadConfig(); err == nil {
		t.Fatal("remote HTTP API accepted")
	}
}

func TestUnauthorizedControlPlaneResponseIsFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	_, err := request(context.Background(), server.URL, http.MethodPost, "/agents/heartbeat", nil, nil)
	if !isUnauthorized(err) {
		t.Fatalf("unauthorized response was not classified as fatal: %v", err)
	}
}

func TestSupervisorStopsTransportWhenDeviceIsRevoked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/heartbeat":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/agents/authorized-keys":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	pigeons := filepath.Join(dir, "pigeons")
	script := "#!/bin/sh\nif [ \"$1\" = endpoint-id ]; then printf '%s\\n' '" + strings.Repeat("a", 64) + "'; exit 0; fi\nexec sleep 60\n"
	if err := os.WriteFile(pigeons, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "device.key")
	if _, err := deviceauth.GenerateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(dir, "device.json")
	if err := deviceauth.WriteIdentity(identityPath, deviceauth.Identity{Serial: "TEST-REVOKED", HostKeyFingerprint: "SHA256:" + strings.Repeat("A", 43)}); err != nil {
		t.Fatal(err)
	}
	cfg := config{
		APIURL: server.URL, Pigeons: pigeons, DeviceKey: keyPath, IdentityFile: identityPath,
		SSHUser: "ops", SSHPort: 22, HeartbeatEvery: time.Hour, RestartBackoff: time.Millisecond,
		StatusFile: filepath.Join(dir, "status.json"), AuthorizedKeysPath: filepath.Join(dir, "authorized_keys"),
	}
	if err := supervise(context.Background(), cfg); !isUnauthorized(err) {
		t.Fatalf("supervisor did not stop on revocation: %v", err)
	}
	var status runtimeStatus
	data, err := os.ReadFile(cfg.StatusFile)
	if err != nil || json.Unmarshal(data, &status) != nil || status.Service != "revoked" || status.Child != "stopped" {
		t.Fatalf("revoked status=%#v read error=%v", status, err)
	}
}
