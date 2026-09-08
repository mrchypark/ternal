package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/deviceauth"
	"github.com/mrchypark/ternal/internal/store"
)

func TestDeviceStoreUnavailableIsNotRevocation(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	token, err := s.CreateManufacturingToken(ctx, "", &expires, "test")
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, fingerprint := strings.Repeat("a", 64), "SHA256:"+strings.Repeat("A", 43)
	device, err := s.EnrollDevice(ctx, token.Token, endpoint, "UNAVAILABLE-1", "test", fingerprint, base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := NewServer(s).Router()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	heartbeat, _ := json.Marshal(map[string]any{"serial": device.SerialNumber, "endpoint_id": endpoint, "ssh_host_key_fingerprint": fingerprint, "timestamp": now, "service_status": "healthy", "signature": deviceauth.Sign(private, deviceauth.HeartbeatPayload(device.SerialNumber, endpoint, fingerprint, now, "healthy", nil))})
	digest := strings.Repeat("0", 64)
	for _, tc := range []struct{ name, method, path, body, payload string }{
		{"heartbeat", http.MethodPost, "/agents/heartbeat", string(heartbeat), ""},
		{"keys", http.MethodGet, "/agents/authorized-keys?ssh_user=ops", "", deviceauth.AuthorizedKeysPayload(device.SerialNumber, endpoint, fingerprint, now, "ops")},
		{"ack", http.MethodPost, "/agents/authorized-keys/ack", `{"ssh_user":"ops","generation":1,"sha256":"` + digest + `"}`, deviceauth.AuthorizedKeysAckPayload(device.SerialNumber, endpoint, fingerprint, now, "ops", 1, digest)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Ternal-Device-Serial", device.SerialNumber)
			req.Header.Set("X-Ternal-Device-Endpoint-Id", endpoint)
			req.Header.Set("X-Ternal-Device-Ssh-Host-Key-Fingerprint", fingerprint)
			req.Header.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(now, 10))
			req.Header.Set("X-Ternal-Device-Signature", deviceauth.Sign(private, tc.payload))
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d, want 503", res.Code)
			}
		})
	}
}
