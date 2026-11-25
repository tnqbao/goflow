package web

import (
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const maxFormBodyBytes = 16384

// WorkflowController serves the HTML pages for the Mock workflow view.
type WorkflowController struct {
	Service *service.WorkflowService
}

// Register mounts the workflow UI routes onto e.
func (ctrl WorkflowController) Register(e *echo.Echo) {
	e.GET("/workflows", echo.WrapHandler(templ.Handler(MockWorkflowForm(""))))
	e.POST("/workflows", ctrl.SubmitForm)
	e.GET("/workflows/:id", ctrl.ShowStatus)
}

// SubmitForm validates the HTML form submission and creates a queued Mock workflow.
func (ctrl WorkflowController) SubmitForm(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxFormBodyBytes)
	if err := req.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form\n")
	}

	created, err := ctrl.Service.Create(req.Context(), req.PostForm.Get("prompt"), service.DefaultProviderID)
	if errors.Is(err, workflow.ErrInvalid) {
		form := MockWorkflowForm("Enter a scene description.")
		templ.Handler(form, templ.WithStatus(http.StatusBadRequest)).ServeHTTP(c.Response(), req)
		return nil
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to create workflow\n")
	}

	return c.Redirect(http.StatusSeeOther, "/workflows/"+created.ID)
}

// ShowStatus renders the status and MP4 player/download page for a single workflow.
func (ctrl WorkflowController) ShowStatus(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	wf, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if errors.Is(err, workflow.ErrNotFound) {
		return c.NoContent(http.StatusNotFound)
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to read workflow\n")
	}

	templ.Handler(MockWorkflowStatus(wf)).ServeHTTP(c.Response(), c.Request())
	return nil
}
