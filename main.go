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

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api"
	"github.com/tnqbao/kubereel/internal/api/controller"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/executor"
	"github.com/tnqbao/kubereel/internal/kubernetes"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/web"
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
		return executor.Execute(ctx, artifacts, inputKey, outputKey)
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

	authSvc := service.NewAuthService(repository.NewAuthRepository(db), cfg.Auth)
	if err := authSvc.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap authentication: %w", err)
	}

	providerSvc := service.NewProviderService(provider.NewManager())
	workflowRepo := repository.NewWorkflowRepository(db)
	workflowSvc := service.NewWorkflowService(workflowRepo, artifacts, providerSvc)

	operator, err := kubernetes.NewControllerFromCluster(ctx, cfg, workflowSvc, artifacts)
	if err != nil {
		return err
	}

	router := newBaseEcho(db)
	(web.AuthController{Service: authSvc}).Register(router)
	if operator != nil {
		registerClusterControllers(router, authSvc, workflowSvc, providerSvc, service.NewProviderManagement(operator.ProviderRepository), service.NewWorkflowDefinitions(operator.WorkflowDefinitions),
			service.NewExecutions(operator.ExecutionRepository, operator.WorkflowDefinitions),
			service.NewArtifacts(operator.ExecutionRepository, artifacts), operator.Schedules)
		stopOperator := startBackgroundController(ctx, operator)
		defer stopOperator()
	} else {
		registerStandaloneControllers(router, authSvc)
	}

	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	slog.Info("starting HTTP server", "address", cfg.Address)
	return serveHTTP(ctx, server, shutdownTimeout)
}

func registerClusterControllers(
	e *echo.Echo,
	auth *service.AuthService,
	workflows *service.WorkflowService,
	providers *service.ProviderService,
	management *service.ProviderManagement,
	definitions *service.WorkflowDefinitions,
	executions *service.Executions,
	artifacts *service.Artifacts,
	schedules *service.Schedules,
) {
	(api.Router{
		Auth:                auth,
		Workflows:           workflows,
		Providers:           providers,
		ProviderManagement:  management,
		WorkflowDefinitions: definitions,
		Executions:          executions,
		Artifacts:           artifacts,
		Schedules:           schedules,
	}).Register(e)

	(web.WorkflowController{
		Auth:    auth,
		Service: workflows,
	}).Register(e)
}

func registerStandaloneControllers(e *echo.Echo, auth *service.AuthService) {
	(controller.AuthController{Service: auth}).Register(e)
	e.GET("/workflows", func(c echo.Context) error {
		return c.String(http.StatusServiceUnavailable, "Mock workflows require Kubernetes mode\n")
	}, web.RequireWebAuth(auth))
}

func startBackgroundController(ctx context.Context, operator *kubernetes.Controller) func() {
	controllerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		operator.Run(controllerCtx)
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

func newBaseEcho(db *sql.DB) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.SecurityHeaders())

	(controller.SystemController{DB: db}).Register(e)
	return e
}
