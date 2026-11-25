package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/service"
)

const DashboardPath = "/dashboard"

//go:embed dashboard/index.html
var dashboardHTML []byte

//go:embed dashboard/assets/*
var dashboardFiles embed.FS

// RegisterDashboard mounts the Web Dashboard and its public, fingerprinted assets.
func RegisterDashboard(e *echo.Echo, auth *service.AuthService) {
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusSeeOther, DashboardPath)
	})
	e.GET(DashboardPath, func(c echo.Context) error {
		c.Response().Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'")
		return c.HTMLBlob(http.StatusOK, dashboardHTML)
	}, noStoreWebCache, RequireWebAuth(auth))

	e.GET("/dashboard-static/assets/*", func(c echo.Context) error {
		name := c.Param("*")
		if name == "" || strings.ContainsAny(name, "/\\") {
			return echo.ErrNotFound
		}
		data, err := fs.ReadFile(dashboardFiles, "dashboard/assets/"+name)
		if err != nil {
			return echo.ErrNotFound
		}
		contentType := "text/javascript; charset=utf-8"
		if path.Ext(name) == ".css" {
			contentType = "text/css; charset=utf-8"
		}
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return c.Blob(http.StatusOK, contentType, data)
	})
}
