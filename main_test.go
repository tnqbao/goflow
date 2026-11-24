package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/settings/providers", http.StatusOK},
		{http.MethodPost, "/settings/providers/google-flow/connect", http.StatusNotImplemented},
		{http.MethodGet, "/settings/providers/google-flow/connect", http.StatusMethodNotAllowed},
		{http.MethodGet, "/static/app.css", http.StatusOK},
		{http.MethodGet, "/static/htmx.min.js", http.StatusOK},
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
	if err := serve(ctx, server); err != nil {
		t.Fatalf("serve after cancellation: %v", err)
	}
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

func TestHomeAssets(t *testing.T) {
	handler := routes(testDB(t))
	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"<h1>Projects</h1>", `href="#main-content"`, `id="main-content"`, "Your stories start here", `href="/static/app.css"`, `src="/static/htmx.min.js"`} {
		if !strings.Contains(home.Body.String(), want) {
			t.Errorf("home page missing %q", want)
		}
	}
	for _, tt := range []struct{ path, contentType, content string }{
		{"/static/app.css", "text/css", "font-family"},
		{"/static/htmx.min.js", "javascript", "htmx"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if !strings.Contains(response.Header().Get("Content-Type"), tt.contentType) {
				t.Errorf("content type = %q, want %q", response.Header().Get("Content-Type"), tt.contentType)
			}
			if !strings.Contains(response.Body.String(), tt.content) {
				t.Errorf("asset missing %q", tt.content)
			}
		})
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{Address: "127.0.0.1:0", Database: filepath.Join(dir, "data", "goflow.db")}
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

func TestProviderSelection(t *testing.T) {
	handler := routes(testDB(t))
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings/providers", nil))
	for _, want := range []string{
		`href="/settings/providers" aria-current="page"`,
		`<h2 id="google-flow-title">Google Flow</h2>`,
		`action="/settings/providers/google-flow/connect"`,
		`type="submit">Connect with Google</button>`,
		`<h2 id="seedance-title">Seedance</h2>`,
		`<h2 id="grok-title">Grok</h2>`,
		"Connection is not available yet.",
	} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("provider page missing %q", want)
		}
	}
	if got := strings.Count(page.Body.String(), `type="button" disabled`); got != 2 {
		t.Errorf("disabled provider buttons = %d, want 2", got)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/settings/providers/google-flow/connect", nil))
	if response.Code != http.StatusNotImplemented {
		t.Errorf("connect status = %d, want 501", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Google Flow connection is not available yet") {
		t.Error("connect response does not explain that connection is unavailable")
	}
}
