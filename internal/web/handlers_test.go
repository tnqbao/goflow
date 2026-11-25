package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/config"
	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
)

var csrfInputPattern = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

func TestWebAuthSetupLoginCSRFAndWorkflowUI(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kubereel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	authSvc := service.NewAuthService(repository.NewAuthRepository(db), config.Defaults().Auth)
	workflowSvc := service.NewWorkflowService(
		repository.NewWorkflowRepository(db),
		nil,
		service.NewProviderService(provider.NewManager()),
	)

	server := echo.New()
	(AuthController{Service: authSvc}).Register(server)
	(WorkflowController{Auth: authSvc, Service: workflowSvc}).Register(server)

	// 1. Uninitialized visits redirect to /setup.
	uninitResp := httptest.NewRecorder()
	server.ServeHTTP(uninitResp, httptest.NewRequest(http.MethodGet, "/workflows", nil))
	if uninitResp.Code != http.StatusSeeOther || uninitResp.Header().Get("Location") != "/setup" {
		t.Fatalf("uninitialized /workflows = %d %q, want 303 /setup", uninitResp.Code, uninitResp.Header().Get("Location"))
	}

	setupFormResp := httptest.NewRecorder()
	server.ServeHTTP(setupFormResp, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if setupFormResp.Code != http.StatusOK || !strings.Contains(setupFormResp.Body.String(), "Create administrator") {
		t.Fatalf("GET /setup = %d %s", setupFormResp.Code, setupFormResp.Body.String())
	}

	// Cross-site POST /setup is rejected.
	crossSiteSetup := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader("username=admin"))
	crossSiteSetup.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossSiteSetup.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteResp := httptest.NewRecorder()
	server.ServeHTTP(crossSiteResp, crossSiteSetup)
	if crossSiteResp.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST /setup = %d, want 403", crossSiteResp.Code)
	}

	// 2. Complete initial administrator setup.
	setupValues := url.Values{
		"username":         {"admin"},
		"password":         {"ui-admin-password-123"},
		"confirm_password": {"ui-admin-password-123"},
	}
	setupPost := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(setupValues.Encode()))
	setupPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setupDone := httptest.NewRecorder()
	server.ServeHTTP(setupDone, setupPost)
	if setupDone.Code != http.StatusSeeOther || setupDone.Header().Get("Location") != "/workflows" {
		t.Fatalf("POST /setup = %d %q", setupDone.Code, setupDone.Header().Get("Location"))
	}

	sessionCookie := extractCookie(t, setupDone.Result().Cookies(), service.SessionCookieName)
	if !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected cookie attributes: %+v", sessionCookie)
	}

	// 3. Unauthenticated visit after setup redirects to /login?next=%2Fworkflows.
	unauthResp := httptest.NewRecorder()
	server.ServeHTTP(unauthResp, httptest.NewRequest(http.MethodGet, "/workflows", nil))
	if unauthResp.Code != http.StatusSeeOther || unauthResp.Header().Get("Location") != "/login?next=%2Fworkflows" {
		t.Fatalf("unauthenticated /workflows = %d %q", unauthResp.Code, unauthResp.Header().Get("Location"))
	}

	// Open-redirect protection on POST /login.
	loginValues := url.Values{
		"username": {"admin"},
		"password": {"ui-admin-password-123"},
		"next":     {"//evil.example.com/phish"},
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginValues.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp := httptest.NewRecorder()
	server.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusSeeOther || loginResp.Header().Get("Location") != "/workflows" {
		t.Fatalf("POST /login redirect = %d %q, want 303 /workflows", loginResp.Code, loginResp.Header().Get("Location"))
	}
	sessionCookie = extractCookie(t, loginResp.Result().Cookies(), service.SessionCookieName)

	// 4. Authenticated GET /workflows renders CSRF token; POST without CSRF fails with 403.
	formReq := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	formReq.AddCookie(sessionCookie)
	formResp := httptest.NewRecorder()
	server.ServeHTTP(formResp, formReq)
	if formResp.Code != http.StatusOK || !strings.Contains(formResp.Body.String(), "Run mock workflow") {
		t.Fatalf("form: %d %s", formResp.Code, formResp.Body.String())
	}

	matches := csrfInputPattern.FindStringSubmatch(formResp.Body.String())
	if len(matches) < 2 || matches[1] == "" {
		t.Fatalf("missing csrf_token hidden input in form: %s", formResp.Body.String())
	}
	csrfToken := matches[1]

	noCSRFReq := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader("prompt=missing+csrf"))
	noCSRFReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noCSRFReq.AddCookie(sessionCookie)
	noCSRFResp := httptest.NewRecorder()
	server.ServeHTTP(noCSRFResp, noCSRFReq)
	if noCSRFResp.Code != http.StatusForbidden {
		t.Fatalf("POST /workflows without CSRF = %d, want 403", noCSRFResp.Code)
	}

	values := url.Values{
		"prompt":     {"<scene>"},
		"csrf_token": {csrfToken},
	}
	createReq := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(values.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.AddCookie(sessionCookie)

	createdResp := httptest.NewRecorder()
	server.ServeHTTP(createdResp, createReq)
	if createdResp.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", createdResp.Code, createdResp.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, createdResp.Header().Get("Location"), nil)
	statusReq.AddCookie(sessionCookie)
	statusResp := httptest.NewRecorder()
	server.ServeHTTP(statusResp, statusReq)
	if statusResp.Code != http.StatusOK || !strings.Contains(statusResp.Body.String(), "&lt;scene&gt;") {
		t.Fatalf("status: %d %s", statusResp.Code, statusResp.Body.String())
	}

	workflowID := strings.TrimPrefix(createdResp.Header().Get("Location"), "/workflows/")
	if err := workflowSvc.Transition(context.Background(), workflowID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := workflowSvc.Transition(context.Background(), workflowID, "running", "succeeded", "workflows/"+workflowID+"/final.mp4", ""); err != nil {
		t.Fatal(err)
	}

	doneReq := httptest.NewRequest(http.MethodGet, createdResp.Header().Get("Location"), nil)
	doneReq.AddCookie(sessionCookie)
	doneResp := httptest.NewRecorder()
	server.ServeHTTP(doneResp, doneReq)
	if doneResp.Code != http.StatusOK || !strings.Contains(doneResp.Body.String(), "Download mock MP4") || !strings.Contains(doneResp.Body.String(), "<video") {
		t.Fatalf("completed status: %d %s", doneResp.Code, doneResp.Body.String())
	}

	invalidForm := url.Values{"prompt": {""}, "csrf_token": {csrfToken}}
	invalidReq := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(invalidForm.Encode()))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidReq.AddCookie(sessionCookie)

	rejectedResp := httptest.NewRecorder()
	server.ServeHTTP(rejectedResp, invalidReq)
	if rejectedResp.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: %d", rejectedResp.Code)
	}

	// 5. POST /logout revokes the session.
	logoutForm := url.Values{"csrf_token": {csrfToken}}
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(logoutForm.Encode()))
	logoutReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutReq.AddCookie(sessionCookie)
	logoutResp := httptest.NewRecorder()
	server.ServeHTTP(logoutResp, logoutReq)
	if logoutResp.Code != http.StatusSeeOther || logoutResp.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout = %d %q", logoutResp.Code, logoutResp.Header().Get("Location"))
	}

	afterLogoutReq := httptest.NewRequest(http.MethodGet, "/workflows", nil)
	afterLogoutReq.AddCookie(sessionCookie)
	afterLogoutResp := httptest.NewRecorder()
	server.ServeHTTP(afterLogoutResp, afterLogoutReq)
	if afterLogoutResp.Code != http.StatusSeeOther || !strings.HasPrefix(afterLogoutResp.Header().Get("Location"), "/login") {
		t.Fatalf("GET /workflows with revoked cookie = %d %q", afterLogoutResp.Code, afterLogoutResp.Header().Get("Location"))
	}
}

func extractCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, item := range cookies {
		if item.Name == name {
			return item
		}
	}

	t.Fatalf("cookie %q not found", name)
	return nil
}
