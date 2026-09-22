// Command ternal-anchor serves the external recovery anchor over HTTPS.
//
// It is deployed separately from the API: the operator activates the anchor
// while the recovered StatefulSet is still unready, so this process must not
// open Ternal's replicated store, wait for application readiness, or depend on
// the voters' Service.  It owns no state either; the transition lives in the
// trust-anchor ConfigMap, which is why a restart cannot lose one.
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

	"github.com/mrchypark/ternal/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ternal-anchor:", err)
		os.Exit(1)
	}
}

func run() error {
	// The base context outlives any single request: an activation that a client
	// abandons keeps its service-owned budget and is cancelled only by shutdown.
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler, err := store.OpenAnchorService(base)
	if err != nil {
		return err
	}
	certFile, keyFile := os.Getenv("TERNAL_ANCHOR_TLS_CERT_FILE"), os.Getenv("TERNAL_ANCHOR_TLS_KEY_FILE")
	if certFile == "" || keyFile == "" {
		return fmt.Errorf("TERNAL_ANCHOR_TLS_CERT_FILE and TERNAL_ANCHOR_TLS_KEY_FILE are required")
	}
	bind := os.Getenv("TERNAL_ANCHOR_BIND")
	if bind == "" {
		bind = "0.0.0.0:9191"
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: handler,
		// Activation is bounded by the service, not by the transport, so the
		// write deadline has to leave room for the full budget.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      store.AnchorActivationBudget + 30*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ServeTLS(listener, certFile, keyFile) }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-base.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	return nil
}
