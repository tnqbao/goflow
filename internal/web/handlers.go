package web

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const maxFormBodyBytes = 16384

// WorkflowHandler serves the HTML pages for the Mock workflow prototype.
type WorkflowHandler struct {
	Store *workflow.Store
}

// Register mounts the workflow UI routes onto mux.
func (h WorkflowHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /workflows", templ.Handler(MockWorkflowForm("")))
	mux.HandleFunc("POST /workflows", h.submitForm)
	mux.HandleFunc("GET /workflows/{id}", h.showStatus)
}

func (h WorkflowHandler) submitForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	created, err := h.Store.Create(r.Context(), r.PostForm.Get("prompt"))
	if errors.Is(err, workflow.ErrInvalid) {
		form := MockWorkflowForm("Enter a scene description.")
		templ.Handler(form, templ.WithStatus(http.StatusBadRequest)).ServeHTTP(w, r)
		return
	}
	if err != nil {
		http.Error(w, "unable to create workflow", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workflows/"+created.ID, http.StatusSeeOther)
}

func (h WorkflowHandler) showStatus(w http.ResponseWriter, r *http.Request) {
	wf, err := h.Store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "unable to read workflow", http.StatusInternalServerError)
		return
	}

	templ.Handler(MockWorkflowStatus(wf)).ServeHTTP(w, r)
}
