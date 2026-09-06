package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionRevokedCanceledReadDoesNotWaitForWriter(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.SessionRevoked(ctx, "test-session")
		done <- err
	}()
	select {
	case err := <-done:
		s.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled read returned %v", err)
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		<-done
		t.Fatal("canceled revocation read waited for unrelated writer")
	}
}
