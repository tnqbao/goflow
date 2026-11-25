package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/tnqbao/kubereel/internal/workflow"
)

const (
	maxWorkflowRequestBytes = 16384
	defaultProviderID       = "mock"
)

var workflowIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type createWorkflowRequest struct {
	Prompt   string `json:"prompt"`
	Provider string `json:"provider"`
}

func (h Handler) createWorkflow(w http.ResponseWriter, r *http.Request) {
	var req createWorkflowRequest
	if err := decodeJSON(w, r, maxWorkflowRequestBytes, &req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Provider == "" {
		req.Provider = defaultProviderID
	}

	if h.Providers == nil || !h.Providers.Available(req.Provider) {
		http.Error(w, "provider unavailable", http.StatusBadRequest)
		return
	}

	created, err := h.Workflows.Create(r.Context(), req.Prompt)
	if errors.Is(err, workflow.ErrInvalid) {
		http.Error(w, "invalid prompt", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "unable to create workflow", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Location", "/api/workflows/"+created.ID)
	writeJSON(w, http.StatusAccepted, created)
}

func (h Handler) getWorkflow(w http.ResponseWriter, r *http.Request) {
	wf, ok := h.findWorkflow(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, wf)
}

func (h Handler) findWorkflow(w http.ResponseWriter, r *http.Request) (workflow.Workflow, bool) {
	id := r.PathValue("id")
	if !workflowIDPattern.MatchString(id) {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return workflow.Workflow{}, false
	}

	wf, err := h.Workflows.Get(r.Context(), id)
	if errors.Is(err, workflow.ErrNotFound) {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return workflow.Workflow{}, false
	}
	if err != nil {
		http.Error(w, "unable to read workflow", http.StatusInternalServerError)
		return workflow.Workflow{}, false
	}

	return wf, true
}
