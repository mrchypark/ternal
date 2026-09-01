package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthMiddlewareRejectsRevokedSession(t *testing.T) {
	key := strings.Repeat("k", 32)
	cookie, err := SignSession(SessionData{
		User: UserClaims{Subject: "user@example.com"}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	handler := AuthMiddlewareWithRevocation(key, false, "ternal-admins", func(context.Context, string) (bool, error) {
		return true, nil
	})(RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("revoked session reached protected handler")
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d", res.Code)
	}
}

func TestAuthMiddlewareDoesNotCheckRevocationForInvalidSession(t *testing.T) {
	handler := AuthMiddlewareWithRevocation(strings.Repeat("k", 32), false, "ternal-admins", func(context.Context, string) (bool, error) {
		t.Fatal("invalid session reached revocation store")
		return false, nil
	})(RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid session reached protected handler")
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "invalid"})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d", res.Code)
	}
}

func TestAuthMiddlewareFailsClosedWhenRevocationCheckFails(t *testing.T) {
	key := strings.Repeat("k", 32)
	cookie, err := SignSession(SessionData{
		User: UserClaims{Subject: "user@example.com"}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	handler := AuthMiddlewareWithRevocation(key, false, "ternal-admins", func(context.Context, string) (bool, error) {
		return false, context.DeadlineExceeded
	})(RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unverified revocation reached protected handler")
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("revocation failure status = %d", res.Code)
	}
}

func TestCSRFBrowserOriginMustMatchRequestHost(t *testing.T) {
	handler := AuthMiddleware(strings.Repeat("k", 32), true)(RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	request := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "https://ternal.example/resource", nil)
		req.Header.Set("X-Ternal-User", "admin")
		req.Header.Set(CSRFHeader, "dev-csrf")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res.Code
	}

	if got := request("https://ternal.example"); got != http.StatusNoContent {
		t.Fatalf("same origin status = %d", got)
	}
	if got := request("https://attacker.example"); got != http.StatusForbidden {
		t.Fatalf("cross origin status = %d", got)
	}
	if got := request(""); got != http.StatusNoContent {
		t.Fatalf("non-browser client status = %d", got)
	}
}

func TestAudienceAndAuthorizedPartyAreBoundToClient(t *testing.T) {
	claims := func(aud, azp string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"aud": json.RawMessage(aud), "azp": json.RawMessage(azp)}
	}
	if err := validateAudienceAndAuthorizedParty(claims(`"ternal"`, `"ternal"`), "ternal"); err != nil {
		t.Fatal(err)
	}
	if err := validateAudienceAndAuthorizedParty(claims(`["ternal","other"]`, `"ternal"`), "ternal"); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]map[string]json.RawMessage{
		"wrong audience": claims(`"legacy"`, `"legacy"`),
		"wrong azp":      claims(`["ternal","other"]`, `"legacy"`),
		"missing azp":    {"aud": json.RawMessage(`["ternal","other"]`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAudienceAndAuthorizedParty(raw, "ternal"); err == nil {
				t.Fatal("claim accepted")
			}
		})
	}
}

func TestSessionSigningRequiresStrongKeyAndRejectsTampering(t *testing.T) {
	data := SessionData{User: UserClaims{Subject: "user-1"}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if _, err := SignSession(data, "short"); err == nil {
		t.Fatal("short session key accepted")
	}
	key := strings.Repeat("k", 32)
	signed, err := SignSession(data, key)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifySession(signed, key)
	if err != nil || verified.User.Subject != data.User.Subject {
		t.Fatalf("valid session rejected: %#v, %v", verified, err)
	}
	if _, err := VerifySession(signed+"x", key); err == nil {
		t.Fatal("tampered session accepted")
	}
}

func TestOIDCConfigRejectsOldOriginEndpointsAndInsecureRemoteIssuer(t *testing.T) {
	config := OIDCConfig{
		Issuer: "https://auth.ternal.example.invalid/auth/v1/", ClientID: "ternal",
		ClientSecret: "secret", RedirectURL: "https://ternal.example.invalid/auth/callback",
		AdminGroup: "ternal-admins", GroupsClaim: "groups",
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.validateProviderEndpoint("https://legacy-auth.example.invalid/auth/v1/token"); err == nil {
		t.Fatal("legacy provider endpoint accepted")
	}
	config.Issuer = "http://rauthy.example/auth/v1/"
	if err := config.Validate(); err == nil {
		t.Fatal("remote cleartext issuer accepted")
	}
}

func TestStartDeviceUsesConfidentialClientPostAuthentication(t *testing.T) {
	const clientSecret = "confidential-device-client-secret"
	var received url.Values
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
				"device_authorization_endpoint": server.URL + "/device",
			})
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			received = r.PostForm
			writeTestJSON(t, w, map[string]any{
				"device_code": "device-code", "user_code": "ABCD-EFGH",
				"verification_uri": server.URL + "/verify", "expires_in": 300, "interval": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewOIDCClient(OIDCConfig{
		Issuer: server.URL, ClientID: "ternal", ClientSecret: clientSecret,
		RedirectURL: server.URL + "/callback", AdminGroup: "admins", GroupsClaim: "groups",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.StartDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if response.DeviceCode != "device-code" || received.Get("client_id") != "ternal" || received.Get("scope") != oidcScope {
		t.Fatalf("unexpected device response or public request fields")
	}
	if received.Get("client_secret") != clientSecret {
		t.Fatal("confidential client authentication was omitted or changed")
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
