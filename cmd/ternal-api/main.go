package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrchypark/ternal/internal/api"
	"github.com/mrchypark/ternal/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ternal-api:", err)
		os.Exit(1)
	}
}

// newHTTPServer applies the single timeout policy for all listeners:
// headers fast, bodies bounded (1 MiB endpoints), handlers given room for
// upstream OIDC round trips. No endpoint streams, so WriteTimeout is safe.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func run() error {
	s, err := store.OpenFromEnv(context.Background())
	if err != nil {
		return err
	}
	defer s.Close()

	bind := os.Getenv("TERNAL_BIND")
	if bind == "" {
		bind = "127.0.0.1:3000"
	}
	apiServer := api.NewServer(s)
	if err := apiServer.ValidateRuntime(bind); err != nil {
		return err
	}
	server := newHTTPServer(bind, apiServer.Router())
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return err
	}
	servers := []*http.Server{server}
	listeners := []net.Listener{listener}
	if relayBind := os.Getenv("TERNAL_RELAY_BIND"); relayBind != "" {
		relayListener, err := net.Listen("tcp", relayBind)
		if err != nil {
			_ = listener.Close()
			return err
		}
		servers = append(servers, newHTTPServer(relayBind, apiServer.RelayRouter()))
		listeners = append(listeners, relayListener)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, len(servers))
	for i := range servers {
		fmt.Printf("ternal-api listening on http://%s\n", servers[i].Addr)
		go func(server *http.Server, listener net.Listener) {
			errCh <- server.Serve(listener)
		}(servers[i], listeners[i])
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		_ = server.Shutdown(shutdownCtx)
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
