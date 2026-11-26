package controller

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type ArtifactController struct{ Service *service.Artifacts }

func (ctrl ArtifactController) Register(e *echo.Echo, auth echo.MiddlewareFunc) {
	group := e.Group("/api/v1/artifacts", noStoreCache, auth)
	group.GET("", ctrl.list)
	group.POST("/references", ctrl.uploadReference)
	group.GET("/references/:name", ctrl.getReference)
	group.GET("/:id", ctrl.get)
	group.GET("/:id/download", ctrl.download)
	group.HEAD("/:id/download", ctrl.download)
	group.DELETE("/:id", ctrl.delete)
}

func (ctrl ArtifactController) uploadReference(c echo.Context) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return respondArtifactError(c, workflow.ErrInvalid)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return respondArtifactError(c, workflow.ErrInvalid)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			slog.Error("close uploaded reference image", "error", closeErr)
		}
	}()

	ref, err := ctrl.Service.UploadReference(c.Request().Context(), file)
	if err != nil {
		return respondArtifactError(c, err)
	}
	return c.JSON(http.StatusCreated, ref)
}

func (ctrl ArtifactController) getReference(c echo.Context) error {
	ref, err := ctrl.Service.OpenReference(c.Request().Context(), c.Param("name"))
	if err != nil {
		return respondArtifactError(c, err)
	}
	defer func() {
		if closeErr := ref.Object.Close(); closeErr != nil {
			slog.Error("close reference image stream", "error", closeErr)
		}
	}()

	info, err := ref.Object.Stat()
	if err != nil {
		return respondArtifactError(c, err)
	}

	headers := c.Response().Header()
	headers.Set("Content-Type", ref.ContentType)
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(c.Response(), c.Request(), ref.Name, info.ModTime(), ref.Object)
	return nil
}

func (ctrl ArtifactController) list(c echo.Context) error {
	raw := c.QueryParam("limit")
	if raw == "" {
		raw = "50"
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return respondArtifactError(c, workflow.ErrInvalid)
	}
	page, err := ctrl.Service.List(c.Request().Context(), c.QueryParam("runId"), limit, c.QueryParam("continue"))
	if err != nil {
		return respondArtifactError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

func (ctrl ArtifactController) get(c echo.Context) error {
	file, err := ctrl.Service.Open(c.Request().Context(), c.Param("id"))
	if err != nil {
		return respondArtifactError(c, err)
	}
	if err := file.Object.Close(); err != nil {
		return respondArtifactError(c, err)
	}
	return c.JSON(http.StatusOK, file.Artifact)
}

func (ctrl ArtifactController) download(c echo.Context) error {
	file, err := ctrl.Service.Open(c.Request().Context(), c.Param("id"))
	if err != nil {
		return respondArtifactError(c, err)
	}
	defer func() {
		if err := file.Object.Close(); err != nil {
			slog.Error("close artifact download", "error", err)
		}
	}()
	headers := c.Response().Header()
	headers.Set("Content-Type", file.ContentType)
	headers.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(c.Response(), c.Request(), file.Name, file.ModifiedAt, file.Object)
	return nil
}

func (ctrl ArtifactController) delete(c echo.Context) error {
	if err := ctrl.Service.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return respondArtifactError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func respondArtifactError(c echo.Context, err error) error {
	status, code, message := http.StatusServiceUnavailable, "service_unavailable", "artifact storage unavailable"
	switch {
	case errors.Is(err, service.ErrArtifactNotFound):
		status, code, message = http.StatusNotFound, "not_found", "artifact not found"
	case errors.Is(err, service.ErrArtifactRunActive):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	case apierrors.IsResourceExpired(err):
		status, code, message = http.StatusGone, "expired_cursor", "continuation token expired"
	case errors.Is(err, workflow.ErrInvalid), apierrors.IsBadRequest(err):
		status, code, message = http.StatusBadRequest, "invalid_request", "invalid artifact list parameters or continuation token"
	}
	return c.JSON(status, middleware.APIError{Error: code, Message: message})
}
