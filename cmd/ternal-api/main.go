package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrchypark/ternal/internal/api"
	"github.com/mrchypark/ternal/internal/operatoridentity"
	"github.com/mrchypark/ternal/internal/store"
)

func main() {
	if err := runArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ternal-api:", err)
		os.Exit(1)
	}
}

func runArgs(args []string) error {
	if len(args) == 0 {
		return run()
	}
	if len(args) != 1 || (args[0] != "local-preflight" && args[0] != "local-restore") {
		return fmt.Errorf("usage: ternal-api [local-preflight|local-restore]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if args[0] == "local-restore" {
		return store.LocalRestoreFromEnv(ctx)
	}
	return store.LocalPreflightFromEnv(ctx)
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

func closeStorageOnReturn(result *error, storage io.Closer) {
	*result = errors.Join(*result, storage.Close())
}

func run() (err error) {
	// Operator-managed generation identity first: it must be in place before the
	// store reads its configuration, and an unadaptable token map must not start.
	if os.Getenv("TERNAL_DATA_LOCAL") != "1" {
		if err := operatoridentity.Apply(); err != nil {
			return err
		}
	}
	s, err := store.OpenFromEnv(context.Background())
	if err != nil {
		return err
	}
	defer closeStorageOnReturn(&err, s)

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
	if operatorBind := os.Getenv("TERNAL_OPERATOR_BIND"); operatorBind != "" {
		operatorListener, err := net.Listen("tcp", operatorBind)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return err
		}
		servers = append(servers, newHTTPServer(operatorBind, s.OperatorHandler()))
		listeners = append(listeners, operatorListener)
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
