package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/mrchypark/ternal/internal/auth"
	"github.com/mrchypark/ternal/internal/core"
	"github.com/mrchypark/ternal/internal/deviceauth"
	"github.com/mrchypark/ternal/internal/store"
	"github.com/mrchypark/ternal/internal/web"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	store      *store.Store
	auth       auth.OIDCConfig
	oidc       *auth.OIDCClient
	oidcErr    error
	sessionKey string
	relayToken string
	sessionTTL time.Duration
	devHeaders bool
	configErr  error
}

func NewServer(s *store.Store) *Server {
	config := auth.OIDCConfigFromEnv()
	oidcClient, oidcErr := auth.NewOIDCClient(config)
	sessionKey := os.Getenv("TERNAL_SESSION_KEY")
	var configErr error
	if sessionKey == "" {
		sessionKey, _ = auth.NewRandomToken(48)
	} else if len(sessionKey) < 32 {
		configErr = fmt.Errorf("TERNAL_SESSION_KEY must be at least 32 bytes")
	}
	devHeaders := os.Getenv("TERNAL_DEV_HEADERS") == "1"
	sessionTTL := time.Hour
	if raw := os.Getenv("TERNAL_SESSION_TTL_SECONDS"); raw != "" {
		seconds, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || seconds < 60 || seconds > 3600 {
			configErr = fmt.Errorf("TERNAL_SESSION_TTL_SECONDS must be between 60 and 3600")
		} else {
			sessionTTL = time.Duration(seconds) * time.Second
		}
	}
	return &Server{
		store:      s,
		auth:       config,
		oidc:       oidcClient,
		oidcErr:    oidcErr,
		sessionKey: sessionKey,
		relayToken: os.Getenv("TERNAL_RELAY_ACCESS_TOKEN"),
		sessionTTL: sessionTTL,
		devHeaders: devHeaders,
		configErr:  configErr,
	}
}

func (s *Server) ValidateRuntime(bind string) error {
	if s.configErr != nil {
		return s.configErr
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("invalid TERNAL_BIND: %w", err)
	}
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if s.devHeaders && !loopback {
		return fmt.Errorf("TERNAL_DEV_HEADERS requires a loopback bind")
	}
	if !s.devHeaders {
		if os.Getenv("TERNAL_SESSION_KEY") == "" {
			return fmt.Errorf("TERNAL_SESSION_KEY is required")
		}
		if s.oidcErr != nil {
			return fmt.Errorf("invalid OIDC configuration: %w", s.oidcErr)
		}
	}
	if len(s.relayToken) < 32 {
		return fmt.Errorf("TERNAL_RELAY_ACCESS_TOKEN must be at least 32 bytes")
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	})
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			next.ServeHTTP(w, r)
		})
	})
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(auth.AuthMiddleware(s.sessionKey, s.devHeaders, s.auth.AdminGroup))

	portal := web.New(s.store)
	r.Handle("/assets/*", web.Assets())
	r.Get("/", portal.Index)

	r.Get("/health", s.handleHealth)
	r.Get("/ready", s.handleReady)
	r.Get("/metrics", s.handleMetrics)

	r.Route("/auth", func(r chi.Router) {
		r.Get("/oidc-config", s.handleOIDCConfig)
		r.Get("/login", s.handleAuthLogin)
		r.Get("/callback", s.handleAuthCallback)
		r.Post("/device/start", s.handleDeviceStart)
		r.Post("/device/token", s.handleDeviceToken)
		r.With(auth.RequireAuth, auth.RequireCSRF).Post("/logout", s.handleLogout)
		r.Get("/session", s.handleSession)
	})

	r.Route("/hosts", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", s.handleListHosts)
		r.With(auth.RequireAdmin(s.auth.AdminGroup), auth.RequireCSRF).Post("/", s.handleCreateHost)
		r.Get("/{id}", s.handleGetHost)
		r.With(auth.RequireAdmin(s.auth.AdminGroup), auth.RequireCSRF).Put("/{id}", s.handleUpdateHost)
		r.With(auth.RequireAdmin(s.auth.AdminGroup), auth.RequireCSRF).Delete("/{id}", s.handleDeleteHost)
	})

	r.Route("/policies", func(r chi.Router) {
		r.Use(auth.RequireAuth, auth.RequireAdmin(s.auth.AdminGroup))
		r.Get("/", s.handleListPolicies)
		r.With(auth.RequireCSRF).Post("/", s.handleCreatePolicy)
		r.With(auth.RequireCSRF).Put("/{id}", s.handleUpdatePolicy)
		r.With(auth.RequireCSRF).Delete("/{id}", s.handleDeletePolicy)
	})

	r.Route("/access", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.With(auth.RequireCSRF).Post("/ssh", s.handleIssueSSHCommand)
		r.Get("/ssh-config", s.handleSSHConfig)
		r.With(auth.RequireCSRF).Post("/relay-grants", s.handleIssueRelayGrant)
		r.Get("/discovery/{host}", s.handleGetEndpointDiscovery)
		r.Get("/grants/{id}/key-status", s.handleKeyStatus)
		r.Get("/grants", s.handleListAccessGrants)
		r.Get("/requests", s.handleListAccessRequests)
	})

	r.Route("/ssh-keys", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", s.handleListSSHKeys)
		r.With(auth.RequireCSRF).Post("/", s.handleCreateSSHKey)
		r.With(auth.RequireCSRF).Delete("/{id}", s.handleDeleteSSHKey)
	})

	r.Route("/manufacturing", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth, auth.RequireAdmin(s.auth.AdminGroup))
			r.Get("/tokens", s.handleListManufacturingTokens)
			r.With(auth.RequireCSRF).Post("/tokens", s.handleCreateManufacturingToken)
			r.Get("/batches", s.handleListManufacturingBatches)
			r.With(auth.RequireCSRF).Post("/batches", s.handleCreateManufacturingBatch)
			r.With(auth.RequireCSRF).Post("/batches/{id}/close", s.handleCloseManufacturingBatch)
		})
		r.Post("/enroll", s.handleEnrollDevice)
	})

	r.Route("/devices", func(r chi.Router) {
		r.Use(auth.RequireAuth, auth.RequireAdmin(s.auth.AdminGroup))
		r.Get("/", s.handleListDevices)
		r.With(auth.RequireCSRF).Delete("/{id}", s.handleDeleteDevice)
	})

	r.Route("/agents", func(r chi.Router) {
		r.Post("/heartbeat", s.handleHeartbeat)
		r.Get("/authorized-keys", s.handleAgentAuthorizedKeys)
		r.Post("/authorized-keys/ack", s.handleAgentAuthorizedKeysAck)
	})

	r.Post("/internal/iroh-relay/access", s.handleRelayAccess)

	return r
}

func allowedOrigins() []string {
	if origin := strings.TrimSpace(os.Getenv("TERNAL_CORS_ORIGIN")); origin != "" {
		return []string{strings.TrimSuffix(origin, "/")}
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ready(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte("# HELP ternal_up Whether the Ternal API process is serving requests.\n# TYPE ternal_up gauge\nternal_up 1\n"))
}

func (s *Server) handleOIDCConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"issuer":       s.auth.Issuer,
		"client_id":    s.auth.ClientID,
		"redirect_url": s.auth.RedirectURL,
		"admin_group":  s.auth.AdminGroup,
		"groups_claim": s.auth.GroupsClaim,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidcErr != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC is not configured")
		return
	}
	authorizationURL, signedState, err := s.oidc.BeginLogin(r.Context(), s.sessionKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OIDC provider unavailable")
		return
	}
	auth.SetOIDCStateCookie(w, signedState, s.auth.SecureCookies())
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidcErr != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC is not configured")
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	stateCookie, err := r.Cookie(auth.OIDCStateCookie)
	if code == "" || state == "" || err != nil {
		writeError(w, http.StatusBadRequest, "invalid OIDC callback")
		return
	}
	claims, err := s.oidc.CompleteLogin(r.Context(), code, state, stateCookie.Value, s.sessionKey)
	auth.ClearOIDCStateCookie(w, s.auth.SecureCookies())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "OIDC login failed")
		return
	}
	if _, _, err := s.issueSession(w, *claims, time.Now().Add(s.sessionTTL)); err != nil {
		writeError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	if s.oidcErr != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC is not configured")
		return
	}
	device, err := s.oidc.StartDevice(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "device authorization unavailable")
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	if s.oidcErr != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC is not configured")
		return
	}
	var request struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid device token request")
		return
	}
	claims, providerExpiry, err := s.oidc.PollDevice(r.Context(), request.DeviceCode)
	if err != nil {
		if providerErr, ok := err.(*auth.ProviderError); ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": providerErr.Code})
			return
		}
		writeError(w, http.StatusBadGateway, "device token exchange failed")
		return
	}
	expiry := time.Now().Add(s.sessionTTL)
	if !providerExpiry.IsZero() && providerExpiry.Before(expiry) {
		expiry = providerExpiry
	}
	signed, csrf, err := s.issueSession(nil, *claims, expiry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session creation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"session_cookie": signed, "csrf_token": csrf, "expires_at": expiry.Unix()})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSessionCookie(w, s.auth.SecureCookies())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) issueSession(w http.ResponseWriter, claims auth.UserClaims, expiry time.Time) (string, string, error) {
	csrfToken, err := auth.NewRandomToken(32)
	if err != nil {
		return "", "", err
	}
	signed, err := auth.SignSession(auth.SessionData{User: claims, CSRFToken: csrfToken, ExpiresAt: expiry.Unix()}, s.sessionKey)
	if err != nil {
		return "", "", err
	}
	if w != nil {
		ttl := int(time.Until(expiry).Seconds())
		if ttl < 1 {
			return "", "", fmt.Errorf("session expiry is not in the future")
		}
		auth.SetSessionCookie(w, signed, ttl, s.auth.SecureCookies())
	}
	return signed, csrfToken, nil
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	a := auth.GetAuth(r)
	if a == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"user":          a.User,
		"is_admin":      a.IsAdmin,
		"csrf_token":    a.CSRFToken,
	})
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	identity := auth.GetAuth(r)
	if identity.IsAdmin {
		writeJSON(w, http.StatusOK, hosts)
		return
	}
	policies, err := s.store.ListPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, core.FilterVisibleHosts(authClaims(identity), hosts, policies))
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var h store.NewHost
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := s.store.CreateHost(r.Context(), h)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	host, err := s.store.GetHost(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	identity := auth.GetAuth(r)
	if !identity.IsAdmin {
		policies, err := s.store.ListPolicies(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		allowed := false
		for i := range policies {
			if core.PolicyAllowsSSHUser(authClaims(identity), host, &policies[i], host.SSHUser) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
	}
	writeJSON(w, http.StatusOK, host)
}

func authClaims(identity *auth.AuthContext) *core.UserClaims {
	return &core.UserClaims{
		Subject:      identity.User.Subject,
		Groups:       identity.User.Groups,
		CustomClaims: identity.User.CustomClaims,
	}
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var h store.NewHost
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.UpdateHost(r.Context(), id, h); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteHost(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := s.store.ListPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, policies)
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	var p store.NewPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := s.store.CreatePolicy(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var p store.NewPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.UpdatePolicy(r.Context(), id, p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeletePolicy(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleIssueSSHCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID  string `json:"host_id"`
		SSHUser string `json:"ssh_user"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	host, err := s.store.GetHost(r.Context(), req.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	identity := auth.GetAuth(r)
	if req.SSHUser == "" {
		req.SSHUser = host.SSHUser
	}
	if !identity.IsAdmin {
		policies, err := s.store.ListPolicies(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		allowed := false
		for i := range policies {
			if core.PolicyAllowsSSHUser(authClaims(identity), host, &policies[i], req.SSHUser) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
	}
	device, err := s.store.GetDeviceByHostID(r.Context(), host.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if device == nil || !core.ValidHostKeyFingerprint(device.SSHHostKeyFingerprint) {
		writeError(w, http.StatusConflict, "host has no verified SSH host-key fingerprint")
		return
	}
	discovery, err := s.store.GetEndpointDiscovery(r.Context(), host.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if discovery == nil {
		writeError(w, http.StatusConflict, "host has no explicit relay or direct route")
		return
	}
	cmd, err := core.BuildStrictGrantAwareSSHCommand(
		"ternalctl", host.ID, host.EndpointID, req.SSHUser, host.SSHPort,
		device.SSHHostKeyFingerprint,
		&core.RelayConfig{RelayURLs: discovery.RelayURLs}, discovery.DirectAddresses,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, err := s.store.ListSSHKeys(r.Context(), identity.User.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(keys) == 0 {
		writeError(w, http.StatusConflict, "register an SSH public key before requesting access")
		return
	}
	if err := s.store.IssueSSHAccess(r.Context(), identity.User.Subject, host.ID, req.SSHUser, time.Now().Add(5*time.Minute).Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue access grant")
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

func (s *Server) handleSSHConfig(w http.ResponseWriter, r *http.Request) {
	a := auth.GetAuth(r)
	if a == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	policies, err := s.store.ListPolicies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	visible := hosts
	if !a.IsAdmin {
		visible = core.FilterVisibleHosts(&core.UserClaims{Subject: a.User.Subject, Groups: a.User.Groups, CustomClaims: a.User.CustomClaims}, hosts, policies)
	}
	var configs []string
	for _, h := range visible {
		device, deviceErr := s.store.GetDeviceByHostID(r.Context(), h.ID)
		discovery, discoveryErr := s.store.GetEndpointDiscovery(r.Context(), h.ID)
		if deviceErr != nil || discoveryErr != nil || device == nil || discovery == nil {
			continue
		}
		cmd, err := core.BuildStrictGrantAwareSSHCommand("ternalctl", h.ID, h.EndpointID, h.SSHUser, h.SSHPort, device.SSHHostKeyFingerprint, &core.RelayConfig{RelayURLs: discovery.RelayURLs}, discovery.DirectAddresses)
		if err != nil {
			continue
		}
		options := make([]string, 0)
		for i := 0; i+1 < len(cmd.Args); i++ {
			if cmd.Args[i] == "-o" {
				options = append(options, "  "+strings.Replace(cmd.Args[i+1], "=", " ", 1))
			}
		}
		configs = append(configs, fmt.Sprintf("Host %s\n  HostName %s\n  Port %d\n  User %s\n%s",
			h.Name, h.EndpointID, h.SSHPort, h.SSHUser, strings.Join(options, "\n")))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"configs": configs})
}

func (s *Server) handleIssueRelayGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID           string `json:"host_id"`
		ClientEndpointID string `json:"client_endpoint_id"`
		TTL              int64  `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TTL != 0 && req.TTL != 300 {
		writeError(w, http.StatusBadRequest, "relay grants are fixed at 300 seconds")
		return
	}
	if len(req.ClientEndpointID) != 64 || !isHex(req.ClientEndpointID) {
		writeError(w, http.StatusBadRequest, "invalid client endpoint id")
		return
	}
	identity := auth.GetAuth(r)
	host, err := s.store.GetHost(r.Context(), req.HostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	if !identity.IsAdmin {
		policies, err := s.store.ListPolicies(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		claims := &core.UserClaims{Subject: identity.User.Subject, Groups: identity.User.Groups, CustomClaims: identity.User.CustomClaims}
		allowed := false
		for i := range policies {
			if core.PolicyAllowsSSHUser(claims, host, &policies[i], host.SSHUser) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
	}
	grant, err := s.store.CreateRelayAccessGrant(r.Context(), req.HostID, strings.ToLower(req.ClientEndpointID), identity.User.Subject, 300)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Server) handleGetEndpointDiscovery(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "host")
	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if host == nil || !s.canAccessHost(r, host, host.SSHUser) {
		writeError(w, http.StatusNotFound, "discovery not found")
		return
	}
	discovery, err := s.store.GetEndpointDiscovery(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if discovery == nil {
		writeError(w, http.StatusNotFound, "discovery not found")
		return
	}
	writeJSON(w, http.StatusOK, discovery)
}

func (s *Server) handleKeyStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	grant, err := s.store.GetAccessGrant(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grant == nil {
		writeError(w, http.StatusNotFound, "grant not found")
		return
	}
	identity := auth.GetAuth(r)
	if !identity.IsAdmin && grant.UserID != identity.User.Subject {
		writeError(w, http.StatusNotFound, "grant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"installed": grant.KeyInstalled,
	})
}

func (s *Server) handleListAccessGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := s.store.ListAccessGrants(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	identity := auth.GetAuth(r)
	if !identity.IsAdmin {
		filtered := grants[:0]
		for _, grant := range grants {
			if grant.UserID == identity.User.Subject {
				filtered = append(filtered, grant)
			}
		}
		grants = filtered
	}
	writeJSON(w, http.StatusOK, grants)
}

func (s *Server) handleListAccessRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := s.store.ListAccessRequests(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	identity := auth.GetAuth(r)
	if !identity.IsAdmin {
		filtered := requests[:0]
		for _, request := range requests {
			if request.UserID == identity.User.Subject {
				filtered = append(filtered, request)
			}
		}
		requests = filtered
	}
	writeJSON(w, http.StatusOK, requests)
}

func (s *Server) canAccessHost(r *http.Request, host *core.Host, sshUser string) bool {
	identity := auth.GetAuth(r)
	if identity == nil {
		return false
	}
	if identity.IsAdmin {
		return true
	}
	policies, err := s.store.ListPolicies(r.Context())
	if err != nil {
		return false
	}
	for i := range policies {
		if core.PolicyAllowsSSHUser(authClaims(identity), host, &policies[i], sshUser) {
			return true
		}
	}
	return false
}

func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	a := auth.GetAuth(r)
	if a == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	keys, err := s.store.ListSSHKeys(r.Context(), a.User.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleCreateSSHKey(w http.ResponseWriter, r *http.Request) {
	a := auth.GetAuth(r)
	if a == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	parsed, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(req.PublicKey)))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		writeError(w, http.StatusBadRequest, "invalid SSH public key")
		return
	}
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed)))
	if strings.TrimSpace(comment) != "" {
		canonical += " " + strings.TrimSpace(comment)
	}
	key, err := s.store.CreateSSHKey(r.Context(), a.User.Subject, canonical, ssh.FingerprintSHA256(parsed))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

func (s *Server) handleDeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	identity := auth.GetAuth(r)
	deleted, err := s.store.DeleteSSHKeyForUser(r.Context(), id, identity.User.Subject, identity.IsAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "SSH key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListManufacturingTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListManufacturingTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateManufacturingToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BatchID   string `json:"batch_id"`
		ExpiresAt *int64 `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	token, err := s.store.CreateManufacturingToken(r.Context(), req.BatchID, req.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) handleListManufacturingBatches(w http.ResponseWriter, r *http.Request) {
	batches, err := s.store.ListManufacturingBatches(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, batches)
}

func (s *Server) handleCreateManufacturingBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		SerialPrefix string `json:"serial_prefix"`
		ExpiresAt    int64  `json:"expires_at"`
		MaxDevices   int64  `json:"max_devices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	batch, token, err := s.store.CreateManufacturingBatch(r.Context(), req.Name, req.SerialPrefix, req.ExpiresAt, req.MaxDevices)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"item": batch, "token": token})
}

func (s *Server) handleCloseManufacturingBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.CloseManufacturingBatch(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleEnrollDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token           string            `json:"token"`
		EndpointID      string            `json:"endpoint_id"`
		SerialNumber    string            `json:"serial"`
		Model           string            `json:"model"`
		Fingerprint     string            `json:"ssh_host_key_fingerprint"`
		DevicePublicKey string            `json:"device_public_key"`
		SSHUser         string            `json:"ssh_user"`
		SSHPort         uint16            `json:"ssh_port"`
		Tags            map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	publicKey, decodeErr := base64.StdEncoding.DecodeString(req.DevicePublicKey)
	if len(req.EndpointID) != 64 || !isHex(req.EndpointID) || !core.ValidHostKeyFingerprint(req.Fingerprint) || decodeErr != nil || len(publicKey) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "invalid enrollment request")
		return
	}
	device, err := s.store.EnrollDevice(r.Context(), req.Token, strings.ToLower(req.EndpointID), req.SerialNumber, req.Model, req.Fingerprint, req.DevicePublicKey, req.SSHUser, req.SSHPort, req.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, device)
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteDevice(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status      string                `json:"service_status"`
		Serial      string                `json:"serial"`
		EndpointID  string                `json:"endpoint_id"`
		Fingerprint string                `json:"ssh_host_key_fingerprint"`
		Timestamp   int64                 `json:"timestamp"`
		Signature   string                `json:"signature"`
		Discovery   *deviceauth.Discovery `json:"discovery"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	device, err := s.verifyDevice(r, req.Serial, req.EndpointID, req.Fingerprint, req.Timestamp, req.Signature,
		deviceauth.HeartbeatPayload(req.Serial, req.EndpointID, req.Fingerprint, req.Timestamp, req.Status, req.Discovery))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	var direct, relays []string
	if req.Discovery != nil {
		direct, relays = req.Discovery.DirectAddresses, req.Discovery.RelayURLs
	}
	if err := s.store.TouchDevice(r.Context(), device.ID, device.HostID, req.EndpointID, req.Fingerprint, req.Status, direct, relays); err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	if req.Status == "healthy" {
		if _, err := s.store.RenewRelayAccessGrant(r.Context(), device.HostID, strings.ToLower(req.EndpointID), "device:"+device.SerialNumber, 300); err != nil {
			writeError(w, http.StatusInternalServerError, "could not renew device relay admission")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthorizedKeys(w http.ResponseWriter, r *http.Request) {
	sshUser := r.URL.Query().Get("ssh_user")
	serial := r.Header.Get("X-Ternal-Device-Serial")
	endpointID := r.Header.Get("X-Ternal-Device-Endpoint-Id")
	fingerprint := r.Header.Get("X-Ternal-Device-Ssh-Host-Key-Fingerprint")
	timestamp, err := parseTimestamp(r.Header.Get("X-Ternal-Device-Timestamp"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	signature := r.Header.Get("X-Ternal-Device-Signature")
	device, err := s.verifyDevice(r, serial, endpointID, fingerprint, timestamp, signature, deviceauth.AuthorizedKeysPayload(serial, endpointID, fingerprint, timestamp, sshUser))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	keys, err := s.store.AuthorizedKeysForHost(r.Context(), device.HostID, sshUser)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	body := strings.Join(keys, "\n")
	if body != "" {
		body += "\n"
	}
	digest := sha256.Sum256([]byte(body))
	digestHex := hex.EncodeToString(digest[:])
	generation, err := s.store.AuthorizedKeysGeneration(r.Context(), device.HostID, sshUser, digestHex)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Ternal-Authorized-Keys-Generation", fmt.Sprint(generation))
	w.Header().Set("X-Ternal-Authorized-Keys-Sha256", digestHex)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleAgentAuthorizedKeysAck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SSHUser    string `json:"ssh_user"`
		Generation int64  `json:"generation"`
		SHA256     string `json:"sha256"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid acknowledgement")
		return
	}
	serial := r.Header.Get("X-Ternal-Device-Serial")
	endpointID := r.Header.Get("X-Ternal-Device-Endpoint-Id")
	fingerprint := r.Header.Get("X-Ternal-Device-Ssh-Host-Key-Fingerprint")
	timestamp, err := parseTimestamp(r.Header.Get("X-Ternal-Device-Timestamp"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	signature := r.Header.Get("X-Ternal-Device-Signature")
	payload := deviceauth.AuthorizedKeysAckPayload(serial, endpointID, fingerprint, timestamp, req.SSHUser, req.Generation, req.SHA256)
	if _, err := s.verifyDevice(r, serial, endpointID, fingerprint, timestamp, signature, payload); err != nil {
		writeError(w, http.StatusUnauthorized, "device authentication failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyDevice(r *http.Request, serial, endpointID, fingerprint string, timestamp int64, signature, payload string) (*store.Device, error) {
	if !deviceauth.Fresh(timestamp, time.Now()) {
		return nil, fmt.Errorf("stale signature")
	}
	device, err := s.store.GetDeviceBySerial(r.Context(), serial)
	if err != nil || device == nil || device.State == "revoked" || device.EndpointID != endpointID || device.SSHHostKeyFingerprint != fingerprint {
		return nil, fmt.Errorf("device identity mismatch")
	}
	if err := deviceauth.Verify(device.DevicePublicKey, payload, signature); err != nil {
		return nil, err
	}
	return device, nil
}

func parseTimestamp(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}

func (s *Server) handleRelayAccess(w http.ResponseWriter, r *http.Request) {
	if s.relayToken == "" {
		writeRelayDecision(w, http.StatusServiceUnavailable, false)
		return
	}
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader || !relaySecretMatches(s.relayToken, token) {
		writeRelayDecision(w, http.StatusUnauthorized, false)
		return
	}
	endpointID := r.Header.Get("X-Iroh-Nodeid")
	if len(endpointID) != 64 || !isHex(endpointID) {
		writeRelayDecision(w, http.StatusBadRequest, false)
		return
	}
	endpointID = strings.ToLower(endpointID)
	allowed, err := s.store.RelayEndpointAllowed(r.Context(), endpointID)
	if err != nil {
		writeRelayDecision(w, http.StatusInternalServerError, false)
		return
	}
	if allowed {
		writeRelayDecision(w, http.StatusOK, true)
	} else {
		writeRelayDecision(w, http.StatusForbidden, false)
	}
}

func relaySecretMatches(expected, presented string) bool {
	expectedHash := sha256.Sum256([]byte(expected))
	presentedHash := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expectedHash[:], presentedHash[:]) == 1
}

func isHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func writeRelayDecision(w http.ResponseWriter, status int, allowed bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if allowed {
		_, _ = w.Write([]byte("true"))
	} else {
		_, _ = w.Write([]byte("false"))
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	if status == http.StatusInternalServerError {
		msg = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
