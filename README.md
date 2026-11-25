# KubeReel

Self-hosted, Kubernetes-native platform for automating AI video production.

**Install once. Connect your providers. Automate video production.**

## Architecture

KubeReel runs as a single Go executable and single container image (`iamqbao/kubereel`) containing the control plane, embedded UI, and FFmpeg:

- `kubereel serve` — starts the HTTP server, embedded dashboard, and Kubernetes controllers.
- `kubereel worker` — executes an assigned workflow step inside a temporary Kubernetes Job.
- `kubereel render` — executes controlled FFmpeg media operations (`merge`, `trim`, `resize`, `aspect_ratio`, `add_audio`, `normalize`, `render_mp4`) inside an isolated scratch directory without shell execution.

Kubernetes state is managed via three CRDs (`VideoWorkflow`, `VideoRun`, `VideoProvider`) and PVC-backed artifact storage, with no external database or message broker required.

> **Security notice:** Authentication is not yet implemented. Run KubeReel only on a trusted network and access it via local port-forwarding; do not expose the service or Ingress publicly.

---

## Quick Start — Kubernetes (Helm via Docker Hub)

Install directly from Docker Hub OCI registry (zero extra configuration required on clusters with a default `StorageClass`):

```sh
helm install kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
  --version 0.1.0 \
  --namespace kubereel \
  --create-namespace
```

Forward the service port locally:

```sh
kubectl -n kubereel port-forward service/kubereel-kubereel 8080:8080
```

- **Web UI:** Open `http://localhost:8080/workflows` to run the built-in Mock workflow.
- **Health checks:** `http://localhost:8080/healthz` and `http://localhost:8080/readyz`.
- **API test:**

```sh
curl -i -X POST http://localhost:8080/api/workflows \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"A quiet lake"}'

curl http://localhost:8080/api/workflows/<WORKFLOW_ID>
curl -o mock-scene.svg http://localhost:8080/api/workflows/<WORKFLOW_ID>/artifact
```

### Common Helm Values

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `iamqbao/kubereel` | Container image repository for control plane and Jobs |
| `image.tag` | `"0.1.0"` | Pinned image tag (`latest` is rejected) |
| `storage.size` | `10Gi` | PVC size for `/data` |
| `storage.accessMode` | `ReadWriteOnce` | Use `ReadWriteMany` for multi-node Job scheduling |
| `storage.existingClaim` | `""` | Existing PVC name (must match `storage.accessMode`) |
| `storage.maxArtifactBytes` | `1073741824` | Max size per artifact (1 GiB) |
| `storage.retentionSeconds` | `0` | Expiry for completed `VideoRun` artifacts (`0` disables) |
| `execution.maxConcurrentJobs` | `5` | Max concurrent Kubernetes Jobs |
| `execution.jobTTLSeconds` | `86400` | Finished Job retention window (24h) |
| `ingress.enabled` | `false` | Optional Ingress (requires `ingress.host`) |

### Upgrading & CRDs

Helm installs CRDs on initial install but does not automatically update them on upgrade. Apply CRD changes before running `helm upgrade`:

```sh
kubectl apply -f charts/kubereel/crds/
helm upgrade kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
  --version 0.1.0 \
  --namespace kubereel
```

---

## Quick Start — Docker & Local

### Docker Hub Image

```sh
docker run --rm -p 127.0.0.1:8080:8080 -v kubereel-data:/data iamqbao/kubereel:0.1.0 serve
```

### Docker Compose

```sh
docker compose up --build
```

### Run from Source

```sh
DATA_DIR=./data go run . serve
```

> Standalone mode exposes health endpoints (`/healthz`, `/readyz`); full workflow execution requires Kubernetes mode.

---

## Build & Publish to Docker Hub

```sh
# 1. Authenticate Docker and Helm with Docker Hub
docker login -u iamqbao
helm registry login registry-1.docker.io -u iamqbao

# 2. Build, smoke-test, and push container image
docker build -t iamqbao/kubereel:0.1.0 .
./tests/docker-smoke.sh iamqbao/kubereel:0.1.0
docker push iamqbao/kubereel:0.1.0

# 3. Package and push Helm chart (OCI)
helm lint ./charts/kubereel
helm package ./charts/kubereel
helm push kubereel-chart-0.1.0.tgz oci://registry-1.docker.io/iamqbao
```

CI/CD (`.github/workflows/pipeline.yml`) automatically runs `build` -> `test` -> `push` to publish verified container images on pushes to `develop`, `main`, and `v*` tags.

---

## Development & Validation

```sh
go fmt ./...
go vet ./...
go test -race ./...
go build ./...

helm lint ./charts/kubereel
helm template kubereel ./charts/kubereel > /dev/null
python3 charts/kubereel/tests/test_chart.py

# Full Kind end-to-end test (requires Docker, Kind, Helm, kubectl, Python 3)
./tests/e2e-kind.sh
```

## Current Status & Limitations

- The `mock` provider is enabled out of the box (no API keys required). Real provider integrations (Gemini, fal.ai), the complete Mock end-to-end MP4 workflow, authentication, and the `/api/v1` contract are in active development.
- Current `VideoRun` execution supports `image` steps (`providerRef: mock`, producing SVG artifacts) and `ffmpeg` steps (`merge`, `trim`, `resize`, `aspect_ratio`, `add_audio`, `normalize`, `render_mp4`, producing validated H.264 MP4 artifacts); failed Jobs retry up to `maxAttempts`, while missing/uncertain Jobs transition to `RequiresReview` rather than risking duplicate submission.
