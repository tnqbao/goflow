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

func TestRoutes(t *testing.T) {
	db := testDB(t)
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
			routes(db).ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))
			if recorder.Code != tt.status {
				t.Errorf("status = %d, want %d", recorder.Code, tt.status)
			}
		})
	}
}

func TestRunListenError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cfg := testConfig(t)
	cfg.Address = listener.Addr().String()
	if err := run(context.Background(), cfg); err == nil {
		t.Fatal("run succeeded on an occupied address")
	}
}

func TestServeStopsOnCancellation(t *testing.T) {
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
	if err := serve(ctx, server, time.Second); err != nil {
		t.Fatalf("serve after cancellation: %v", err)
	}
}

func TestServeClosesConnectionsAfterShutdownTimeout(t *testing.T) {
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
	go func() { result <- serve(ctx, server, 50*time.Millisecond) }()
	client := &http.Client{Timeout: 5 * time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := client.Get("http://" + <-address)
		if err == nil {
			response.Body.Close()
		}
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serve error = %v, want shutdown deadline exceeded", err)
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
	request := filepath.Join(dir, "request.json")
	scene := filepath.Join(dir, "scene.svg")
	final := filepath.Join(dir, "final.svg")
	if err := os.WriteFile(request, []byte(`{"prompt":"A scene"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"worker", "--input", request, "--output", scene},
		{"render", "--input", scene, "--output", final},
	} {
		if err := execute(context.Background(), args, config.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), []string{"worker"}, config.Config{}); err == nil {
		t.Fatal("worker accepted missing paths")
	}
}

func TestSingleExecutableCommands(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "kubereel")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}
	request := filepath.Join(dir, "request.json")
	scene := filepath.Join(dir, "scene.svg")
	result := filepath.Join(dir, "result.svg")
	if err := os.WriteFile(request, []byte(`{"prompt":"Single executable"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"worker", "--input", request, "--output", scene},
		{"render", "--input", scene, "--output", result},
	} {
		command := exec.Command(binary, args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s command: %v\n%s", args[0], err, output)
		}
	}
	data, err := os.ReadFile(result)
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
	handler := routes(testDB(t))
	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := home.Header().Get("Location"); got != "/workflows" {
		t.Errorf("home redirect = %q, want /workflows", got)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if !strings.Contains(response.Header().Get("Content-Type"), "text/css") {
		t.Errorf("content type = %q, want text/css", response.Header().Get("Content-Type"))
	}
	if !strings.Contains(response.Body.String(), "font-family") {
		t.Error("stylesheet missing font-family")
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{Address: "127.0.0.1:0", Database: filepath.Join(dir, "data", "kubereel.db")}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := initialize(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInitializePreservesData(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	db, err := initialize(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE saved (id INTEGER); INSERT INTO saved VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = initialize(ctx, cfg)
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

func TestInitializeFailure(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Dir(cfg.Database), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := initialize(context.Background(), cfg); err == nil {
		db.Close()
		t.Fatal("initialization succeeded with a file as data directory")
	}
}

func TestReadinessDatabaseFailure(t *testing.T) {
	db := testDB(t)
	handler := routes(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path   string
		status int
	}{{"/readyz", http.StatusServiceUnavailable}, {"/healthz", http.StatusOK}} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if recorder.Code != tt.status {
			t.Errorf("%s status = %d, want %d", tt.path, recorder.Code, tt.status)
		}
	}
}
