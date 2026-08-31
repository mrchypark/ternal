package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestRauthyConfigRejectsOldOriginEndpointsAndInsecureRemoteIssuer(t *testing.T) {
	config := RauthyConfig{
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
