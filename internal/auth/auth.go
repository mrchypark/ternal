package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	SessionCookie   = "ternal_session"
	OIDCStateCookie = "ternal_oidc_state"
	CSRFHeader      = "X-CSRF-Token"
)

type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AdminGroup   string
	GroupsClaim  string
}

func (c OIDCConfig) Validate() error {
	issuer, err := url.Parse(c.Issuer)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("invalid OIDC issuer")
	}
	if issuer.Scheme != "https" && !(issuer.Scheme == "http" && isLoopbackHost(issuer.Hostname())) {
		return fmt.Errorf("OIDC issuer must use HTTPS outside loopback development")
	}
	if c.ClientID == "" || len(c.ClientID) > 256 || c.ClientSecret == "" || len(c.ClientSecret) > 4096 {
		return fmt.Errorf("OIDC client credentials are required")
	}
	redirect, err := url.Parse(c.RedirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" || redirect.RawQuery != "" || redirect.Fragment != "" {
		return fmt.Errorf("invalid OIDC redirect URL")
	}
	if redirect.Scheme != "https" && !(redirect.Scheme == "http" && isLoopbackHost(redirect.Hostname())) {
		return fmt.Errorf("OIDC redirect URL must use HTTPS outside loopback development")
	}
	if c.AdminGroup == "" || c.GroupsClaim == "" {
		return fmt.Errorf("OIDC group configuration is required")
	}
	return nil
}

func (c OIDCConfig) validateProviderEndpoint(raw string) error {
	issuer, _ := url.Parse(c.Issuer)
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != issuer.Scheme || endpoint.Host != issuer.Host || endpoint.User != nil || endpoint.Fragment != "" {
		return fmt.Errorf("OIDC endpoint is outside the configured issuer origin")
	}
	return nil
}

func (c OIDCConfig) SecureCookies() bool {
	redirect, err := url.Parse(c.RedirectURL)
	return err == nil && redirect.Scheme == "https"
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type UserClaims struct {
	Subject      string              `json:"sub"`
	Groups       []string            `json:"groups"`
	CustomClaims map[string][]string `json:"custom_claims,omitempty"`
}

type SessionData struct {
	User      UserClaims `json:"user"`
	CSRFToken string     `json:"csrf_token"`
	ExpiresAt int64      `json:"expires_at"`
}

type AuthContext struct {
	User      UserClaims
	IsAdmin   bool
	CSRFToken string
}

func OIDCConfigFromEnv() OIDCConfig {
	return OIDCConfig{
		Issuer:       getEnv("TERNAL_OIDC_ISSUER", "http://localhost:8080/auth/v1/"),
		ClientID:     getEnv("TERNAL_OIDC_CLIENT_ID", "ternal"),
		ClientSecret: os.Getenv("TERNAL_OIDC_CLIENT_SECRET"),
		RedirectURL:  getEnv("TERNAL_OIDC_REDIRECT_URL", "http://127.0.0.1:3000/auth/callback"),
		AdminGroup:   getEnv("TERNAL_OIDC_ADMIN_GROUP", "ternal-admins"),
		GroupsClaim:  getEnv("TERNAL_OIDC_GROUPS_CLAIM", "groups"),
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func SignSession(data SessionData, key string) (string, error) {
	return signValue(data, key)
}

func signValue(data any, key string) (string, error) {
	if len(key) < 32 {
		return "", fmt.Errorf("signing key must be at least 32 bytes")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("v1.%s.%s", encoded, sig), nil
}

func VerifySession(cookie, key string) (*SessionData, error) {
	var data SessionData
	if err := verifyValue(cookie, key, &data); err != nil {
		return nil, err
	}
	if data.ExpiresAt <= time.Now().Unix() {
		return nil, fmt.Errorf("session expired")
	}
	return &data, nil
}

func verifyValue(value, key string, destination any) error {
	if len(key) < 32 || len(value) > 32*1024 {
		return fmt.Errorf("invalid signed value")
	}
	parts := strings.SplitN(value, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return fmt.Errorf("invalid session format")
	}
	encoded := parts[1]
	sig := parts[2]
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(encoded))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !constantEqual(sig, expected) {
		return fmt.Errorf("invalid session signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid session encoding")
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return fmt.Errorf("invalid session data")
	}
	return nil
}

func constantEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func SetSessionCookie(w http.ResponseWriter, value string, ttlSeconds int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   ttlSeconds,
	})
}

func SetOIDCStateCookie(w http.ResponseWriter, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: OIDCStateCookie, Value: value, Path: "/auth/callback", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 300})
}

func ClearOIDCStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: OIDCStateCookie, Value: "", Path: "/auth/callback", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func GetSessionFromRequest(r *http.Request, key string) (*SessionData, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, fmt.Errorf("no session cookie")
	}
	return VerifySession(cookie.Value, key)
}

func AuthMiddleware(sessionKey string, devHeaders bool, adminGroups ...string) func(http.Handler) http.Handler {
	adminGroup := "ternal-admins"
	if len(adminGroups) > 0 && adminGroups[0] != "" {
		adminGroup = adminGroups[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var claims *UserClaims
			var csrfToken string

			if devHeaders {
				if user := r.Header.Get("X-Ternal-User"); user != "" {
					groups := strings.Split(r.Header.Get("X-Ternal-Groups"), ",")
					claims = &UserClaims{Subject: user, Groups: groups}
					csrfToken = "dev-csrf"
				}
			}

			if claims == nil {
				session, err := GetSessionFromRequest(r, sessionKey)
				if err == nil {
					claims = &session.User
					csrfToken = session.CSRFToken
				}
			}

			if claims != nil {
				isAdmin := false
				for _, group := range claims.Groups {
					if group == adminGroup {
						isAdmin = true
						break
					}
				}
				ctx := context.WithValue(r.Context(), authContextKey{}, &AuthContext{
					User:      *claims,
					IsAdmin:   isAdmin,
					CSRFToken: csrfToken,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
			} else {
				next.ServeHTTP(w, r)
			}
		})
	}
}

func GetAuth(r *http.Request) *AuthContext {
	if v := r.Context().Value(authContextKey{}); v != nil {
		return v.(*AuthContext)
	}
	return nil
}

type authContextKey struct{}

func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetAuth(r) == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireAdmin(adminGroup string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := GetAuth(r)
			if identity == nil || !identity.IsAdmin {
				http.Error(w, `{"error":"administrator access required"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := GetAuth(r)
		if auth == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" {
			if !browserRequestOriginAllowed(r) {
				http.Error(w, `{"error":"invalid request origin"}`, http.StatusForbidden)
				return
			}
			csrf := r.Header.Get(CSRFHeader)
			if csrf == "" {
				csrf = r.FormValue("_csrf")
			}
			if !hmac.Equal([]byte(csrf), []byte(auth.CSRFToken)) {
				http.Error(w, `{"error":"invalid csrf token"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func browserRequestOriginAllowed(r *http.Request) bool {
	source := r.Header.Get("Origin")
	if source == "" {
		source = r.Header.Get("Referer")
	}
	if source == "" {
		// Non-browser API clients authenticate the request with the CSRF token.
		return true
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))
}
