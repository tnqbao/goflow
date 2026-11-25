package api

import (
	"log/slog"
	"net/http"
	"path/filepath"
)

const downloadSVGFilename = "mock-scene.svg"

func (h Handler) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	wf, ok := h.findWorkflow(w, r)
	if !ok {
		return
	}

	expectedPath := filepath.Join("workflows", wf.ID, "final.svg")
	if wf.Status != "succeeded" || wf.Result != expectedPath {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}

	if h.Artifacts == nil {
		http.Error(w, "artifact storage unavailable", http.StatusServiceUnavailable)
		return
	}

	object, err := h.Artifacts.Open(r.Context(), filepath.ToSlash(expectedPath))
	if err != nil {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}
	defer func() {
		if closeErr := object.Close(); closeErr != nil {
			slog.Error("close artifact", "error", closeErr)
		}
	}()

	info, err := object.Stat()
	if err != nil {
		http.Error(w, "artifact not available", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+downloadSVGFilename+`"`)
	http.ServeContent(w, r, downloadSVGFilename, info.ModTime(), object)
}
