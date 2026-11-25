// Package api exposes KubeReel HTTP endpoints.
package api

import (
	"net/http"

	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

// Handler serves the HTTP API endpoints for providers, workflows, and artifacts.
type Handler struct {
	Workflows *workflow.Store
	Artifacts storage.Store
	Providers *provider.Manager
}

// Register mounts the API routes onto mux.
func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/providers", h.listProviders)
	mux.HandleFunc("POST /api/workflows", h.createWorkflow)
	mux.HandleFunc("GET /api/workflows/{id}", h.getWorkflow)
	mux.HandleFunc("GET /api/workflows/{id}/artifact", h.downloadArtifact)
}
