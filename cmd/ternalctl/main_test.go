package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
