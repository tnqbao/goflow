package api

import "net/http"

func (h Handler) listProviders(w http.ResponseWriter, _ *http.Request) {
	if h.Providers == nil {
		http.Error(w, "provider manager unavailable", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, http.StatusOK, h.Providers.List())
}
