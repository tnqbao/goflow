package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
)

const maxAuthFormBytes = 4096

// AuthController serves the Web UI initial setup, sign-in, and sign-out routes.
type AuthController struct {
	Service *service.AuthService
}

// Register mounts Web UI authentication routes onto e.
func (ctrl AuthController) Register(e *echo.Echo) {
	e.GET("/setup", ctrl.ShowSetup, noStoreWebCache)
	e.POST("/setup", ctrl.SubmitSetup, noStoreWebCache)
	e.GET("/login", ctrl.ShowLogin, noStoreWebCache)
	e.POST("/login", ctrl.SubmitLogin, noStoreWebCache)
	e.POST("/logout", ctrl.Logout, noStoreWebCache, RequireWebAuth(ctrl.Service))
}

func noStoreWebCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		return next(c)
	}
}

// RequireWebAuth ensures initial setup is completed and a valid browser session cookie is present.
func RequireWebAuth(authSvc *service.AuthService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if authSvc == nil {
				return c.String(http.StatusServiceUnavailable, "authentication service unavailable\n")
			}

			ctx := c.Request().Context()
			initialized, err := authSvc.IsInitialized(ctx)
			if err != nil {
				return c.String(http.StatusServiceUnavailable, "unable to check authentication state\n")
			}
			if !initialized {
				return c.Redirect(http.StatusSeeOther, "/setup")
			}

			cookie, cookieErr := c.Cookie(service.SessionCookieName)
			if cookieErr != nil || strings.TrimSpace(cookie.Value) == "" {
				return redirectUnauthenticatedWeb(c)
			}

			principal, authErr := authSvc.Authenticate(ctx, cookie.Value)
			if authErr != nil {
				middleware.ClearSessionCookie(c)
				return redirectUnauthenticatedWeb(c)
			}

			principal.ViaCookie = true
			middleware.SetPrincipal(c, principal)
			return next(c)
		}
	}
}

func redirectUnauthenticatedWeb(c echo.Context) error {
	if c.Request().Method != http.MethodGet && c.Request().Method != http.MethodHead {
		return c.Redirect(http.StatusSeeOther, "/login")
	}

	nextTarget := sanitizeNextPath(c.Request().URL.RequestURI())
	return c.Redirect(http.StatusSeeOther, "/login?next="+url.QueryEscape(nextTarget))
}

// ShowSetup renders the one-time initial administrator setup page.
func (ctrl AuthController) ShowSetup(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "authentication service unavailable\n")
	}

	initialized, err := ctrl.Service.IsInitialized(c.Request().Context())
	if err != nil {
		return c.String(http.StatusServiceUnavailable, "unable to check setup state\n")
	}
	if initialized {
		return redirectInitializedUser(c, ctrl.Service)
	}

	return renderWithinLayout(c, http.StatusOK, "Initial setup", setupPageTmpl, setupViewData{
		Username:           "admin",
		SetupTokenRequired: ctrl.Service.RequiresSetupToken(),
	})
}

// SubmitSetup processes the one-time initial administrator creation form.
func (ctrl AuthController) SubmitSetup(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "authentication service unavailable\n")
	}
	if !middleware.IsSameOriginBrowserPost(c.Request()) {
		return c.String(http.StatusForbidden, "cross-origin form submission rejected\n")
	}

	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxAuthFormBytes)
	if err := req.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form\n")
	}

	username := req.PostForm.Get("username")
	password := req.PostForm.Get("password")
	confirm := req.PostForm.Get("confirm_password")
	setupToken := req.PostForm.Get("setup_token")

	if password != confirm {
		return renderWithinLayout(c, http.StatusBadRequest, "Initial setup", setupPageTmpl, setupViewData{
			Username:           username,
			Message:            "Passwords do not match.",
			SetupTokenRequired: ctrl.Service.RequiresSetupToken(),
		})
	}

	issued, err := ctrl.Service.Setup(req.Context(), username, password, setupToken, repository.TokenKindSession)
	if errors.Is(err, service.ErrAlreadyInitialized) {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	if errors.Is(err, service.ErrInvalidSetupToken) {
		return renderWithinLayout(c, http.StatusForbidden, "Initial setup", setupPageTmpl, setupViewData{
			Username:           username,
			Message:            "Invalid setup token.",
			SetupTokenRequired: ctrl.Service.RequiresSetupToken(),
		})
	}
	if errors.Is(err, service.ErrInvalidAuthInput) {
		return renderWithinLayout(c, http.StatusBadRequest, "Initial setup", setupPageTmpl, setupViewData{
			Username:           username,
			Message:            err.Error(),
			SetupTokenRequired: ctrl.Service.RequiresSetupToken(),
		})
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to complete setup\n")
	}

	middleware.SetSessionCookie(c, issued.Token, ctrl.Service.SessionTTL())
	return c.Redirect(http.StatusSeeOther, "/workflows")
}

// ShowLogin renders the administrator sign-in form.
func (ctrl AuthController) ShowLogin(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "authentication service unavailable\n")
	}

	initialized, err := ctrl.Service.IsInitialized(c.Request().Context())
	if err != nil {
		return c.String(http.StatusServiceUnavailable, "unable to check authentication state\n")
	}
	if !initialized {
		return c.Redirect(http.StatusSeeOther, "/setup")
	}
	if hasValidBrowserSession(c, ctrl.Service) {
		return c.Redirect(http.StatusSeeOther, sanitizeNextPath(c.QueryParam("next")))
	}

	return renderWithinLayout(c, http.StatusOK, "Sign in", loginPageTmpl, loginViewData{
		Next: sanitizeNextPath(c.QueryParam("next")),
	})
}

// SubmitLogin authenticates administrator credentials and sets the browser session cookie.
func (ctrl AuthController) SubmitLogin(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "authentication service unavailable\n")
	}
	if !middleware.IsSameOriginBrowserPost(c.Request()) {
		return c.String(http.StatusForbidden, "cross-origin form submission rejected\n")
	}

	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxAuthFormBytes)
	if err := req.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form\n")
	}

	username := req.PostForm.Get("username")
	password := req.PostForm.Get("password")
	nextTarget := sanitizeNextPath(req.PostForm.Get("next"))

	issued, retryAfter, err := ctrl.Service.Login(
		req.Context(),
		middleware.ClientIPKey(req),
		username,
		password,
		repository.TokenKindSession,
	)
	if errors.Is(err, service.ErrSetupRequired) {
		return c.Redirect(http.StatusSeeOther, "/setup")
	}
	if errors.Is(err, service.ErrLoginRateLimited) {
		seconds := int(retryAfter.Seconds()) + 1
		c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
		return renderWithinLayout(c, http.StatusTooManyRequests, "Sign in", loginPageTmpl, loginViewData{
			Username: username,
			Next:     nextTarget,
			Message:  "Too many failed sign-in attempts. Please wait and try again.",
		})
	}
	if errors.Is(err, service.ErrInvalidCredentials) {
		return renderWithinLayout(c, http.StatusUnauthorized, "Sign in", loginPageTmpl, loginViewData{
			Username: username,
			Next:     nextTarget,
			Message:  "Invalid username or password.",
		})
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to sign in\n")
	}

	middleware.SetSessionCookie(c, issued.Token, ctrl.Service.SessionTTL())
	return c.Redirect(http.StatusSeeOther, nextTarget)
}

// Logout verifies the CSRF token, revokes the active session, and clears the session cookie.
func (ctrl AuthController) Logout(c echo.Context) error {
	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.Redirect(http.StatusSeeOther, "/login")
	}

	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxAuthFormBytes)
	if err := req.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form\n")
	}

	csrfToken := req.PostForm.Get("csrf_token")
	if csrfToken == "" {
		csrfToken = req.Header.Get(middleware.CSRFHeaderName)
	}
	if !ctrl.Service.VerifyCSRF(principal, csrfToken) {
		return c.String(http.StatusForbidden, "invalid CSRF token\n")
	}

	if cookie, err := c.Cookie(service.SessionCookieName); err == nil {
		_ = ctrl.Service.Logout(req.Context(), cookie.Value)
	}

	middleware.ClearSessionCookie(c)
	return c.Redirect(http.StatusSeeOther, "/login")
}

func redirectInitializedUser(c echo.Context, authSvc *service.AuthService) error {
	if hasValidBrowserSession(c, authSvc) {
		return c.Redirect(http.StatusSeeOther, "/workflows")
	}

	return c.Redirect(http.StatusSeeOther, "/login")
}

func hasValidBrowserSession(c echo.Context, authSvc *service.AuthService) bool {
	cookie, err := c.Cookie(service.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}

	_, authErr := authSvc.Authenticate(c.Request().Context(), cookie.Value)
	return authErr == nil
}

func sanitizeNextPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return "/workflows"
	}
	if strings.ContainsAny(trimmed, "\\\r\n\x00") {
		return "/workflows"
	}
	if strings.HasPrefix(trimmed, "/login") || strings.HasPrefix(trimmed, "/setup") || strings.HasPrefix(trimmed, "/logout") {
		return "/workflows"
	}

	return trimmed
}
