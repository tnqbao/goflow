# GoFlow

GoFlow is a planned self-hosted AI filmmaking workspace. A responsive server-rendered workspace shell and health endpoints are available; the workspace features are not implemented yet.

## Requirements

Go 1.23.3 or later.

## Run

```sh
go run ./cmd/goflow
```

The server listens on `:8080` and serves the page at `/`. HTMX 2.0.8 and the base stylesheet are embedded in the binary. The server requires no Node.js runtime.

At startup, GoFlow creates `/data` and `/data/browser` if needed, opens `/data/goflow.db`, and applies database migrations before listening. The process needs permission to create and write to the data directory. For local development without access to `/data`, run `GOFLOW_DATA_DIR=./data go run ./cmd/goflow`; the database and browser directory will be created there. Existing data is preserved across restarts.

`GET /healthz` returns HTTP 200 while the server is running. `GET /readyz` checks the database connection and returns HTTP 503 if it is unavailable. The browser directory is reserved for a future browser integration.

## SQLite foundation

The internal database package opens a SQLite file, enables foreign keys for each connection, and applies versioned migrations transactionally. No application tables have been added yet.

## Template generation

After editing a `.templ` file, run `go generate ./internal/web` and commit the generated Go file. The generator version is pinned in the source; the first run downloads it using Go.

## Validate

```sh
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```
