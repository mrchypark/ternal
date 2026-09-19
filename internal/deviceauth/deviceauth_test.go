package deviceauth

import (
	"math"
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

func TestAtomicWriteFilePersistsBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sub/file"
	if err := AtomicWriteFile(path, []byte("data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fsyncDir(dir + "/sub"); err != nil {
		t.Fatalf("parent dir sync = %v", err)
	}
}

func TestGenerateKeyRefusesOverwrite(t *testing.T) {
	path := t.TempDir() + "/device.key"
	first, err := GenerateKey(path)
	if err != nil || first == "" {
		t.Fatal(err)
	}
	if _, err := GenerateKey(path); err == nil {
		t.Fatal("second key generation overwrote the enrolled key")
	}
	if _, err := LoadPrivate(path); err != nil {
		t.Fatalf("original key unreadable after refused overwrite: %v", err)
	}
}

func TestFreshRejectsExtremeTimestamps(t *testing.T) {
	now := time.Now()
	for _, ts := range []int64{math.MinInt64, math.MaxInt64, math.MinInt64 + now.Unix()} {
		if Fresh(ts, now) {
			t.Fatalf("Fresh(%d) accepted an extreme timestamp", ts)
		}
	}
	if !Fresh(now.Unix(), now) || !Fresh(now.Unix()+60, now) || Fresh(now.Unix()+3600, now) {
		t.Fatal("Fresh misjudged in-window timestamps")
	}
}
