package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kjson "sigs.k8s.io/json"
)

const workflowDefinitionsPath = "/api/v1/workflows"
const maxWorkflowDefinitionBytes = 512 << 10

type WorkflowDefinitionController struct {
	Service *service.WorkflowDefinitions
}

func (ctrl WorkflowDefinitionController) Register(e *echo.Echo, auth echo.MiddlewareFunc) {
	group := e.Group(workflowDefinitionsPath, noStoreCache, auth)
	group.GET("", ctrl.list)
	group.GET("/:id", ctrl.get)
	group.POST("", ctrl.create)
	group.PATCH("/:id", ctrl.update)
	group.DELETE("/:id", ctrl.delete)
}

func (ctrl WorkflowDefinitionController) list(c echo.Context) error {
	limit := int64(50)
	if raw := c.QueryParam("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 100 {
			return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "limit must be between 1 and 100"})
		}
		limit = value
	}
	cursor := c.QueryParam("continue")
	if len(cursor) > 4096 {
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "invalid continuation token"})
	}
	page, err := ctrl.Service.List(c.Request().Context(), limit, cursor)
	if err != nil {
		return workflowDefinitionError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

func (ctrl WorkflowDefinitionController) get(c echo.Context) error {
	item, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return workflowDefinitionError(c, err)
	}
	return c.JSON(http.StatusOK, item)
}

func (ctrl WorkflowDefinitionController) create(c echo.Context) error {
	var req struct {
		ID   string          `json:"id"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), maxWorkflowDefinitionBytes, &req); err != nil {
		return workflowDefinitionError(c, workflow.ErrInvalid)
	}
	spec, ok := decodeWorkflowSpec(req.Spec)
	if !ok {
		return workflowDefinitionError(c, workflow.ErrInvalid)
	}
	item, err := ctrl.Service.Create(c.Request().Context(), req.ID, spec)
	if err != nil {
		return workflowDefinitionError(c, err)
	}
	c.Response().Header().Set("Location", workflowDefinitionsPath+"/"+item.ID)
	return c.JSON(http.StatusCreated, item)
}

func (ctrl WorkflowDefinitionController) update(c echo.Context) error {
	var req struct {
		ResourceVersion string          `json:"resourceVersion"`
		Spec            json.RawMessage `json:"spec"`
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), maxWorkflowDefinitionBytes, &req); err != nil {
		return workflowDefinitionError(c, workflow.ErrInvalid)
	}
	spec, ok := decodeWorkflowSpec(req.Spec)
	if !ok {
		return workflowDefinitionError(c, workflow.ErrInvalid)
	}
	item, err := ctrl.Service.Update(c.Request().Context(), c.Param("id"), req.ResourceVersion, spec)
	if err != nil {
		return workflowDefinitionError(c, err)
	}
	return c.JSON(http.StatusOK, item)
}

func (ctrl WorkflowDefinitionController) delete(c echo.Context) error {
	if err := ctrl.Service.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return workflowDefinitionError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func decodeWorkflowSpec(raw json.RawMessage) (map[string]any, bool) {
	var spec map[string]any
	strict, err := kjson.UnmarshalStrict(raw, &spec)
	return spec, err == nil && len(strict) == 0 && spec != nil
}

func workflowDefinitionError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, workflow.ErrInvalid):
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: err.Error()})
	case apierrors.IsResourceExpired(err):
		return c.JSON(http.StatusGone, middleware.APIError{Error: "expired_cursor", Message: "continuation token expired"})
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return c.JSON(http.StatusBadRequest, middleware.APIError{Error: "invalid_request", Message: "Kubernetes rejected the workflow definition or continuation token"})
	case apierrors.IsNotFound(err):
		return c.JSON(http.StatusNotFound, middleware.APIError{Error: "not_found", Message: "workflow definition not found"})
	case errors.Is(err, workflow.ErrConflict), apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return c.JSON(http.StatusConflict, middleware.APIError{Error: "conflict", Message: "workflow definition already exists or changed concurrently"})
	default:
		return c.JSON(http.StatusServiceUnavailable, middleware.APIError{Error: "service_unavailable", Message: "workflow definitions unavailable"})
	}
}
