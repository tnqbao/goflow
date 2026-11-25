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

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestWorkflowEndpoints(t *testing.T) {
	dir := t.TempDir()

	db, err := database.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()

	authSvc := service.NewAuthService(repository.NewAuthRepository(db), config.Defaults().Auth)
	providerSvc := service.NewProviderService(provider.NewManager())
	workflowSvc := service.NewWorkflowService(repository.NewWorkflowRepository(db), artifacts, providerSvc)

	server := echo.New()
	(Router{
		Auth:      authSvc,
		Workflows: workflowSvc,
		Providers: providerSvc,
	}).Register(server)

	unauthProviders := httptest.NewRecorder()
	server.ServeHTTP(unauthProviders, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if unauthProviders.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated provider list: %d", unauthProviders.Code)
	}

	issued, err := authSvc.Setup(context.Background(), "admin", "workflow-test-password-123", "", repository.TokenKindAPIToken)
	if err != nil {
		t.Fatal(err)
	}
	authedReq := func(method, target, body string) *http.Request {
		var reader *strings.Reader
		if body != "" {
			reader = strings.NewReader(body)
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, reader)
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		return req
	}

	providersResp := httptest.NewRecorder()
	server.ServeHTTP(providersResp, authedReq(http.MethodGet, "/api/providers", ""))
	if providersResp.Code != http.StatusOK || strings.TrimSpace(providersResp.Body.String()) != `[{"id":"mock","available":true}]` {
		t.Fatalf("provider list: %d %s", providersResp.Code, providersResp.Body.String())
	}

	createResp := httptest.NewRecorder()
	server.ServeHTTP(createResp, authedReq(http.MethodPost, "/api/workflows", `{"prompt":"hello"}`))
	if createResp.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", createResp.Code, createResp.Body.String())
	}

	var created workflow.Workflow
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "queued" || created.ID == "" {
		t.Fatalf("unexpected workflow: %+v", created)
	}

	getResp := httptest.NewRecorder()
	server.ServeHTTP(getResp, authedReq(http.MethodGet, "/api/workflows/"+created.ID, ""))
	if getResp.Code != http.StatusOK {
		t.Fatalf("get: %d", getResp.Code)
	}

	pendingArtifactResp := httptest.NewRecorder()
	server.ServeHTTP(pendingArtifactResp, authedReq(http.MethodGet, "/api/workflows/"+created.ID+"/artifact", ""))
	if pendingArtifactResp.Code != http.StatusNotFound {
		t.Fatalf("pending artifact: %d", pendingArtifactResp.Code)
	}

	if err := workflowSvc.Transition(context.Background(), created.ID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}

	resultPath := filepath.Join("workflows", created.ID, "final.mp4")
	if err := os.MkdirAll(filepath.Join(dir, "workflows", created.ID), 0700); err != nil {
		t.Fatal(err)
	}
	mp4Stub := []byte("\x00\x00\x00\x14ftypisom\x00\x00\x02\x00mp41")
	if err := os.WriteFile(filepath.Join(dir, resultPath), mp4Stub, 0600); err != nil {
		t.Fatal(err)
	}
	if err := workflowSvc.Transition(context.Background(), created.ID, "running", "succeeded", resultPath, ""); err != nil {
		t.Fatal(err)
	}

	unauthArtifact := httptest.NewRecorder()
	server.ServeHTTP(unauthArtifact, httptest.NewRequest(http.MethodGet, "/api/workflows/"+created.ID+"/artifact", nil))
	if unauthArtifact.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated artifact download: %d", unauthArtifact.Code)
	}

	artifactResp := httptest.NewRecorder()
	server.ServeHTTP(artifactResp, authedReq(http.MethodGet, "/api/workflows/"+created.ID+"/artifact", ""))
	if artifactResp.Code != http.StatusOK || artifactResp.Header().Get("Content-Type") != "video/mp4" || artifactResp.Body.String() != string(mp4Stub) {
		t.Fatalf("artifact: %d %s %q", artifactResp.Code, artifactResp.Header().Get("Content-Type"), artifactResp.Body.String())
	}

	rangeReq := authedReq(http.MethodGet, "/api/workflows/"+created.ID+"/artifact", "")
	rangeReq.Header.Set("Range", "bytes=4-7")
	rangeResp := httptest.NewRecorder()
	server.ServeHTTP(rangeResp, rangeReq)
	if rangeResp.Code != http.StatusPartialContent || rangeResp.Body.String() != "ftyp" {
		t.Fatalf("artifact range: %d %s", rangeResp.Code, rangeResp.Body.String())
	}

	if err := os.Remove(filepath.Join(dir, resultPath)); err != nil {
		t.Fatal(err)
	}

	outsideFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, resultPath)); err != nil {
		t.Fatal(err)
	}

	blockedResp := httptest.NewRecorder()
	server.ServeHTTP(blockedResp, authedReq(http.MethodGet, "/api/workflows/"+created.ID+"/artifact", ""))
	if blockedResp.Code != http.StatusNotFound {
		t.Fatalf("symlink artifact: %d", blockedResp.Code)
	}

	invalidPayloads := []string{
		`{"prompt":""}`,
		`{"prompt":"hi","extra":1}`,
		`{"prompt":"hi"}{}`,
		`{"prompt":"hi","provider":"unknown"}`,
	}
	for _, payload := range invalidPayloads {
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, authedReq(http.MethodPost, "/api/workflows", payload))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid request %s: %d", payload, resp.Code)
		}
	}
}
