package controller

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/service"
)

// ProviderController exposes provider discovery endpoints.
type ProviderController struct {
	Service *service.ProviderService
}

// Register mounts provider routes onto e.
func (ctrl ProviderController) Register(e *echo.Echo, middlewares ...echo.MiddlewareFunc) {
	e.GET("/api/providers", ctrl.List, middlewares...)
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
