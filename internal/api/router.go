// Package api wires KubeReel Echo API controllers onto an Echo server.
package api

import (
	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/controller"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
)

// Router groups the application services required by HTTP API controllers.
type Router struct {
	Auth      *service.AuthService
	Workflows *service.WorkflowService
	Providers *service.ProviderService
}

// Register mounts auth and protected management/artifact API controllers onto e.
func (r Router) Register(e *echo.Echo) {
	(controller.AuthController{Service: r.Auth}).Register(e)

	authGuard := middleware.RequireAPIAuth(r.Auth)
	(controller.ProviderController{Service: r.Providers}).Register(e, authGuard)
	(controller.WorkflowController{Service: r.Workflows}).Register(e, authGuard)
}
