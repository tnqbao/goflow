# GoFlow

GoFlow is a planned self-hosted AI filmmaking workspace. This repository currently contains the application foundation; the HTTP server and other workspace features are not implemented yet.

## Requirements

Go 1.23.3 or later.

## Run

```sh
go run ./cmd/goflow
```

The foundation reports its defaults and exits. The planned server address is `:8080`, the database path is `/data/goflow.db`, and the browser profile path is `/data/browser`. These paths are not created or used yet.

## Validate

```sh
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```
