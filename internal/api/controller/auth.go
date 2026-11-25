package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
)

const maxAuthRequestBytes = 4096

type setupAuthRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	SetupToken string `json:"setupToken"`
}

type loginAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type createAPITokenRequest struct {
	Name             string `json:"name"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
}

type meResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
	CSRFToken string    `json:"csrfToken,omitempty"`
}

// AuthController exposes initial administrator setup, sign-in, session inspection, and API token endpoints.
type AuthController struct {
	Service *service.AuthService
}

// Register mounts public and authenticated /api/v1/auth endpoints onto e.
func (ctrl AuthController) Register(e *echo.Echo) {
	publicGroup := e.Group("/api/v1/auth", noStoreCache)
	publicGroup.GET("/status", ctrl.Status)
	publicGroup.POST("/setup", ctrl.Setup)
	publicGroup.POST("/login", ctrl.Login)

	protectedGroup := e.Group("/api/v1/auth", noStoreCache, middleware.RequireAPIAuth(ctrl.Service))
	protectedGroup.GET("/me", ctrl.Me)
	protectedGroup.POST("/logout", ctrl.Logout)
	protectedGroup.GET("/tokens", ctrl.ListTokens)
	protectedGroup.POST("/tokens", ctrl.CreateToken)
	protectedGroup.DELETE("/tokens/:id", ctrl.RevokeToken)
}

func noStoreCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		return next(c)
	}
}

// Status reports whether initial setup is completed and whether the caller holds a valid credential.
func (ctrl AuthController) Status(c echo.Context) error {
	if ctrl.Service == nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{
			Error:   "service_unavailable",
			Message: "authentication service unavailable",
		})
	}

	rawToken, viaCookie, _ := middleware.ExtractCredential(c.Request())
	status, err := ctrl.Service.Status(c.Request().Context(), rawToken, viaCookie)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{
			Error:   "service_unavailable",
			Message: "unable to query authentication status",
		})
	}

	return c.JSON(http.StatusOK, status)
}

// Setup creates the single initial administrator account and issues an initial API token.
func (ctrl AuthController) Setup(c echo.Context) error {
	if ctrl.Service == nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{
			Error:   "service_unavailable",
			Message: "authentication service unavailable",
		})
	}

	var req setupAuthRequest
	if err := decodeStrictJSON(c.Response(), c.Request(), maxAuthRequestBytes, &req); err != nil {
		return c.JSON(http.StatusBadRequest, middleware.APIError{
			Error:   "invalid_request",
			Message: "invalid JSON request body",
		})
	}

	setupToken := strings.TrimSpace(req.SetupToken)
	if setupToken == "" {
		setupToken = strings.TrimSpace(c.Request().Header.Get(middleware.SetupTokenHeader))
	}

	issued, err := ctrl.Service.Setup(
		c.Request().Context(),
		req.Username,
		req.Password,
		setupToken,
		repository.TokenKindAPIToken,
	)
	if err != nil {
		return writeSetupError(c, err)
	}

	return c.JSON(http.StatusCreated, issued)
}

func writeSetupError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidSetupToken):
		return c.JSON(http.StatusForbidden, middleware.APIError{
			Error:   "invalid_setup_token",
			Message: err.Error(),
		})
	case errors.Is(err, service.ErrAlreadyInitialized):
		return c.JSON(http.StatusConflict, middleware.APIError{
			Error:   "already_initialized",
			Message: err.Error(),
		})
	case errors.Is(err, service.ErrInvalidAuthInput):
		return c.JSON(http.StatusBadRequest, middleware.APIError{
			Error:   "invalid_input",
			Message: err.Error(),
		})
	default:
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to complete initial setup",
		})
	}
}

// Login authenticates administrator credentials and returns a Bearer API token.
func (ctrl AuthController) Login(c echo.Context) error {
	if ctrl.Service == nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{
			Error:   "service_unavailable",
			Message: "authentication service unavailable",
		})
	}

	var req loginAuthRequest
	if err := decodeStrictJSON(c.Response(), c.Request(), maxAuthRequestBytes, &req); err != nil {
		return c.JSON(http.StatusBadRequest, middleware.APIError{
			Error:   "invalid_request",
			Message: "invalid JSON request body",
		})
	}

	clientKey := middleware.ClientIPKey(c.Request())
	issued, retryAfter, err := ctrl.Service.Login(
		c.Request().Context(),
		clientKey,
		req.Username,
		req.Password,
		repository.TokenKindAPIToken,
	)
	if err != nil {
		return writeLoginError(c, err, retryAfter)
	}

	return c.JSON(http.StatusOK, issued)
}

func writeLoginError(c echo.Context, err error, retryAfter time.Duration) error {
	switch {
	case errors.Is(err, service.ErrLoginRateLimited):
		seconds := int(retryAfter.Seconds()) + 1
		c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
		return c.JSON(http.StatusTooManyRequests, middleware.APIError{
			Error:   "rate_limited",
			Message: err.Error(),
		})
	case errors.Is(err, service.ErrSetupRequired):
		return c.JSON(http.StatusConflict, middleware.APIError{
			Error:   "setup_required",
			Message: err.Error(),
		})
	case errors.Is(err, service.ErrInvalidCredentials):
		c.Response().Header().Set("WWW-Authenticate", `Bearer realm="kubereel"`)
		return c.JSON(http.StatusUnauthorized, middleware.APIError{
			Error:   "invalid_credentials",
			Message: err.Error(),
		})
	default:
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to authenticate",
		})
	}
}

// Me returns the profile of the currently authenticated caller.
func (ctrl AuthController) Me(c echo.Context) error {
	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, middleware.APIError{
			Error:   "unauthorized",
			Message: "authentication required",
		})
	}

	resp := meResponse{
		ID:        principal.UserID,
		Username:  principal.Username,
		CreatedAt: principal.CreatedAt,
	}
	if principal.ViaCookie {
		resp.CSRFToken = principal.CSRFToken
	}

	return c.JSON(http.StatusOK, resp)
}

// Logout revokes the active Bearer token or browser session cookie.
func (ctrl AuthController) Logout(c echo.Context) error {
	rawToken, viaCookie, ok := middleware.ExtractCredential(c.Request())
	if !ok {
		return c.NoContent(http.StatusNoContent)
	}

	if err := ctrl.Service.Logout(c.Request().Context(), rawToken); err != nil {
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to revoke credential",
		})
	}
	if viaCookie {
		middleware.ClearSessionCookie(c)
	}

	return c.NoContent(http.StatusNoContent)
}

// ListTokens returns metadata for active personal API tokens owned by the authenticated user.
func (ctrl AuthController) ListTokens(c echo.Context) error {
	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, middleware.APIError{
			Error:   "unauthorized",
			Message: "authentication required",
		})
	}

	items, err := ctrl.Service.ListAPITokens(c.Request().Context(), principal.UserID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to list API tokens",
		})
	}

	return c.JSON(http.StatusOK, items)
}

// CreateToken mints a new named personal API token for the authenticated user.
func (ctrl AuthController) CreateToken(c echo.Context) error {
	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, middleware.APIError{
			Error:   "unauthorized",
			Message: "authentication required",
		})
	}

	var req createAPITokenRequest
	if err := decodeStrictJSON(c.Response(), c.Request(), maxAuthRequestBytes, &req); err != nil {
		return c.JSON(http.StatusBadRequest, middleware.APIError{
			Error:   "invalid_request",
			Message: "invalid JSON request body",
		})
	}

	issued, err := ctrl.Service.CreateAPIToken(c.Request().Context(), principal, req.Name, req.ExpiresInSeconds)
	if errors.Is(err, service.ErrInvalidAuthInput) {
		return c.JSON(http.StatusBadRequest, middleware.APIError{
			Error:   "invalid_input",
			Message: err.Error(),
		})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to create API token",
		})
	}

	return c.JSON(http.StatusCreated, issued)
}

// RevokeToken deletes an active API token owned by the authenticated user.
func (ctrl AuthController) RevokeToken(c echo.Context) error {
	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, middleware.APIError{
			Error:   "unauthorized",
			Message: "authentication required",
		})
	}

	err := ctrl.Service.RevokeAPIToken(c.Request().Context(), principal.UserID, c.Param("id"))
	if errors.Is(err, service.ErrAuthTokenNotFound) {
		return c.JSON(http.StatusNotFound, middleware.APIError{
			Error:   "not_found",
			Message: "API token not found",
		})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, middleware.APIError{
			Error:   "internal_error",
			Message: "unable to revoke API token",
		})
	}

	return c.NoContent(http.StatusNoContent)
}
