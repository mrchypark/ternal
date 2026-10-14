package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/store"
)

func TestPortalLogoutFormRedirectsAndRevokesSession(t *testing.T) {
	key := strings.Repeat("s", 32)
	t.Setenv("TERNAL_SESSION_KEY", key)
	t.Setenv("TERNAL_DEV_HEADERS", "0")
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cookie, err := auth.SignSession(auth.SessionData{User: auth.UserClaims{Issuer: auth.OIDCConfigFromEnv().Issuer, Subject: "portal-user"}, CSRFToken: "form-csrf", ExpiresAt: time.Now().Add(time.Hour).Unix()}, key)
	if err != nil {
		t.Fatal(err)
	}
	router := NewServer(s).Router()
	for _, token := range []string{"wrong", "form-csrf"} {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(url.Values{"_csrf": {token}}.Encode()))
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if token == "wrong" {
			if res.Code != http.StatusForbidden {
				t.Fatalf("invalid CSRF status=%d", res.Code)
			}
			continue
		}
		if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/" {
			t.Fatalf("logout: %d %s", res.Code, res.Body.String())
		}
		cleared := false
		for _, c := range res.Result().Cookies() {
			if c.Name == auth.SessionCookie && c.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Fatal("session cookie was not cleared")
		}
	}
	replay := httptest.NewRequest(http.MethodGet, "/", nil)
	replay.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
	res := httptest.NewRecorder()
	router.ServeHTTP(res, replay)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `class="portal-landing"`) || strings.Contains(res.Body.String(), `class="portal-account"`) {
		t.Fatal("revoked cookie retained portal access")
	}
}
