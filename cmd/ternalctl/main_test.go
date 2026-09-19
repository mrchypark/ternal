package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestDevicePollingPreservesProviderPendingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer server.Close()
	result, status, err := postJSONResponse(server.Client(), server.URL, map[string]string{"device_code": "pending"})
	if err != nil || status != http.StatusBadRequest || result["error"] != "authorization_pending" {
		t.Fatalf("result=%v status=%d err=%v", result, status, err)
	}
}

func TestKnownHostKeyStrictAcceptanceAndRejection(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyType := sshKey.Type()
	keyData := base64.StdEncoding.EncodeToString(sshKey.Marshal())
	fingerprint := ssh.FingerprintSHA256(sshKey)

	var output bytes.Buffer
	if err := writeKnownHostKey(&output, fingerprint, []string{"HOSTNAME", fingerprint, keyType, keyData}); err != nil {
		t.Fatalf("valid pinned key rejected: %v", err)
	}
	if got, want := output.String(), "* "+keyType+" "+keyData+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if err := writeKnownHostKey(&bytes.Buffer{}, "SHA256:"+strings.Repeat("A", 43), []string{"HOSTNAME", fingerprint, keyType, keyData}); err == nil {
		t.Fatal("mismatched expected fingerprint accepted")
	}
	if err := writeKnownHostKey(&bytes.Buffer{}, fingerprint, []string{"ADDRESS", fingerprint, keyType, keyData}); err == nil {
		t.Fatal("ADDRESS invocation accepted")
	}
}

func TestKnownHostKeyOrderProbeIsEmpty(t *testing.T) {
	var output bytes.Buffer
	if err := writeKnownHostKey(&output, "SHA256:"+strings.Repeat("A", 43), []string{"ORDER", "NONE", "NONE", "NONE"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("ORDER probe wrote %q", output.String())
	}
}

func TestProxyRequiresExplicitRoute(t *testing.T) {
	endpoint := strings.Repeat("a", 64) + ":22"
	if err := validateProxyInvocation("host-1", endpoint, nil); err == nil {
		t.Fatal("EndpointId-only proxy invocation accepted")
	}
	if err := validateProxyInvocation("host-1", endpoint, []string{"--relay-url", "https://relay.example"}); err != nil {
		t.Fatalf("explicit relay rejected: %v", err)
	}
}

func TestProxyUsesGrantedHomeIdentityForEndpointAndFly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake pigeons uses the repository's POSIX shell test convention")
	}
	home := filepath.Join(t.TempDir(), "home with spaces")
	keyDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	t.Setenv("TERNAL_CSRF_TOKEN", "")

	logPath := filepath.Join(t.TempDir(), "pigeons-args")
	pigeonsPath := filepath.Join(t.TempDir(), "pigeons")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$PIGEONS_ARGS_LOG\"\nprintf '%s\\n' -- >> \"$PIGEONS_ARGS_LOG\"\nif [ \"$1\" = endpoint-id ]; then mkdir -p \"$3\"; printf '%s\\n' '" + strings.Repeat("a", 64) + "'; fi\n"
	if err := os.WriteFile(pigeonsPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_TRANSPORT_BIN", pigeonsPath)
	t.Setenv("PIGEONS_ARGS_LOG", logPath)
	t.Setenv("TERNAL_DEV_HEADERS", "1")

	endpointID := strings.Repeat("a", 64)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hosts/host-1":
			calls = append(calls, "host")
			_, _ = w.Write([]byte(`{"id":"host-1","ssh_user":"ops"}`))
		case r.URL.Path == "/access/ssh":
			calls = append(calls, "ssh-grant")
			var req struct {
				HostID  string `json:"host_id"`
				SSHUser string `json:"ssh_user"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.HostID != "host-1" || req.SSHUser != "ops" {
				t.Errorf("ssh grant request = %#v", req)
			}
			_, _ = w.Write([]byte(`{"program":"ssh","args":[],"grant_id":"grant-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/access/grants/grant-1/key-status":
			calls = append(calls, "key-status")
			_, _ = w.Write([]byte(`{"installed":true}`))
		case r.URL.Path == "/access/relay-grants":
			calls = append(calls, "relay-grant")
			var grant struct {
				ClientEndpointID string `json:"client_endpoint_id"`
				SSHUser          string `json:"ssh_user"`
				TTL              int    `json:"ttl"`
			}
			if err := json.NewDecoder(r.Body).Decode(&grant); err != nil {
				t.Error(err)
			}
			if grant.ClientEndpointID != endpointID || grant.SSHUser != "ops" || grant.TTL != 300 {
				t.Errorf("grant = %#v", grant)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cmdProxy(server.Client(), server.URL, "host-1", endpointID+":22", []string{"--relay-url", "https://relay.example"})
	wantCalls := []string{"host", "ssh-grant", "key-status", "relay-grant"}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("proxy calls = %v, want %v", calls, wantCalls)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "endpoint-id\n--key-dir\n" + keyDir + "\n--\nfly\n--stdio\n" + endpointID + "\n--key-dir\n" + keyDir + "\n--relay-url\nhttps://relay.example\n--\n"
	if string(got) != want {
		t.Fatalf("pigeons argv = %q, want %q", got, want)
	}
	if info, err := os.Stat(keyDir); err != nil || !info.IsDir() {
		t.Fatalf("pigeons did not create fresh key directory: info=%v err=%v", info, err)
	}
}

func TestDevelopmentHeadersAreLoopbackOnly(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	t.Setenv("TERNAL_USER", "smoke-user")
	t.Setenv("TERNAL_GROUPS", "smoke-admins")

	if _, err := newHTTPClient("https://ternal.example"); err == nil {
		t.Fatal("development headers accepted for a non-loopback API")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Ternal-User") != "smoke-user" || r.Header.Get("X-Ternal-Groups") != "smoke-admins" {
			t.Error("development identity headers were not attached")
		}
		if r.Header.Get("X-CSRF-Token") != "dev-csrf" {
			t.Error("development CSRF header was not attached")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := newHTTPClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestSessionCanBeSuppliedWithoutPersistentState(t *testing.T) {
	t.Setenv("TERNAL_SESSION_COOKIE", "ephemeral-session")
	t.Setenv("TERNAL_CSRF_TOKEN", "ephemeral-csrf")
	session, err := loadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.Cookie != "ephemeral-session" || session.CSRFToken != "ephemeral-csrf" {
		t.Fatalf("session = %#v", session)
	}
}

func TestConfigDirOverrideIsolatesSessionLifecycle(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TERNAL_CONFIG_DIR", configDir)
	t.Setenv("TERNAL_SESSION_COOKIE", "")

	path, err := sessionPath()
	if err != nil {
		t.Fatal(err)
	}
	if wantDir := filepath.Join(configDir, "ternal"); filepath.Dir(path) != wantDir || !strings.HasPrefix(filepath.Base(path), "session-") {
		t.Fatalf("session path = %q is not namespaced under %q", path, wantDir)
	}
	session := &Session{Cookie: "isolated-session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	if got, err := loadSession(); err != nil || got.Cookie != session.Cookie {
		t.Fatalf("loaded session = %#v, err=%v", got, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ternal_session")
		if err != nil || cookie.Value != session.Cookie {
			t.Errorf("logout cookie=%v err=%v", cookie, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("isolated session remained after logout: %v", err)
	}
}

func TestLogoutRevokesServerBeforeClearingSession(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	session := &Session{Cookie: "signed-session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ternal_session")
		if err != nil || cookie.Value != session.Cookie || r.Header.Get("X-CSRF-Token") != session.CSRFToken {
			t.Errorf("logout request cookie=%v err=%v csrf=%q", cookie, err, r.Header.Get("X-CSRF-Token"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(); err == nil {
		t.Fatal("logout retained local session")
	}
}

func TestLogoutRetainsSessionWhenRevocationFails(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	session := &Session{Cookie: "signed-session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err == nil {
		t.Fatal("failed server revocation was accepted")
	}
	if got, err := loadSession(); err != nil || got.Cookie != session.Cookie {
		t.Fatalf("session after failed revocation = %#v, err=%v", got, err)
	}
}

func TestLogoutRetainsSessionWhenServerReturnsUnauthorized(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	session := &Session{Cookie: "signed-session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err == nil {
		t.Fatal("unauthorized logout was accepted without confirmed revocation")
	}
	got, err := loadSession()
	if err != nil || got.Cookie != session.Cookie {
		t.Fatalf("session after unauthorized logout = %#v, err=%v", got, err)
	}
}

func TestLogoutSubmitsLocallyExpiredSession(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	session := &Session{Cookie: "server-still-valid", CSRFToken: "csrf", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		cookie, err := r.Cookie("ternal_session")
		if err != nil || cookie.Value != session.Cookie {
			t.Errorf("logout request cookie=%v err=%v", cookie, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("locally expired session was not submitted for server revocation")
	}
}

func TestLogoutOfEnvironmentSessionPreservesDiskSession(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	diskSession := &Session{Cookie: "disk-session", CSRFToken: "disk-csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(diskSession); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_SESSION_COOKIE", "environment-session")
	t.Setenv("TERNAL_CSRF_TOKEN", "environment-csrf")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("ternal_session")
		if err != nil || cookie.Value != "environment-session" {
			t.Errorf("logout request cookie=%v err=%v", cookie, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := logout(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	got, err := loadSession()
	if err != nil || got.Cookie != diskSession.Cookie {
		t.Fatalf("disk session after environment logout = %#v, err=%v", got, err)
	}
}

func TestSSHWaitsForKeyInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh uses the repository's POSIX shell test convention")
	}
	t.Setenv("TERNAL_SESSION_COOKIE", "session-for-wait-test")
	t.Setenv("TERNAL_CSRF_TOKEN", "")
	sshLog := filepath.Join(t.TempDir(), "ssh-args")
	fakeSSH := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(fakeSSH, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+sshLog+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls []string
	quotedSSH, _ := json.Marshal(fakeSSH)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/hosts":
			_, _ = w.Write([]byte(`[{"id":"host-9","name":"edge-9","ssh_user":"ops"}]`))
		case r.URL.Path == "/access/ssh":
			calls = append(calls, "ssh-grant")
			_, _ = w.Write([]byte(`{"program":` + string(quotedSSH) + `,"args":["-p","22","-o","ProxyCommand=ternalctl proxy host-9 %h:%p","edge-9"],"grant_id":"grant-9"}`))
		case r.URL.Path == "/access/grants/grant-9/key-status":
			calls = append(calls, "key-status")
			_, _ = w.Write([]byte(`{"installed":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cmdSSH(server.Client(), server.URL, "edge-9")
	if strings.Join(calls, ",") != "ssh-grant,key-status" {
		t.Fatalf("ssh calls = %v, want grant then key-status wait", calls)
	}
	if _, err := os.Stat(sshLog); err != nil {
		t.Fatal("ssh was not executed after key installation")
	}
	logged, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "=ternalctl ") {
		t.Fatalf("ssh received bare recursive path: %q", logged)
	}
}

func TestSessionRejectsCrossOriginReuse(t *testing.T) {
	t.Setenv("TERNAL_CONFIG_DIR", t.TempDir())
	t.Setenv("TERNAL_SESSION_COOKIE", "")
	t.Setenv("TERNAL_API_URL", "http://127.0.0.1:3000")
	session := &Session{Cookie: "origin-session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveSession(session); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(); err != nil {
		t.Fatalf("same-origin load = %v", err)
	}
	t.Setenv("TERNAL_API_URL", "http://127.0.0.1:4000")
	if _, err := loadSession(); err == nil {
		t.Fatal("cross-origin session reuse accepted")
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

func TestRecursiveCTLResolvesAbsolutePath(t *testing.T) {
	exe := "/opt/ternal with spaces/ternalctl"
	in := "ProxyCommand=ternalctl proxy host-1 %h:%p"
	out := rewriteCTLPath(in, exe)
	if out != "ProxyCommand='/opt/ternal with spaces/ternalctl' proxy host-1 %h:%p" {
		t.Fatalf("proxy rewrite = %q", out)
	}
	plain := "/usr/local/bin/ternalctl"
	in2 := "KnownHostsCommand=ternalctl known-host-key SHA256:x %I %f %t %K"
	if out := rewriteCTLPath(in2, plain); out != "KnownHostsCommand=/usr/local/bin/ternalctl known-host-key SHA256:x %I %f %t %K" {
		t.Fatalf("known-hosts rewrite = %q", out)
	}
	if out := rewriteCTLPath("StrictHostKeyChecking=yes", plain); out != "StrictHostKeyChecking=yes" {
		t.Fatalf("unrelated arg changed: %q", out)
	}
}
