package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
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
	body := []byte("expiry-time=\"20330101000000Z\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n")
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
	oldKeys := []byte("expiry-time=\"20330101000000Z\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D old\n")
	newKeys := []byte("expiry-time=\"20330101000000Z\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D new\n")
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
	dir := t.TempDir()
	ready := filepath.Join(dir, "pigeons-ready")
	t.Setenv("PIGEONS_READY", ready)
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
			for deadline := time.Now().Add(time.Second); ; {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Error("pigeons did not install SIGINT trap before revocation")
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				time.Sleep(time.Millisecond)
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	pigeons := filepath.Join(dir, "pigeons")
	interrupted := filepath.Join(dir, "pigeons-interrupted")
	t.Setenv("PIGEONS_INTERRUPTED", interrupted)
	script := "#!/bin/sh\nif [ \"$1\" = \"endpoint-id\" ]; then printf '%s\\n' '" + strings.Repeat("a", 64) + "'; exit 0; fi\ntrap 'printf interrupted > \"$PIGEONS_INTERRUPTED\"; exit 0' INT\nprintf ready > \"$PIGEONS_READY\"\nwhile :; do sleep 1; done\n"
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
	if _, err := os.Stat(interrupted); err != nil {
		t.Fatalf("revoked supervisor did not deliver SIGINT to pigeons: %v", err)
	}
	var status runtimeStatus
	data, err := os.ReadFile(cfg.StatusFile)
	if err != nil || json.Unmarshal(data, &status) != nil || status.Service != "revoked" || status.Child != "stopped" {
		t.Fatalf("revoked status=%#v read error=%v", status, err)
	}
}

func TestStopChildFallsBackToKillAfterGrace(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	ready := filepath.Join(t.TempDir(), "pigeons-ready")
	t.Setenv("PIGEONS_READY", ready)
	child := exec.Command("/bin/sh", "-c", "trap '' INT; printf ready > \"$PIGEONS_READY\"; exec sleep 60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exit := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() { exit <- child.Wait(); close(waitDone) }()
	t.Cleanup(func() {
		select {
		case <-waitDone:
			return
		default:
			_ = child.Process.Kill()
			<-waitDone
		}
	})
	for deadline := time.Now().Add(time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("INT-ignoring child did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	grace := 10 * time.Millisecond
	started := time.Now()
	exitErr := stopChild(child, exit, grace)
	elapsed := time.Since(started)
	if elapsed < grace || elapsed > time.Second {
		t.Fatalf("fallback shutdown took %s", elapsed)
	}
	if exitErr == nil {
		t.Fatal("fallback child was not killed and reaped")
	}
}

func TestAuthorizedKeysRequireExpiryTime(t *testing.T) {
	good := []byte("expiry-time=\"20330101000000Z\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n")
	if err := validateAuthorizedKeys(good); err != nil {
		t.Fatalf("expiry-time line rejected: %v", err)
	}
	for _, bad := range []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n",
		"expiry-time=20330101000000 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n",
		"expiry-time=\"notatime\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n",
		// A local-time deadline would be read in the device own zone by sshd.
		"expiry-time=\"20330101000000\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n",
		"command=\"evil\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D\n",
	} {
		if err := validateAuthorizedKeys([]byte(bad)); err == nil {
			t.Errorf("line without valid expiry-time accepted: %q", bad)
		}
	}
}

func TestSyncAuthorizedKeysRejectsOversizedSnapshot(t *testing.T) {
	body := bytes.Repeat([]byte("x"), authorizedKeysMaxBytes+1)
	sum := sha256.Sum256(body)
	digestHex := hex.EncodeToString(sum[:])
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/authorized-keys":
			w.Header().Set("X-Ternal-Authorized-Keys-Generation", "7")
			w.Header().Set("X-Ternal-Authorized-Keys-Sha256", digestHex)
			_, _ = w.Write(body)
		case "/agents/authorized-keys/ack":
			acks++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	pigeons := filepath.Join(dir, "pigeons")
	if err := os.WriteFile(pigeons, []byte("#!/bin/sh\nprintf '%s\\n' '"+strings.Repeat("a", 64)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "device.key")
	if _, err := deviceauth.GenerateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(dir, "device.json")
	if err := deviceauth.WriteIdentity(identityPath, deviceauth.Identity{Serial: "TEST-HUGE", HostKeyFingerprint: "SHA256:" + strings.Repeat("A", 43)}); err != nil {
		t.Fatal(err)
	}
	err := syncAuthorizedKeys(context.Background(), config{APIURL: server.URL, Pigeons: pigeons, DeviceKey: keyPath, IdentityFile: identityPath, SSHUser: "ops"}, path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") || acks != 0 {
		t.Fatalf("oversized snapshot err = %v acks = %d", err, acks)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("oversized snapshot was installed")
	}
}

func TestEnsureUserOwnedAssignsTargetAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown behavior requires root")
	}
	target, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	if err := atomicWrite(path, []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureUserOwned(path, "nobody"); err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(target.Uid)
	gid, _ := strconv.Atoi(target.Gid)
	if !fileOwnedBy(path, uid, gid) {
		t.Fatal("authorized_keys not owned by target account after ensure")
	}
	if !fileOwnedBy(filepath.Dir(path), uid, gid) {
		t.Fatal("fresh parent dir not owned by target account after ensure")
	}
}

func TestEnsureUserOwnedIsNoopUnprivileged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unprivileged behavior only")
	}
	if err := ensureUserOwned(filepath.Join(t.TempDir(), "keys"), "nobody"); err != nil {
		t.Fatalf("unprivileged ensure = %v, want nil", err)
	}
}

func TestRoostEndpointIDHasDeadlineAndCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake pigeons uses the repository's POSIX shell test convention")
	}
	oldTimeout := endpointIDTimeout
	endpointIDTimeout = 3 * time.Second
	t.Cleanup(func() { endpointIDTimeout = oldTimeout })
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	slow := filepath.Join(dir, "pigeons")
	script := "#!/bin/sh\necho x >> \"" + calls + "\"\nsleep 10\nprintf '%s\\n' '" + strings.Repeat("b", 64) + "'\n"
	if err := os.WriteFile(slow, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := roostEndpointID(config{Pigeons: slow}); err == nil {
		t.Fatal("hung endpoint-id helper was not bounded")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("endpoint-id took %v without deadline", took)
	}
	fast := filepath.Join(dir, "fast-pigeons")
	fastScript := "#!/bin/sh\necho x >> \"" + calls + "\"\nprintf '%s\\n' '" + strings.Repeat("c", 64) + "'\n"
	if err := os.WriteFile(fast, []byte(fastScript), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config{Pigeons: fast}
	first, err := roostEndpointID(cfg)
	if err != nil || first != strings.Repeat("c", 64) {
		t.Fatalf("endpoint = %q, err=%v", first, err)
	}
	before, _ := os.ReadFile(calls)
	if _, err := roostEndpointID(cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(calls)
	if len(after) != len(before) {
		t.Fatal("immutable endpoint identity was re-executed instead of cached")
	}
}

func TestPigeonsFileNameIsPlatformAware(t *testing.T) {
	if got := pigeonsFileName("windows"); got != "pigeons.exe" {
		t.Fatalf("windows binary = %q, want pigeons.exe", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := pigeonsFileName(goos); got != "pigeons" {
			t.Fatalf("%s binary = %q, want pigeons", goos, got)
		}
	}
}

func TestValidateAuthorizedKeysRejectsLegacyLines(t *testing.T) {
	// A pre-hardening snapshot (plain keys, no expiry-time) must not be
	// installable: the agent replaces the file wholesale, so accepting it
	// would silently keep keys alive without a deadline.
	legacy := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D legacy\n")
	if err := validateAuthorizedKeys(legacy); err == nil {
		t.Fatal("legacy key line without expiry-time was accepted")
	}
}

func TestPurgeLegacyAuthorizedKeysDropsUnboundedLines(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/authorized_keys"
	legacy := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D legacy\n" +
		"expiry-time=\"20330101000000Z\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D kept\n"
	if err := atomicWrite(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := purgeLegacyAuthorizedKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("legacy lines were not reported as removed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "legacy") {
		t.Fatalf("unbounded legacy line survived the purge: %q", data)
	}
	if !strings.Contains(string(data), "kept") {
		t.Fatalf("expiring line was dropped: %q", data)
	}
	if removed, err := purgeLegacyAuthorizedKeys(path); err != nil || removed {
		t.Fatalf("second purge = %v, %v; want no-op", removed, err)
	}
}
