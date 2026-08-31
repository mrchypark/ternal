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

func TestSignedDeviceHeartbeatAndAuthorizedKeys(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.Now().Add(time.Hour).Unix()
	manufacturing, err := s.CreateManufacturingToken(ctx, "", &expires)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	endpointID := strings.Repeat("a", 64)
	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	device, err := s.EnrollDevice(ctx, manufacturing.Token, endpointID, "TEST-000001", "test", fingerprint, base64.StdEncoding.EncodeToString(public), "ops", 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEYKd11nBOnZgxjuU5AtNj5UWnfHEZGdRjL4pxr9u16D test"
	if _, err := s.CreateSSHKey(ctx, "user-1", key, "SHA256:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessGrant(ctx, "request-1", "user-1", device.HostID, "ops", time.Now().Add(time.Minute).Unix(), ""); err != nil {
		t.Fatal(err)
	}

	router := NewServer(s).Router()
	discovery := &deviceauth.Discovery{RelayURLs: []string{"https://relay.example"}}
	unhealthyTimestamp := time.Now().Unix() - 1
	unhealthyPayload := deviceauth.HeartbeatPayload(device.SerialNumber, endpointID, fingerprint, unhealthyTimestamp, "unhealthy", discovery)
	unhealthyBody, _ := json.Marshal(map[string]any{
		"serial": device.SerialNumber, "endpoint_id": endpointID, "ssh_host_key_fingerprint": fingerprint,
		"timestamp": unhealthyTimestamp, "service_status": "unhealthy", "discovery": discovery,
		"signature": deviceauth.Sign(private, unhealthyPayload),
	})
	unhealthyReq := httptest.NewRequest(http.MethodPost, "/agents/heartbeat", strings.NewReader(string(unhealthyBody)))
	unhealthyReq.Header.Set("Content-Type", "application/json")
	unhealthyRes := httptest.NewRecorder()
	router.ServeHTTP(unhealthyRes, unhealthyReq)
	if unhealthyRes.Code != http.StatusOK {
		t.Fatalf("unhealthy heartbeat status=%d body=%s", unhealthyRes.Code, unhealthyRes.Body.String())
	}
	unhealthyAllowed, err := s.RelayEndpointAllowed(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if unhealthyAllowed {
		t.Fatal("unhealthy device heartbeat renewed relay admission")
	}

	timestamp := time.Now().Unix()
	payload := deviceauth.HeartbeatPayload(device.SerialNumber, endpointID, fingerprint, timestamp, "healthy", discovery)
	heartbeatBody, _ := json.Marshal(map[string]any{
		"serial": device.SerialNumber, "endpoint_id": endpointID, "ssh_host_key_fingerprint": fingerprint,
		"timestamp": timestamp, "service_status": "healthy", "discovery": discovery,
		"signature": deviceauth.Sign(private, payload),
	})
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/agents/heartbeat", strings.NewReader(string(heartbeatBody)))
	heartbeatReq.Header.Set("Content-Type", "application/json")
	heartbeatRes := httptest.NewRecorder()
	router.ServeHTTP(heartbeatRes, heartbeatReq)
	if heartbeatRes.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", heartbeatRes.Code, heartbeatRes.Body.String())
	}
	relayAllowed, err := s.RelayEndpointAllowed(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	otherRelayAllowed, err := s.RelayEndpointAllowed(ctx, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !relayAllowed || otherRelayAllowed {
		t.Fatalf("signed heartbeat relay admission: enrolled=%v other=%v", relayAllowed, otherRelayAllowed)
	}

	keysTime := time.Now().Unix()
	keysPayload := deviceauth.AuthorizedKeysPayload(device.SerialNumber, endpointID, fingerprint, keysTime, "ops")
	keysReq := httptest.NewRequest(http.MethodGet, "/agents/authorized-keys?ssh_user=ops", nil)
	keysReq.Header.Set("X-Ternal-Device-Serial", device.SerialNumber)
	keysReq.Header.Set("X-Ternal-Device-Endpoint-Id", endpointID)
	keysReq.Header.Set("X-Ternal-Device-Ssh-Host-Key-Fingerprint", fingerprint)
	keysReq.Header.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(keysTime, 10))
	keysReq.Header.Set("X-Ternal-Device-Signature", deviceauth.Sign(private, keysPayload))
	keysRes := httptest.NewRecorder()
	router.ServeHTTP(keysRes, keysReq)
	if keysRes.Code != http.StatusOK || keysRes.Body.String() != key+"\n" {
		t.Fatalf("keys status=%d body=%q", keysRes.Code, keysRes.Body.String())
	}

	stale := keysReq.Clone(context.Background())
	stale.Header = keysReq.Header.Clone()
	stale.Header.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10))
	staleRes := httptest.NewRecorder()
	router.ServeHTTP(staleRes, stale)
	if staleRes.Code != http.StatusUnauthorized {
		t.Fatalf("stale signed request status=%d", staleRes.Code)
	}
}
