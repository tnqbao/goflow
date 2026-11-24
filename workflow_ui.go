package main

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/tnqbao/kubereel/internal/web"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func registerWorkflowUI(mux *http.ServeMux, store *workflow.Store) {
	mux.Handle("GET /workflows", templ.Handler(web.MockWorkflowForm("")))
	mux.HandleFunc("POST /workflows", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		item, err := store.Create(r.Context(), r.PostForm.Get("prompt"))
		if errors.Is(err, workflow.ErrInvalid) {
			templ.Handler(web.MockWorkflowForm("Enter a scene description."), templ.WithStatus(http.StatusBadRequest)).ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, "unable to create workflow", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/workflows/"+item.ID, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		item, err := store.Get(r.Context(), r.PathValue("id"))
		if errors.Is(err, workflow.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "unable to read workflow", http.StatusInternalServerError)
			return
		}
		templ.Handler(web.MockWorkflowStatus(item)).ServeHTTP(w, r)
	})
}
