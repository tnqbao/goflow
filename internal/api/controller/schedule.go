package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kjson "sigs.k8s.io/json"
)

type ScheduleController struct{ Service *service.Schedules }

func (ctrl ScheduleController) Register(e *echo.Echo, auth echo.MiddlewareFunc) {
	group := e.Group("/api/v1/workflows/:id/schedule", noStoreCache, auth)
	group.GET("", ctrl.get)
	group.PUT("", ctrl.put)
	group.DELETE("", ctrl.delete)
}

func (ctrl ScheduleController) get(c echo.Context) error {
	schedule, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return scheduleError(c, err)
	}
	return c.JSON(http.StatusOK, schedule)
}

func (ctrl ScheduleController) put(c echo.Context) error {
	var req struct {
		Cron               string          `json:"cron"`
		Paused             bool            `json:"paused"`
		Inputs             json.RawMessage `json:"inputs"`
		MaxConcurrentSteps int64           `json:"maxConcurrentSteps"`
		AllowPaid          bool            `json:"allowPaid"`
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), 32<<10, &req); err != nil {
		return scheduleError(c, workflow.ErrInvalid)
	}
	var input map[string]any
	if len(req.Inputs) > 0 {
		strict, err := kjson.UnmarshalStrict(req.Inputs, &input)
		if err != nil || len(strict) > 0 || input == nil {
			return scheduleError(c, workflow.ErrInvalid)
		}
	}
	opts := service.ScheduleOptions{
		Cron: req.Cron, Paused: req.Paused, ExecutionOptions: service.ExecutionOptions{
			Inputs: input, AllowPaid: req.AllowPaid, MaxConcurrentSteps: req.MaxConcurrentSteps,
		},
	}
	schedule, err := ctrl.Service.Put(c.Request().Context(), c.Param("id"), opts)
	if err != nil {
		return scheduleError(c, err)
	}
	return c.JSON(http.StatusOK, schedule)
}

func (ctrl ScheduleController) delete(c echo.Context) error {
	if err := ctrl.Service.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return scheduleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func scheduleError(c echo.Context, err error) error {
	status, code, message := http.StatusServiceUnavailable, "service_unavailable", "workflow schedules unavailable"
	switch {
	case errors.Is(err, workflow.ErrInvalid), errors.Is(err, service.ErrPaidExecutionApproval):
		status, code, message = http.StatusBadRequest, "invalid_request", err.Error()
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		status, code, message = http.StatusBadRequest, "invalid_request", "Kubernetes rejected the schedule or run options"
	case errors.Is(err, service.ErrScheduleNotFound), apierrors.IsNotFound(err):
		status, code, message = http.StatusNotFound, "not_found", "workflow or schedule not found"
	case apierrors.IsConflict(err):
		status, code, message = http.StatusConflict, "conflict", "workflow schedule changed concurrently"
	}
	return c.JSON(status, middleware.APIError{Error: code, Message: message})
}
