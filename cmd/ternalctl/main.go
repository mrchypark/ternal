package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultAPIURL = "http://127.0.0.1:3000"
	sessionFile   = "session.json"
)

type Session struct {
	Cookie    string `json:"cookie"`
	CSRFToken string `json:"csrf_token"`
	ExpiresAt int64  `json:"expires_at"`
}

type Host struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	EndpointID string            `json:"endpoint_id"`
	SSHUser    string            `json:"ssh_user"`
	Tags       map[string]string `json:"tags"`
	SSHPort    uint16            `json:"ssh_port"`
	Status     string            `json:"status"`
}

type SshCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	apiURL := getEnv("TERNAL_API_URL", defaultAPIURL)
	if err := validateAPIURL(apiURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := newHTTPClient(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "login":
		cmdLogin(client, apiURL)
	case "logout":
		cmdLogout()
	case "whoami":
		cmdWhoami(client, apiURL)
	case "hosts":
		cmdHosts(client, apiURL)
	case "ssh":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: ternalctl ssh <host>\n")
			os.Exit(1)
		}
		cmdSSH(client, apiURL, os.Args[2])
	case "ssh-config":
		cmdSSHConfig(client, apiURL)
	case "submit-key":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: ternalctl submit-key <path>\n")
			os.Exit(1)
		}
		cmdSubmitKey(client, apiURL, os.Args[2])
	case "endpoint":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: ternalctl endpoint <host>\n")
			os.Exit(1)
		}
		cmdEndpoint(client, apiURL, os.Args[2])
	case "proxy":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "usage: ternalctl proxy <host> <endpoint:port> [route-args...]\n")
			os.Exit(1)
		}
		cmdProxy(client, apiURL, os.Args[2], os.Args[3], os.Args[4:])
	case "known-host-key":
		if len(os.Args) != 7 {
			fmt.Fprintf(os.Stderr, "usage: ternalctl known-host-key <fingerprint> <invocation> <actual-fingerprint> <key-type> <key>\n")
			os.Exit(1)
		}
		if err := writeKnownHostKey(os.Stdout, os.Args[2], os.Args[3:]); err != nil {
			fmt.Fprintf(os.Stderr, "host key rejected: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

type devHeadersTransport struct {
	base http.RoundTripper
}

func (t devHeadersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("X-Ternal-User", getEnv("TERNAL_USER", "local-admin"))
	clone.Header.Set("X-Ternal-Groups", getEnv("TERNAL_GROUPS", "ternal-admins"))
	if clone.Method != http.MethodGet && clone.Method != http.MethodHead && clone.Method != http.MethodOptions {
		clone.Header.Set("X-CSRF-Token", "dev-csrf")
	}
	return t.base.RoundTrip(clone)
}

func newHTTPClient(apiURL string) (*http.Client, error) {
	transport := http.DefaultTransport
	if os.Getenv("TERNAL_DEV_HEADERS") == "1" {
		if !developmentHeadersAllowed(apiURL) {
			return nil, fmt.Errorf("TERNAL_DEV_HEADERS requires a loopback API URL")
		}
		transport = devHeadersTransport{base: transport}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}

func developmentHeadersAllowed(apiURL string) bool {
	parsed, err := url.Parse(apiURL)
	return err == nil && os.Getenv("TERNAL_DEV_HEADERS") == "1" && isLoopbackHost(parsed.Hostname())
}

func printUsage() {
	fmt.Println("usage: ternalctl <command> [<args>]")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  login          Login via OIDC device flow")
	fmt.Println("  logout         Clear stored session")
	fmt.Println("  whoami         Show current user")
	fmt.Println("  hosts          List accessible hosts")
	fmt.Println("  ssh <host>     Connect to host")
	fmt.Println("  ssh-config     Print SSH config")
	fmt.Println("  submit-key     Submit SSH public key")
	fmt.Println("  endpoint       Show endpoint discovery")
	fmt.Println("  proxy          SSH ProxyCommand handler")
	fmt.Println("  known-host-key KnownHostsCommand handler")
}

func cmdLogin(client *http.Client, apiURL string) {
	fmt.Println("Starting device authorization flow...")

	startResp, err := postJSON(client, apiURL+"/auth/device/start", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	deviceCode, _ := startResp["device_code"].(string)
	userCode, _ := startResp["user_code"].(string)
	verificationURI, _ := startResp["verification_uri"].(string)
	expiresIn := numberAsInt64(startResp["expires_in"], 600)
	interval := numberAsInt64(startResp["interval"], 5)
	if deviceCode == "" || userCode == "" || verificationURI == "" || expiresIn < 1 || expiresIn > 3600 || interval < 1 || interval > 60 {
		fmt.Fprintln(os.Stderr, "server returned an invalid device authorization response")
		os.Exit(1)
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)

	fmt.Printf("Visit: %s\n", verificationURI)
	fmt.Printf("Enter code: %s\n", userCode)
	fmt.Println("Waiting for authorization...")

	for {
		if time.Now().Add(time.Duration(interval) * time.Second).After(deadline) {
			fmt.Fprintln(os.Stderr, "device authorization expired")
			os.Exit(1)
		}
		time.Sleep(time.Duration(interval) * time.Second)

		tokenResp, status, err := postJSONResponse(client, apiURL+"/auth/device/token", map[string]string{
			"device_code": deviceCode,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if status < 200 || status >= 300 {
			providerError, _ := tokenResp["error"].(string)
			switch providerError {
			case "authorization_pending":
				continue
			case "slow_down":
				if interval <= 55 {
					interval += 5
				}
				continue
			default:
				fmt.Fprintf(os.Stderr, "device authorization failed with HTTP %d\n", status)
				os.Exit(1)
			}
		}

		if sessionCookie, ok := tokenResp["session_cookie"].(string); ok && sessionCookie != "" {
			csrfToken, _ := tokenResp["csrf_token"].(string)
			expiresAt, _ := tokenResp["expires_at"].(float64)
			session := Session{
				Cookie:    sessionCookie,
				CSRFToken: csrfToken,
				ExpiresAt: int64(expiresAt),
			}
			if session.ExpiresAt <= time.Now().Unix() || session.CSRFToken == "" {
				fmt.Fprintln(os.Stderr, "server returned an invalid local session")
				os.Exit(1)
			}
			if err := saveSession(&session); err != nil {
				fmt.Fprintf(os.Stderr, "error saving session: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Login successful!")
			return
		}

	}
}

func numberAsInt64(value any, fallback int64) int64 {
	if number, ok := value.(float64); ok && number == float64(int64(number)) {
		return int64(number)
	}
	return fallback
}

func cmdLogout() {
	if err := removeSession(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Logged out.")
}

func cmdWhoami(client *http.Client, apiURL string) {
	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	req, _ := http.NewRequest("GET", apiURL+"/auth/session", nil)
	addSession(req, session)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if authenticated, ok := result["authenticated"].(bool); ok && authenticated {
		if user, ok := result["user"].(map[string]interface{}); ok {
			fmt.Printf("User: %s\n", user["sub"])
			if groups, ok := user["groups"].([]interface{}); ok {
				fmt.Printf("Groups: %v\n", groups)
			}
		}
	} else {
		fmt.Println("Not authenticated")
	}
}

func cmdHosts(client *http.Client, apiURL string) {
	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	req, _ := http.NewRequest("GET", apiURL+"/hosts", nil)
	addSession(req, session)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var hosts []Host
	json.NewDecoder(resp.Body).Decode(&hosts)

	for _, h := range hosts {
		fmt.Printf("%-20s %s\n", h.Name, h.Status)
	}
}

func cmdSSH(client *http.Client, apiURL, hostName string) {
	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	hosts, err := listHosts(client, apiURL, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var target *Host
	for _, h := range hosts {
		if h.Name == hostName {
			target = &h
			break
		}
	}
	if target == nil {
		fmt.Fprintf(os.Stderr, "host not found: %s\n", hostName)
		os.Exit(1)
	}

	cmdResp, err := postJSON(client, apiURL+"/access/ssh", map[string]string{
		"host_id":  target.ID,
		"ssh_user": target.SSHUser,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	program, _ := cmdResp["program"].(string)
	args := make([]string, 0)
	if argsRaw, ok := cmdResp["args"].([]interface{}); ok {
		for _, a := range argsRaw {
			args = append(args, fmt.Sprint(a))
		}
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not resolve ternalctl: %v\n", err)
		os.Exit(1)
	}
	for i, arg := range args {
		if strings.HasPrefix(arg, "KnownHostsCommand=ternalctl ") {
			args[i] = "KnownHostsCommand=" + shellQuote(executable) + strings.TrimPrefix(arg, "KnownHostsCommand=ternalctl")
		}
	}

	cmd := exec.Command(program, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "ssh failed: %v\n", err)
		os.Exit(1)
	}
}

func cmdSSHConfig(client *http.Client, apiURL string) {
	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	req, _ := http.NewRequest("GET", apiURL+"/access/ssh-config", nil)
	addSession(req, session)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if configs, ok := result["configs"].([]interface{}); ok {
		for _, c := range configs {
			fmt.Println(c)
			fmt.Println()
		}
	}
}

func cmdSubmitKey(client *http.Client, apiURL, keyPath string) {
	_, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading key: %v\n", err)
		os.Exit(1)
	}

	_, err = postJSON(client, apiURL+"/ssh-keys", map[string]string{
		"public_key": strings.TrimSpace(string(keyData)),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Key submitted successfully.")
}

func cmdEndpoint(client *http.Client, apiURL, hostName string) {
	session, err := loadSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	hosts, err := listHosts(client, apiURL, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var target *Host
	for _, h := range hosts {
		if h.Name == hostName {
			target = &h
			break
		}
	}
	if target == nil {
		fmt.Fprintf(os.Stderr, "host not found: %s\n", hostName)
		os.Exit(1)
	}

	req, _ := http.NewRequest("GET", apiURL+"/access/discovery/"+target.ID, nil)
	addSession(req, session)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var discovery map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&discovery)

	fmt.Printf("Host: %s\n", hostName)
	if addrs, ok := discovery["direct_addresses"].([]interface{}); ok {
		fmt.Printf("Direct addresses: %v\n", addrs)
	}
	if relays, ok := discovery["relay_urls"].([]interface{}); ok {
		fmt.Printf("Relay URLs: %v\n", relays)
	}
}

func cmdProxy(client *http.Client, apiURL, hostRef, endpointPort string, routeArgs []string) {
	_, err := loadSession()
	if err != nil && !developmentHeadersAllowed(apiURL) {
		fmt.Fprintf(os.Stderr, "not logged in\n")
		os.Exit(1)
	}

	if err := validateProxyInvocation(hostRef, endpointPort, routeArgs); err != nil {
		fmt.Fprintf(os.Stderr, "invalid proxy invocation: %v\n", err)
		os.Exit(1)
	}
	pigeonsPath := findPigeons()
	if pigeonsPath == "" {
		fmt.Fprintf(os.Stderr, "pigeons binary not found\n")
		os.Exit(1)
	}
	clientEndpointID, err := persistentEndpointID(pigeonsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not read persistent pigeons identity: %v\n", err)
		os.Exit(1)
	}
	grantResp, err := postJSON(client, apiURL+"/access/relay-grants", map[string]interface{}{
		"host_id":            hostRef,
		"client_endpoint_id": clientEndpointID,
		"ttl":                300,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_ = grantResp

	parts := strings.SplitN(endpointPort, ":", 2)
	endpointID := parts[0]
	port := "22"
	if len(parts) > 1 {
		port = parts[1]
	}

	args := []string{"fly", "--stdio", endpointID}
	args = append(args, routeArgs...)

	cmd := exec.Command(pigeonsPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	_ = port
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "pigeons failed: %v\n", err)
		os.Exit(1)
	}
}

func writeKnownHostKey(w io.Writer, expected string, args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("expected four OpenSSH arguments")
	}
	invocation, actual, keyType, keyData := args[0], args[1], args[2], args[3]
	if invocation == "ORDER" && actual == "NONE" && keyType == "NONE" && keyData == "NONE" {
		return nil
	}
	if invocation != "HOSTNAME" {
		return fmt.Errorf("unsupported invocation %q", invocation)
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return fmt.Errorf("fingerprint mismatch")
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyType + " " + keyData))
	if err != nil {
		return fmt.Errorf("invalid public key")
	}
	if parsed.Type() != keyType || subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(parsed)), []byte(expected)) != 1 {
		return fmt.Errorf("key does not match fingerprint")
	}
	_, err = fmt.Fprintf(w, "* %s %s\n", keyType, keyData)
	return err
}

func persistentEndpointID(pigeonsPath string) (string, error) {
	out, err := exec.Command(pigeonsPath, "endpoint-id").Output()
	if err != nil {
		return "", err
	}
	endpointID := strings.TrimSpace(string(out))
	if len(endpointID) != 64 {
		return "", fmt.Errorf("unexpected endpoint id")
	}
	for _, ch := range endpointID {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return "", fmt.Errorf("unexpected endpoint id")
		}
	}
	return strings.ToLower(endpointID), nil
}

func validateProxyInvocation(hostRef, endpointPort string, routeArgs []string) error {
	if hostRef == "" || strings.HasPrefix(hostRef, "-") || strings.ContainsAny(hostRef, " \t\r\n") {
		return fmt.Errorf("invalid host reference")
	}
	parts := strings.Split(endpointPort, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid endpoint")
	}
	if len(routeArgs) == 0 || len(routeArgs)%2 != 0 {
		return fmt.Errorf("explicit relay or direct route required")
	}
	for i := 0; i < len(routeArgs); i += 2 {
		switch routeArgs[i] {
		case "--direct-address", "--relay-url", "--extra-relay-url":
		default:
			return fmt.Errorf("unsupported route flag")
		}
		if routeArgs[i+1] == "" || strings.HasPrefix(routeArgs[i+1], "-") || strings.ContainsAny(routeArgs[i+1], " \t\r\n") {
			return fmt.Errorf("invalid route value")
		}
	}
	return nil
}

func findPigeons() string {
	if bin := os.Getenv("TERNAL_TRANSPORT_BIN"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}

	execPath, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(execPath)
		candidate := filepath.Join(dir, "pigeons")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	path, err := exec.LookPath("pigeons")
	if err == nil {
		return path
	}

	return ""
}

func listHosts(client *http.Client, apiURL string, session *Session) ([]Host, error) {
	req, _ := http.NewRequest("GET", apiURL+"/hosts", nil)
	addSession(req, session)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API returned HTTP %d", resp.StatusCode)
	}

	var hosts []Host
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return nil, err
	}
	return hosts, nil
}

func postJSON(client *http.Client, url string, body interface{}) (map[string]interface{}, error) {
	result, status, err := postJSONResponse(client, url, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		if message, _ := result["error"].(string); message != "" {
			return nil, fmt.Errorf("API returned HTTP %d: %s", status, message)
		}
		return nil, fmt.Errorf("API returned HTTP %d", status)
	}
	return result, nil
}

func postJSONResponse(client *http.Client, url string, body interface{}) (map[string]interface{}, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest("POST", url, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	session, err := loadSession()
	if err == nil {
		addSession(req, session)
		if session.CSRFToken != "" {
			req.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return nil, resp.StatusCode, err
	}
	return result, resp.StatusCode, nil
}

func addSession(req *http.Request, session *Session) {
	req.AddCookie(&http.Cookie{Name: "ternal_session", Value: session.Cookie})
}

func sessionPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "ternal", sessionFile), nil
}

func loadSession() (*Session, error) {
	if cookie := os.Getenv("TERNAL_SESSION_COOKIE"); cookie != "" {
		return &Session{Cookie: cookie, CSRFToken: os.Getenv("TERNAL_CSRF_TOKEN"), ExpiresAt: time.Now().Add(time.Minute).Unix()}, nil
	}
	path, err := sessionPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	if session.ExpiresAt < time.Now().Unix() {
		return nil, fmt.Errorf("session expired")
	}
	return &session, nil
}

func saveSession(session *Session) error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func removeSession() error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validateAPIURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid TERNAL_API_URL")
	}
	loopback := isLoopbackHost(parsed.Hostname())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return fmt.Errorf("TERNAL_API_URL must use HTTPS outside loopback development")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func shellQuote(value string) string {
	if !strings.ContainsAny(value, " \t'\\\"") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
