package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/a-h/templ"
	"github.com/tnqbao/kubereel/internal/api"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/web"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], config.Load()); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, cfg config.Config) error {
	if len(args) == 0 {
		return run(ctx, cfg)
	}
	if args[0] == "serve" && len(args) == 1 {
		return run(ctx, cfg)
	}
	if args[0] != "worker" && args[0] != "render" {
		return fmt.Errorf("usage: kubereel serve | worker --input PATH --output PATH | render --input PATH --output PATH")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	input := flags.String("input", "", "input artifact path")
	output := flags.String("output", "", "output artifact path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *output == "" {
		return fmt.Errorf("%s requires --input and --output", args[0])
	}
	if args[0] == "worker" {
		return mock.Generate(ctx, *input, *output)
	}
	return mock.Render(ctx, *input, *output)
}

func run(ctx context.Context, cfg config.Config) (runErr error) {
	db, err := initialize(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, db.Close()) }()
	store := workflow.NewStore(db)
	controller, err := controllerFromEnv(ctx, store, filepath.Dir(cfg.Database))
	if err != nil {
		return err
	}
	mux := routes(db)
	if controller != nil {
		api.Workflows{Store: store, DataDir: filepath.Dir(cfg.Database), Providers: provider.NewManager()}.Register(mux)
		registerWorkflowUI(mux, store)
		controllerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			controller.Run(controllerCtx)
		}()
		defer func() {
			cancel()
			<-done
		}()
	} else {
		mux.HandleFunc("GET /workflows", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Mock workflows require Kubernetes mode", http.StatusServiceUnavailable)
		})
	}

	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
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

func routes(db *sql.DB) *http.ServeMux {
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
