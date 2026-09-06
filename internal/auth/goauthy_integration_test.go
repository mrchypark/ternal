package auth

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

var goauthyInteraction = regexp.MustCompile(`name="interaction" value="([A-Za-z0-9_-]{43})"`)

func TestGoAuthyAuthorizationCodeConsumer(t *testing.T) {
	if os.Getenv("TERNAL_GOAUTHY_E2E") != "1" {
		t.Skip("set TERNAL_GOAUTHY_E2E=1 to run the live GoAuthy consumer test")
	}
	issuer := strings.TrimSuffix(os.Getenv("TERNAL_GOAUTHY_E2E_URL"), "/")
	username := os.Getenv("TERNAL_GOAUTHY_E2E_USERNAME")
	password := os.Getenv("TERNAL_GOAUTHY_E2E_PASSWORD")
	secret := os.Getenv("TERNAL_GOAUTHY_E2E_CLIENT_SECRET")
	redirect := os.Getenv("TERNAL_GOAUTHY_E2E_REDIRECT_URL")
	if issuer == "" || username == "" || password == "" || secret == "" || redirect == "" {
		t.Fatal("live GoAuthy consumer test configuration is incomplete")
	}

	client, err := NewOIDCClient(OIDCConfig{
		Issuer: issuer, ClientID: "ternal", ClientSecret: secret, RedirectURL: redirect,
		AdminGroup: "ternal-admins", GroupsClaim: "groups",
	})
	if err != nil {
		t.Fatal(err)
	}
	const signingKey = "goauthy-consumer-e2e-signing-key"
	authorizationURL, signedState, err := client.BeginLogin(t.Context(), signingKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("code_challenge") == "" || parsed.Query().Get("code_verifier") != "" {
		t.Fatal("Ternal authorization request does not expose the required S256 boundary")
	}

	for _, invalid := range []struct {
		name   string
		mutate func(url.Values)
	}{
		{name: "missing PKCE", mutate: func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") }},
		{name: "plain PKCE", mutate: func(q url.Values) { q.Set("code_challenge_method", "plain") }},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			bad := *parsed
			query := bad.Query()
			invalid.mutate(query)
			bad.RawQuery = query.Encode()
			response := goauthyRequest(t, nil, http.MethodGet, bad.String(), nil)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || response.Header.Get("Location") != "" {
				t.Fatalf("invalid PKCE status=%d redirect=%t", response.StatusCode, response.Header.Get("Location") != "")
			}
		})
	}

	code, state := goauthyLogin(t, authorizationURL, issuer, username, password)
	claims, err := client.CompleteLogin(t.Context(), code, state, signedState, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != issuer || claims.Subject == "" || claims.PrincipalID() == claims.Subject || !containsString(claims.Groups, "ternal-admins") {
		t.Fatal("GoAuthy claims did not satisfy Ternal issuer, subject, principal, and groups binding")
	}
	if _, err := client.CompleteLogin(t.Context(), code, state, signedState, signingKey); err == nil {
		t.Fatal("GoAuthy authorization code replay was accepted")
	}

	wrongVerifierURL, wrongVerifierState, err := client.BeginLogin(t.Context(), signingKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongVerifierCode, wrongVerifierCallbackState := goauthyLogin(t, wrongVerifierURL, issuer, username, password)
	var saved loginState
	if err := verifyValue(wrongVerifierState, signingKey, &saved); err != nil {
		t.Fatal(err)
	}
	saved.CodeVerifier = strings.Repeat("x", 43)
	wrongVerifierState, err = signValue(saved, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := client.CompleteLogin(t.Context(), wrongVerifierCode, wrongVerifierCallbackState, wrongVerifierState, signingKey); err == nil || claims != nil {
		t.Fatal("GoAuthy accepted an authorization code with the wrong PKCE verifier")
	}
}

func goauthyLogin(t *testing.T, authorizationURL, issuer, username, password string) (string, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response := goauthyRequest(t, httpClient, http.MethodGet, authorizationURL, nil)
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GoAuthy authorize status=%d", response.StatusCode)
	}
	match := goauthyInteraction.FindSubmatch(body)
	if len(match) != 2 {
		t.Fatal("GoAuthy login form omitted the interaction token")
	}
	form := url.Values{"interaction": {string(match[1])}, "username": {username}, "password": {password}}
	response = goauthyRequest(t, httpClient, http.MethodPost, issuer+"/auth/login", strings.NewReader(form.Encode()))
	response.Body.Close()
	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusSeeOther {
		t.Fatalf("GoAuthy login status=%d", response.StatusCode)
	}
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil || callback.Query().Get("code") == "" || callback.Query().Get("state") == "" || callback.Query().Get("error") != "" {
		t.Fatal("GoAuthy login did not return a valid authorization callback")
	}
	return callback.Query().Get("code"), callback.Query().Get("state")
}

func goauthyRequest(t *testing.T, client *http.Client, method, target string, body io.Reader) *http.Response {
	t.Helper()
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	request, err := http.NewRequestWithContext(t.Context(), method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
