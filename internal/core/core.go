package core

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type Host struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	EndpointID string            `json:"endpoint_id"`
	SSHUser    string            `json:"ssh_user"`
	Tags       map[string]string `json:"tags"`
	SSHPort    uint16            `json:"ssh_port"`
	Status     string            `json:"status"`
	Owner      string            `json:"owner"`
	LastSeen   *int64            `json:"last_seen,omitempty"`
}

type Policy struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	RauthyGroup  string   `json:"rauthy_group"`
	HostSelector string   `json:"host_selector"`
	SSHUsers     []string `json:"ssh_users"`
	ExpiresAt    *int64   `json:"expires_at,omitempty"`
}

type UserClaims struct {
	Subject      string              `json:"subject"`
	Groups       []string            `json:"groups"`
	CustomClaims map[string][]string `json:"custom_claims,omitempty"`
}

type SshCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

type RelayConfig struct {
	RelayURLs      []string `json:"relay_urls"`
	ExtraRelayURLs []string `json:"extra_relay_urls"`
}

type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string {
	return e.Message
}

var (
	ErrInvalidEndpointID    = &CommandError{"INVALID_ENDPOINT_ID", "invalid pigeons endpoint id"}
	ErrInvalidSSHUser       = &CommandError{"INVALID_SSH_USER", "invalid ssh user"}
	ErrInvalidSSHPort       = &CommandError{"INVALID_SSH_PORT", "invalid ssh port"}
	ErrInvalidRelayURL      = &CommandError{"INVALID_RELAY_URL", "invalid pigeons relay url"}
	ErrInvalidDirectAddress = &CommandError{"INVALID_DIRECT_ADDRESS", "invalid pigeons direct address"}
	ErrInvalidProxyCommand  = &CommandError{"INVALID_PROXY_COMMAND", "invalid ssh proxy command"}
	ErrInvalidHostReference = &CommandError{"INVALID_HOST_REFERENCE", "invalid host reference"}
	ErrInvalidHostKey       = &CommandError{"INVALID_HOST_KEY", "invalid SSH host-key fingerprint"}
	ErrInvalidCTLPath       = &CommandError{"INVALID_CTL_PATH", "invalid ternalctl executable path"}
	ErrMissingRoute         = &CommandError{"MISSING_ENDPOINT_ROUTE", "remote endpoint requires at least one relay or direct address"}
)

func (c *RelayConfig) Validate() error {
	for _, url := range append(c.RelayURLs, c.ExtraRelayURLs...) {
		if !validRelayURL(url) {
			return ErrInvalidRelayURL
		}
	}
	return nil
}

func FilterVisibleHosts(claims *UserClaims, hosts []Host, policies []Policy) []Host {
	var visible []Host
	for _, host := range hosts {
		for _, policy := range policies {
			if PolicyAllows(claims, &host, &policy) {
				visible = append(visible, host)
				break
			}
		}
	}
	return visible
}

func PolicyAllows(claims *UserClaims, host *Host, policy *Policy) bool {
	return policyPrincipalMatches(claims, policy.RauthyGroup) &&
		hostSelectorMatches(policy.HostSelector, host) &&
		(policy.ExpiresAt == nil || *policy.ExpiresAt > now())
}

func hostSelectorMatches(selector string, host *Host) bool {
	if strings.HasPrefix(selector, "tag:") {
		parts := strings.SplitN(strings.TrimPrefix(selector, "tag:"), "=", 2)
		if len(parts) != 2 {
			return false
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" {
			return false
		}
		tag, ok := host.Tags[key]
		return ok && tag == value
	}
	return wildcardMatches(selector, host.Name)
}

func policyPrincipalMatches(claims *UserClaims, principal string) bool {
	if idx := strings.Index(principal, "="); idx >= 0 {
		claim := strings.TrimSpace(principal[:idx])
		value := strings.TrimSpace(principal[idx+1:])
		if claim == "" || value == "" {
			return false
		}
		if claim == "groups" {
			for _, group := range claims.Groups {
				if group == value {
					return true
				}
			}
			return false
		}
		values, ok := claims.CustomClaims[claim]
		if !ok {
			return false
		}
		for _, v := range values {
			if v == value {
				return true
			}
		}
		return false
	}
	for _, group := range claims.Groups {
		if group == principal {
			return true
		}
	}
	return false
}

func PolicyAllowsSSHUser(claims *UserClaims, host *Host, policy *Policy, sshUser string) bool {
	if !PolicyAllows(claims, host, policy) {
		return false
	}
	for _, user := range policy.SSHUsers {
		if user == sshUser {
			return true
		}
	}
	return false
}

func BuildSSHCommand(endpointID, sshUser string, sshPort uint16) (*SshCommand, error) {
	return BuildSSHCommandWithRelays(endpointID, sshUser, sshPort, &RelayConfig{})
}

func BuildSSHCommandWithRelays(endpointID, sshUser string, sshPort uint16, relayConfig *RelayConfig) (*SshCommand, error) {
	return BuildSSHCommandWithRoutes(endpointID, sshUser, sshPort, relayConfig, nil)
}

func BuildSSHCommandWithRoutes(endpointID, sshUser string, sshPort uint16, relayConfig *RelayConfig, directAddresses []string) (*SshCommand, error) {
	if !validEndpointID(endpointID) {
		return nil, ErrInvalidEndpointID
	}
	if !validSSHUser(sshUser) {
		return nil, ErrInvalidSSHUser
	}
	if sshPort == 0 {
		return nil, ErrInvalidSSHPort
	}
	if err := relayConfig.Validate(); err != nil {
		return nil, err
	}
	proxyCmd, err := buildProxyCommandWithRoutes(relayConfig, directAddresses)
	if err != nil {
		return nil, err
	}
	return &SshCommand{
		Program: "ssh",
		Args: []string{
			"-p", strconv.Itoa(int(sshPort)),
			"-o", fmt.Sprintf("ProxyCommand=%s", proxyCmd),
			fmt.Sprintf("%s@%s", sshUser, endpointID),
		},
	}, nil
}

func BuildGrantAwareSSHCommand(hostRef, endpointID, sshUser string, sshPort uint16, relayConfig *RelayConfig, directAddresses []string) (*SshCommand, error) {
	if !validHostReference(hostRef) {
		return nil, ErrInvalidHostReference
	}
	if !validEndpointID(endpointID) {
		return nil, ErrInvalidEndpointID
	}
	if !validSSHUser(sshUser) {
		return nil, ErrInvalidSSHUser
	}
	if sshPort == 0 {
		return nil, ErrInvalidSSHPort
	}
	proxyCmd, err := buildGrantAwareProxyCommand(hostRef, relayConfig, directAddresses)
	if err != nil {
		return nil, err
	}
	return &SshCommand{
		Program: "ssh",
		Args: []string{
			"-p", strconv.Itoa(int(sshPort)),
			"-o", fmt.Sprintf("ProxyCommand=%s", proxyCmd),
			fmt.Sprintf("%s@%s", sshUser, endpointID),
		},
	}, nil
}

func BuildStrictGrantAwareSSHCommand(ctlPath, hostRef, endpointID, sshUser string, sshPort uint16, fingerprint string, relayConfig *RelayConfig, directAddresses []string) (*SshCommand, error) {
	cmd, err := BuildGrantAwareSSHCommand(hostRef, endpointID, sshUser, sshPort, relayConfig, directAddresses)
	if err != nil {
		return nil, err
	}
	if ctlPath == "" || strings.ContainsAny(ctlPath, "\x00\r\n") {
		return nil, ErrInvalidCTLPath
	}
	if !ValidHostKeyFingerprint(fingerprint) {
		return nil, ErrInvalidHostKey
	}
	knownHostsCommand := fmt.Sprintf("KnownHostsCommand=%s known-host-key %s %%I %%f %%t %%K", shellQuote(ctlPath), fingerprint)
	strict := []string{
		"-o", "UserKnownHostsFile=none",
		"-o", "GlobalKnownHostsFile=none",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "CheckHostIP=no",
		"-o", "UpdateHostKeys=no",
		"-o", knownHostsCommand,
	}
	cmd.Args = append(cmd.Args[:len(cmd.Args)-1], append(strict, cmd.Args[len(cmd.Args)-1])...)
	return cmd, nil
}

func ValidHostKeyFingerprint(value string) bool {
	if !strings.HasPrefix(value, "SHA256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "SHA256:")
	if len(digest) < 40 || len(digest) > 44 {
		return false
	}
	for _, b := range []byte(digest) {
		if !isAlphanumeric(b) && b != '+' && b != '/' && b != '_' && b != '-' && b != '=' {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	if !strings.ContainsAny(value, " \t'\\\"") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func buildGrantAwareProxyCommand(hostRef string, relayConfig *RelayConfig, directAddresses []string) (string, error) {
	if !validHostReference(hostRef) {
		return "", ErrInvalidHostReference
	}
	command := fmt.Sprintf("ternalctl proxy %s %%h:%%p", hostRef)
	command, err := appendRouteArgs(command, relayConfig, directAddresses)
	if err != nil {
		return "", err
	}
	return command, nil
}

func BuildProxyCommand(relayConfig *RelayConfig) (string, error) {
	return buildProxyCommandWithRoutes(relayConfig, nil)
}

func buildProxyCommandWithRoutes(relayConfig *RelayConfig, directAddresses []string) (string, error) {
	command := "pigeons fly --stdio %h"
	return appendRouteArgs(command, relayConfig, directAddresses)
}

func appendRouteArgs(command string, relayConfig *RelayConfig, directAddresses []string) (string, error) {
	if err := relayConfig.Validate(); err != nil {
		return "", err
	}
	if len(directAddresses) == 0 && len(relayConfig.RelayURLs) == 0 && len(relayConfig.ExtraRelayURLs) == 0 {
		return "", ErrMissingRoute
	}
	for _, addr := range directAddresses {
		if !validDirectAddress(addr) {
			return "", ErrInvalidDirectAddress
		}
		command += " --direct-address " + addr
	}
	for _, url := range relayConfig.RelayURLs {
		command += " --relay-url " + url
	}
	for _, url := range relayConfig.ExtraRelayURLs {
		command += " --extra-relay-url " + url
	}
	return command, nil
}

func ValidateProxyCommand(value string) error {
	proxy := strings.TrimPrefix(value, "ProxyCommand=")
	if proxy == value {
		return ErrInvalidProxyCommand
	}
	parts := strings.Fields(proxy)
	routeStart := -1
	if len(parts) >= 4 && parts[0] == "pigeons" && parts[1] == "fly" && parts[2] == "--stdio" && parts[3] == "%h" {
		routeStart = 4
	} else if len(parts) >= 4 && parts[0] == "ternalctl" && parts[1] == "proxy" && validHostReference(parts[2]) && parts[3] == "%h:%p" {
		routeStart = 4
	}
	if routeStart < 0 {
		return ErrInvalidProxyCommand
	}
	return validateRouteArgs(parts[routeStart:])
}

func ValidateProxyInvocation(hostRef, endpointPort string, routeArgs []string) error {
	if !validHostReference(hostRef) {
		return ErrInvalidHostReference
	}
	idx := strings.LastIndex(endpointPort, ":")
	if idx < 0 {
		return ErrInvalidProxyCommand
	}
	endpointID := endpointPort[:idx]
	port := endpointPort[idx+1:]
	if !validEndpointID(endpointID) {
		return ErrInvalidEndpointID
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return ErrInvalidSSHPort
	}
	return validateRouteArgs(routeArgs)
}

func validateRouteArgs(parts []string) error {
	for i := 0; i < len(parts); i++ {
		flag := parts[i]
		if i+1 >= len(parts) {
			return ErrInvalidProxyCommand
		}
		value := parts[i+1]
		i++
		switch flag {
		case "--direct-address":
			if !validDirectAddress(value) {
				return ErrInvalidDirectAddress
			}
		case "--relay-url", "--extra-relay-url":
			if !validRelayURL(value) {
				return ErrInvalidRelayURL
			}
		default:
			return ErrInvalidProxyCommand
		}
	}
	return nil
}

func validDirectAddress(value string) bool {
	addr, err := net.ResolveTCPAddr("tcp", value)
	if err != nil {
		return false
	}
	return addr.Port != 0 && !addr.IP.IsUnspecified() && !addr.IP.IsMulticast()
}

func validEndpointID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, b := range []byte(value) {
		if !((b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') || (b >= '0' && b <= '9')) {
			return false
		}
	}
	return true
}

func validHostReference(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	if value[0] == '-' {
		return false
	}
	for _, b := range []byte(value) {
		if !isAlphanumeric(b) && b != '-' && b != '_' && b != '.' {
			return false
		}
	}
	return true
}

func validSSHUser(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	if value[0] == '-' {
		return false
	}
	for _, b := range []byte(value) {
		if !isAlphanumeric(b) && b != '_' && b != '-' && b != '.' {
			return false
		}
	}
	return true
}

func validRelayURL(value string) bool {
	if len(value) < 8 || len(value) > 512 {
		return false
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return false
	}
	for _, b := range []byte(value) {
		if !isAlphanumeric(b) && b != ':' && b != '/' && b != '.' && b != '_' && b != '-' {
			return false
		}
	}
	return true
}

func wildcardMatches(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	rest := value
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 && !strings.HasPrefix(pattern, "*") {
			if !strings.HasPrefix(rest, part) {
				return false
			}
			rest = rest[len(part):]
			continue
		}
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return strings.HasSuffix(pattern, "*") || strings.HasSuffix(value, parts[len(parts)-1])
}

func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func now() int64 {
	return time.Now().Unix()
}
