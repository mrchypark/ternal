package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/store"

	"golang.org/x/crypto/ssh"
)

func TestPolicyDeletionRefusesNewSSHGrants(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	router := NewServer(s).Router()
	admin := map[string]string{"X-Ternal-User": "admin@example.com", "X-Ternal-Groups": "ternal-admins"}
	user := map[string]string{"X-Ternal-User": "user@example.com", "X-Ternal-Groups": "operators"}
	call := func(method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("c", 64), "edge-2", "test", "SHA256:"+strings.Repeat("B", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEndpointDiscovery(ctx, device.HostID, nil, []string{"https://relay.example"}); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		ID string `json:"id"`
	}
	w := call(http.MethodPost, "/policies/", admin, map[string]any{"name": "ops-access", "principal": "operators", "host_selector": "*", "ssh_users": []string{"ops"}})
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &policy) != nil || policy.ID == "" {
		t.Fatalf("create policy status=%d body=%q", w.Code, w.Body.String())
	}
	userKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(userKey)
	if err != nil {
		t.Fatal(err)
	}
	userPub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if w := call(http.MethodPost, "/ssh-keys/", user, map[string]any{"public_key": userPub}); w.Code != http.StatusCreated {
		t.Fatalf("create ssh key status=%d body=%q", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/access/ssh", user, map[string]any{"host_id": device.HostID, "ssh_user": "ops"}); w.Code != http.StatusOK {
		t.Fatalf("issue ssh status=%d body=%q", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, "/policies/"+policy.ID, admin, nil); w.Code != http.StatusOK {
		t.Fatalf("delete policy status=%d body=%q", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/access/ssh", user, map[string]any{"host_id": device.HostID, "ssh_user": "ops"}); w.Code != http.StatusNotFound {
		t.Fatalf("issue ssh after policy deletion status=%d body=%q, want 404", w.Code, w.Body.String())
	}
}

func TestRelayGrantAuthorizesRequestedAccount(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	router := NewServer(s).Router()
	admin := map[string]string{"X-Ternal-User": "admin@example.com", "X-Ternal-Groups": "ternal-admins"}
	user := map[string]string{"X-Ternal-User": "user@example.com", "X-Ternal-Groups": "operators"}
	call := func(method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("d", 64), "RELAY-1", "test", "SHA256:"+strings.Repeat("C", 43), base64.StdEncoding.EncodeToString(public), "root", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPost, "/policies/", admin, map[string]any{"name": "ops-only", "principal": "operators", "host_selector": "*", "ssh_users": []string{"ops"}}); w.Code != http.StatusCreated {
		t.Fatalf("create policy status=%d body=%q", w.Code, w.Body.String())
	}
	grant := map[string]any{"host_id": device.HostID, "client_endpoint_id": strings.Repeat("e", 64), "ttl": 300, "ssh_user": "ops"}
	if w := call(http.MethodPost, "/access/relay-grants", user, grant); w.Code != http.StatusCreated {
		t.Fatalf("relay grant for allowed account status=%d body=%q, want 201", w.Code, w.Body.String())
	}
	noAccount := map[string]any{"host_id": device.HostID, "client_endpoint_id": strings.Repeat("e", 64), "ttl": 300}
	if w := call(http.MethodPost, "/access/relay-grants", user, noAccount); w.Code != http.StatusNotFound {
		t.Fatalf("relay grant for default (unallowed) account status=%d body=%q, want 404", w.Code, w.Body.String())
	}
}
