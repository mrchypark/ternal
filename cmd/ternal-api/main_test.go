package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServersBoundBodyAndWriteDeadlines(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadTimeout < time.Second || server.WriteTimeout < time.Second {
		t.Fatalf("server timeouts = read %v write %v, want bounded body-read and response-write policies", server.ReadTimeout, server.WriteTimeout)
	}
	if server.ReadHeaderTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("server kept header/idle defaults cleared: %+v", server)
	}
}
