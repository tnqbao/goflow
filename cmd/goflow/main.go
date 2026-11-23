package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/a-h/templ"
	"github.com/tnqbao/goflow/internal/config"
	"github.com/tnqbao/goflow/internal/database"
	"github.com/tnqbao/goflow/internal/web"
)

func main() {
	if err := run(context.Background(), config.Load()); err != nil {
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
	return server.ListenAndServe()
}

func initialize(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Database), 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.BrowserData, 0700); err != nil {
		return nil, fmt.Errorf("create browser directory: %w", err)
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
