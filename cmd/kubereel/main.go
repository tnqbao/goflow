package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/a-h/templ"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/web"
)

func main() {
	if len(os.Args) > 2 || (len(os.Args) == 2 && os.Args[1] != "serve") {
		fmt.Fprintln(os.Stderr, "usage: kubereel [serve]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, config.Load()); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) (runErr error) {
	db, err := initialize(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, db.Close()) }()

	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           routes(db),
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("starting HTTP server", "address", cfg.Address)
	return serve(ctx, server)
}

func serve(ctx context.Context, server *http.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down server: %w", err)
		}
		err := <-stopped
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func initialize(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Database), 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return db, nil
}

func routes(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", templ.Handler(web.Home()))
	mux.Handle("GET /settings/providers", templ.Handler(web.Providers(false)))
	mux.Handle("POST /settings/providers/google-flow/connect", templ.Handler(web.Providers(true), templ.WithStatus(http.StatusNotImplemented)))
	mux.Handle("GET /static/", web.Assets())
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
