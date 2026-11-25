package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kjson "sigs.k8s.io/json"
)

const runsPath = "/api/v1/runs"
const maxExecutionRequestBytes = 512 << 10

type ExecutionController struct{ Service *service.Executions }

func (ctrl ExecutionController) Register(e *echo.Echo, auth echo.MiddlewareFunc) {
	e.POST("/api/v1/workflows/:id/runs", ctrl.create, noStoreCache, auth)
	group := e.Group(runsPath, noStoreCache, auth)
	group.GET("", ctrl.list)
	group.GET("/:id", ctrl.get)
	group.POST("/:id/cancel", ctrl.cancel)
	group.POST("/:id/retry", ctrl.retry)
	group.GET("/:id/logs", ctrl.logs)
	group.GET("/:id/artifacts", ctrl.artifacts)
}

func (ctrl ExecutionController) create(c echo.Context) error {
	var req struct {
		Inputs             json.RawMessage `json:"inputs"`
		MaxConcurrentSteps int64           `json:"maxConcurrentSteps"`
		AllowPaid          bool            `json:"allowPaid"`
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), maxExecutionRequestBytes, &req); err != nil {
		return executionError(c, workflow.ErrInvalid)
	}
	var inputs map[string]any
	if len(req.Inputs) > 0 {
		strict, err := kjson.UnmarshalStrict(req.Inputs, &inputs)
		if err != nil || len(strict) > 0 || inputs == nil {
			return executionError(c, workflow.ErrInvalid)
		}
	}
	run, err := ctrl.Service.Create(c.Request().Context(), c.Param("id"), service.ExecutionOptions{
		Inputs: inputs, MaxConcurrentSteps: req.MaxConcurrentSteps, AllowPaid: req.AllowPaid,
	})
	return acceptedRun(c, run, err)
}

func (ctrl ExecutionController) list(c echo.Context) error {
	limit, err := strconv.ParseInt(c.QueryParam("limit"), 10, 64)
	if c.QueryParam("limit") == "" {
		limit, err = 50, nil
	}
	cursor := c.QueryParam("continue")
	if err != nil || limit < 1 || limit > 100 || len(cursor) > 4096 {
		return executionError(c, workflow.ErrInvalid)
	}
	page, err := ctrl.Service.List(c.Request().Context(), limit, cursor)
	if err != nil {
		return executionError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

func (ctrl ExecutionController) get(c echo.Context) error {
	run, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return executionError(c, err)
	}
	return c.JSON(http.StatusOK, run)
}

func (ctrl ExecutionController) cancel(c echo.Context) error {
	run, err := ctrl.Service.Cancel(c.Request().Context(), c.Param("id"))
	return acceptedRun(c, run, err)
}

func (ctrl ExecutionController) retry(c echo.Context) error {
	var req struct {
		AllowPaid bool `json:"allowPaid"`
	}
	if err := decodeStrictJSON(c.Response(), c.Request(), 4096, &req); err != nil && !(errors.Is(err, io.EOF) && c.Request().ContentLength == 0) {
		return executionError(c, workflow.ErrInvalid)
	}
	run, err := ctrl.Service.Retry(c.Request().Context(), c.Param("id"), req.AllowPaid)
	return acceptedRun(c, run, err)
}

func (ctrl ExecutionController) logs(c echo.Context) error {
	tail, err := strconv.ParseInt(c.QueryParam("tailLines"), 10, 64)
	if c.QueryParam("tailLines") == "" {
		tail, err = 100, nil
	}
	step := c.QueryParam("step")
	if err != nil || tail < 1 || tail > 1000 || len(step) > 252 {
		return executionError(c, workflow.ErrInvalid)
	}
	logs, err := ctrl.Service.Logs(c.Request().Context(), c.Param("id"), step, tail)
	if err != nil {
		return executionError(c, err)
	}
	return c.JSON(http.StatusOK, logs)
}

func (ctrl ExecutionController) artifacts(c echo.Context) error {
	items, err := ctrl.Service.Artifacts(c.Request().Context(), c.Param("id"))
	if err != nil {
		return executionError(c, err)
	}
	return c.JSON(http.StatusOK, map[string][]string{"items": items})
}

func acceptedRun(c echo.Context, run service.Execution, err error) error {
	if err != nil {
		return executionError(c, err)
	}
	c.Response().Header().Set("Location", runsPath+"/"+run.ID)
	return c.JSON(http.StatusAccepted, run)
}

func executionError(c echo.Context, err error) error {
	status, code, message := http.StatusServiceUnavailable, "service_unavailable", "execution service unavailable"
	switch {
	case errors.Is(err, service.ErrPaidExecutionApproval), errors.Is(err, workflow.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_request", err.Error()
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		status, code, message = http.StatusBadRequest, "invalid_request", "Kubernetes rejected the run or continuation token"
	case errors.Is(err, repository.ErrExecutionLogsUnavailable):
		status, code, message = http.StatusNotFound, "logs_unavailable", "logs are unavailable for this step"
	case apierrors.IsNotFound(err):
		status, code, message = http.StatusNotFound, "not_found", "workflow or run not found"
	case errors.Is(err, workflow.ErrConflict), apierrors.IsConflict(err):
		status, code, message = http.StatusConflict, "conflict", "run state does not permit this operation or changed concurrently"
	case apierrors.IsResourceExpired(err):
		status, code, message = http.StatusGone, "expired_cursor", "continuation token expired"
	}
	return c.JSON(status, middleware.APIError{Error: code, Message: message})
}
