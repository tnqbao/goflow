// Package middleware provides HTTP security, authentication, and CSRF middleware for Echo.
package middleware

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/service"
)

const (
	principalContextKey = "kubereel.auth.principal"
	CSRFHeaderName      = "X-CSRF-Token"
	SetupTokenHeader    = "X-Setup-Token"
)

// APIError is the structured JSON error envelope returned by auth middleware and endpoints.
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// SecurityHeaders attaches baseline browser hardening headers to every HTTP response.
func SecurityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			headers := c.Response().Header()
			headers.Set("X-Content-Type-Options", "nosniff")
			headers.Set("X-Frame-Options", "DENY")
			headers.Set("Referrer-Policy", "same-origin")
			return next(c)
		}
	}
}

// PrincipalFromContext retrieves the authenticated Principal stored by auth middleware.
func PrincipalFromContext(c echo.Context) (service.Principal, bool) {
	raw := c.Get(principalContextKey)
	principal, ok := raw.(service.Principal)
	return principal, ok
}

// SetPrincipal stores an authenticated Principal on the Echo request context.
func SetPrincipal(c echo.Context, principal service.Principal) {
	c.Set(principalContextKey, principal)
}

// RequireAPIAuth protects REST API and artifact routes using Bearer tokens or CSRF-verified session cookies.
func RequireAPIAuth(authSvc *service.AuthService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if authSvc == nil {
				return c.JSON(http.StatusServiceUnavailable, APIError{
					Error:   "service_unavailable",
					Message: "authentication service unavailable",
				})
			}

			ctx := c.Request().Context()
			initialized, err := authSvc.IsInitialized(ctx)
			if err != nil {
				return c.JSON(http.StatusServiceUnavailable, APIError{
					Error:   "service_unavailable",
					Message: "unable to check authentication state",
				})
			}
			if !initialized {
				c.Response().Header().Set("WWW-Authenticate", `Bearer realm="kubereel"`)
				return c.JSON(http.StatusUnauthorized, APIError{
					Error:   "setup_required",
					Message: "initial administrator setup is required",
				})
			}

			rawToken, viaCookie, ok := ExtractCredential(c.Request())
			if !ok {
				return writeUnauthorizedAPI(c)
			}

			principal, authErr := authSvc.Authenticate(ctx, rawToken)
			if errors.Is(authErr, service.ErrUnauthorized) {
				return writeUnauthorizedAPI(c)
			}
			if authErr != nil {
				return c.JSON(http.StatusServiceUnavailable, APIError{
					Error:   "service_unavailable",
					Message: "unable to verify credentials",
				})
			}

			principal.ViaCookie = viaCookie
			if viaCookie && IsUnsafeMethod(c.Request().Method) && !authSvc.VerifyCSRF(principal, c.Request().Header.Get(CSRFHeaderName)) {
				return c.JSON(http.StatusForbidden, APIError{
					Error:   "csrf_invalid",
					Message: "missing or invalid CSRF token",
				})
			}

			SetPrincipal(c, principal)
			return next(c)
		}
	}
}

func writeUnauthorizedAPI(c echo.Context) error {
	c.Response().Header().Set("WWW-Authenticate", `Bearer realm="kubereel"`)
	return c.JSON(http.StatusUnauthorized, APIError{
		Error:   "unauthorized",
		Message: "authentication required",
	})
}

// ExtractCredential extracts a Bearer token (if Authorization is present) or falls back to the session cookie.
func ExtractCredential(r *http.Request) (string, bool, bool) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		token, ok := parseBearerHeader(authHeader)
		return token, false, ok
	}

	cookie, err := r.Cookie(service.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false, false
	}

	return strings.TrimSpace(cookie.Value), true, true
}

func parseBearerHeader(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}

	token := strings.TrimSpace(header[len(prefix):])
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}

	return token, true
}

// IsUnsafeMethod reports whether an HTTP method mutates state and requires CSRF verification for cookies.
func IsUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// IsSameOriginBrowserPost rejects cross-site browser form submissions using Fetch Metadata and Origin headers.
func IsSameOriginBrowserPost(r *http.Request) bool {
	secFetch := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if secFetch != "" && secFetch != "same-origin" && secFetch != "none" {
		return false
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}

	return strings.EqualFold(parsed.Host, r.Host)
}

// ClientIPKey extracts the direct peer IP address from RemoteAddr for login rate limiting.
func ClientIPKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	return strings.TrimSpace(r.RemoteAddr)
}

// SetSessionCookie writes an HttpOnly SameSite=Lax session cookie onto the response.
func SetSessionCookie(c echo.Context, rawToken string, ttl time.Duration) {
	maxAge := int(ttl / time.Second)
	if maxAge < 1 {
		maxAge = 86400
	}

	c.SetCookie(&http.Cookie{
		Name:     service.SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isHTTPSRequest(c.Request()),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the browser session cookie immediately.
func ClearSessionCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     service.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPSRequest(c.Request()),
		SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
