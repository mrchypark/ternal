package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const oidcScope = "openid groups"

type OIDCClient struct {
	config OIDCConfig
	client *http.Client
}

type ProviderError struct {
	Code string
}

func (e *ProviderError) Error() string { return e.Code }

type loginState struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expires_at"`
}

type providerMetadata struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

func NewOIDCClient(config OIDCConfig) (*OIDCClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &OIDCClient{
		config: config,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("OIDC redirects are not followed")
		}},
	}, nil
}

func (c *OIDCClient) authorizationURL(ctx context.Context, state, nonce string) (string, error) {
	ctx = c.requestContext(ctx)
	provider, metadata, err := c.provider(ctx)
	if err != nil {
		return "", err
	}
	config := c.oauthConfig(provider, metadata)
	return config.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce)), nil
}

func (c *OIDCClient) BeginLogin(ctx context.Context, signingKey string) (authorizationURL, signedState string, err error) {
	state, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	payload := loginState{State: state, Nonce: nonce, ExpiresAt: time.Now().Add(5 * time.Minute).Unix()}
	signedState, err = signValue(payload, signingKey)
	if err != nil {
		return "", "", err
	}
	authorizationURL, err = c.authorizationURL(ctx, state, nonce)
	return authorizationURL, signedState, err
}

func (c *OIDCClient) CompleteLogin(ctx context.Context, code, state, signedState, signingKey string) (*UserClaims, error) {
	ctx = c.requestContext(ctx)
	var saved loginState
	if err := verifyValue(signedState, signingKey, &saved); err != nil {
		return nil, fmt.Errorf("invalid OIDC state: %w", err)
	}
	if saved.ExpiresAt <= time.Now().Unix() || !constantEqual(saved.State, state) {
		return nil, errors.New("invalid OIDC state")
	}
	provider, metadata, err := c.provider(ctx)
	if err != nil {
		return nil, err
	}
	config := c.oauthConfig(provider, metadata)
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("OIDC code exchange failed: %w", err)
	}
	return c.verifyToken(ctx, provider, token, saved.Nonce)
}

func (c *OIDCClient) StartDevice(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	ctx = c.requestContext(ctx)
	provider, metadata, err := c.provider(ctx)
	if err != nil {
		return nil, err
	}
	if metadata.DeviceAuthorizationEndpoint == "" {
		return nil, errors.New("provider does not advertise device authorization")
	}
	config := c.oauthConfig(provider, metadata)
	// DeviceAuth omits client authentication unless it is supplied explicitly.
	return config.DeviceAuth(ctx, oauth2.SetAuthURLParam("client_secret", c.config.ClientSecret))
}

func (c *OIDCClient) PollDevice(ctx context.Context, deviceCode string) (*UserClaims, time.Time, error) {
	ctx = c.requestContext(ctx)
	if deviceCode == "" || len(deviceCode) > 2048 {
		return nil, time.Time{}, errors.New("invalid device code")
	}
	provider, metadata, err := c.provider(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	form := url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"device_code":   {deviceCode},
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	var payload struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, time.Time{}, errors.New("invalid token response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch payload.Error {
		case "authorization_pending", "slow_down", "access_denied", "expired_token":
			return nil, time.Time{}, &ProviderError{Code: payload.Error}
		default:
			return nil, time.Time{}, errors.New("device token request failed")
		}
	}
	token := (&oauth2.Token{AccessToken: payload.AccessToken, TokenType: payload.TokenType, RefreshToken: payload.RefreshToken, Expiry: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)}).WithExtra(map[string]any{"id_token": payload.IDToken})
	claims, err := c.verifyToken(ctx, provider, token, "")
	return claims, token.Expiry, err
}

func (c *OIDCClient) verifyToken(ctx context.Context, provider *oidc.Provider, token *oauth2.Token, expectedNonce string) (*UserClaims, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("provider omitted id_token")
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: c.config.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return nil, errors.New("invalid id_token")
	}
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return nil, errors.New("invalid id_token claims")
	}
	if err := validateAudienceAndAuthorizedParty(raw, c.config.ClientID); err != nil {
		return nil, err
	}
	if expectedNonce != "" {
		var nonce string
		_ = json.Unmarshal(raw["nonce"], &nonce)
		if !constantEqual(nonce, expectedNonce) {
			return nil, errors.New("invalid id_token nonce")
		}
	}
	var subject string
	if err := json.Unmarshal(raw["sub"], &subject); err != nil || subject == "" || len(subject) > 512 {
		return nil, errors.New("invalid id_token subject")
	}
	var groups []string
	if claim, ok := raw[c.config.GroupsClaim]; ok {
		if err := json.Unmarshal(claim, &groups); err != nil || len(groups) > 128 {
			return nil, errors.New("invalid groups claim")
		}
	}
	for _, group := range groups {
		if group == "" || len(group) > 256 {
			return nil, errors.New("invalid groups claim")
		}
	}
	return &UserClaims{Subject: subject, Groups: groups}, nil
}

func validateAudienceAndAuthorizedParty(raw map[string]json.RawMessage, clientID string) error {
	var audiences []string
	if claim := raw["aud"]; len(claim) != 0 {
		var one string
		if json.Unmarshal(claim, &one) == nil {
			audiences = []string{one}
		} else if json.Unmarshal(claim, &audiences) != nil {
			return errors.New("invalid id_token audience")
		}
	}
	found := false
	for _, audience := range audiences {
		if constantEqual(audience, clientID) {
			found = true
		}
	}
	if !found {
		return errors.New("invalid id_token audience")
	}
	var authorizedParty string
	if claim := raw["azp"]; len(claim) != 0 && json.Unmarshal(claim, &authorizedParty) != nil {
		return errors.New("invalid id_token authorized party")
	}
	if (len(audiences) > 1 || authorizedParty != "") && !constantEqual(authorizedParty, clientID) {
		return errors.New("invalid id_token authorized party")
	}
	return nil
}

func (c *OIDCClient) provider(ctx context.Context) (*oidc.Provider, providerMetadata, error) {
	provider, err := oidc.NewProvider(ctx, c.config.Issuer)
	if err != nil {
		return nil, providerMetadata{}, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	var metadata providerMetadata
	if err := provider.Claims(&metadata); err != nil {
		return nil, metadata, errors.New("invalid OIDC discovery metadata")
	}
	for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.DeviceAuthorizationEndpoint} {
		if endpoint != "" {
			if err := c.config.validateProviderEndpoint(endpoint); err != nil {
				return nil, metadata, err
			}
		}
	}
	return provider, metadata, nil
}

func (c *OIDCClient) requestContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.client)
	return oidc.ClientContext(ctx, c.client)
}

func (c *OIDCClient) oauthConfig(provider *oidc.Provider, metadata providerMetadata) oauth2.Config {
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	endpoint.DeviceAuthURL = metadata.DeviceAuthorizationEndpoint
	return oauth2.Config{
		ClientID:     c.config.ClientID,
		ClientSecret: c.config.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  c.config.RedirectURL,
		Scopes:       strings.Fields(oidcScope),
	}
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func NewRandomToken(size int) (string, error) {
	if size < 16 || size > 128 {
		return "", errors.New("invalid random token size")
	}
	return randomToken(size)
}
