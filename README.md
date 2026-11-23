# GoFlow

GoFlow is a planned self-hosted AI filmmaking workspace. A responsive server-rendered workspace shell and health endpoints are available; the workspace features are not implemented yet.

## Requirements

Go 1.23.3 or later.

## Run

```sh
go run ./cmd/goflow
```

The server listens on `:8080` and serves the page at `/`. HTMX 2.0.8 and the base stylesheet are embedded in the binary. The server requires no Node.js runtime.

`GET /healthz` and `GET /readyz` return HTTP 200 while the server is running. Readiness currently checks only that the server can handle requests; no database or other dependencies are initialized yet.

The planned database path is `/data/goflow.db` and the browser profile path is `/data/browser`. These paths are not created or used yet.

## SQLite foundation

The internal database package opens a SQLite file, enables foreign keys for each connection, and applies versioned migrations transactionally. No application tables have been added yet. Database initialization is not connected to server startup; the database parent directory must exist when using the package.

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
