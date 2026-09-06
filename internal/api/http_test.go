package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/store"
)

func TestInternalErrorsDoNotExposeDetails(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, "database path and query details")
	if body := w.Body.String(); body != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestWriteJSONEncodesNilSlicesAsEmptyArrays(t *testing.T) {
	w := httptest.NewRecorder()
	var items []string
	writeJSON(w, http.StatusOK, items)
	if body := w.Body.String(); body != "[]\n" {
		t.Fatalf("nil slice body = %q, want empty JSON array", body)
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

func TestOIDCConfigReportsDevelopmentHeaderMode(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	w := httptest.NewRecorder()
	NewServer(nil).handleOIDCConfig(w, httptest.NewRequest(http.MethodGet, "/auth/oidc-config", nil))
	if got := w.Body.String(); !strings.Contains(got, `"dev_headers":true`) {
		t.Fatalf("OIDC config did not report development header mode: %s", got)
	}
}

func TestRouterDoesNotLogOIDCCallbackSecrets(t *testing.T) {
	var logs bytes.Buffer
	original := middleware.DefaultLogger
	middleware.DefaultLogger = middleware.RequestLogger(&middleware.DefaultLogFormatter{
		Logger:  log.New(&logs, "", 0),
		NoColor: true,
	})
	t.Cleanup(func() { middleware.DefaultLogger = original })

	w := httptest.NewRecorder()
	NewServer(nil).Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/callback?code=do-not-log&state=do-not-log", nil))
	if strings.Contains(logs.String(), "do-not-log") {
		t.Fatalf("OIDC callback secret appeared in request logs: %q", logs.String())
	}
}

func TestPublicRouterDoesNotExposeRelayCallback(t *testing.T) {
	w := httptest.NewRecorder()
	NewServer(nil).Router().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/iroh-relay/access", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("public relay callback status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHostListDoesNotExposeTransportEndpoint(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	endpoint := strings.Repeat("a", 64)
	if _, err := s.CreateHost(context.Background(), store.NewHost{Name: "private-route", EndpointID: endpoint, SSHUser: "ops", SSHPort: 22}, "system"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/hosts/", nil)
	req.Header.Set("X-Ternal-User", "admin@example.com")
	req.Header.Set("X-Ternal-Groups", "ternal-admins")
	w := httptest.NewRecorder()
	NewServer(s).Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), endpoint) || strings.Contains(w.Body.String(), "endpoint_id") {
		t.Fatalf("host list exposed transport endpoint: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthenticatedPolicyDenialsAreAuditedWithoutChangingResponse(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(context.Background(), "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(context.Background(), token.Token, strings.Repeat("d", 64), "DENIED-TEST", "test", "SHA256:"+strings.Repeat("A", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := NewServer(s).Router()
	for _, request := range []struct {
		path, body string
	}{
		{"/access/ssh", `{"host_id":"` + device.HostID + `","ssh_user":"ops"}`},
		{"/access/relay-grants", `{"host_id":"` + device.HostID + `","client_endpoint_id":"` + strings.Repeat("e", 64) + `"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(request.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ternal-User", "operator@example.com")
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"host not found\"}\n" {
			t.Fatalf("%s response = %d %q", request.path, w.Code, w.Body.String())
		}
	}
	events, err := s.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"access.ssh.denied", "relay.grant.denied"} {
		found := false
		for _, event := range events {
			found = found || (event.Action == action && event.ResourceID == device.HostID && event.UserID == "operator@example.com" && string(event.After) == `{"reason":"policy_denied"}`)
		}
		if !found {
			t.Fatalf("missing %s audit event: %#v", action, events)
		}
	}
}

// Exercises a real Store actor-validation error, not an end-to-end disk outage.
func TestPolicyDenialActorValidationFailureFailsClosed(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(context.Background(), "", &expires, "system")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device, err := s.EnrollDevice(context.Background(), token.Token, strings.Repeat("f", 64), "AUDIT-FAIL", "test", "SHA256:"+strings.Repeat("B", 43), base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(s)
	protected := func(next http.Handler) http.Handler {
		return auth.AuthMiddleware(strings.Repeat("s", 32), true, "ternal-admins")(auth.RequireAuth(auth.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := auth.GetAuth(r)
			identity.User.Subject, identity.User.Issuer = "", ""
			next.ServeHTTP(w, r)
		}))))
	}
	endpointID := strings.Repeat("a", 64)
	for _, request := range []struct {
		path, body string
		handler    http.HandlerFunc
	}{
		{"/access/ssh", `{"host_id":"` + device.HostID + `","ssh_user":"ops"}`, server.handleIssueSSHCommand},
		{"/access/relay-grants", `{"host_id":"` + device.HostID + `","client_endpoint_id":"` + endpointID + `"}`, server.handleIssueRelayGrant},
	} {
		req := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(request.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ternal-User", "operator@example.com")
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		w := httptest.NewRecorder()
		protected(request.handler).ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"internal server error\"}\n" {
			t.Fatalf("%s response = %d %q", request.path, w.Code, w.Body.String())
		}
	}
	grants, err := s.ListAccessGrants(context.Background())
	if err != nil || len(grants) != 0 {
		t.Fatalf("SSH grants after audit failure = %#v, err=%v", grants, err)
	}
	allowed, err := s.RelayEndpointAllowed(context.Background(), endpointID)
	if err != nil || allowed {
		t.Fatalf("relay access after audit failure = %v, err=%v", allowed, err)
	}
}

func TestLogoutRejectsReplayedSession(t *testing.T) {
	key := strings.Repeat("s", 32)
	t.Setenv("TERNAL_SESSION_KEY", key)
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cookie, err := auth.SignSession(auth.SessionData{
		User: auth.UserClaims{Issuer: auth.OIDCConfigFromEnv().Issuer, Subject: "user@example.com"}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	router := NewServer(s).Router()
	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logout.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
	logout.Header.Set(auth.CSRFHeader, "csrf")
	logoutResponse := httptest.NewRecorder()
	router.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	replay := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	replay.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
	replayResponse := httptest.NewRecorder()
	router.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || replayResponse.Body.String() != "{\"authenticated\":false}\n" {
		t.Fatalf("replayed session status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
}
