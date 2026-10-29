package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type failedStorageCloser struct{ err error }

func (c failedStorageCloser) Close() error { return c.err }

func TestStorageCloseFailurePreservesRunError(t *testing.T) {
	closeErr := errors.New("storage close failed")
	runErr := errors.New("server failed")
	for _, prior := range []error{nil, runErr} {
		result := prior
		closeStorageOnReturn(&result, failedStorageCloser{closeErr})
		if !errors.Is(result, closeErr) || (prior != nil && !errors.Is(result, prior)) {
			t.Fatalf("close result = %v, want close and prior errors", result)
		}
	}
	result := runErr
	closeStorageOnReturn(&result, failedStorageCloser{})
	if !errors.Is(result, runErr) {
		t.Fatalf("successful close lost original error: %v", result)
	}
}

func TestLocalPreflightCommandDoesNotRequireServerSecrets(t *testing.T) {
	t.Setenv("TERNAL_DATA_LOCAL", "0")
	t.Setenv("TERNAL_OIDC_ISSUER", "")
	t.Setenv("TERNAL_SESSION_KEY", "")
	for _, command := range []string{"local-preflight", "local-restore"} {
		if err := runArgs([]string{command}); err == nil || !strings.Contains(err.Error(), "TERNAL_DATA_LOCAL=1") {
			t.Fatalf("%s dispatch = %v", command, err)
		}
	}
	if err := runArgs([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("unknown command = %v", err)
	}
}

func TestHTTPServersBoundBodyAndWriteDeadlines(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadTimeout < time.Second || server.WriteTimeout < time.Second {
		t.Fatalf("server timeouts = read %v write %v, want bounded body-read and response-write policies", server.ReadTimeout, server.WriteTimeout)
	}
	if server.ReadHeaderTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("server kept header/idle defaults cleared: %+v", server)
	}
}
