package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrchypark/ternal/internal/store"
)

func TestBatchLinkedOneUseTokensAllocateSerialsThroughAPI(t *testing.T) {
	t.Setenv("TERNAL_DEV_HEADERS", "1") // Unit boundary only; release qualification uses real OIDC.
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	router := NewServer(s).Router()
	request := func(path string, body any, want int, result any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Ternal-User", "admin@example.com")
		req.Header.Set("X-Ternal-Groups", "ternal-admins")
		req.Header.Set("X-CSRF-Token", "dev-csrf")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s status=%d want=%d", path, response.Code, want)
		}
		if result != nil {
			if err := json.Unmarshal(response.Body.Bytes(), result); err != nil {
				t.Fatal(err)
			}
		}
	}
	var batch struct {
		Item store.ManufacturingBatch `json:"item"`
	}
	request("/manufacturing/batches", map[string]any{"name": "one-use-batch", "serial_prefix": "ONCE", "max_devices": 2, "expires_at": time.Now().Add(time.Hour).Unix()}, http.StatusCreated, &batch)
	var tokens [2]store.ManufacturingToken
	for i := range tokens {
		request("/manufacturing/tokens", map[string]any{"batch_id": batch.Item.ID}, http.StatusCreated, &tokens[i])
	}
	for i, token := range tokens {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		enrollment := map[string]any{"token": token.Token, "endpoint_id": strings.Repeat(fmt.Sprint(i+1), 64), "device_public_key": base64.StdEncoding.EncodeToString(public), "ssh_host_key_fingerprint": "SHA256:" + strings.Repeat("A", 43), "ssh_user": "ops"}
		var device store.Device
		request("/manufacturing/enroll", enrollment, http.StatusCreated, &device)
		if device.SerialNumber != fmt.Sprintf("ONCE-%06d", i+1) {
			t.Fatalf("server serial=%q", device.SerialNumber)
		}
		enrollment["endpoint_id"] = strings.Repeat(fmt.Sprint(i+3), 64)
		request("/manufacturing/enroll", enrollment, http.StatusBadRequest, nil)
	}
	batches, err := s.ListManufacturingBatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || batches[0].UsedCount != 2 || batches[0].Status != "closed" {
		t.Fatalf("batch did not close at its limit: %+v", batches)
	}
	remaining, err := s.ListManufacturingTokens(context.Background())
	if err != nil || len(remaining) != 0 {
		t.Fatalf("one-use tokens remain: count=%d err=%v", len(remaining), err)
	}
}
