package main

import (
	"context"
	"errors"
	"fmt"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("ternal-api listening on http://%s\n", bind)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
