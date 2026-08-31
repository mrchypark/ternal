package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	if _, err := s.CreateRelayAccessGrant(context.Background(), "host-1", endpointID, "user-1", 300); err != nil {
		t.Fatal(err)
	}
	checkRelay("active grant", relaySecret, strings.ToUpper(endpointID), http.StatusOK, "true")
}
