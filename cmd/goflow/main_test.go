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

	"github.com/tnqbao/goflow/internal/config"
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
	return config.Config{Address: "127.0.0.1:0", Database: filepath.Join(dir, "data", "goflow.db"), BrowserData: filepath.Join(dir, "data", "browser")}
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
	info, err := os.Stat(cfg.BrowserData)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Errorf("browser directory mode = %s", info.Mode())
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
