package core

import (
	"errors"
	"strings"
	"testing"
)

func TestStrictGrantAwareSSHCommandPinsHostKeyAndRoutes(t *testing.T) {
	endpointID := strings.Repeat("a", 64)
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	cmd, err := BuildStrictGrantAwareSSHCommand(
		"/usr/local/bin/ternalctl", "host-1", endpointID, "ops", 22, fingerprint,
		&RelayConfig{RelayURLs: []string{"https://relay.example"}}, []string{"127.0.0.1:4444"},
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, required := range []string{
		"StrictHostKeyChecking=yes", "UserKnownHostsFile=none", "GlobalKnownHostsFile=none",
		"CheckHostIP=no", "UpdateHostKeys=no", "KnownHostsCommand=/usr/local/bin/ternalctl known-host-key " + fingerprint,
		"--direct-address 127.0.0.1:4444", "--relay-url https://relay.example",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %q", required, joined)
		}
	}
}

func TestEndpointIDOnlyRouteIsRejected(t *testing.T) {
	_, err := BuildGrantAwareSSHCommand("host-1", strings.Repeat("a", 64), "ops", 22, &RelayConfig{}, nil)
	if !errors.Is(err, ErrMissingRoute) {
		t.Fatalf("expected missing route error, got %v", err)
	}
}

func TestEndpointIDRequiresIrohV1HexIdentity(t *testing.T) {
	_, err := BuildGrantAwareSSHCommand("host-1", "endpoint-only", "ops", 22, &RelayConfig{RelayURLs: []string{"https://relay.example"}}, nil)
	if !errors.Is(err, ErrInvalidEndpointID) {
		t.Fatalf("expected invalid endpoint id, got %v", err)
	}
}

func TestPolicyPrincipalAcceptsGroupAndCustomClaim(t *testing.T) {
	host := &Host{Name: "edge-1", Tags: map[string]string{"site": "west"}}
	claims := &UserClaims{Groups: []string{"operators"}, CustomClaims: map[string][]string{"role": {"support"}}}
	for _, principal := range []string{"operators", "groups=operators", "role=support"} {
		policy := &Policy{Principal: principal, HostSelector: "tag:site=west"}
		if !PolicyAllows(claims, host, policy) {
			t.Fatalf("principal %q was not accepted", principal)
		}
	}
}

func TestDirectAddressRejectsShellInjection(t *testing.T) {
	malicious := []string{
		"[fe80::1%eth0]:22",
		"[fe80::1%x$(touch /tmp/pwned)]:22",
		"[fe80::1%x`id`]:22",
		"[fe80::1%x${IFS}]:22",
		"127.0.0.1:22;echo pwned",
		"127.0.0.1:22|id",
	}
	for _, addr := range malicious {
		if validDirectAddress(addr) {
			t.Errorf("validDirectAddress(%q) = true, want false", addr)
		}
		_, err := BuildGrantAwareSSHCommand("host-1", strings.Repeat("a", 64), "ops", 22, &RelayConfig{RelayURLs: []string{"https://relay.example"}}, []string{addr})
		if !errors.Is(err, ErrInvalidDirectAddress) {
			t.Errorf("BuildGrantAwareSSHCommand(%q) err = %v, want ErrInvalidDirectAddress", addr, err)
		}
	}
}

func TestDirectAddressAcceptsLiteralIPs(t *testing.T) {
	valid := []string{"127.0.0.1:4444", "192.168.1.20:22", "[::1]:22", "[2001:db8::1]:443"}
	for _, addr := range valid {
		if !validDirectAddress(addr) {
			t.Errorf("validDirectAddress(%q) = false, want true", addr)
		}
	}
}

func TestValidHostNameRejectsConfigInjection(t *testing.T) {
	malicious := []string{
		"",
		"has space",
		"tab\there",
		"new\nline",
		"carriage\rreturn",
		"wild*card",
		"question?mark",
		"bang!mark",
		"-leading-dash",
		"evil\n  ProxyCommand=evil",
		strings.Repeat("x", 129),
	}
	for _, name := range malicious {
		if ValidHostName(name) {
			t.Errorf("ValidHostName(%q) = true, want false", name)
		}
	}
	valid := []string{"edge-1", "TEST-000001", "IED-000001", "DUPLICATE", "host_1.example", "A"}
	for _, name := range valid {
		if !ValidHostName(name) {
			t.Errorf("ValidHostName(%q) = false, want true", name)
		}
	}
}

func TestProxyCommandQuotesGlobMetacharacters(t *testing.T) {
	addr := "[2001:db8::1]:443"
	cmd, err := BuildGrantAwareSSHCommand("host-1", strings.Repeat("a", 64), "ops", 22, &RelayConfig{}, []string{addr})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--direct-address '"+addr+"'") {
		t.Fatalf("IPv6 address is unquoted, so sh would glob-expand its brackets: %q", joined)
	}
	if err := ValidateProxyCommand("ProxyCommand=" + strings.TrimPrefix(strings.Join(cmd.Args, " "), "-o ProxyCommand=")); err != nil {
		// ValidateProxyCommand is exercised below with its exact input shape.
		_ = err
	}
	proxy := ""
	for i, arg := range cmd.Args {
		if arg == "ProxyCommand" || strings.HasPrefix(arg, "ProxyCommand=") {
			proxy = arg
			_ = i
		}
	}
	if proxy == "" {
		t.Fatal("no proxy command in args")
	}
	if err := ValidateProxyCommand(proxy); err != nil {
		t.Fatalf("quoted address rejected by the validator: %v", err)
	}
}
