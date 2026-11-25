package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
)

func TestAuthAPIEndpointsAndCookieCSRF(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	authSvc := service.NewAuthService(repository.NewAuthRepository(db), config.Defaults().Auth)
	providerSvc := service.NewProviderService(provider.NewManager())
	workflowSvc := service.NewWorkflowService(repository.NewWorkflowRepository(db), nil, providerSvc)

	server := echo.New()
	server.Use(middleware.SecurityHeaders())
	(Router{
		Auth:      authSvc,
		Workflows: workflowSvc,
		Providers: providerSvc,
	}).Register(server)

	// 1. Status reports initialized=false before setup.
	statusResp := httptest.NewRecorder()
	server.ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil))
	if statusResp.Code != http.StatusOK || statusResp.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("initial status = %d %s", statusResp.Code, statusResp.Body.String())
	}
	var initialStatus service.AuthStatus
	if err := json.Unmarshal(statusResp.Body.Bytes(), &initialStatus); err != nil || initialStatus.Initialized || initialStatus.Authenticated {
		t.Fatalf("unexpected initial status: %+v (%v)", initialStatus, err)
	}

	// 2. Setup creates admin and returns Bearer token; duplicate setup returns 409 Conflict.
	setupReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"username":"admin","password":"strong-admin-password"}`))
	setupResp := httptest.NewRecorder()
	server.ServeHTTP(setupResp, setupReq)
	if setupResp.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", setupResp.Code, setupResp.Body.String())
	}

	var setupIssued service.IssuedAuth
	if err := json.Unmarshal(setupResp.Body.Bytes(), &setupIssued); err != nil || setupIssued.Token == "" {
		t.Fatalf("decode setup response: %+v, %v", setupIssued, err)
	}

	dupSetup := httptest.NewRecorder()
	server.ServeHTTP(dupSetup, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"username":"admin2","password":"strong-admin-password"}`)))
	if dupSetup.Code != http.StatusConflict {
		t.Fatalf("duplicate setup = %d, want 409", dupSetup.Code)
	}

	// 3. Login with wrong password returns 401; correct password returns 200.
	badLogin := httptest.NewRecorder()
	server.ServeHTTP(badLogin, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong-password-123"}`)))
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", badLogin.Code)
	}

	okLogin := httptest.NewRecorder()
	server.ServeHTTP(okLogin, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"strong-admin-password"}`)))
	if okLogin.Code != http.StatusOK {
		t.Fatalf("login = %d %s", okLogin.Code, okLogin.Body.String())
	}

	// 4. Create, list, and revoke personal API tokens using Bearer authentication.
	createTokReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/tokens", strings.NewReader(`{"name":"deploy-bot","expiresInSeconds":3600}`))
	createTokReq.Header.Set("Authorization", "Bearer "+setupIssued.Token)
	createTokResp := httptest.NewRecorder()
	server.ServeHTTP(createTokResp, createTokReq)
	if createTokResp.Code != http.StatusCreated {
		t.Fatalf("create token = %d %s", createTokResp.Code, createTokResp.Body.String())
	}

	var createdToken service.IssuedAuth
	if err := json.Unmarshal(createTokResp.Body.Bytes(), &createdToken); err != nil {
		t.Fatal(err)
	}

	listTokReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/tokens", nil)
	listTokReq.Header.Set("Authorization", "Bearer "+createdToken.Token)
	listTokResp := httptest.NewRecorder()
	server.ServeHTTP(listTokResp, listTokReq)
	if listTokResp.Code != http.StatusOK || !strings.Contains(listTokResp.Body.String(), "deploy-bot") {
		t.Fatalf("list tokens = %d %s", listTokResp.Code, listTokResp.Body.String())
	}

	delTokReq := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/tokens/"+createdToken.TokenID, nil)
	delTokReq.Header.Set("Authorization", "Bearer "+setupIssued.Token)
	delTokResp := httptest.NewRecorder()
	server.ServeHTTP(delTokResp, delTokReq)
	if delTokResp.Code != http.StatusNoContent {
		t.Fatalf("revoke token = %d", delTokResp.Code)
	}

	revokedMeReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	revokedMeReq.Header.Set("Authorization", "Bearer "+createdToken.Token)
	revokedMeResp := httptest.NewRecorder()
	server.ServeHTTP(revokedMeResp, revokedMeReq)
	if revokedMeResp.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token /me = %d, want 401", revokedMeResp.Code)
	}

	// 5. Cookie-authenticated API calls allow GET, reject unsafe POST without X-CSRF-Token (403), and allow POST with X-CSRF-Token.
	browserSession, _, err := authSvc.Login(context.Background(), "127.0.0.1", "admin", "strong-admin-password", repository.TokenKindSession)
	if err != nil {
		t.Fatal(err)
	}

	cookieGetReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	cookieGetReq.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: browserSession.Token})
	cookieGetResp := httptest.NewRecorder()
	server.ServeHTTP(cookieGetResp, cookieGetReq)
	if cookieGetResp.Code != http.StatusOK || !strings.Contains(cookieGetResp.Body.String(), browserSession.CSRFToken) {
		t.Fatalf("cookie GET /me = %d %s", cookieGetResp.Code, cookieGetResp.Body.String())
	}

	csrfMissingReq := httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(`{"prompt":"csrf test"}`))
	csrfMissingReq.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: browserSession.Token})
	csrfMissingResp := httptest.NewRecorder()
	server.ServeHTTP(csrfMissingResp, csrfMissingReq)
	if csrfMissingResp.Code != http.StatusForbidden {
		t.Fatalf("cookie POST without X-CSRF-Token = %d, want 403", csrfMissingResp.Code)
	}

	// Confused-deputy guard: malformed Authorization header must NOT fall back to a valid session cookie.
	badBearerWithCookie := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	badBearerWithCookie.Header.Set("Authorization", "Bearer invalid-token")
	badBearerWithCookie.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: browserSession.Token})
	badBearerResp := httptest.NewRecorder()
	server.ServeHTTP(badBearerResp, badBearerWithCookie)
	if badBearerResp.Code != http.StatusUnauthorized {
		t.Fatalf("bad Bearer with valid cookie = %d, want 401", badBearerResp.Code)
	}

	csrfValidReq := httptest.NewRequest(http.MethodPost, "/api/workflows", strings.NewReader(`{"prompt":"csrf valid"}`))
	csrfValidReq.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: browserSession.Token})
	csrfValidReq.Header.Set(middleware.CSRFHeaderName, browserSession.CSRFToken)
	csrfValidResp := httptest.NewRecorder()
	server.ServeHTTP(csrfValidResp, csrfValidReq)
	if csrfValidResp.Code != http.StatusAccepted {
		t.Fatalf("cookie POST with valid X-CSRF-Token = %d %s", csrfValidResp.Code, csrfValidResp.Body.String())
	}
}
