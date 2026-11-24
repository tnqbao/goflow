// Package api exposes workflow operations over HTTP.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/workflow"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Workflows struct {
	Store     *workflow.Store
	DataDir   string
	Providers *provider.Manager
}

func (a Workflows) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/providers", a.providers)
	mux.HandleFunc("POST /api/workflows", a.create)
	mux.HandleFunc("GET /api/workflows/{id}", a.get)
	mux.HandleFunc("GET /api/workflows/{id}/artifact", a.artifact)
}

func (a Workflows) providers(w http.ResponseWriter, _ *http.Request) {
	if a.Providers == nil {
		http.Error(w, "provider manager unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, a.Providers.List())
}

func (a Workflows) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		Prompt   string `json:"prompt"`
		Provider string `json:"provider"`
	}
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if request.Provider == "" {
		request.Provider = "mock"
	}
	if a.Providers == nil || !a.Providers.Available(request.Provider) {
		http.Error(w, "provider unavailable", http.StatusBadRequest)
		return
	}
	item, err := a.Store.Create(r.Context(), request.Prompt)
	if errors.Is(err, workflow.ErrInvalid) {
		http.Error(w, "invalid prompt", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "unable to create workflow", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Location", "/api/workflows/"+item.ID)
	writeJSON(w, http.StatusAccepted, item)
}

func (a Workflows) get(w http.ResponseWriter, r *http.Request) {
	item, ok := a.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a Workflows) artifact(w http.ResponseWriter, r *http.Request) {
	item, ok := a.lookup(w, r)
	if !ok {
		return
	}
	if item.Status != "succeeded" {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}
	expected := filepath.Join("workflows", item.ID, "final.svg")
	if item.Result != expected {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}
	file := filepath.Join(a.DataDir, expected)
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Disposition", `attachment; filename="mock-scene.svg"`)
	http.ServeFile(w, r, file)
}

func (a Workflows) lookup(w http.ResponseWriter, r *http.Request) (workflow.Workflow, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return workflow.Workflow{}, false
	}
	item, err := a.Store.Get(r.Context(), id)
	if errors.Is(err, workflow.ErrNotFound) {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return workflow.Workflow{}, false
	}
	if err != nil {
		http.Error(w, "unable to read workflow", http.StatusInternalServerError)
		return workflow.Workflow{}, false
	}
	return item, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "unable to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		slog.Error("write workflow response", "error", err)
	}
}
