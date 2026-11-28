package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/store"
)

func TestPortalNavigationBoundaries(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	handler := auth.AuthMiddleware(strings.Repeat("s", 32), true)(http.HandlerFunc(New(s).Index))
	for _, tc := range []struct {
		name, view                   string
		authenticated, htmx, history bool
		status                       int
	}{
		{"expired fragment", "hosts", false, true, false, 200},
		{"history restore", "keys", true, true, true, 200},
		{"unknown page", "missing", true, false, false, 404},
		{"unknown fragment", "missing", true, true, false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?view="+tc.view, nil)
			if tc.authenticated {
				req.Header.Set("X-Ternal-User", "user")
			}
			if tc.htmx {
				req.Header.Set("HX-Request", "true")
			}
			if tc.history {
				req.Header.Set("HX-History-Restore-Request", "true")
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.status {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
			if !tc.authenticated && (res.Header().Get("HX-Redirect") != "/" || res.Body.Len() != 0) {
				t.Fatal("expired session must navigate out of the authenticated shell")
			}
			if tc.history && (!strings.Contains(res.Body.String(), "<!doctype html>") || !strings.Contains(res.Body.String(), `class="portal-shell"`)) {
				t.Fatal("history restore requires a complete document")
			}
		})
	}
}
