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
