package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
	t.Setenv("TERNAL_PIGEONS_BIN", os.Args[0])
	if _, err := loadConfig(); err == nil {
		t.Fatal("remote HTTP API accepted")
	}
}
