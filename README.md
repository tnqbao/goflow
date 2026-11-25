# KubeReel

Self-hosted, Kubernetes-native platform for automating AI video production.

**Install once. Connect your providers. Automate video production.**

## Architecture

KubeReel runs as a single Go executable and single container image (`iamqbao/kubereel`) built on Echo (`Controller` → `Service` → `Repository`) with embedded UI and FFmpeg:

- `kubereel serve` — starts the Echo HTTP server, embedded dashboard, and Kubernetes controllers.
- `kubereel worker` — executes an assigned workflow step (`mock`, official `gemini` text generation, or official `fal.ai` Queue API image/video generation) inside a temporary Kubernetes Job.
- `kubereel render` — executes controlled FFmpeg media operations (`merge`, `trim`, `resize`, `aspect_ratio`, `add_audio`, `normalize`, `render_mp4`) inside an isolated scratch directory without shell execution.

Kubernetes state is managed via three CRDs (`VideoWorkflow`, `VideoRun`, `VideoProvider`) and PVC-backed artifact storage, with no external database or message broker required.

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

- **Web UI:** Open `http://localhost:8080`, complete the one-time **Initial Administrator Setup** (`/setup`), then run the built-in Mock workflow at `/workflows` and preview/download the rendered MP4.
- **Health checks:** `http://localhost:8080/healthz` and `http://localhost:8080/readyz` (unauthenticated probes).
- **API authentication & workflow test:**

```sh
# 1. One-time initial administrator setup (or POST /api/v1/auth/login afterwards)
TOKEN=$(curl -sS -X POST http://localhost:8080/api/v1/auth/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"replace-with-strong-password"}' | jq -r .token)

# 2. Submit and download a Mock MP4 workflow using Bearer authentication
curl -i -X POST http://localhost:8080/api/workflows \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"A quiet lake"}'

curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/workflows/<WORKFLOW_ID>
curl -H "Authorization: Bearer $TOKEN" -o mock-video.mp4 http://localhost:8080/api/workflows/<WORKFLOW_ID>/artifact
```

- **Multi-step DAG (`VideoWorkflow` + `VideoRun`):**

```sh
kubectl -n kubereel apply -f examples/mock-horror-shorts.yaml
kubectl -n kubereel get videorun horror-shorts-demo -w
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
| `auth.sessionTTLSeconds` | `86400` | Browser session lifetime in seconds (24h) |
| `auth.existingSecret` | `""` | Optional Kubernetes Secret for headless admin bootstrap |
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

- The `mock` provider and FFmpeg produce complete downloadable H.264 MP4 videos end-to-end without external API keys.
- Official **Google Gemini** (`gemini-4-hydrogen` default via the Interactions API) is supported for `llm` steps and official **fal.ai** (via `https://queue.fal.run`) is supported for `image`, text-to-video, and image-to-video steps using `VideoProvider` resources and Kubernetes `Secret` (`secretRef`) credentials — API keys are injected directly into worker Job pods via `secretKeyRef`, endpoints enforce HTTPS + SSRF protection, `429` rate limits honor `Retry-After` with bounded backoff, and uncertain billable submissions transition to `RequiresReview` without automatic retry.
- Built-in **Authentication** protects the Web Dashboard, management REST APIs, and artifact downloads with one-time initial administrator setup (`/setup` & `/api/v1/auth/setup`), `bcrypt` password hashing, `HttpOnly`/`SameSite=Lax` browser sessions with CSRF protection, SHA-256-hashed personal Bearer API tokens (`/api/v1/auth/tokens`), and per-IP login rate limiting.
- The `/api/v1` provider, workflow, execution, and artifact management endpoints are in active development.
