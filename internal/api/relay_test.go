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

func TestRelayAdmissionRequiresBearerAndActiveGrant(t *testing.T) {
	relaySecret := "relay-secret-for-test-minimum-32-bytes"
	t.Setenv("TERNAL_RELAY_ACCESS_TOKEN", relaySecret)
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	handler := NewServer(s).Router()
	endpointID := strings.Repeat("a", 64)
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(context.Background(), "", &expires)
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(context.Background(), token.Token, strings.Repeat("b", 64), "TEST-RELAY", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}

	checkRelay := func(name, token, endpoint string, wantStatus int, wantBody string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/iroh-relay/access", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			req.Header.Set("X-Iroh-Nodeid", endpoint)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != wantStatus || res.Body.String() != wantBody {
				t.Fatalf("status/body = %d %q, want %d %q", res.Code, res.Body.String(), wantStatus, wantBody)
			}
		})
	}

	checkRelay("missing bearer", "", endpointID, http.StatusUnauthorized, "false")
	checkRelay("wrong bearer", "wrong", endpointID, http.StatusUnauthorized, "false")
	checkRelay("malformed endpoint", relaySecret, strings.Repeat("z", 64), http.StatusBadRequest, "false")
	checkRelay("ungranted endpoint", relaySecret, endpointID, http.StatusForbidden, "false")

	if _, err := s.CreateRelayAccessGrant(context.Background(), device.HostID, endpointID, "user-1", 300); err != nil {
		t.Fatal(err)
	}
	checkRelay("active grant", relaySecret, strings.ToUpper(endpointID), http.StatusOK, "true")
}
