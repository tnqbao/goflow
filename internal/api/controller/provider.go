package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ProviderController exposes provider discovery and management endpoints.
type ProviderController struct {
	Service    *service.ProviderService
	Management *service.ProviderManagement
}

// Register mounts provider routes onto e.
func (ctrl ProviderController) Register(e *echo.Echo, middlewares ...echo.MiddlewareFunc) {
	e.GET("/api/providers", ctrl.List, middlewares...)
	group := e.Group("/api/v1/providers", append([]echo.MiddlewareFunc{noStoreCache}, middlewares...)...)
	group.GET("", ctrl.ListManaged)
	group.GET("/:id", ctrl.GetManaged)
	group.POST("", ctrl.Create)
	group.PATCH("/:id", ctrl.Update)
	group.DELETE("/:id", ctrl.Delete)
	group.POST("/:id/test", ctrl.Test)
}

// List returns configured providers and their current availability.
func (ctrl ProviderController) List(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "provider service unavailable\n")
	}

	items, err := ctrl.Service.List()
	if err != nil {
		return c.String(http.StatusServiceUnavailable, "provider service unavailable\n")
	}

	return c.JSON(http.StatusOK, items)
}

// ListManaged returns a bounded page of non-secret provider configurations.
func (ctrl ProviderController) ListManaged(c echo.Context) error {
	limit := int64(50)
	if raw := c.QueryParam("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 100 {
			return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "limit must be between 1 and 100"})
		}
		limit = value
	}
	if len(c.QueryParam("continue")) > 4096 {
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "invalid continuation token"})
	}
	page, err := ctrl.Management.List(c.Request().Context(), limit, c.QueryParam("continue"))
	if apierrors.IsResourceExpired(err) || apierrors.IsBadRequest(err) {
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "invalid or expired continuation token"})
	}
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{Error: "service_unavailable", Message: "provider service unavailable"})
	}
	return c.JSON(http.StatusOK, page)
}

// GetManaged returns a single provider configuration without credentials.
func (ctrl ProviderController) GetManaged(c echo.Context) error {
	item, err := ctrl.Management.Get(c.Request().Context(), c.Param("id"))
	if apierrors.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, middleware.APIError{Error: "not_found", Message: "provider not found"})
	}
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{Error: "service_unavailable", Message: "provider service unavailable"})
	}
	return c.JSON(http.StatusOK, item)
}

const maxProviderRequestBytes = 65536

func (ctrl ProviderController) Create(c echo.Context) error {
	var req struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		service.ProviderChanges
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), maxProviderRequestBytes, &req); err != nil {
		return providerError(c, service.ErrInvalidProvider)
	}
	item, err := ctrl.Management.Create(c.Request().Context(), req.ID, req.Type, req.ProviderChanges)
	if err != nil {
		return providerError(c, err)
	}
	c.Response().Header().Set("Location", "/api/v1/providers/"+item.ID)
	return c.JSON(http.StatusCreated, item)
}

func (ctrl ProviderController) Update(c echo.Context) error {
	var req service.ProviderChanges
	if err := decodeStrictJSON(c.Response(), c.Request(), maxProviderRequestBytes, &req); err != nil {
		return providerError(c, service.ErrInvalidProvider)
	}
	item, err := ctrl.Management.Update(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return providerError(c, err)
	}
	return c.JSON(http.StatusOK, item)
}

func (ctrl ProviderController) Delete(c echo.Context) error {
	if err := ctrl.Management.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return providerError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (ctrl ProviderController) Test(c echo.Context) error {
	result, err := ctrl.Management.Test(c.Request().Context(), c.Param("id"))
	if err != nil {
		return providerError(c, err)
	}
	return c.JSON(http.StatusOK, result)
}

func providerError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrInvalidProvider):
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: err.Error()})
	case errors.Is(err, service.ErrProviderCredentials):
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{Error: "service_unavailable", Message: service.ErrProviderCredentials.Error()})
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "Kubernetes rejected provider configuration"})
	case apierrors.IsNotFound(err):
		return c.JSON(http.StatusNotFound, middleware.APIError{Error: "not_found", Message: "provider not found"})
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return c.JSON(http.StatusConflict, middleware.APIError{Error: "conflict", Message: "provider already exists or changed concurrently"})
	case errors.Is(err, service.ErrProviderProbeCredentials):
		return c.JSON(http.StatusUnprocessableEntity, middleware.APIError{Error: "provider_credentials", Message: err.Error()})
	case errors.Is(err, service.ErrProviderProbe):
		return c.JSON(http.StatusBadGateway, middleware.APIError{Error: "provider_check_failed", Message: err.Error()})
	default:
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{Error: "service_unavailable", Message: "provider service unavailable"})
	}
}
