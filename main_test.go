package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/config"
)

func TestBaseRoutes(t *testing.T) {
	db := openTestDatabase(t)

	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/", http.StatusSeeOther},
		{http.MethodGet, "/settings/providers", http.StatusNotFound},
		{http.MethodGet, "/static/app.css", http.StatusOK},
		{http.MethodGet, "/static/missing.css", http.StatusNotFound},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodGet, "/healthz/extra", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newBaseMux(db).ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))
			if recorder.Code != tt.status {
				t.Errorf("status = %d, want %d", recorder.Code, tt.status)
			}
		})
	}
}

func TestRunServerListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cfg := newTestConfig(t)
	cfg.Address = listener.Addr().String()

	if err := runServer(context.Background(), cfg); err == nil {
		t.Fatal("runServer succeeded on an occupied address")
	}
}

func TestServeHTTPStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	server := &http.Server{
		Addr: "127.0.0.1:0",
		BaseContext: func(net.Listener) context.Context {
			close(started)
			return context.Background()
		},
	}

	go func() {
		<-started
		cancel()
	}()

	if err := serveHTTP(ctx, server, time.Second); err != nil {
		t.Fatalf("serveHTTP after cancellation: %v", err)
	}
}

func TestServeHTTPClosesConnectionsAfterShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	address := make(chan string, 1)
	started := make(chan struct{})
	finished := make(chan struct{})

	server := &http.Server{
		Addr: "127.0.0.1:0",
		BaseContext: func(listener net.Listener) context.Context {
			address <- listener.Addr().String()
			return context.Background()
		},
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(finished)
		}),
	}

	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(ctx, server, 50*time.Millisecond)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := client.Get("http://" + <-address)
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	cancel()

	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serveHTTP error = %v, want shutdown deadline exceeded", err)
	}

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("active handler was not cancelled")
	}

	<-requestDone
}

func TestWorkerAndRenderCommands(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	scenePath := filepath.Join(dir, "scene.svg")
	finalPath := filepath.Join(dir, "final.svg")

	if err := os.WriteFile(requestPath, []byte(`{"prompt":"A scene"}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Database = filepath.Join(dir, "db")

	commands := [][]string{
		{"worker", "--input", requestPath, "--output", scenePath},
		{"render", "--input", scenePath, "--output", finalPath},
	}
	for _, args := range commands {
		if err := executeCommand(context.Background(), args, cfg); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(finalPath); err != nil {
		t.Fatal(err)
	}

	if err := executeCommand(context.Background(), []string{"worker"}, config.Config{}); err == nil {
		t.Fatal("worker accepted missing paths")
	}
}

func TestSingleExecutableCommands(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "kubereel")

	buildCmd := exec.Command("go", "build", "-o", binary, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}

	requestPath := filepath.Join(dir, "request.json")
	scenePath := filepath.Join(dir, "scene.svg")
	resultPath := filepath.Join(dir, "result.svg")

	if err := os.WriteFile(requestPath, []byte(`{"prompt":"Single executable"}`), 0600); err != nil {
		t.Fatal(err)
	}

	commands := [][]string{
		{"worker", "--input", requestPath, "--output", scenePath},
		{"render", "--input", scenePath, "--output", resultPath},
	}
	for _, args := range commands {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), config.EnvDataDir+"="+dir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s command: %v\n%s", args[0], err, output)
		}
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Single executable") {
		t.Fatal("rendered artifact does not contain the request")
	}

	if err := exec.Command(binary, "unknown").Run(); err == nil {
		t.Fatal("executable accepted an unknown command")
	}
}

func TestEmbeddedAssets(t *testing.T) {
	handler := newBaseMux(openTestDatabase(t))

	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := home.Header().Get("Location"); got != "/workflows" {
		t.Errorf("home redirect = %q, want /workflows", got)
	}

	stylesheet := httptest.NewRecorder()
	handler.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if !strings.Contains(stylesheet.Header().Get("Content-Type"), "text/css") {
		t.Errorf("content type = %q, want text/css", stylesheet.Header().Get("Content-Type"))
	}
	if !strings.Contains(stylesheet.Body.String(), "font-family") {
		t.Error("stylesheet missing font-family")
	}
}

func TestOpenDatabasePreservesData(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t)

	db, err := openDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE saved (id INTEGER); INSERT INTO saved VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var id int
	if err := db.QueryRowContext(ctx, `SELECT id FROM saved`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("saved id = %d, want 42", id)
	}
}

func TestOpenDatabaseFailure(t *testing.T) {
	cfg := newTestConfig(t)
	if err := os.WriteFile(filepath.Dir(cfg.Database), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}

	if db, err := openDatabase(context.Background(), cfg); err == nil {
		db.Close()
		t.Fatal("initialization succeeded with a file as data directory")
	}
}

func TestReadinessDatabaseFailure(t *testing.T) {
	db := openTestDatabase(t)
	handler := newBaseMux(db)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path   string
		status int
	}{
		{"/readyz", http.StatusServiceUnavailable},
		{"/healthz", http.StatusOK},
	}

	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if recorder.Code != tt.status {
			t.Errorf("%s status = %d, want %d", tt.path, recorder.Code, tt.status)
		}
	}
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Address = "127.0.0.1:0"
	cfg.Database = filepath.Join(dir, "data", "kubereel.db")

	return cfg
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	db, err := openDatabase(context.Background(), newTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}
