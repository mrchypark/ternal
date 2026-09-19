package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/store"
)

func TestSSHConfigEntrySkipsInvalidHostNames(t *testing.T) {
	for _, name := range []string{"evil\n  ProxyCommand=evil", "has space", "wild*card", "-leading-dash", ""} {
		if _, ok := sshConfigEntry(name, strings.Repeat("a", 64), 22, "ops", nil); ok {
			t.Errorf("sshConfigEntry(%q) accepted config-injecting name", name)
		}
	}
	for _, user := range []string{"evil\n  ProxyCommand=evil", "has space", ""} {
		if _, ok := sshConfigEntry("edge-1", strings.Repeat("a", 64), 22, user, nil); ok {
			t.Errorf("sshConfigEntry(user %q) accepted config-injecting account", user)
		}
	}
	entry, ok := sshConfigEntry("edge-1", strings.Repeat("a", 64), 22, "ops", []string{"  StrictHostKeyChecking yes"})
	if !ok || !strings.HasPrefix(entry, "Host edge-1\n  HostName "+strings.Repeat("a", 64)+"\n") {
		t.Fatalf("sshConfigEntry good name = %q, %v", entry, ok)
	}
}

func TestSSHConfigEmitsSingleHostBlock(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(ctx, token.Token, strings.Repeat("b", 64), "edge-1", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEndpointDiscovery(ctx, device.HostID, nil, []string{"https://relay.example"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/access/ssh-config", nil)
	req.Header.Set("X-Ternal-User", "admin@example.com")
	req.Header.Set("X-Ternal-Groups", "ternal-admins")
	w := httptest.NewRecorder()
	NewServer(s).Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ssh-config status = %d body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Count(body, "Host edge-1") != 1 || strings.Count(body, `"Host `) != 1 {
		t.Fatalf("ssh-config emitted unexpected host blocks: %s", body)
	}
}
