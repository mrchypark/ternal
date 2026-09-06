package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/deviceauth"
)

func TestRoostArgsPreserveManagedAndCustomRoutes(t *testing.T) {
	args := roostArgs(config{SSHPort: 2222, RelayURLs: []string{"https://managed.example"}, ExtraRelayURLs: []string{"https://extra.example"}})
	got := strings.Join(args, " ")
	want := "roost --ssh-port 2222 --relay-url https://managed.example --relay-url https://extra.example"
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

func TestSyncAuthorizedKeysRejectsRollbackAndEquivocation(t *testing.T) {
	oldKeys := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D old\n")
	newKeys := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D new\n")
	digest := func(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
	for _, test := range []struct {
		name       string
		generation int64
		body       []byte
		wantErr    bool
	}{
		{"lower generation", 4, newKeys, true},
		{"same generation different digest", 5, newKeys, true},
		{"same snapshot", 5, oldKeys, false},
		{"higher generation empty snapshot", 6, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "authorized_keys")
			statePath := path + ".ternal-state"
			if err := atomicWrite(path, oldKeys, 0600); err != nil {
				t.Fatal(err)
			}
			state, _ := json.Marshal(authorizedKeysState{Generation: 5, SHA256: digest(oldKeys)})
			if err := atomicWrite(statePath, append(state, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			beforeKeys, _ := os.ReadFile(path)
			beforeState, _ := os.ReadFile(statePath)
			acks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agents/authorized-keys":
					w.Header().Set("X-Ternal-Authorized-Keys-Generation", strconv.FormatInt(test.generation, 10))
					w.Header().Set("X-Ternal-Authorized-Keys-Sha256", digest(test.body))
					_, _ = w.Write(test.body)
				case "/agents/authorized-keys/ack":
					acks++
					installedKeys, keyErr := os.ReadFile(path)
					installedState, stateErr := readAuthorizedKeysState(statePath)
					if keyErr != nil || stateErr != nil || installedState == nil || string(installedKeys) != string(test.body) || installedState.Generation != test.generation || installedState.SHA256 != digest(test.body) {
						t.Error("ACK preceded persistence of matching keys and state")
					}
					var ack struct {
						Generation int64  `json:"generation"`
						SHA256     string `json:"sha256"`
					}
					if err := json.NewDecoder(r.Body).Decode(&ack); err != nil {
						t.Error(err)
					} else if ack.Generation != test.generation || ack.SHA256 != digest(test.body) {
						t.Errorf("ack = %#v", ack)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			pigeons := filepath.Join(dir, "pigeons")
			if err := os.WriteFile(pigeons, []byte("#!/bin/sh\nprintf '%s\\n' '"+strings.Repeat("a", 64)+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(dir, "device.key")
			if _, err := deviceauth.GenerateKey(keyPath); err != nil {
				t.Fatal(err)
			}
			identityPath := filepath.Join(dir, "device.json")
			if err := deviceauth.WriteIdentity(identityPath, deviceauth.Identity{Serial: "TEST-SYNC", HostKeyFingerprint: "SHA256:" + strings.Repeat("A", 43)}); err != nil {
				t.Fatal(err)
			}
			err := syncAuthorizedKeys(context.Background(), config{APIURL: server.URL, Pigeons: pigeons, DeviceKey: keyPath, IdentityFile: identityPath, SSHUser: "ops"}, path)
			if test.wantErr {
				if err == nil || err.Error() != "authorized_keys snapshot rollback or equivocation rejected" {
					t.Fatalf("snapshot rejection = %v", err)
				}
				afterKeys, _ := os.ReadFile(path)
				afterState, _ := os.ReadFile(statePath)
				if string(afterKeys) != string(beforeKeys) || string(afterState) != string(beforeState) || acks != 0 {
					t.Fatalf("rejection changed keys=%q state=%q acks=%d", afterKeys, afterState, acks)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			afterKeys, _ := os.ReadFile(path)
			afterState, err := readAuthorizedKeysState(statePath)
			if err != nil || afterState == nil || string(afterKeys) != string(test.body) || afterState.Generation != test.generation || afterState.SHA256 != digest(test.body) || acks != 1 {
				t.Fatalf("accepted snapshot keys=%q state=%#v acks=%d err=%v", afterKeys, afterState, acks, err)
			}
		})
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
	heartbeats := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/heartbeat":
			var body struct {
				Status string `json:"service_status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			heartbeats <- body.Status
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
	if first, second := <-heartbeats, <-heartbeats; first != "starting" || second != "healthy" {
		t.Fatalf("supervisor heartbeat states = %q, %q", first, second)
	}
	var status runtimeStatus
	data, err := os.ReadFile(cfg.StatusFile)
	if err != nil || json.Unmarshal(data, &status) != nil || status.Service != "revoked" || status.Child != "stopped" {
		t.Fatalf("revoked status=%#v read error=%v", status, err)
	}
}
