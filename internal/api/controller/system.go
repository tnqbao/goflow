// Package controller implements Echo HTTP controllers for KubeReel endpoints.
package controller

import (
	"database/sql"
	"net/http"
	"runtime/debug"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/web"
)

// SystemController handles health, readiness, root redirect, and static asset routes.
type SystemController struct {
	DB *sql.DB
}

// Register mounts base system endpoints onto e.
func (ctrl SystemController) Register(e *echo.Echo) {
	e.GET("/", ctrl.RedirectRoot)
	e.GET("/static/*", echo.WrapHandler(web.Assets()))
	e.GET("/healthz", ctrl.Health)
	e.HEAD("/healthz", ctrl.Health)
	e.GET("/version", ctrl.Version)
	e.GET("/readyz", ctrl.Readiness)
	e.HEAD("/readyz", ctrl.Readiness)
}

// RedirectRoot redirects the root path to the workflows view.
func (ctrl SystemController) RedirectRoot(c echo.Context) error {
	return c.Redirect(http.StatusSeeOther, "/workflows")
}

// Health reports liveness without external checks.
func (ctrl SystemController) Health(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}

// Readiness checks local state store connectivity.
func (ctrl SystemController) Readiness(c echo.Context) error {
	if ctrl.DB == nil {
		return c.String(http.StatusServiceUnavailable, "not ready\n")
	}

	if err := ctrl.DB.PingContext(c.Request().Context()); err != nil {
		return c.String(http.StatusServiceUnavailable, "not ready\n")
	}

	return c.NoContent(http.StatusOK)
}

// BuildVersion and BuildRevision can be set at link time for packaged binaries.
var BuildVersion = "dev"
var BuildRevision = ""

func (ctrl SystemController) Version(c echo.Context) error {
	version, revision := BuildVersion, BuildRevision
	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, setting := range info.Settings {
			if revision == "" && setting.Key == "vcs.revision" {
				revision = setting.Value
			}
		}
	}
	return c.JSON(http.StatusOK, struct {
		Version  string `json:"version"`
		Revision string `json:"revision,omitempty"`
	}{version, revision})
}
