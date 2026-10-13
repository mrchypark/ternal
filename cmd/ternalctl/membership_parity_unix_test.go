//go:build !windows

package main

import (
	"strings"
	"testing"

	"github.com/mrchypark/rhiza"
)

func TestPeerPublicKeyMatchesRhiza(t *testing.T) {
	for _, tt := range []struct {
		clusterID string
		nodeID    string
		token     string
	}{
		{"ternal-ha", "ternal-0", strings.Repeat("0", 64)},
		{"cluster/한글", "node-1", strings.Repeat("a", 32)},
		{"cluster\x00id", "node\x00id", strings.Repeat("z", 48)},
	} {
		if got, want := peerPublicKey(tt.clusterID, tt.nodeID, tt.token), memberPublicKey(rhiza.PeerPublicKey(tt.clusterID, tt.nodeID, tt.token)); got != want {
			t.Fatalf("peerPublicKey(%q, %q) = %x, want %x", tt.clusterID, tt.nodeID, got, want)
		}
	}
}
