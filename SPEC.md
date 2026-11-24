# KubeReel — Product & Technical Specification

Version: 2.0.0  
Status: Initial Development  
Architecture: Kubernetes-native Modular Monolith  
Deployment: Single Image, Single Control Plane

## 0. Mandatory Project Migration

This specification completely replaces all previous GoFlow specifications, requirements, roadmaps, and architectural decisions.

KubeReel is a new product developed from the existing repository.

GoFlow is discontinued.

The existing Google Flow integration must not be preserved as a legacy feature, optional feature, disabled feature, or future integration.

### Mandatory Removal

Remove all obsolete components related to:

- Google Flow integration.
- Google Flow authentication.
- Google Flow OAuth.
- Google Flow session management.
- Google Flow credit management.
- Browser automation.
- Chrome and Chromium dependencies.
- Playwright, Rod, and chromedp dependencies used for browser automation.
- Browser extensions.
- Cookie extraction and management.
- Reverse-engineered Google endpoints.
- Google Flow project management.
- Legacy Seedance and Grok UI placeholders.
- Obsolete frontend pages and routes.
- Obsolete environment variables.
- Obsolete configuration files.
- Obsolete tests and documentation specific to discontinued features.

Do not preserve obsolete code for backward compatibility.

Do not introduce replacement browser automation mechanisms.

Google Gemini integration is permitted only through documented official Gemini APIs.

Google Gemini API and Google Flow are separate products.

Preserve Git history, unrelated user changes, and genuinely reusable infrastructure.

Audit the repository before removing components.

Do not rename the remote repository or change the Go module path without confirming the canonical repository location.

## 1. Product Definition

KubeReel is an open-source, self-hosted, Kubernetes-native infrastructure platform for automated AI video production.

KubeReel enables users to design reusable video production workflows, connect external AI providers, execute distributed workloads, process media, and generate downloadable videos.

KubeReel is not an AI model.

KubeReel is not a browser automation tool.

KubeReel is not an AI subscription gateway.

KubeReel is a video production orchestration platform.

Product slogan:

Install once. Connect your providers. Automate video production.

## 2. Product Objectives

- Provide one-command Helm installation.
- Require minimal user configuration.
- Support complete demonstrations without paid APIs.
- Allow users to connect their own AI providers.
- Automate repetitive video production tasks.
- Support parallel and distributed workload execution.
- Provide reliable execution and failure recovery.
- Support cloud and on-premises Kubernetes environments.
- Provide a simple Web UI and documented REST API.

## 3. User Experience

The expected user journey is:

1. Install KubeReel using Helm.
2. Access the Web Dashboard.
3. Complete initial authentication setup.
4. Configure AI providers using API keys.
5. Select or create a video workflow.
6. Enter workflow parameters.
7. Execute the workflow.
8. Monitor execution progress.
9. Preview generated media.
10. Download the final video.

Users must not need to manually create Kubernetes Jobs, Secrets, ConfigMaps, or workflow YAML files for normal usage.

## 4. Technology Stack

### Core

- Golang.
- Gin.
- Kubernetes.
- client-go.
- controller-runtime.
- Kubebuilder.
- Helm.
- Docker.
- FFmpeg.

### Frontend

- React.
- TypeScript.
- Vite.
- Tailwind CSS.
- React Flow.

### Infrastructure

- Kubernetes Custom Resources.
- PersistentVolumeClaim.
- Optional S3-compatible object storage.
- AWS EKS for cloud validation.
- AWS S3 for distributed artifact storage.

### Engineering

- GitHub Actions.
- Prometheus.
- OpenTelemetry.
- Go testing.
- envtest.
- Kind.

Use stable, supported, explicitly pinned dependency versions.

## 5. Architecture

KubeReel must use a modular monolith architecture.

The default installation must contain exactly one permanent application container.

All control plane components must run inside the same Go process.

### Internal Components

- HTTP API Server.
- Embedded Web UI.
- Kubernetes Operator.
- Workflow Engine.
- Execution Manager.
- Scheduler.
- Provider Manager.
- Credential Manager.
- Artifact Manager.
- Observability.

These components are internal Go modules, not independent microservices.

Do not deploy separate permanent containers for the frontend, API server, scheduler, operator, or workers.

### Dynamic Workloads

The control plane creates temporary Kubernetes Jobs when workflow steps require execution.

Temporary Jobs may run concurrently and on different Kubernetes nodes when storage configuration permits.

Jobs must be cleaned up according to configurable retention policies.

### External Services

The application must not require:

- Redis.
- RabbitMQ.
- PostgreSQL.
- Kafka.
- MinIO.
- External workflow engines.
- Browser runtimes.

Optional external object storage is permitted.

## 6. Single Image Architecture

Maintain:

- One Dockerfile.
- One Docker image.
- One Go executable.
- One release pipeline.

The executable must support the following runtime commands:

```bash
kubereel serve
kubereel worker
kubereel render
```

### serve

Starts the complete control plane:

- REST API.
- Web UI.
- Kubernetes controllers.
- Workflow scheduling.
- Provider management.
- Artifact management.

### worker

Executes a single assigned workflow step:

- LLM generation.
- Image generation.
- Video generation.
- Provider status polling.
- Artifact retrieval.

### render

Executes media processing tasks using FFmpeg.

The same Docker image must be reused for control plane and temporary execution Jobs.

Frontend assets must be embedded into the Go executable using go:embed.

FFmpeg must be included in the runtime image.

All components must support graceful shutdown and context cancellation.

## 7. Kubernetes Resources

API Group: kubereel.io

Initial Version: v1alpha1

### 7.1 VideoWorkflow

Defines a reusable workflow.

Fields:

- Workflow metadata.
- Input parameter definitions.
- Step definitions.
- Step dependencies.
- Provider references.
- Retry policies.
- Timeout policies.
- Resource requirements.
- Output definitions.

### 7.2 VideoRun

Represents an individual workflow execution.

Fields:

- Workflow reference.
- Immutable workflow snapshot.
- Input parameters.
- Execution configuration.
- Cancellation request.
- Execution status.
- Step statuses.
- Artifact references.
- Error information.
- Execution timestamps.

### 7.3 VideoProvider

Represents an AI provider configuration.

Fields:

- Provider type.
- Supported capabilities.
- Model configuration.
- Base URL when applicable.
- Kubernetes Secret reference.
- Concurrency limits.
- Timeout configuration.

API keys must not be stored directly inside Custom Resources.

Use Kubernetes Secrets for credentials.

Do not store binary media in Kubernetes resources or etcd.

Bound resource sizes and execution history retention.

## 8. Workflow Execution Model

Workflows must use a Directed Acyclic Graph.

Execution lifecycle:

1. User creates a workflow.
2. User submits a workflow execution.
3. API Server creates a VideoRun.
4. Operator watches the VideoRun.
5. Operator validates the workflow snapshot.
6. Operator identifies executable steps.
7. Operator creates Kubernetes Jobs.
8. Workers execute assigned tasks.
9. Workers persist outputs to artifact storage.
10. Operator observes Job status and execution results.
11. Operator schedules dependent steps.
12. FFmpeg renders the final video.
13. Operator marks the VideoRun as completed.
14. User downloads the final artifact.

Independent steps must support parallel execution.

The Operator must not perform long-running provider requests inside reconciliation loops.

### Execution States

- Pending.
- Running.
- Succeeded.
- Failed.
- Cancelling.
- Cancelled.
- RequiresReview.

### Reliability Requirements

- Idempotent reconciliation.
- Deterministic Job identifiers.
- Duplicate workload prevention.
- Controller restart recovery.
- Step-level retries.
- Configurable timeouts.
- Cancellation.
- Dependency validation.
- Cycle detection.
- Bounded concurrency.
- Preservation of successful step outputs.

External provider request identifiers must be persisted when available.

If a billable submission has an uncertain outcome, do not automatically submit another request.

Mark the step as RequiresReview when safe reconciliation is impossible.

Do not claim exactly-once external API execution.

## 9. AI Provider Architecture

KubeReel must use a Bring Your Own Key model.

Users configure API keys through the Dashboard or REST API.

The platform must not provide shared commercial AI credentials.

Provider integrations must use documented, authorized APIs.

### Provider Capabilities

- Text generation.
- Image generation.
- Video generation.
- Audio generation.

Use capability-specific Go interfaces.

Example video provider contract:

```go
type VideoProvider interface {
    Submit(
        ctx context.Context,
        request VideoRequest,
    ) (GenerationHandle, error)

    GetStatus(
        ctx context.Context,
        handle GenerationHandle,
    ) (GenerationStatus, error)

    GetResult(
        ctx context.Context,
        handle GenerationHandle,
    ) (VideoResult, error)
}
```

Provider-specific parameters and response formats must remain inside provider adapters.

Support cancellation when the provider exposes a documented cancellation API.

## 10. Initial Providers

### 10.1 Mock Provider

Mandatory and enabled by default.

Capabilities:

- Generate sample scripts.
- Generate sample images.
- Generate valid sample video clips.
- Simulate asynchronous generation.
- Simulate latency.
- Simulate failures.
- Simulate rate limiting.
- Support deterministic testing.

No API key required.

No external network requests required.

The default demonstration workflow must produce a valid MP4 using only the Mock Provider and FFmpeg.

### 10.2 Gemini Provider

Purpose:

- Generate video ideas.
- Generate scripts.
- Generate storyboards.
- Generate structured prompts.

Authentication:

- Official Gemini API key.

API documentation:

https://ai.google.dev/gemini-api/docs

Do not use Google OAuth for Gemini integration.

Do not use Google Flow sessions, cookies, subscriptions, or credits.

### 10.3 fal.ai Provider

Purpose:

- Text-to-image generation.
- Text-to-video generation.
- Image-to-video generation.

Authentication:

- Official fal.ai API key.

API documentation:

https://docs.fal.ai/

Requirements:

- Configurable model identifiers.
- Asynchronous queue submission.
- Generation status polling.
- Artifact retrieval.
- Provider-specific parameter validation.
- Rate-limit handling.
- Bounded retries.

Do not hardcode undocumented endpoints.

Do not assume all models support identical parameters.

### Future Providers

- OpenAI-compatible endpoints.
- 9Router.
- ComfyUI.
- Replicate.
- Official video generation APIs.

Future providers are not part of the initial implementation.

Do not create placeholder integrations for unsupported providers.

## 11. Workflow Nodes

### Input Node

Receives workflow parameters.

Examples:

- Topic.
- Video description.
- Duration.
- Aspect ratio.
- Style.
- Number of clips.

### LLM Node

Generates:

- Ideas.
- Scripts.
- Scene descriptions.
- Structured prompts.

### Image Node

Generates reference images and scene images.

### Video Node

Generates video clips through a configured video provider.

### FFmpeg Node

Supports controlled media processing operations:

- Merge clips.
- Trim clips.
- Resize video.
- Change aspect ratio.
- Add audio.
- Normalize media formats.
- Render final MP4.

Do not allow arbitrary shell commands.

### Output Node

Persists final artifacts and exposes them through the Media Library.

### Future Nodes

- Text-to-Speech.
- Subtitle generation.
- Audio generation.
- Conditional execution.
- Dynamic fan-out.
- Human approval.
- Scene regeneration.
- Social media publishing.

## 12. Example Workflow

Workflow name: Horror Shorts

Input:

```json
{
  "topic": "Unknown creatures in the forest",
  "style": "Found footage horror",
  "aspect_ratio": "9:16"
}
```

Execution:

1. Gemini generates a script and scene prompts.
2. Two independent image generation steps create reference images.
3. Two video generation steps create video clips.
4. FFmpeg merges the clips into a final video.
5. The final video is stored in the Media Library.

The initial MVP may use a fixed number of explicitly defined scenes.

Dynamic scene expansion is a future feature.

The same workflow must support execution with Mock Provider instead of paid providers.

## 13. Web Dashboard

The application must provide a clean, responsive, modern interface.

### Navigation

- Overview.
- Workflows.
- Executions.
- Providers.
- Media Library.
- Settings.

### Overview

Display:

- Total workflows.
- Active executions.
- Successful executions.
- Failed executions.
- Generated videos.
- Recent activity.

### Providers

Users can:

- Add a provider.
- Configure API keys.
- Select supported models.
- Test provider connectivity.
- Enable or disable configured providers.
- Configure concurrency limits.

Do not display Google Flow login buttons.

Do not display unavailable provider placeholders.

### Workflows

Users can:

- Create workflows.
- Edit workflows.
- Delete workflows.
- Import and export workflow definitions.
- Configure workflow inputs.
- Execute workflows.

### Visual Editor

Use React Flow for node-based workflow editing.

Support:

- Drag and drop.
- Node configuration.
- Dependency connections.
- Workflow validation.
- Saving workflow definitions.

### Executions

Display:

- Execution status.
- Individual step status.
- Progress.
- Logs.
- Errors.
- Start and completion timestamps.
- Retry actions.
- Cancellation actions.

### Media Library

Display:

- Generated images.
- Generated clips.
- Final videos.
- Artifact metadata.
- Preview.
- Download.
- Delete.

The frontend must consume the REST API.

Do not place provider credentials or business logic inside frontend code.

## 14. REST API

Base path: /api/v1

### Providers

- POST /providers
- GET /providers
- GET /providers/{id}
- PATCH /providers/{id}
- DELETE /providers/{id}
- POST /providers/{id}/test

### Workflows

- POST /workflows
- GET /workflows
- GET /workflows/{id}
- PATCH /workflows/{id}
- DELETE /workflows/{id}

### Executions

- POST /workflows/{id}/runs
- GET /runs
- GET /runs/{id}
- POST /runs/{id}/cancel
- POST /runs/{id}/retry
- GET /runs/{id}/logs
- GET /runs/{id}/artifacts

### Artifacts

- GET /artifacts
- GET /artifacts/{id}
- GET /artifacts/{id}/download
- DELETE /artifacts/{id}

### System

- GET /healthz
- GET /readyz
- GET /metrics
- GET /version

Requirements:

- OpenAPI documentation.
- Consistent structured responses.
- Input validation.
- Authentication.
- Authorization.
- Pagination.
- Safe error handling.
- SSE for live execution updates where appropriate.

## 15. Artifact Storage

Implement a storage abstraction.

Supported artifacts:

- JSON.
- Text.
- Images.
- Video clips.
- Audio.
- Final MP4 files.
- Execution manifests.

### Default Storage

PVC-backed filesystem storage.

The default deployment must support a single-node Kubernetes cluster.

For ReadWriteOnce storage, ensure worker placement is compatible with volume topology and access restrictions.

### Production Storage

Optional S3-compatible storage.

Support AWS S3 for multi-node cloud deployments.

Use workload identity where available.

Do not require static AWS credentials when workload identity can be used.

### Requirements

- Unique artifact identifiers.
- Atomic artifact publication where possible.
- Configurable retention.
- Safe cleanup.
- Protected downloads.
- File size limits.
- Path traversal prevention.

## 16. Helm Installation

Helm is the primary installation method.

Target installation command after publication:

```bash
helm install kubereel \
  oci://ghcr.io/OWNER/charts/kubereel \
  --namespace kubereel \
  --create-namespace
```

The Helm Chart must install:

- One Deployment.
- One permanent application container.
- One Service.
- One ServiceAccount.
- Required RBAC resources.
- Three CRDs.
- One PVC.
- Required configuration.

Optional:

- Ingress.
- NetworkPolicy.
- ServiceMonitor.

Temporary execution Jobs must not be deployed as permanent workloads.

### Default Configuration

```yaml
replicaCount: 1

execution:
  mode: kubernetes
  maxConcurrentJobs: 5

storage:
  type: pvc
  size: 10Gi

providers:
  mock:
    enabled: true

service:
  type: ClusterIP
  port: 8080

ingress:
  enabled: false
```

The values above define the intended configuration contract.

The actual chart must use pinned release image versions.

### Zero-config Requirements

A user with a supported Kubernetes cluster and a compatible default StorageClass must be able to install and run the demonstration workflow without:

- AI API keys.
- External databases.
- External message brokers.
- External object storage.
- GPU hardware.
- Browser installation.
- Manual Kubernetes resource configuration.

Provide a secure initial administrator setup.

Document Helm upgrade, uninstall, backup, and CRD migration behavior.

## 17. Security

- Namespace-scoped application resources.
- Least-privilege RBAC.
- Separate restricted worker identity where necessary.
- Kubernetes Secrets for provider credentials.
- No secrets in logs.
- No secrets in API responses.
- Authenticated management endpoints.
- Protected artifact access.
- Non-root containers.
- Resource limits.
- Restricted container privileges.
- SSRF protection.
- Safe provider endpoint validation.
- Safe FFmpeg arguments.
- No arbitrary shell execution.
- No privileged containers.
- No hostPath mounts by default.

Management APIs must not be publicly exposed without authentication.

## 18. Resource Management

Support:

- Maximum concurrent workflows.
- Maximum concurrent Jobs.
- Provider concurrency limits.
- CPU requests and limits.
- Memory requests and limits.
- Job deadlines.
- Retry limits.
- Artifact size limits.
- Maximum generation requests per execution.

Delegate Pod placement to the Kubernetes scheduler.

Do not implement a custom Kubernetes scheduler in the MVP.

GPU workloads are optional future functionality.

## 19. Observability

Provide structured logging.

Every execution log must include relevant identifiers:

- Workflow ID.
- Run ID.
- Step ID.
- Job ID.

Prometheus metrics:

- Active workflow executions.
- Completed executions.
- Failed executions.
- Execution duration.
- Step duration.
- Active Jobs.
- Provider request duration.
- Provider errors.
- Retry counts.
- Generated artifacts.

Do not use unbounded execution identifiers as Prometheus metric labels.

Support health and readiness probes.

## 20. Cost Management

Users are responsible for external provider charges.

The MVP must support:

- Explicit opt-in for paid provider execution.
- Maximum generation requests.
- Provider concurrency limits.
- Retry limits.
- Configurable duration limits.
- Provider request tracking.

Estimated cost information may be displayed only when reliable pricing data is available.

Never claim estimated costs are actual charges.

Advanced budget enforcement and provider routing are future features.

## 21. Repository Structure

```text
kubereel/
├── cmd/
│   └── kubereel/
│       └── main.go
├── api/
│   └── v1alpha1/
├── internal/
│   ├── app/
│   ├── server/
│   ├── controller/
│   ├── workflow/
│   ├── executor/
│   ├── scheduler/
│   ├── provider/
│   │   ├── mock/
│   │   ├── gemini/
│   │   └── fal/
│   ├── renderer/
│   ├── storage/
│   ├── config/
│   └── observability/
├── web/
├── charts/
│   └── kubereel/
├── config/
│   ├── crd/
│   └── rbac/
├── examples/
├── tests/
├── docs/
├── .github/
│   └── workflows/
├── Dockerfile
├── Makefile
├── go.mod
├── README.md
├── SPEC.md
└── AGENTS.md
```

This structure is a target, not a requirement to create empty directories.

Reuse existing components only when they are compatible with the new architecture.

## 22. Testing

Required testing:

- Unit tests.
- Provider adapter tests using fake HTTP servers.
- Workflow DAG validation tests.
- Kubernetes controller tests.
- Job execution tests.
- Restart recovery tests.
- Duplicate workload prevention tests.
- FFmpeg output validation.
- Helm rendering tests.
- Kind end-to-end tests.

Automated tests must use Mock Provider by default.

Do not make paid API requests during automated tests.

## 23. Development Roadmap

### Phase 0 — Migration

- F01: Audit and remove discontinued GoFlow functionality.
- F02: Rename application and update project documentation.

### Phase 1 — Foundation

- F03: Initialize single-executable application.
- F04: Implement application lifecycle management.
- F05: Implement initial Docker image.
- F06: Implement initial Helm Chart.
- F07: Configure CI/CD and Kind testing.

### Phase 2 — Kubernetes Core

- F08: Implement VideoWorkflow CRD.
- F09: Implement VideoRun CRD.
- F10: Implement VideoProvider CRD.
- F11: Implement DAG validation.
- F12: Implement workflow reconciliation.
- F13: Implement Kubernetes Job execution.
- F14: Implement retry and cancellation.
- F15: Implement restart recovery.

### Phase 3 — Video Production

- F16: Implement artifact storage.
- F17: Implement provider interfaces.
- F18: Implement Mock Provider.
- F19: Implement FFmpeg rendering.
- F20: Complete Mock Provider end-to-end workflow.
- F21: Implement Gemini provider.
- F22: Implement fal.ai provider.

### Phase 4 — Product

- F23: Implement authentication.
- F24: Implement provider management API.
- F25: Implement workflow management API.
- F26: Implement execution management API.
- F27: Implement artifact management API.
- F28: Implement scheduled execution.
- F29: Implement observability.
- F30: Implement embedded Web Dashboard.
- F31: Implement visual workflow editor.
- F32: Implement Media Library.

### Phase 5 — Cloud Validation

- F33: Validate Helm installation.
- F34: Implement S3 storage.
- F35: Deploy to AWS EKS.
- F36: Validate distributed workloads.
- F37: Benchmark throughput and recovery.
- F38: Publish the MVP release.

Implement one task at a time.

Do not implement the entire roadmap in one request.

## 24. MVP Acceptance Criteria

The MVP is accepted when:

- Helm installs one permanent application container.
- No obsolete Google Flow functionality exists in active application code.
- No browser automation dependencies remain.
- The Dashboard is served by the Go application.
- Users can configure AI providers.
- Users can create and execute workflows.
- The Operator dynamically provisions Kubernetes Jobs.
- Independent workflow steps execute concurrently.
- Mock Provider produces a complete downloadable MP4.
- Official Gemini and fal.ai integrations are implemented and validated.
- Controller restarts preserve execution state.
- Duplicate reconciliation does not create duplicate workloads.
- Provider credentials remain protected.
- Automated tests pass.
- Installation documentation is complete.

## 25. Cloud Native Validation

Deploy the completed application to AWS EKS.

Validate:

- Helm deployment.
- Multi-node workload execution.
- AWS S3 artifact storage.
- Workload identity.
- Resource isolation.
- Concurrent workflow execution.
- Controller recovery.
- Job failure recovery.
- Observability.

Publish only actual measured benchmark results.

Do not introduce AWS-specific dependencies into the core workflow engine.

## 26. Final Architectural Rule

KubeReel must remain a lightweight, Kubernetes-native video production infrastructure platform.

The default product is one image, one permanent container, one Helm release, and dynamically provisioned execution workloads.

Google Flow is discontinued and must not be restored.

Any conflicting legacy requirement is superseded by this specification.