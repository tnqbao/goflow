package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/a-h/templ"
	"github.com/tnqbao/goflow/internal/config"
	"github.com/tnqbao/goflow/internal/web"
)

func main() {
	addr := config.Defaults().Address
	slog.Info("starting HTTP server", "address", addr)
	if err := run(addr); err != nil {
		slog.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", templ.Handler(web.Home()))
	mux.Handle("GET /static/", web.Assets())
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", health)
	return mux
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
