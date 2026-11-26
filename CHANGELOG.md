# Changelog

All notable changes to KubeReel are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and KubeReel adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.3] — 1-Click AI Video Studio & Official Release

### Added

- **1-Click AI Video Studio (`#/studio` default landing experience):**
  - Streamlined Dark AI Studio UI making **Create Video** the default dashboard screen while keeping the React Flow DAG editor available under **Workflows**.
  - Inline 1-step **Connect API Key** quick-connect bar for **fal.ai** (Image & Video AI) and **Google Gemini** (Script AI), alongside built-in **$0 Mock Test** mode.
  - Real end-to-end **Reference Image** support: upload PNG/JPEG/WebP reference images (`POST/GET /api/v1/artifacts/references`, up to 10 MiB, magic-byte validated, path-traversal safe) directly into `fal.ai` Image-to-Video, or use **Auto Reference Frame** generation, **Style Presets**, **Starter Prompts**, and **Reference Notes**.
  - Visual aspect ratio selector (`9:16`, `16:9`, `1:1`, `4:5`), scene count toggle (`1 Clip` / `2 Clips`), live Kubernetes step-by-step progress stepper, inline MP4 player, and recent generated videos strip.
- **One-Command Local Startup:**
  - Added `scripts/dev-up.sh` and `scripts/dev-down.sh` for 1-command local Kind + Helm + Studio UI startup and teardown.
- **Visual Polish & Theme Consistency:**
  - Unified Dark AI Studio theme across `/login`, `/setup`, `/dashboard`, native `<dialog>` modals, Media Library cards/tabs, and interactive controls.

## [0.1.0] — MVP Release

### Added

- **Single-Binary Modular Monolith & Container Image:**
  - Single Go executable (`kubereel`) supporting `serve`, `worker`, and `render` modes packaged in one pinned multi-stage Docker image with FFmpeg and non-root execution (`UID 10001`).
  - Echo-based REST API (`/api/v1`) and embedded React + TypeScript + Tailwind CSS Web Dashboard (`go:embed`, zero Node.js runtime dependency).
- **Kubernetes-Native Workflow Orchestration:**
  - Three CustomResourceDefinitions (`VideoWorkflow`, `VideoRun`, `VideoProvider`) under `kubereel.io/v1alpha1` with CEL validation rules.
  - Directed Acyclic Graph (DAG) validation, concurrent independent step scheduling, dynamic per-step Kubernetes Jobs (`worker` and `render`), bounded retries, cooperative cancellation, duplicate workload prevention, and deterministic restart recovery.
  - Recurring cron scheduling (`/api/v1/workflows/:id/schedule`) with UTC cron expressions, pause/resume, and missed-window protection.
- **Video Production Engine & Official Providers (BYOK):**
  - Built-in **Mock Provider** producing complete downloadable H.264 MP4 videos end-to-end with zero external API keys.
  - Official **Google Gemini** provider (`llm` steps via the official Gemini API).
  - Official **fal.ai** Queue API provider (`image`, `text-to-video`, and `image-to-video` steps with async polling, `Retry-After` rate-limit handling, HTTPS + SSRF protection, and `RequiresReview` guarding on uncertain billable submissions).
  - Controlled **FFmpeg** rendering (`merge`, `trim`, `resize`, `aspect_ratio`, `add_audio`, `normalize`, `render_mp4`) in isolated scratch directories without shell execution.
- **Artifact Storage (PVC & S3-Compatible with Workload Identity):**
  - Bounded atomic PVC filesystem storage (`storage.type=pvc`, default) supporting `ReadWriteOnce` (with control-plane pod affinity) and `ReadWriteMany` (multi-node).
  - S3-compatible object storage (`storage.type=s3`) supporting AWS EKS Workload Identity (Pod Identity / IRSA via dedicated zero-RBAC worker `ServiceAccount` without static AWS credentials) and optional Secret credentials for S3-compatible endpoints (MinIO, Ceph RGW, Cloudflare R2), with HTTP `Range` streaming, size limits, path traversal protection, and configurable retention.
- **Security & Authentication:**
  - Built-in administrator authentication (`/setup`, `/login`, `/api/v1/auth/*`) with `bcrypt` password hashing, `HttpOnly`/`SameSite=Lax` sessions, CSRF protection, SHA-256-hashed Bearer API tokens, and per-IP login rate limiting.
  - Namespace-scoped Kubernetes Secrets (`secretKeyRef`) for provider credentials, least-privilege RBAC, and optional `NetworkPolicy` isolation.
- **Web Dashboard, Visual Workflow Editor & Media Library:**
  - Overview metrics, Workflow CRUD with React Flow drag-and-drop DAG editor and JSON import/export, Execution monitoring with step logs/cancel/retry, Provider management with connectivity checks, Media Library (inline preview, metadata, Range download, safe deletion), and API token management.
- **Observability & Helm Packaging:**
  - Structured JSON logging with workflow/run/step/job correlation IDs, `/healthz` & `/readyz` probes, authenticated Prometheus `/metrics`, and optional `ServiceMonitor`.
  - Production-ready Helm Chart (`charts/kubereel`) with `values.schema.json`, `NOTES.txt`, optional `Ingress`, `NetworkPolicy`, `ServiceMonitor`, and complete installation/upgrade/backup/uninstall documentation ([docs/helm-operations.md](docs/helm-operations.md)).

### Notes

- CRDs in `charts/kubereel/crds/` are installed automatically on `helm install` and must be applied explicitly (`kubectl apply --server-side -f charts/kubereel/crds/`) before `helm upgrade`.
- Cloud validation on live AWS EKS (`F35`–`F37` live cloud benchmarks) is deferred until an AWS EKS environment is provisioned; S3 Workload Identity (`AssumeRoleWithWebIdentity`), multi-node Job specs, Helm templates, and Kind `v1.32.2` E2E tests are verified automatically in CI.
