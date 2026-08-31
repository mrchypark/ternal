package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrchypark/ternal/internal/store"
)

func TestInternalErrorsDoNotExposeDetails(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, "database path and query details")
	if body := w.Body.String(); body != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestMetricsArePrometheusText(t *testing.T) {
	w := httptest.NewRecorder()
	NewServer(nil).handleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("unexpected content type: %q", got)
	}
	if got := w.Body.String(); !strings.Contains(got, "ternal_up 1\n") {
		t.Fatalf("unexpected metrics: %q", got)
	}
}

func TestReadyRequiresStoreRead(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	w := httptest.NewRecorder()
	NewServer(s).handleReady(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusOK || w.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatalf("unexpected readiness response: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestRouterSetsBrowserSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	NewServer(nil).Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	for name, expected := range map[string]string{
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "script-src 'self'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := w.Header().Get(name); !strings.Contains(got, expected) {
			t.Errorf("%s = %q, want substring %q", name, got, expected)
		}
	}
}
