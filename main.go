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

	"github.com/tnqbao/kubereel/internal/api"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/kubernetes"
	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/web"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	if err := executeCommand(ctx, os.Args[1:], cfg); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}

func executeCommand(ctx context.Context, args []string, cfg config.Config) error {
	if len(args) == 0 || (args[0] == "serve" && len(args) == 1) {
		return runServer(ctx, cfg)
	}

	command := args[0]
	if command != kubernetes.StageWorker && command != kubernetes.StageRender {
		return fmt.Errorf("usage: kubereel serve | worker --input PATH --output PATH | render --input PATH --output PATH")
	}

	inputPath, outputPath, err := parseTaskFlags(command, args[1:])
	if err != nil {
		return err
	}

	return runTask(ctx, command, inputPath, outputPath, cfg)
}

func parseTaskFlags(command string, args []string) (string, string, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	inputPath := flags.String("input", "", "input artifact path")
	outputPath := flags.String("output", "", "output artifact path")

	if err := flags.Parse(args); err != nil {
		return "", "", err
	}

	if flags.NArg() != 0 || *inputPath == "" || *outputPath == "" {
		return "", "", fmt.Errorf("%s requires --input and --output", command)
	}

	return *inputPath, *outputPath, nil
}

func runTask(ctx context.Context, command, inputPath, outputPath string, cfg config.Config) (runErr error) {
	if cfg.Database == "" {
		return fmt.Errorf("artifact data directory is required")
	}

	artifacts, err := storage.NewFilesystem(filepath.Dir(cfg.Database), cfg.ArtifactMaxBytes, cfg.ArtifactRetention)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, artifacts.Close())
	}()

	inputKey, err := artifacts.Key(inputPath)
	if err != nil {
		return fmt.Errorf("input artifact: %w", err)
	}

	outputKey, err := artifacts.Key(outputPath)
	if err != nil {
		return fmt.Errorf("output artifact: %w", err)
	}

	if command == kubernetes.StageWorker {
		return mock.Execute(ctx, artifacts, inputKey, outputKey)
	}

	return renderer.New().RenderFile(ctx, artifacts, inputKey, outputKey)
}

func runServer(ctx context.Context, cfg config.Config) (runErr error) {
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, db.Close())
	}()

	artifacts, err := storage.NewFilesystem(filepath.Dir(cfg.Database), cfg.ArtifactMaxBytes, cfg.ArtifactRetention)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, artifacts.Close())
	}()

	store := workflow.NewStore(db)
	controller, err := kubernetes.NewControllerFromCluster(ctx, cfg, store, artifacts)
	if err != nil {
		return err
	}

	mux := newBaseMux(db)
	if controller != nil {
		registerClusterRoutes(mux, store, artifacts)
		stopController := startBackgroundController(ctx, controller)
		defer stopController()
	} else {
		registerStandaloneFallback(mux)
	}

	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	slog.Info("starting HTTP server", "address", cfg.Address)
	return serveHTTP(ctx, server, shutdownTimeout)
}

func registerClusterRoutes(mux *http.ServeMux, store *workflow.Store, artifacts storage.Store) {
	(api.Handler{
		Workflows: store,
		Artifacts: artifacts,
		Providers: provider.NewManager(),
	}).Register(mux)

	(web.WorkflowHandler{Store: store}).Register(mux)
}

func registerStandaloneFallback(mux *http.ServeMux) {
	mux.HandleFunc("GET /workflows", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Mock workflows require Kubernetes mode", http.StatusServiceUnavailable)
	})
}

func startBackgroundController(ctx context.Context, controller *kubernetes.Controller) func() {
	controllerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		controller.Run(controllerCtx)
	}()

	return func() {
		cancel()
		<-done
	}
}

func serveHTTP(ctx context.Context, server *http.Server, gracePeriod time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	stopped := make(chan error, 1)
	go func() {
		stopped <- server.ListenAndServe()
	}()

	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		return shutdownServer(server, stopped, gracePeriod)
	}
}

func shutdownServer(server *http.Server, stopped <-chan error, gracePeriod time.Duration) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	shutdownErr := server.Shutdown(shutdownCtx)

	var closeErr error
	if shutdownErr != nil {
		closeErr = server.Close()
	}

	serveErr := <-stopped
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}

	if shutdownErr != nil {
		return errors.Join(fmt.Errorf("shut down server: %w", shutdownErr), closeErr, serveErr)
	}

	return serveErr
}

func openDatabase(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Database), 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}

	return db, nil
}

func newBaseMux(db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/workflows", http.StatusSeeOther)
	})

	mux.Handle("GET /static/", web.Assets())
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /readyz", handleReadiness(db))

	return mux
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func handleReadiness(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
