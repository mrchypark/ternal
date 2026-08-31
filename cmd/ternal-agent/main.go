package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mrchypark/ternal/internal/deviceauth"
	"golang.org/x/crypto/ssh"
)

const defaultAPIURL = "http://127.0.0.1:3000"

type config struct {
	APIURL                 string
	Pigeons                string
	DeviceKey              string
	IdentityFile           string
	AuthorizedKeysPath     string
	SSHUser                string
	SSHPort                uint16
	RelayURLs              []string
	ExtraRelayURLs         []string
	HeartbeatEvery         time.Duration
	RestartBackoff         time.Duration
	StatusFile             string
	ManufacturingTokenFile string
}

type enrollmentResponse struct {
	ID           string `json:"id"`
	HostID       string `json:"host_id"`
	SerialNumber string `json:"serial_number"`
}

type runtimeStatus struct {
	Service    string `json:"service"`
	EndpointID string `json:"endpoint_id"`
	Child      string `json:"child"`
	LastError  string `json:"last_error,omitempty"`
	UpdatedAt  int64  `json:"updated_at"`
}

type authorizedKeysState struct {
	Generation int64  `json:"generation"`
	SHA256     string `json:"sha256"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ternal-agent: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ternal-agent device-keygen|enroll|heartbeat|sync-authorized-keys|run|status")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "device-keygen":
		path := cfg.DeviceKey
		if len(args) == 2 {
			path = args[1]
		} else if len(args) != 1 {
			return errors.New("usage: ternal-agent device-keygen [path]")
		}
		public, err := deviceauth.GenerateKey(path)
		if err != nil {
			return err
		}
		fmt.Printf("device_public_key\t%s\n", public)
		return nil
	case "enroll":
		if len(args) != 2 && len(args) != 3 {
			return errors.New("usage: ternal-agent enroll <ssh-host-key-fingerprint> [serial] (token is read from TERNAL_MANUFACTURING_TOKEN_FILE)")
		}
		serial := ""
		if len(args) == 3 {
			serial = args[2]
		}
		token, err := readEnrollmentToken(cfg.ManufacturingTokenFile)
		if err != nil {
			return err
		}
		return enroll(ctx, cfg, serial, token, args[1])
	case "heartbeat":
		return heartbeat(ctx, cfg, "healthy")
	case "sync-authorized-keys":
		path := cfg.AuthorizedKeysPath
		if len(args) == 2 {
			path = args[1]
		} else if len(args) != 1 {
			return errors.New("usage: ternal-agent sync-authorized-keys [path]")
		}
		return syncAuthorizedKeys(ctx, cfg, path)
	case "run":
		return supervise(ctx, cfg)
	case "status":
		data, err := os.ReadFile(cfg.StatusFile)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func loadConfig() (config, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return config{}, err
	}
	key := envOr("TERNAL_DEVICE_KEY_FILE", filepath.Join(configHome, "ternal", "device.key"))
	identityFile := envOr("TERNAL_DEVICE_IDENTITY_FILE", deviceauth.IdentityPath(key))
	sshPort, err := parsePort(envOr("TERNAL_AGENT_SSH_PORT", "22"))
	if err != nil {
		return config{}, err
	}
	heartbeatEvery, err := parsePositiveDuration("TERNAL_AGENT_HEARTBEAT_SECONDS", 60*time.Second)
	if err != nil {
		return config{}, err
	}
	restartBackoff, err := parsePositiveDuration("TERNAL_AGENT_RESTART_BACKOFF_SECONDS", 5*time.Second)
	if err != nil {
		return config{}, err
	}
	apiURL := strings.TrimSuffix(envOr("TERNAL_API_URL", defaultAPIURL), "/")
	parsedAPI, err := url.Parse(apiURL)
	if err != nil || parsedAPI.Scheme == "" || parsedAPI.Host == "" || parsedAPI.User != nil || parsedAPI.RawQuery != "" || parsedAPI.Fragment != "" {
		return config{}, errors.New("invalid TERNAL_API_URL")
	}
	if parsedAPI.Scheme != "https" && !(parsedAPI.Scheme == "http" && isLoopback(parsedAPI.Hostname())) {
		return config{}, errors.New("TERNAL_API_URL must use HTTPS outside loopback development")
	}
	pigeons, err := findPigeons()
	if err != nil {
		return config{}, err
	}
	sshUser := envOr("TERNAL_AGENT_SSH_USER", "root")
	if !validLocalUser(sshUser) {
		return config{}, errors.New("invalid SSH user")
	}
	return config{
		APIURL: apiURL, Pigeons: pigeons, DeviceKey: key,
		IdentityFile: identityFile, AuthorizedKeysPath: os.Getenv("TERNAL_AGENT_AUTHORIZED_KEYS_PATH"),
		SSHUser: sshUser, SSHPort: sshPort,
		RelayURLs: parseList(os.Getenv("TERNAL_AGENT_RELAY_URLS")), ExtraRelayURLs: parseList(os.Getenv("TERNAL_AGENT_EXTRA_RELAY_URLS")),
		HeartbeatEvery: heartbeatEvery, RestartBackoff: restartBackoff,
		StatusFile:             envOr("TERNAL_AGENT_STATUS_FILE", filepath.Join(configHome, "ternal", "agent-status.json")),
		ManufacturingTokenFile: os.Getenv("TERNAL_MANUFACTURING_TOKEN_FILE"),
	}, nil
}

func readEnrollmentToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("TERNAL_MANUFACTURING_TOKEN_FILE is required for enrollment")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4096 {
		return "", errors.New("invalid manufacturing token file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("invalid manufacturing token")
	}
	return token, nil
}

func enroll(ctx context.Context, cfg config, serial, token, fingerprint string) error {
	if token == "" || !validFingerprint(fingerprint) {
		return errors.New("invalid enrollment arguments")
	}
	private, err := deviceauth.LoadPrivate(cfg.DeviceKey)
	if err != nil {
		return fmt.Errorf("load device key: %w", err)
	}
	endpointID, err := roostEndpointID(cfg)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"serial": serial, "token": token, "device_public_key": deviceauth.PublicKey(private),
		"endpoint_id": endpointID, "ssh_host_key_fingerprint": fingerprint,
		"ssh_user": cfg.SSHUser, "ssh_port": cfg.SSHPort,
	}
	var response enrollmentResponse
	if err := requestJSON(ctx, cfg.APIURL, http.MethodPost, "/manufacturing/enroll", payload, nil, &response); err != nil {
		return err
	}
	if response.SerialNumber == "" {
		response.SerialNumber = serial
	}
	if err := deviceauth.WriteIdentity(cfg.IdentityFile, deviceauth.Identity{Serial: response.SerialNumber, HostKeyFingerprint: fingerprint}); err != nil {
		return err
	}
	fmt.Printf("device\t%s\t%s\t%s\n", response.ID, response.HostID, response.SerialNumber)
	return nil
}

func heartbeat(ctx context.Context, cfg config, status string) error {
	private, identity, endpointID, err := loadDevice(cfg)
	if err != nil {
		return err
	}
	discovery := &deviceauth.Discovery{DirectAddresses: parseList(os.Getenv("TERNAL_PIGEONS_DIRECT_ADDRESSES")), RelayURLs: append(append([]string{}, cfg.RelayURLs...), cfg.ExtraRelayURLs...)}
	timestamp := time.Now().Unix()
	payload := deviceauth.HeartbeatPayload(identity.Serial, endpointID, identity.HostKeyFingerprint, timestamp, status, discovery)
	body := map[string]any{
		"serial": identity.Serial, "endpoint_id": endpointID, "ssh_host_key_fingerprint": identity.HostKeyFingerprint,
		"timestamp": timestamp, "service_status": status, "discovery": discovery, "signature": deviceauth.Sign(private, payload),
	}
	return requestJSON(ctx, cfg.APIURL, http.MethodPost, "/agents/heartbeat", body, nil, nil)
}

func syncAuthorizedKeys(ctx context.Context, cfg config, path string) error {
	if path == "" {
		return errors.New("TERNAL_AGENT_AUTHORIZED_KEYS_PATH is required")
	}
	unlock, err := acquireSyncLock(path)
	if err != nil {
		return err
	}
	defer unlock()
	private, identity, endpointID, err := loadDevice(cfg)
	if err != nil {
		return err
	}
	timestamp := time.Now().Unix()
	payload := deviceauth.AuthorizedKeysPayload(identity.Serial, endpointID, identity.HostKeyFingerprint, timestamp, cfg.SSHUser)
	headers := signedHeaders(identity, endpointID, timestamp, deviceauth.Sign(private, payload))
	response, err := request(ctx, cfg.APIURL, http.MethodGet, "/agents/authorized-keys?ssh_user="+url.QueryEscape(cfg.SSHUser), nil, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])
	if response.Header.Get("X-Ternal-Authorized-Keys-Sha256") != digestHex {
		return errors.New("authorized_keys digest mismatch")
	}
	generation, err := strconv.ParseInt(response.Header.Get("X-Ternal-Authorized-Keys-Generation"), 10, 64)
	if err != nil || generation < 0 {
		return errors.New("invalid authorized_keys generation")
	}
	if err := validateAuthorizedKeys(body); err != nil {
		return err
	}
	statePath := path + ".ternal-state"
	current, err := readAuthorizedKeysState(statePath)
	if err != nil {
		return err
	}
	if current != nil && (generation < current.Generation || (generation == current.Generation && digestHex != current.SHA256)) {
		return errors.New("authorized_keys snapshot rollback or equivocation rejected")
	}
	if err := atomicWrite(path, body, 0600); err != nil {
		return err
	}
	state, err := json.Marshal(authorizedKeysState{Generation: generation, SHA256: digestHex})
	if err != nil {
		return err
	}
	if err := atomicWrite(statePath, append(state, '\n'), 0600); err != nil {
		return err
	}
	ackTime := time.Now().Unix()
	ackPayload := deviceauth.AuthorizedKeysAckPayload(identity.Serial, endpointID, identity.HostKeyFingerprint, ackTime, cfg.SSHUser, generation, digestHex)
	ackHeaders := signedHeaders(identity, endpointID, ackTime, deviceauth.Sign(private, ackPayload))
	ack := map[string]any{"ssh_user": cfg.SSHUser, "generation": generation, "sha256": digestHex}
	return requestJSON(ctx, cfg.APIURL, http.MethodPost, "/agents/authorized-keys/ack", ack, ackHeaders, nil)
}

func acquireSyncLock(path string) (func(), error) {
	lockPath := path + ".ternal-lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(lockPath, 0700); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("authorized_keys synchronization is already running; remove %s only after confirming no agent is active", lockPath)
		}
		return nil, err
	}
	return func() { _ = os.Remove(lockPath) }, nil
}

func readAuthorizedKeysState(path string) (*authorizedKeysState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state authorizedKeysState
	if err := json.Unmarshal(data, &state); err != nil || state.Generation < 1 || len(state.SHA256) != 64 {
		return nil, errors.New("invalid authorized_keys synchronization state")
	}
	return &state, nil
}

func supervise(parent context.Context, cfg config) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	endpointID, err := roostEndpointID(cfg)
	if err != nil {
		return err
	}
	for {
		child := exec.CommandContext(ctx, cfg.Pigeons, roostArgs(cfg)...)
		child.Stdout, child.Stderr = os.Stderr, os.Stderr
		if err := child.Start(); err != nil {
			return err
		}
		_ = writeStatus(cfg.StatusFile, runtimeStatus{Service: "starting", EndpointID: endpointID, Child: "running", UpdatedAt: time.Now().Unix()})
		exit := make(chan error, 1)
		go func() { exit <- child.Wait() }()
		ticker := time.NewTicker(cfg.HeartbeatEvery)
		_ = heartbeat(ctx, cfg, "pigeons-running")
		if cfg.AuthorizedKeysPath != "" {
			_ = syncAuthorizedKeys(ctx, cfg, cfg.AuthorizedKeysPath)
		}
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				select {
				case <-exit:
				case <-time.After(10 * time.Second):
					_ = child.Process.Kill()
				}
				_ = writeStatus(cfg.StatusFile, runtimeStatus{Service: "stopped", EndpointID: endpointID, Child: "stopped", UpdatedAt: time.Now().Unix()})
				return nil
			case childErr := <-exit:
				ticker.Stop()
				_ = writeStatus(cfg.StatusFile, runtimeStatus{Service: "degraded", EndpointID: endpointID, Child: "exited", LastError: exitLabel(childErr), UpdatedAt: time.Now().Unix()})
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(cfg.RestartBackoff):
				}
				goto restart
			case <-ticker.C:
				heartbeatErr := heartbeat(ctx, cfg, "pigeons-running")
				if cfg.AuthorizedKeysPath != "" {
					if syncErr := syncAuthorizedKeys(ctx, cfg, cfg.AuthorizedKeysPath); heartbeatErr == nil {
						heartbeatErr = syncErr
					}
				}
				state := runtimeStatus{Service: "healthy", EndpointID: endpointID, Child: "running", UpdatedAt: time.Now().Unix()}
				if heartbeatErr != nil {
					state.Service, state.LastError = "degraded", "control-plane synchronization failed"
				}
				_ = writeStatus(cfg.StatusFile, state)
			}
		}
	restart:
	}
}

func loadDevice(cfg config) (ed25519.PrivateKey, deviceauth.Identity, string, error) {
	private, err := deviceauth.LoadPrivate(cfg.DeviceKey)
	if err != nil {
		return nil, deviceauth.Identity{}, "", err
	}
	identity, err := deviceauth.ReadIdentity(cfg.IdentityFile)
	if err != nil {
		return nil, identity, "", err
	}
	endpointID, err := roostEndpointID(cfg)
	return private, identity, endpointID, err
}

func roostEndpointID(cfg config) (string, error) {
	output, err := exec.Command(cfg.Pigeons, "endpoint-id", "--roost").Output()
	if err != nil {
		return "", fmt.Errorf("read persistent roost identity: %w", err)
	}
	id := strings.TrimSpace(string(output))
	if !validEndpointID(id) {
		return "", errors.New("pigeons returned invalid roost endpoint id")
	}
	return strings.ToLower(id), nil
}

func roostArgs(cfg config) []string {
	args := []string{"roost", "--ssh-port", strconv.Itoa(int(cfg.SSHPort))}
	for _, value := range cfg.RelayURLs {
		args = append(args, "--relay-url", value)
	}
	for _, value := range cfg.ExtraRelayURLs {
		args = append(args, "--extra-relay-url", value)
	}
	return args
}

func signedHeaders(identity deviceauth.Identity, endpointID string, timestamp int64, signature string) http.Header {
	headers := make(http.Header)
	headers.Set("X-Ternal-Device-Serial", identity.Serial)
	headers.Set("X-Ternal-Device-Endpoint-Id", endpointID)
	headers.Set("X-Ternal-Device-Ssh-Host-Key-Fingerprint", identity.HostKeyFingerprint)
	headers.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(timestamp, 10))
	headers.Set("X-Ternal-Device-Signature", signature)
	return headers
}

func requestJSON(ctx context.Context, base, method, path string, body any, headers http.Header, destination any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	response, err := request(ctx, base, method, path, reader, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if destination == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination); err != nil {
		return errors.New("invalid API response")
	}
	return nil
}

func request(ctx context.Context, base, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("redirect rejected") }}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("API returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func validateAuthorizedKeys(body []byte) error {
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(bytes.TrimSpace(rest)) != 0 || key == nil {
			return errors.New("server returned invalid authorized_keys content")
		}
	}
	return nil
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ternal-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeStatus(path string, status runtimeStatus) error {
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}

func findPigeons() (string, error) {
	if configured := os.Getenv("TERNAL_PIGEONS_BIN"); configured != "" {
		if filepath.IsAbs(configured) {
			if info, err := os.Stat(configured); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				return configured, nil
			}
		}
		return "", errors.New("configured pigeons binary is not executable")
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "pigeons")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("pigeons")
	if err != nil {
		return "", errors.New("pigeons binary not found")
	}
	return path, nil
}

func parsePositiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseUint(value, 10, 32)
	if err != nil || seconds == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return time.Duration(seconds) * time.Second, nil
}

func parsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("invalid SSH port")
	}
	return uint16(port), nil
}

func parseList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func validEndpointID(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validFingerprint(value string) bool {
	return strings.HasPrefix(value, "SHA256:") && len(value) >= 47 && len(value) <= 51 && !strings.ContainsAny(value, " \t\r\n")
}

func validLocalUser(value string) bool {
	if value == "" || len(value) > 32 || value[0] == '-' {
		return false
	}
	for _, ch := range value {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func isLoopback(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func exitLabel(err error) string {
	if err == nil {
		return "exit-0"
	}
	return "child-exited"
}
