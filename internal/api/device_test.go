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
	t.Setenv("TERNAL_DEV_HEADERS", "1")
	const relaySecret = "relay-secret-for-device-revocation-test"
	t.Setenv("TERNAL_RELAY_ACCESS_TOKEN", relaySecret)
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
	generation := keysRes.Header().Get("X-Ternal-Authorized-Keys-Generation")
	digest := keysRes.Header().Get("X-Ternal-Authorized-Keys-Sha256")
	ackTime := time.Now().Unix()
	ackBody := `{"ssh_user":"ops","generation":` + generation + `,"sha256":"` + digest + `"}`
	ackPayload := deviceauth.AuthorizedKeysAckPayload(device.SerialNumber, endpointID, fingerprint, ackTime, "ops", 1, digest)
	ackReq := httptest.NewRequest(http.MethodPost, "/agents/authorized-keys/ack", strings.NewReader(ackBody))
	ackReq.Header.Set("Content-Type", "application/json")
	ackReq.Header.Set("X-Ternal-Device-Serial", device.SerialNumber)
	ackReq.Header.Set("X-Ternal-Device-Endpoint-Id", endpointID)
	ackReq.Header.Set("X-Ternal-Device-Ssh-Host-Key-Fingerprint", fingerprint)
	ackReq.Header.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(ackTime, 10))
	ackReq.Header.Set("X-Ternal-Device-Signature", deviceauth.Sign(private, ackPayload))
	ackRes := httptest.NewRecorder()
	router.ServeHTTP(ackRes, ackReq)
	if ackRes.Code != http.StatusNoContent {
		t.Fatalf("keys acknowledgement status=%d body=%q", ackRes.Code, ackRes.Body.String())
	}
	replayed := httptest.NewRequest(http.MethodPost, "/agents/authorized-keys/ack", strings.NewReader(`{"ssh_user":"root","generation":`+generation+`,"sha256":"`+digest+`"}`))
	replayed.Header = ackReq.Header.Clone()
	replayedRes := httptest.NewRecorder()
	router.ServeHTTP(replayedRes, replayed)
	if replayedRes.Code != http.StatusUnauthorized {
		t.Fatalf("cross-user acknowledgement replay status=%d body=%q", replayedRes.Code, replayedRes.Body.String())
	}
	grants, err := s.ListAccessGrants(ctx)
	if err != nil || len(grants) != 1 || !grants[0].KeyInstalled {
		t.Fatalf("acknowledged grants = %#v, err=%v", grants, err)
	}
	clientEndpointID := strings.Repeat("b", 64)
	if _, err := s.CreateRelayAccessGrant(ctx, device.HostID, clientEndpointID, "user-1", 300); err != nil {
		t.Fatal(err)
	}

	stale := keysReq.Clone(context.Background())
	stale.Header = keysReq.Header.Clone()
	stale.Header.Set("X-Ternal-Device-Timestamp", strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10))
	staleRes := httptest.NewRecorder()
	router.ServeHTTP(staleRes, stale)
	if staleRes.Code != http.StatusUnauthorized {
		t.Fatalf("stale signed request status=%d", staleRes.Code)
	}

	if err := s.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	revokedHeartbeatReq := httptest.NewRequest(http.MethodPost, "/agents/heartbeat", strings.NewReader(string(heartbeatBody)))
	revokedHeartbeatReq.Header.Set("Content-Type", "application/json")
	revokedHeartbeat := httptest.NewRecorder()
	router.ServeHTTP(revokedHeartbeat, revokedHeartbeatReq)
	if revokedHeartbeat.Code != http.StatusUnauthorized {
		t.Fatalf("revoked heartbeat status=%d", revokedHeartbeat.Code)
	}

	for name, testCase := range map[string][3]string{
		"ssh grant":   {http.MethodPost, "/access/ssh", `{"host_id":"` + device.HostID + `","ssh_user":"ops"}`},
		"relay grant": {http.MethodPost, "/access/relay-grants", `{"host_id":"` + device.HostID + `","client_endpoint_id":"` + clientEndpointID + `","ttl":300}`},
		"discovery":   {http.MethodGet, "/access/discovery/" + device.HostID, ""},
	} {
		method, path, body := testCase[0], testCase[1], testCase[2]
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Ternal-User", "admin")
		req.Header.Set("X-Ternal-Groups", "ternal-admins")
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound {
			t.Errorf("%s on revoked host status=%d body=%s", name, res.Code, res.Body.String())
		}
	}
	relayReq := httptest.NewRequest(http.MethodPost, "/internal/iroh-relay/access", nil)
	relayReq.Header.Set("Authorization", "Bearer "+relaySecret)
	relayReq.Header.Set("X-Iroh-Nodeid", clientEndpointID)
	relayRes := httptest.NewRecorder()
	NewServer(s).RelayRouter().ServeHTTP(relayRes, relayReq)
	if relayRes.Code != http.StatusForbidden || relayRes.Body.String() != "false" {
		t.Fatalf("revoked relay callback status/body=%d %q", relayRes.Code, relayRes.Body.String())
	}
}
