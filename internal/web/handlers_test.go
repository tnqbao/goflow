package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestMockWorkflowUI(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kubereel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	(WorkflowHandler{Store: workflow.NewStore(db)}).Register(mux)

	formResp := httptest.NewRecorder()
	mux.ServeHTTP(formResp, httptest.NewRequest(http.MethodGet, "/workflows", nil))
	if formResp.Code != http.StatusOK || !strings.Contains(formResp.Body.String(), "Run mock workflow") {
		t.Fatalf("form: %d %s", formResp.Code, formResp.Body.String())
	}

	values := url.Values{"prompt": []string{"<scene>"}}
	createReq := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(values.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	createdResp := httptest.NewRecorder()
	mux.ServeHTTP(createdResp, createReq)
	if createdResp.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", createdResp.Code)
	}

	statusResp := httptest.NewRecorder()
	mux.ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, createdResp.Header().Get("Location"), nil))
	if statusResp.Code != http.StatusOK || !strings.Contains(statusResp.Body.String(), "&lt;scene&gt;") {
		t.Fatalf("status: %d %s", statusResp.Code, statusResp.Body.String())
	}

	workflowID := strings.TrimPrefix(createdResp.Header().Get("Location"), "/workflows/")
	store := workflow.NewStore(db)
	if err := store.Transition(context.Background(), workflowID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(context.Background(), workflowID, "running", "succeeded", "workflows/"+workflowID+"/final.mp4", ""); err != nil {
		t.Fatal(err)
	}

	doneResp := httptest.NewRecorder()
	mux.ServeHTTP(doneResp, httptest.NewRequest(http.MethodGet, createdResp.Header().Get("Location"), nil))
	if doneResp.Code != http.StatusOK || !strings.Contains(doneResp.Body.String(), "Download mock MP4") || !strings.Contains(doneResp.Body.String(), "<video") {
		t.Fatalf("completed status: %d %s", doneResp.Code, doneResp.Body.String())
	}

	invalidReq := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader("prompt="))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rejectedResp := httptest.NewRecorder()
	mux.ServeHTTP(rejectedResp, invalidReq)
	if rejectedResp.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: %d", rejectedResp.Code)
	}
}
