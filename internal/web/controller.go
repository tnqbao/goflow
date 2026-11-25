package web

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/api/middleware"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/workflow"
)

const maxFormBodyBytes = 16384

// WorkflowController serves the authenticated HTML pages for the Mock workflow view.
type WorkflowController struct {
	Auth    *service.AuthService
	Service *service.WorkflowService
}

// Register mounts the authenticated workflow UI routes onto e.
func (ctrl WorkflowController) Register(e *echo.Echo) {
	guard := RequireWebAuth(ctrl.Auth)
	e.GET("/workflows", ctrl.ShowForm, guard)
	e.POST("/workflows", ctrl.SubmitForm, guard)
	e.GET("/workflows/:id", ctrl.ShowStatus, guard)
}

// ShowForm renders the Mock workflow submission page.
func (ctrl WorkflowController) ShowForm(c echo.Context) error {
	principal, _ := middleware.PrincipalFromContext(c)
	return renderWithinLayout(c, http.StatusOK, "Mock workflow", workflowFormPageTmpl, workflowFormViewData{
		Username:  principal.Username,
		CSRFToken: principal.CSRFToken,
	})
}

// SubmitForm verifies the CSRF token, validates the form, and creates a queued Mock workflow.
func (ctrl WorkflowController) SubmitForm(c echo.Context) error {
	if ctrl.Service == nil {
		return c.String(http.StatusServiceUnavailable, "workflow service unavailable\n")
	}

	principal, ok := middleware.PrincipalFromContext(c)
	if !ok {
		return c.Redirect(http.StatusSeeOther, "/login")
	}

	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxFormBodyBytes)
	if err := req.ParseForm(); err != nil {
		return c.String(http.StatusBadRequest, "invalid form\n")
	}

	csrfToken := req.PostForm.Get("csrf_token")
	if csrfToken == "" {
		csrfToken = req.Header.Get(middleware.CSRFHeaderName)
	}
	if !ctrl.Auth.VerifyCSRF(principal, csrfToken) {
		return c.String(http.StatusForbidden, "invalid CSRF token\n")
	}

	created, err := ctrl.Service.Create(req.Context(), req.PostForm.Get("prompt"), service.DefaultProviderID)
	if errors.Is(err, workflow.ErrInvalid) {
		return renderWithinLayout(c, http.StatusBadRequest, "Mock workflow", workflowFormPageTmpl, workflowFormViewData{
			Username:  principal.Username,
			CSRFToken: principal.CSRFToken,
			Message:   "Enter a scene description.",
		})
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

	principal, _ := middleware.PrincipalFromContext(c)
	wf, err := ctrl.Service.Get(c.Request().Context(), c.Param("id"))
	if errors.Is(err, workflow.ErrNotFound) {
		return c.NoContent(http.StatusNotFound)
	}
	if err != nil {
		return c.String(http.StatusInternalServerError, "unable to read workflow\n")
	}

	return renderWithinLayout(c, http.StatusOK, "Mock workflow", workflowStatusPageTmpl, workflowStatusViewData{
		Username:  principal.Username,
		CSRFToken: principal.CSRFToken,
		Item:      wf,
	})
}
