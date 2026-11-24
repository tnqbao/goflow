// Package web contains the rendered page and its embedded assets.
package web

import (
	"embed"
	"net/http"
)

//go:generate go run github.com/a-h/templ/cmd/templ@v0.3.1001 generate

//go:embed static/app.css
var assets embed.FS

// Assets serves the embedded static files.
func Assets() http.Handler {
	return http.FileServer(http.FS(assets))
}
