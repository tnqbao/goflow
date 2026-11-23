package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/tnqbao/goflow/internal/config"
)

func main() {
	if err := run(os.Stdout); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(w io.Writer) error {
	cfg := config.Defaults()
	_, err := fmt.Fprintf(w, "GoFlow foundation: address=%s database=%s browser=%s\n", cfg.Address, cfg.Database, cfg.BrowserData)
	return err
}
