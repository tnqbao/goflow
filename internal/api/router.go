// Package api wires KubeReel Echo API controllers onto an Echo server.
package api

import (
	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/controller"
	"github.com/tnqbao/kubereel/internal/service"
)

// Router groups the application services required by HTTP API controllers.
type Router struct {
	Workflows *service.WorkflowService
	Providers *service.ProviderService
}

// Register mounts all API controllers onto e.
func (r Router) Register(e *echo.Echo) {
	(controller.ProviderController{Service: r.Providers}).Register(e)
	(controller.WorkflowController{Service: r.Workflows}).Register(e)
}
