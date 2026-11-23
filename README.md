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

## Docker Compose

With Docker and the Compose plugin installed, build and start GoFlow with:

```sh
docker compose up --build -d
```

Open `http://127.0.0.1:8080`. Compose binds to localhost, allocates 1 GB of shared memory, and stores `/data` in the `goflow-data` named volume. `docker compose down` stops the service and keeps the volume. The image runs as UID/GID 10001 and includes Chromium for future integration; browser connection is not implemented. If using a bind mount instead, its directory must be writable by UID 10001. Remote access requires authenticated access protection before browser interaction is enabled.

## Helm

The chart in `charts/goflow` installs a single-replica Deployment, a ClusterIP Service, and a persistent volume claim. Build and publish the image to a registry accessible to your cluster, then install with:

```sh
helm install goflow ./charts/goflow \
  --set image.repository=YOUR_REGISTRY/goflow \
  --set image.tag=YOUR_TAG
```

No image has been published by this repository yet. The claim uses the cluster's default StorageClass; if there is no default, provide `--set persistence.storageClass=YOUR_STORAGE_CLASS` or provision a matching volume. The volume must support writes by UID/GID 10001 and Kubernetes `fsGroup` ownership. The Deployment uses one replica and the Recreate strategy so upgrades do not run two active instances against the same browser profile. Removing the Helm release also removes its PVC; back up the data before uninstalling.

Ingress is disabled by default. To enable it, provide `--set ingress.enabled=true --set ingress.host=YOUR_HOST`, plus an ingress class, TLS and authentication appropriate for your environment. The chart does not provide authentication. Configure `resources`, `persistence.size`, and `sharedMemory.size` in a values file as needed.

To validate the chart locally, run `helm lint charts/goflow` and `python3 charts/goflow/tests/test_chart.py`. The chart tests require Helm and PyYAML. An actual cluster installation has not been verified here.

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
