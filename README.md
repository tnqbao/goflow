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

- **Web Dashboard:** Open `http://localhost:8080` and complete the one-time administrator setup. Use the dashboard to design multi-step DAG workflows in the React Flow visual editor, configure providers, inspect runs, browse/preview/download/delete generated images, clips, and final videos in the **Media Library**, and manage API tokens. To render a sample MP4 with the built-in Mock provider, open **Workflows → Open Mock demo**.
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
| `storage.type` | `pvc` | `pvc` (filesystem) or `s3` (AWS S3 / S3-compatible multi-node storage) |
| `storage.size` | `10Gi` | Control-plane PVC size for `/data` |
| `storage.accessMode` | `ReadWriteOnce` | Use `ReadWriteMany` for multi-node PVC scheduling (not needed when `storage.type=s3`) |
| `storage.existingClaim` | `""` | Existing PVC name (must match `storage.accessMode`) |
| `storage.s3.bucket` | `""` | S3 bucket name (required when `storage.type=s3`) |
| `storage.s3.region` | `""` | S3 region (required when `storage.type=s3`) |
| `storage.s3.existingSecret` | `""` | Optional Secret for S3-compatible credentials (leave empty for EKS Workload Identity) |
| `storage.maxArtifactBytes` | `1073741824` | Max size per artifact (1 GiB) |
| `storage.retentionSeconds` | `0` | Expiry for completed `VideoRun` artifacts (`0` disables) |
| `execution.maxConcurrentJobs` | `5` | Max concurrent Kubernetes Jobs |
| `execution.jobTTLSeconds` | `86400` | Finished Job retention window (24h) |
| `auth.sessionTTLSeconds` | `86400` | Browser session lifetime in seconds (24h) |
| `auth.existingSecret` | `""` | Optional Kubernetes Secret for headless admin bootstrap |
| `ingress.enabled` | `false` | Optional Ingress (requires `ingress.host`) |
| `networkPolicy.enabled` | `false` | Optional NetworkPolicy isolating control plane and Jobs |
| `serviceMonitor.enabled` | `false` | Optional Prometheus ServiceMonitor (requires `bearerTokenSecret.name`) |

### Lifecycle Operations (Upgrade, Backup, CRD Migration, Uninstall)

Helm installs CRDs on initial install but does not automatically update or delete them on upgrade/uninstall. Apply CRD changes before running `helm upgrade`:

```sh
kubectl apply --server-side -f charts/kubereel/crds/
helm upgrade kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
  --version 0.1.0 \
  --namespace kubereel \
  --reuse-values
```

See [docs/helm-operations.md](docs/helm-operations.md) for complete documentation on zero-config vs multi-node `ReadWriteMany` installation, `NetworkPolicy`/`ServiceMonitor` setup, `helm upgrade` & rollback, PVC + CRD backup/restore, and `helm uninstall` behavior.

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

After changing the dashboard source, regenerate the files embedded in the Go binary with Node.js 24:

```sh
npm ci --prefix web
npm test --prefix web
npm run build --prefix web
```

Commit the updated bundle in `internal/web/dashboard/` with the source changes.

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
- The embedded **Web Dashboard**, **Visual Workflow Editor**, and **Media Library** (React, TypeScript, Tailwind CSS, and React Flow) support drag-and-drop DAG design, node configuration (`input`, `llm`, `image`, `video`, `ffmpeg`, `output`), dependency wiring, client/server DAG validation, JSON import/export, execution monitoring, provider setup, media preview/metadata/download/deletion, and API token management.

### Provider management

The [Provider API](docs/provider-api.yaml) runs in Kubernetes mode. Use an administrator Bearer token or a signed-in browser session with a CSRF token for writes. Complete the initial administrator setup over a trusted connection before exposing an ingress.

Create a provider with its Kubernetes name, type and supported capabilities. Gemini and fal also require an API key on creation. Keys go into namespace-scoped Kubernetes Secrets and cannot be retrieved through the API. PATCH accepts a new key for rotation and `enabled: false` to prevent new step submissions. Existing Jobs can still reference the previous Secret version, which is retained until provider deletion. Deleting a provider can affect pending Jobs and retries that reference it, so let them finish before deletion.

The API accepts official provider endpoints for writes. The built-in Mock remains available without configuration, and the versioned list returns persisted VideoProvider resources. The connectivity check validates local Mock configuration or calls the [Gemini model catalog](https://ai.google.dev/api/models) or [fal model pricing API](https://fal.ai/docs/documentation/model-apis/pricing) without submitting generation. fal checks the first configured model. A successful metadata check does not establish access to paid generation.

For an existing Helm installation, apply the updated CRD before upgrading the chart so Kubernetes preserves the enabled setting.

### Workflow management

The authenticated [Workflow API](docs/workflow-api.yaml) manages reusable VideoWorkflow definitions in Kubernetes mode. POST accepts a Kubernetes resource name as `id` and the workflow definition as `spec`. GET returns that spec for export and accepts `limit` and `continue` on the list. PATCH takes only the fields to change under `spec`. Arrays replace the corresponding whole field. Include the returned `resourceVersion` to detect edits made since the definition was read.

Deleting a definition preserves existing runs and their workflow snapshots. For browser writes, send the session's CSRF token. Scripted clients can authenticate with a Bearer token.

### Execution management

The authenticated [Execution API](docs/execution-api.yaml) starts runs from stored VideoWorkflow definitions in Kubernetes mode. POST to `/api/v1/workflows/{id}/runs` with typed inputs and poll the run URL in the `Location` response header. The run keeps the workflow snapshot and input values used at launch.

Cancel requests are reconciled by the operator. A retry starts the entire snapshot in a new run once a failed or cancelled run has no active work. Repeating a retry request for the same source run returns its existing retry. Runs marked `RequiresReview` must be investigated before starting new work. Runs referencing providers other than the built-in Mock require `allowPaid: true` on both launch and retry.

A run exposes its step status and recorded artifact keys. The log endpoint reads a bounded tail from a recorded step Job while its Pod logs remain available.

### Artifact management

The authenticated [Artifact API](docs/artifact-api.yaml) lists available files published by Kubernetes VideoRuns. Filter the list with `runId` and use the returned artifact IDs to fetch metadata, download byte ranges or delete files. Follow `continue` until it is absent, including when a page is empty.

Deletion requires a settled run and no active Jobs. It removes the file while preserving the historical reference in run status. The catalog skips deleted and expired files. The older SQLite workflow prototype continues to serve its MP4 output through its existing workflow download endpoint.

### Scheduled execution

Use the authenticated [Schedule API](docs/schedule-api.yaml) at `/api/v1/workflows/{id}/schedule` to set one recurring schedule per Kubernetes workflow. Supply a five-field UTC cron expression and the typed inputs needed by the workflow. Include `allowPaid: true` to approve recurring use of external providers. PUT replaces the schedule, and `paused: true` pauses future submissions. To resume, PUT the schedule with `paused: false`.

Each launch snapshots the current workflow definition. After downtime, the scheduler evaluates only the newest occurrence within the last 15 minutes. It skips occurrences while the previous scheduled run has active work or needs review. Pause and deletion leave submitted runs in place, and a submission already in progress can complete. Use the Execution API to cancel a submitted run.

### Observability

Use the authenticated Prometheus endpoint at `/metrics` to monitor execution state, Jobs, step retries, artifact publication and provider HTTP requests. The control plane and workers emit structured JSON logs. See the [observability guide](docs/observability.md) for the metric names, scrape configuration and log correlation fields.
