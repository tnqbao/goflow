# KubeReel

KubeReel is a self-hosted, Kubernetes-native platform for automating AI video production.

**Install once. Connect your providers. Automate video production.**

The project is in initial development. Its target architecture is a modular monolith: one Go executable and one image run the control plane, while temporary Kubernetes Jobs execute workflow steps. The full product requirements and roadmap are in [SPEC.md](SPEC.md).

## Current implementation

- A Go service exposes health endpoints, an embedded interface and an experimental workflow API.
- In Kubernetes mode, the control plane persists workflow state and creates worker and render Jobs using the same image.
- The built-in Mock produces a labeled SVG placeholder without external services or credentials. It does **not** yet produce video or an MP4.
- The `VideoWorkflow`, `VideoRun`, and `VideoProvider` CRDs define configuration and execution records; they are not yet connected to execution. The DAG engine, FFmpeg rendering, provider integrations, authentication and target `/api/v1` API remain roadmap work.

The current management API and interface do not provide authentication. Keep them on a trusted network and use local port forwarding; do not expose them publicly.

## Single executable

The same `kubereel` executable provides `serve`, `worker` and `render` commands. `serve` starts the current control plane. `worker` creates a Mock SVG from a JSON request, and `render` validates and copies that SVG; the FFmpeg renderer and real provider execution are future work. Run either worker command with `--input` and `--output` paths.

## Run locally

```sh
KUBEREEL_DATA_DIR=./data go run . serve
```

The data directory defaults to `/data`, with the database stored as `kubereel.db`. Health endpoints are available at `http://localhost:8080/healthz` and `/readyz`. Standalone `serve` currently exposes health endpoints; workflow execution and its interface require Kubernetes mode.

Existing local data and volumes are not moved or deleted automatically; back them up before changing data locations.

## Docker

The image runs as a non-root user and includes FFmpeg. The current `render`
command still copies the Mock SVG; FFmpeg-based rendering is planned for F19.

```sh
docker build -t kubereel:local .
docker run --rm -p 127.0.0.1:8080:8080 -v kubereel-data:/data kubereel:local serve
./tests/docker-smoke.sh kubereel:local
```

`docker compose up --build` also starts the standalone service and uses the `kubereel-data` volume.

## Helm development installation

The current chart uses one permanent control-plane container and creates temporary worker and render Jobs using the same image. Its default `kubereel:0.1.0` is a local development tag, not a published release. Build the image and make it available to the cluster nodes, or provide an image from an accessible registry:

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

The default PVC uses `ReadWriteOnce` and Jobs are placed on the control-plane node. For multi-node execution, provide `ReadWriteMany` storage and set `storage.accessMode=ReadWriteMany`. To use an existing claim, set `storage.existingClaim` and match its access mode.

The chart defaults to a 10 GiB PVC and five concurrent Jobs. It currently supports one replica, Kubernetes execution and PVC storage only. Authentication and a complete MP4 workflow remain future tasks; the optional ingress must not be publicly exposed while authentication is unavailable.

Helm installs the `VideoWorkflow`, `VideoRun`, and `VideoProvider` CRDs on initial installation, but does not upgrade or delete CRDs automatically. When upgrading an existing installation, install new CRDs separately with `kubectl apply -f charts/kubereel/crds/` before upgrading the chart. Back up custom resources and review CRD changes first. Deleting a CRD deletes all its resources in the cluster.

`VideoRun` stores a required immutable copy of the workflow spec. Input values are represented by exactly one of `stringValue`, `integerValue`, `numberValue`, or `booleanValue`. Status is updated through the Kubernetes status subresource; creating a `VideoRun` does not yet start execution.

`VideoProvider` stores provider configuration and references credentials by a Secret name and key in the same namespace. It does not activate an integration; Gemini and fal.ai adapters are future tasks. The HTTPS syntax check on `baseURL` does not replace endpoint and SSRF validation before a provider uses it.

When upgrading an older development installation, migrate `persistence.*` values to `storage.*` and `workflow.maxConcurrent` to `execution.maxConcurrentJobs`. The chart rejects the old keys to avoid silently changing the selected PVC. Back up the PVC before upgrading and preserve its existing claim name, access mode and size; increasing the size requires a StorageClass that supports expansion.

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

With Kind v0.27.0, Docker, Helm, kubectl and Python 3 installed, `tests/e2e-kind.sh` builds and smoke-tests an image, creates an isolated temporary cluster, runs the Mock workflow and verifies Job cleanup. It removes its test resources afterward. CI runs these checks on changes to `develop` and pull requests targeting it. Version tags also publish the image to GitHub Container Registry after both validation jobs pass.
