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
	Schedules           *service.Schedules
	Artifacts           *service.Artifacts
	Executions          *service.Executions
	WorkflowDefinitions *service.WorkflowDefinitions
	Auth                *service.AuthService
	Workflows           *service.WorkflowService
	Providers           *service.ProviderService
	ProviderManagement  *service.ProviderManagement
}

// Register mounts auth and protected management/artifact API controllers onto e.
func (r Router) Register(e *echo.Echo) {
	(controller.AuthController{Service: r.Auth}).Register(e)

	authGuard := middleware.RequireAPIAuth(r.Auth)
	(controller.ProviderController{Service: r.Providers, Management: r.ProviderManagement}).Register(e, authGuard)
	(controller.WorkflowController{Service: r.Workflows}).Register(e, authGuard)
	(controller.WorkflowDefinitionController{Service: r.WorkflowDefinitions}).Register(e, authGuard)
	(controller.ExecutionController{Service: r.Executions}).Register(e, authGuard)
	(controller.ArtifactController{Service: r.Artifacts}).Register(e, authGuard)
	(controller.ScheduleController{Service: r.Schedules}).Register(e, authGuard)
}
