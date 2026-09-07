package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/core"
)

func TestAuthMiddlewareRejectsRevokedSession(t *testing.T) {
	key := strings.Repeat("k", 32)
	cookie, err := SignSession(SessionData{
		User: UserClaims{Subject: "user@example.com"}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	handler := AuthMiddlewareWithRevocation(key, false, "ternal-admins", "", func(context.Context, string) (bool, error) {
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
	handler := AuthMiddlewareWithRevocation(strings.Repeat("k", 32), false, "ternal-admins", "", func(context.Context, string) (bool, error) {
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
	handler := AuthMiddlewareWithRevocation(key, false, "ternal-admins", "", func(context.Context, string) (bool, error) {
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

func TestAuthMiddlewareRechecksExpiryAfterRevocationLookup(t *testing.T) {
	key := strings.Repeat("k", 32)
	clock := time.Unix(100, 0)
	expiresAt := clock.Unix() + 1
	cookie, err := SignSession(SessionData{
		User: UserClaims{Subject: "user@example.com"}, CSRFToken: "csrf", ExpiresAt: expiresAt,
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	handler := authMiddlewareWithClock(key, false, "ternal-admins", "", func(context.Context, string) (bool, error) {
		checks++
		clock = time.Unix(expiresAt, 0)
		return false, nil
	}, func() time.Time { return clock })(RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("session expired during revocation lookup reached protected handler")
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d", res.Code)
	}
	if checks != 1 {
		t.Fatalf("revocation checks = %d, want 1", checks)
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

func TestSessionSigningRejectsCookieOverflow(t *testing.T) {
	key := strings.Repeat("k", 32)
	groups := make([]string, 10)
	for i := range groups {
		groups[i] = strings.Repeat(string(rune('a'+i)), maxPolicyClaimValueBytes)
	}
	data := SessionData{User: UserClaims{Subject: "user-1", Groups: groups, CustomClaims: map[string][]string{"department": {strings.Repeat("x", maxPolicyClaimValueBytes), strings.Repeat("y", maxPolicyClaimValueBytes), strings.Repeat("z", maxPolicyClaimValueBytes)}}}, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if signed, err := SignSession(data, key); err == nil || signed != "" {
		t.Fatal("oversized session cookie accepted")
	}
}

func TestOIDCPrincipalAndSessionAreBoundToIssuer(t *testing.T) {
	first := UserClaims{Issuer: "https://id.example.test", Subject: "user-1"}
	second := UserClaims{Issuer: "https://other.example.test", Subject: "user-1"}
	principalID := first.PrincipalID()
	if principalID == second.PrincipalID() || principalID != first.PrincipalID() {
		t.Fatal("OIDC principal ID is not stable and issuer-bound")
	}

	key := strings.Repeat("k", 32)
	cookie, err := SignSession(SessionData{User: first, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Minute).Unix()}, key)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := AuthMiddlewareWithRevocation(key, false, "admins", second.Issuer, nil)(RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if called {
		t.Fatal("session issued by a different OIDC issuer was accepted")
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

func TestOIDCConfigValidatesPolicyClaims(t *testing.T) {
	base := OIDCConfig{Issuer: "https://auth.ternal.example.invalid/auth/v1/", ClientID: "ternal", ClientSecret: "secret", RedirectURL: "https://ternal.example.invalid/auth/callback", AdminGroup: "ternal-admins", GroupsClaim: "groups"}
	if err := (OIDCConfig{Issuer: base.Issuer, ClientID: base.ClientID, ClientSecret: base.ClientSecret, RedirectURL: base.RedirectURL, AdminGroup: base.AdminGroup, GroupsClaim: base.GroupsClaim, PolicyClaims: []string{"department", "role"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, maxPolicyClaims+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("claim-%d", i)
	}
	for name, claims := range map[string][]string{
		"empty": {""}, "duplicate": {"role", "role"}, "groups claim": {"groups"},
		"literal groups with alternate groups claim": {"groups"}, "configured alternate groups claim": {"roles"},
		"reserved protocol claim": {"sub"}, "control": {"role\x00"}, "too many": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			config.PolicyClaims = claims
			if name == "configured alternate groups claim" {
				config.GroupsClaim = "roles"
				config.PolicyClaims = []string{"roles"}
			}
			if name == "literal groups with alternate groups claim" {
				config.GroupsClaim = "roles"
			}
			if err := config.Validate(); err == nil {
				t.Fatal("invalid policy claims accepted")
			}
		})
	}

	t.Setenv("TERNAL_OIDC_POLICY_CLAIMS", " department, role ")
	if got := OIDCConfigFromEnv().PolicyClaims; strings.Join(got, ",") != "department,role" {
		t.Fatalf("policy claims from environment = %#v", got)
	}
	t.Setenv("TERNAL_OIDC_POLICY_CLAIMS", " ")
	if err := OIDCConfigFromEnv().Validate(); err == nil {
		t.Fatal("whitespace-only policy claims environment accepted")
	}
}

func TestOIDCPolicyClaimsRejectMalformedOrOversizedValues(t *testing.T) {
	client := &OIDCClient{config: OIDCConfig{PolicyClaims: []string{"role"}}}
	for name, raw := range map[string]json.RawMessage{
		"null": json.RawMessage(`null`), "object": json.RawMessage(`{"name":"support"}`),
		"empty string": json.RawMessage(`""`), "whitespace string": json.RawMessage(`" \t "`), "padded string": json.RawMessage(`" support "`), "control": json.RawMessage(`"support\u0000"`),
		"empty array": json.RawMessage(`[]`), "array value not string": json.RawMessage(`["support",1]`),
		"too many values": json.RawMessage(`["1","2","3","4","5","6","7","8","9","10","11","12","13","14","15","16","17"]`),
		"too long value":  json.RawMessage(`"` + strings.Repeat("x", maxPolicyClaimValueBytes+1) + `"`),
	} {
		t.Run(name, func(t *testing.T) {
			if claims, err := client.extractPolicyClaims(map[string]json.RawMessage{"role": raw}); err == nil || claims != nil {
				t.Fatal("malformed policy claim accepted")
			}
		})
	}
	names := []string{strings.Repeat(`a"`, maxPolicyClaimNameBytes/2), strings.Repeat(`b"`, maxPolicyClaimNameBytes/2)}
	client.config.PolicyClaims = names
	raw := make(map[string]json.RawMessage, len(names))
	escapedValue, err := json.Marshal(strings.Repeat(`"`, maxPolicyClaimValueBytes))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		raw[name] = escapedValue
	}
	if claims, err := client.extractPolicyClaims(raw); err == nil || claims != nil {
		t.Fatal("cookie-sized policy claims accepted")
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

func TestOIDCLoginUsesBoundS256PKCE(t *testing.T) {
	const signingKey = "0123456789abcdef0123456789abcdef"
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	var expectedVerifier string
	var expectedNonce string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"EdDSA"},
			})
		case "/jwks":
			writeTestJSON(t, w, map[string]any{"keys": []map[string]any{{
				"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": "test",
				"x": base64.RawURLEncoding.EncodeToString(publicKey),
			}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("client_secret") != "confidential-secret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			if r.PostForm.Get("code_verifier") != expectedVerifier {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			audience := "ternal"
			if r.PostForm.Get("code") == "wrong-audience" {
				audience = "legacy"
			}
			writeTestJSON(t, w, map[string]any{
				"access_token": "access", "token_type": "Bearer",
				"id_token": signedTestIDToken(t, privateKey, map[string]any{
					"iss": server.URL, "sub": "user-1", "aud": audience, "exp": time.Now().Add(time.Minute).Unix(),
					"iat": time.Now().Unix(), "nonce": expectedNonce, "groups": []string{"operators"}, "department": []string{"support"}, "unlisted": "no",
				}),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewOIDCClient(OIDCConfig{
		Issuer: server.URL, ClientID: "ternal", ClientSecret: "confidential-secret",
		RedirectURL: server.URL + "/callback", AdminGroup: "admins", GroupsClaim: "groups", PolicyClaims: []string{"department"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, signedState, err := client.BeginLogin(t.Context(), signingKey)
	if err != nil {
		t.Fatal(err)
	}
	var saved loginState
	if err := verifyValue(signedState, signingKey, &saved); err != nil {
		t.Fatal(err)
	}
	expectedVerifier = saved.CodeVerifier
	expectedNonce = saved.Nonce
	query, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if query.Query().Get("state") != saved.State || query.Query().Get("nonce") != saved.Nonce || query.Query().Get("code_challenge_method") != "S256" || query.Query().Get("code_challenge") != pkceChallenge(saved.CodeVerifier) {
		t.Fatal("authorization request did not bind state, nonce, and S256 PKCE")
	}
	if query.Query().Get("code_verifier") != "" {
		t.Fatal("authorization request exposed the PKCE verifier")
	}

	claims, err := client.CompleteLogin(t.Context(), "code", saved.State, signedState, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != server.URL || claims.Subject != "user-1" || strings.Join(claims.Groups, ",") != "operators" || strings.Join(claims.CustomClaims["department"], ",") != "support" || claims.CustomClaims["unlisted"] != nil {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	if !core.PolicyAllows(&core.UserClaims{Subject: claims.Subject, Groups: claims.Groups, CustomClaims: claims.CustomClaims}, &core.Host{Name: "host", Tags: map[string]string{"site": "ied"}}, &core.Policy{Principal: "department=support", HostSelector: "tag:site=ied"}) {
		t.Fatal("verified policy claim did not reach core policy evaluation")
	}

	wrongNonce := saved
	wrongNonce.Nonce = "wrong-nonce"
	wrongNonceState, err := signValue(wrongNonce, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := client.CompleteLogin(t.Context(), "nonce-check", saved.State, wrongNonceState, signingKey); err == nil || claims != nil {
		t.Fatal("signed token with mismatched nonce was accepted")
	}
	if claims, err := client.CompleteLogin(t.Context(), "wrong-audience", saved.State, signedState, signingKey); err == nil || claims != nil {
		t.Fatal("signed token with wrong audience was accepted")
	}

	saved.CodeVerifier = strings.Repeat("x", 43)
	mismatchedState, err := signValue(saved, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := client.CompleteLogin(t.Context(), "code", saved.State, mismatchedState, signingKey); err == nil || claims != nil {
		t.Fatal("mismatched PKCE verifier was accepted")
	}
}

func TestOIDCInvalidStateRejectedBeforeProviderRequest(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "provider boundary reached", http.StatusServiceUnavailable)
	}))
	t.Cleanup(provider.Close)
	client, err := NewOIDCClient(OIDCConfig{
		Issuer: provider.URL, ClientID: "ternal", ClientSecret: "test-client-secret",
		RedirectURL: provider.URL + "/callback", AdminGroup: "admins", GroupsClaim: "groups",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 32)
	valid := loginState{State: "expected-state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), ExpiresAt: time.Now().Add(time.Hour).Unix()}
	for _, name := range []string{"missing signature", "tampered signature", "wrong signing key", "wrong state", "expired", "short verifier", "long verifier"} {
		t.Run(name, func(t *testing.T) {
			saved, state, signingKey := valid, valid.State, key
			switch name {
			case "wrong signing key":
				signingKey = strings.Repeat("x", 32)
			case "wrong state":
				state = "unrelated-state"
			case "expired":
				saved.ExpiresAt = 1
			case "short verifier":
				saved.CodeVerifier = strings.Repeat("v", 42)
			case "long verifier":
				saved.CodeVerifier = strings.Repeat("v", 129)
			}
			signed, err := signValue(saved, signingKey)
			if err != nil {
				t.Fatal(err)
			}
			if name == "missing signature" {
				signed = ""
			} else if name == "tampered signature" {
				signed += "x"
			}
			claims, err := client.CompleteLogin(t.Context(), "code", state, signed, key)
			if err == nil || !strings.HasPrefix(err.Error(), "invalid OIDC state") || claims != nil || requests.Load() != 0 {
				t.Fatal("invalid state did not fail before the provider boundary")
			}
		})
	}
	// A valid signed state must reach the boundary, ruling out a broken fixture.
	signed, err := signValue(valid, key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CompleteLogin(t.Context(), "code", valid.State, signed, key)
	if err == nil || requests.Load() == 0 {
		t.Fatal("valid state did not reach the failing provider control")
	}
}

func TestOIDCDeviceTokenCarriesOnlyVerifiedPolicyClaims(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"EdDSA"},
			})
		case "/jwks":
			writeTestJSON(t, w, map[string]any{"keys": []map[string]any{{"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": "test", "x": base64.RawURLEncoding.EncodeToString(publicKey)}}})
		case "/token":
			if err := r.ParseForm(); err != nil || r.PostForm.Get("device_code") != "device-code" {
				http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
				return
			}
			writeTestJSON(t, w, map[string]any{
				"access_token": "access", "token_type": "Bearer", "expires_in": 60,
				"id_token": signedTestIDToken(t, privateKey, map[string]any{
					"iss": server.URL, "sub": "device-user", "aud": "ternal", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "groups": []string{"operators"}, "department": "support", "unlisted": "no",
				}),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewOIDCClient(OIDCConfig{Issuer: server.URL, ClientID: "ternal", ClientSecret: "secret", RedirectURL: server.URL + "/callback", AdminGroup: "admins", GroupsClaim: "groups", PolicyClaims: []string{"department"}})
	if err != nil {
		t.Fatal(err)
	}
	claims, expiry, err := client.PollDevice(t.Context(), "device-code")
	if err != nil || expiry.Before(time.Now()) {
		t.Fatalf("device token rejected: %v", err)
	}
	if strings.Join(claims.CustomClaims["department"], ",") != "support" || claims.CustomClaims["unlisted"] != nil {
		t.Fatalf("unexpected device policy claims: %#v", claims.CustomClaims)
	}
	if !core.PolicyAllows(&core.UserClaims{Subject: claims.Subject, Groups: claims.Groups, CustomClaims: claims.CustomClaims}, &core.Host{Name: "host", Tags: map[string]string{"site": "ied"}}, &core.Policy{Principal: "department=support", HostSelector: "tag:site=ied"}) {
		t.Fatal("verified device policy claim did not reach core policy evaluation")
	}
}

func signedTestIDToken(t *testing.T, privateKey ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "test", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(unsigned)))
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
