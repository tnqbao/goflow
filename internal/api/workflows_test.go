package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestWorkflowEndpoints(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := workflow.NewStore(db)
	mux := http.NewServeMux()
	(Workflows{Store: store, DataDir: dir, Providers: provider.NewManager()}).Register(mux)
	providers := httptest.NewRecorder()
	mux.ServeHTTP(providers, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if providers.Code != http.StatusOK || strings.TrimSpace(providers.Body.String()) != `[{"id":"mock","available":true}]` {
		t.Fatalf("provider list: %d %s", providers.Code, providers.Body.String())
	}
	request := httptest.NewRecorder()
	mux.ServeHTTP(request, httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(`{"prompt":"hello"}`)))
	if request.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", request.Code, request.Body.String())
	}
	var item workflow.Workflow
	if err := json.Unmarshal(request.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != "queued" || item.ID == "" {
		t.Fatalf("unexpected item: %+v", item)
	}
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/workflows/"+item.ID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get: %d", get.Code)
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/workflows/"+item.ID+"/artifact", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("pending artifact: %d", missing.Code)
	}
	if err := store.Transition(context.Background(), item.ID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join("workflows", item.ID, "final.svg")
	if err := os.MkdirAll(filepath.Join(dir, "workflows", item.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, result), []byte("<svg/>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(context.Background(), item.ID, "running", "succeeded", result, ""); err != nil {
		t.Fatal(err)
	}
	artifact := httptest.NewRecorder()
	mux.ServeHTTP(artifact, httptest.NewRequest(http.MethodGet, "/api/workflows/"+item.ID+"/artifact", nil))
	if artifact.Code != http.StatusOK || artifact.Body.String() != "<svg/>" {
		t.Fatalf("artifact: %d %s", artifact.Code, artifact.Body.String())
	}
	for _, body := range []string{`{"prompt":""}`, `{"prompt":"hi","extra":1}`, `{"prompt":"hi"}{}`, `{"prompt":"hi","provider":"unknown"}`} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %s: %d", body, response.Code)
		}
	}
}
