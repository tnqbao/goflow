# KubeReel

KubeReel is a self-hosted, Kubernetes-native platform for automating AI video production.

**Install once. Connect your providers. Automate video production.**

The project is in initial development. Its target architecture is a modular monolith: one Go executable and one image run the control plane, while temporary Kubernetes Jobs execute workflow steps. The full product requirements and roadmap are in [SPEC.md](SPEC.md).

## Current implementation

- A Go service exposes health endpoints, an embedded interface and an experimental workflow API.
- In Kubernetes mode, the control plane persists workflow state and creates worker and render Jobs using the same image.
- The built-in Mock produces a labeled SVG placeholder without external services or credentials. It does **not** yet produce video or an MP4.
- Workflow CRDs, the DAG engine, FFmpeg rendering, provider integrations, authentication and the target `/api/v1` API remain roadmap work.

The current management API and interface do not provide authentication. Keep them on a trusted network and use local port forwarding; do not expose them publicly.

## Run locally

```sh
KUBEREEL_DATA_DIR=./data go run . serve
```

The data directory defaults to `/data`, with the database stored as `kubereel.db`. Health endpoints are available at `http://localhost:8080/healthz` and `/readyz`. Standalone `serve` currently exposes health endpoints; workflow execution and its interface require Kubernetes mode.

Existing local data and volumes are not moved or deleted automatically; back them up before changing data locations.

## Docker

```sh
docker build -t kubereel:local .
docker run --rm -p 127.0.0.1:8080:8080 -v kubereel-data:/data kubereel:local serve
```

`docker compose up --build` also starts the standalone service and uses the `kubereel-data` volume.

## Helm development installation

The current chart uses one permanent control-plane container and creates temporary worker and render Jobs using the same image. An image must first be made available to the cluster nodes or a registry accessible to them:

```sh
helm install kubereel ./charts/kubereel \
  --set image.repository=YOUR_REGISTRY/kubereel --set image.tag=TAG
kubectl port-forward service/kubereel-kubereel 8080:8080
```

Open `/workflows` to run the current Mock prototype. The development API also accepts a workflow request and returns a `Location` header for its status:

```sh
curl -i -X POST http://localhost:8080/api/workflows \
  -H 'Content-Type: application/json' -d '{"prompt":"A quiet lake"}'
curl http://localhost:8080/api/workflows/WORKFLOW_ID
curl -o mock-scene.svg http://localhost:8080/api/workflows/WORKFLOW_ID/artifact
```

These endpoints are experimental and do not yet implement the versioned API described in the specification. `GET /api/providers` lists current execution availability; `mock` is the only available provider.

The default PVC uses `ReadWriteOnce` and Jobs are placed on the control-plane node. For multi-node execution, provide `ReadWriteMany` storage and set `persistence.accessMode=ReadWriteMany`. To use an existing claim, set `persistence.existingClaim` and match its access mode.

## Validation

```sh
go fmt ./...
go vet ./...
go test ./...
go build ./...
helm lint ./charts/kubereel
helm template kubereel ./charts/kubereel
python3 charts/kubereel/tests/test_chart.py
```

With `kind`, Docker, Helm, kubectl and Python 3 installed, `tests/e2e-kind.sh` builds an image, creates an isolated temporary cluster, runs the Mock workflow and verifies Job cleanup. It removes its test resources afterward.
