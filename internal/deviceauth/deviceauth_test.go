package deviceauth

import (
	"strings"
	"testing"
	"time"
)

func TestSignedPayloadAndFreshness(t *testing.T) {
	path := t.TempDir() + "/device.key"
	public, err := GenerateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	private, err := LoadPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	payload := HeartbeatPayload("serial", strings.Repeat("a", 64), "SHA256:"+strings.Repeat("A", 43), 100, "healthy", &Discovery{RelayURLs: []string{"https://relay.example"}})
	signature := Sign(private, payload)
	if err := Verify(public, payload, signature); err != nil {
		t.Fatal(err)
	}
	if err := Verify(public, payload+"x", signature); err == nil {
		t.Fatal("tampered payload accepted")
	}
	if !Fresh(100, time.Unix(399, 0)) || Fresh(100, time.Unix(401, 0)) {
		t.Fatal("clock-skew boundary incorrect")
	}
}
