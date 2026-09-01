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
	server := &http.Server{
		Addr:              bind,
		Handler:           apiServer.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
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
		servers = append(servers, &http.Server{
			Addr:              relayBind,
			Handler:           apiServer.RelayRouter(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		})
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
