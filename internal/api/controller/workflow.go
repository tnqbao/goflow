package controller

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const maxWorkflowRequestBytes = 16384

type createWorkflowRequest struct {
	Prompt   string `json:"prompt"`
	Provider string `json:"provider"`
}

// WorkflowController handles HTTP requests for workflow submission, status, and artifacts.
type WorkflowController struct {
	Service *service.WorkflowService
}

// Register mounts workflow API routes onto e.
func (ctrl WorkflowController) Register(e *echo.Echo) {
	e.POST("/api/workflows", ctrl.Create)
	e.GET("/api/workflows/:id", ctrl.Get)
	e.GET("/api/workflows/:id/artifact", ctrl.DownloadArtifact)
}

// Create validates the HTTP payload and delegates workflow creation to WorkflowService.
func (ctrl WorkflowController) Create(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	var req createWorkflowRequest
	if err := decodeStrictJSON(c.Response(), c.Request(), maxWorkflowRequestBytes, &req); err != nil {
		return c.String(http.StatusBadRequest, "invalid request\n")
	}

	created, err := ctrl.Service.Create(c.Request().Context(), req.Prompt, req.Provider)
	if errors.Is(err, service.ErrProviderUnavailable) {
		return c.String(http.StatusBadRequest, "provider unavailable\n")
	}
	if errors.Is(err, workflow.ErrInvalid) {
		return c.String(http.StatusBadRequest, "invalid prompt\n")
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to create workflow\n")
	}

	c.Response().Header().Set("Location", "/api/workflows/"+created.ID)
	return c.JSON(http.StatusAccepted, created)
}

// Get returns the current state of a single workflow.
func (ctrl WorkflowController) Get(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	wf, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if errors.Is(err, workflow.ErrNotFound) {
		return c.String(http.StatusNotFound, "workflow not found\n")
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to read workflow\n")
	}

	return c.JSON(http.StatusOK, wf)
}

// DownloadArtifact streams the completed MP4 video with HTTP Range support.
func (ctrl WorkflowController) DownloadArtifact(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	download, err := ctrl.Service.OpenArtifact(c.Request().Context(), c.Param("id"))
	if errors.Is(err, workflow.ErrNotFound) || errors.Is(err, service.ErrArtifactUnavailable) {
		return c.String(http.StatusNotFound, "artifact not available\n")
	}
	if errors.Is(err, service.ErrStorageUnavailable) {
		return c.String(http.StatusServiceUnavailable, "artifact storage unavailable\n")
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to open artifact\n")
	}
	defer func() {
		if closeErr := download.Object.Close(); closeErr != nil {
			slog.Error("close artifact", "error", closeErr)
		}
	}()

	c.Response().Header().Set("Content-Type", download.ContentType)
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+download.Filename+`"`)
	http.ServeContent(c.Response(), c.Request(), download.Filename, download.ModTime, download.Object)

	return nil
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return err
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return io.ErrUnexpectedEOF
	}

	return nil
}
