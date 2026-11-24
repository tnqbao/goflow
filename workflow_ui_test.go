package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestMockWorkflowUI(t *testing.T) {
	store := workflow.NewStore(testDB(t))
	mux := http.NewServeMux()
	registerWorkflowUI(mux, store)
	form := httptest.NewRecorder()
	mux.ServeHTTP(form, httptest.NewRequest(http.MethodGet, "/workflows", nil))
	if form.Code != http.StatusOK || !strings.Contains(form.Body.String(), "Run mock workflow") {
		t.Fatalf("form: %d %s", form.Code, form.Body.String())
	}
	values := url.Values{"prompt": []string{"<scene>"}}
	req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, req)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", created.Code)
	}
	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, created.Header().Get("Location"), nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "&lt;scene&gt;") {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader("prompt="))
	invalid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rejected := httptest.NewRecorder()
	mux.ServeHTTP(rejected, invalid)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: %d", rejected.Code)
	}
}
