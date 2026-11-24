# KubeReel — Product Specification and Implementation Plan

> Migration in progress: the user renamed GoFlow to KubeReel and replaced the
> deployment direction with the architecture below. Older GoFlow-specific
> requirements and roadmap entries are retained as historical context and must
> not override these newer requirements. This document does not yet define the
> workflow, provider, rendering, or public API contracts in detail.

## Current deployment architecture

- One Dockerfile, one image and one Go executable. The control plane runs the
  REST API, Kubernetes operator, workflow engine, scheduler and provider manager
  in the same process; static frontend assets are embedded using `go:embed`.
- Helm defaults to one Deployment, one replica and one permanent application
  container. Kubernetes mode provisions temporary Jobs using that same image
  for `kubereel worker` and `kubereel render`, with resource limits and cleanup.
- `kubereel serve` starts the control plane. Components use context cancellation
  and graceful shutdown. Worker and render commands are required but not yet
  implemented.
- PVC storage is the default. Worker placement must respect PVC access modes;
  unrestricted multi-node execution requires shared or S3-compatible storage.
- The default Helm installation must support a complete mock-provider workflow
  without external credentials or additional infrastructure. A future optional
  embedded mode executes tasks in bounded worker pools for standalone Docker.
- Direct HTTP provider integration follows the later P05 request. Browser
  automation, a persistent Chrome runtime, CAPTCHA bypass and manual Google
  session copying are not the intended production architecture.

## Implementation status

The single executable implements `serve`, `worker` and `render`. In Kubernetes
mode, the control plane persists workflow state, exposes a REST API and provider
registry, and reconciles Mock worker and render Jobs using the same image.
The Mock provider produces an explicitly labeled SVG artifact, not generated
video. The Helm chart defaults to one control-plane replica and an RWO PVC; RWO
Jobs are scheduled on the same node, and RWX storage enables multi-node Jobs.
Jobs have resource limits and are deleted after completion, with TTL cleanup as
a fallback. The control plane validates the PVC's actual access modes.

Embedded execution is a future mode. Google Flow and other real generation
providers remain unavailable until their direct HTTP authentication and API
contracts are verified; the Mock provider is not evidence of real generation.
The current chart expects the configured image to be available to cluster nodes.

**Version:** 0.1.0
**Status:** Ready for incremental implementation
**Primary language:** Go
**Development branch:** `develop`

## 1. Vision and principles

GoFlow is a lightweight, self-hosted AI filmmaking workspace that interacts with a user's existing Google Flow account. Users organize projects, characters, scripts, references, and scenes; generate scenes from scripts; review outputs; and regenerate unsatisfactory scenes.

- Zero configuration by default; easy Docker and Helm deployment.
- One application container, one HTTP port, one main Go binary.
- Go-native server-rendered Web UI, not a desktop GUI or SPA.
- SQLite stores workspace metadata and persistent job state.
- Google Flow remains the primary location for generated videos; no S3, MinIO, or permanent local video archive.
- Browser automation is an experimental integration that must be validated before functionality is declared supported.
- Clean, creative workspace UI rather than an industrial dashboard.

### Out of scope for v0.1

Multi-user SaaS, billing, PostgreSQL, Redis, RabbitMQ, microservices, FFmpeg rendering, complex timeline editing, Seedance/Grok integration, and native desktop apps.

## 2. Technology stack

| Area | Technology |
| --- | --- |
| Backend | Go, `net/http`, optionally `chi` if routing warrants it |
| UI | `templ`, HTMX, vanilla CSS |
| Static assets | `go:embed` |
| Database | SQLite via `modernc.org/sqlite` |
| Browser automation | `chromedp` and Chromium |
| Jobs | Go goroutines with durable SQLite state |
| Progress updates | Server-Sent Events |
| Logging | `log/slog` |
| Packaging | Multi-stage Docker image and Helm chart |
| Tests | Go standard testing package |

Avoid adding dependencies before they are needed. Node.js must not be required at runtime. Chromium is a separate process inside the same container.

## 3. User journey

1. Install and launch GoFlow; it initializes `/data` and SQLite automatically.
2. Open the Web UI on port `8080`.
3. Choose a provider: **Google Flow** has an active **Connect with Google** button; **Seedance** and **Grok** are visibly dimmed and disabled with “Coming soon”.
4. Connect through a dedicated Chromium profile and an interactive, secure sign-in flow. No Google password is collected by GoFlow.
5. Create a project, add characters and references, write a script, and review a proposed scene breakdown.
6. Generate scenes in the linked Google Flow project, review results, revise prompts, and regenerate individual scenes without losing generation history.

Google OAuth is not equivalent to an authenticated Google Flow browser session. The login mechanism must be proven on localhost and on a remote container/cluster before promising one-click connection. Do not expose a Chromium debugging port publicly or bypass CAPTCHA or service restrictions.

## 4. Workspace and navigation

```text
GoFlow
├── Projects
│   ├── Project A
│   │   ├── Overview
│   │   ├── Characters
│   │   ├── Script
│   │   ├── Scenes
│   │   └── References
│   └── Project B
│       ├── Overview
│       ├── Characters
│       ├── Script
│       ├── Scenes
│       └── References
└── Settings
    └── Providers
```

All characters, scripts, scenes, and references belong to exactly one project. No global asset library in v0.1.

### Projects

Create, rename, delete with confirmation, open workspace, and link to a Google Flow project when supported. Fields: name, description, aspect ratio, preferred model, Flow project reference, timestamps.

### Characters

Create/edit project-local characters with name, role, appearance, personality, clothing, optional voice description, and image references. Link characters to scenes.

### Script

Write, paste, save, edit, and version scripts. Set target film and scene durations. Break down scripts into editable scene drafts, including title, description, dialogue, characters, setting, camera instructions, prompt, and estimated duration. Script analysis requires a separately validated AI capability; do not assume it is freely available through Flow browser automation.

### Scenes

Scenes are ordered and have title, description, prompt, optional negative prompt when supported, model, aspect ratio, duration, characters, references, and generation state.

States: `draft`, `ready`, `queued`, `generating`, `completed`, `failed`, `needs_review`, `approved`.

Allow manual creation, prompt editing, generation, review, individual regeneration, approval, and reordering. Preserve every generation and prompt revision; never overwrite history.

### References

Support character, environment, style, and scene-specific images. Store necessary metadata and provider references. Temporary local files are allowed for browser uploads when required, but not permanent video storage.

## 5. Google Flow integration

Keep browser-specific code behind a small internal provider interface. Potential operations include connect/status, project listing/linking/creation, model discovery, generation submission, and status retrieval. **These are intended application capabilities, not guaranteed public Google Flow APIs.**

Each operation requires a feasibility check against the actual browser UI. Return a typed unsupported error for unavailable operations. Do not invent selectors or silently fall back to unrelated services.

Browser worker responsibilities: launch/stop Chromium, persist a dedicated profile, detect session expiration, manage one active session, submit verified actions, observe jobs, recover from crashes, and handle timeouts.

Generation may consume Flow credits. Persist job IDs and submission states, bound concurrency, never silently retry an uncertain submission, and ask before resubmitting when duplication is possible. No credit-consuming tests by default.

Video assets remain on Google Flow. SQLite stores metadata, IDs, statuses, and available references. Stable direct video URLs and long-term availability must not be assumed.

## 6. UI/UX

Creative, modern, minimal, responsive, accessible workspace. Use a project sidebar, main script/scene editor, contextual prompt/reference panel, scene gallery, small connection indicator, clear empty/loading/error states, and reusable controls. Avoid enterprise dashboard charts and excessive animations. Prefer server-rendered `templ` components and HTMX; use small JavaScript only where needed.

## 7. Data model

Initial planned tables:

```text
projects
characters
scripts
script_versions
scenes
scene_characters
references
scene_references
generations
generation_jobs
provider_connections
settings
```

Use foreign keys, migrations, transactions, and necessary indexes. Enable SQLite foreign key enforcement. Never store videos as SQLite BLOBs. Persist jobs across restarts.

Default locations: `/data/goflow.db` and `/data/browser`. Make local development defaults sensible without requiring an environment file.

## 8. Deployment

**Docker:** One production image containing Go server, embedded Web UI, Chromium, and dependencies. Port `8080`; persistent `/data` volume; multi-stage build; non-root where practical; health endpoints `/healthz` and `/readyz`. Chromium may require adequate shared memory.

Illustrative command after an image is published:

```bash
docker run -d --name goflow -p 8080:8080 -v goflow-data:/data --shm-size=1g --restart unless-stopped ghcr.io/OWNER/goflow:latest
```

**Helm:** One Deployment replica, one Service, one PVC, optional Ingress, probes, configurable resources. Keep SQLite and browser profile on persistent storage. If no default StorageClass exists, document the required override. Do not deploy multiple active replicas against the same browser profile.

Security: Browser profile and session data are sensitive. Never expose them through APIs or commit them. Remote deployments must have authenticated access protection before enabling browser interaction. Avoid unauthenticated public exposure.

## 9. Engineering process

Follow `AGENTS.md` as the mandatory source of coding-agent workflow and Git rules. Each user request maps to one focused task. Keep commits small and use `#feat/...`, `#fix/...`, or `#refactor/...` messages. Validate and push completed work to `develop`; then stop.

## 10. Incremental roadmap

### Phase 1 — Foundation

| ID | Task |
| --- | --- |
| F01 | Initialize Go module/repository foundation |
| F02 | HTTP server, routing, health checks |
| F03 | templ, HTMX, base CSS |
| F04 | Layout and reusable UI primitives |
| F05 | SQLite and migrations |
| F06 | Zero-config initialization |
| F07 | Production Dockerfile and Compose |
| F08 | Helm chart |

### Phase 2 — Provider connection

| ID | Task |
| --- | --- |
| P01 | Provider interfaces and connection states |
| P02 | Provider selection UI; disabled Seedance/Grok |
| P03 | Chromium lifecycle |
| P04 | Persistent browser profile |
| P05 | Interactive Google login feasibility prototype |
| P06 | Verified Google Flow connection status |
| P07 | Session recovery and disconnect |

P05 is a **feasibility gate**: do not mark browser sign-in complete without real end-to-end validation in the intended deployment environment.

### Phase 3 — Project workspace

| ID | Task |
| --- | --- |
| W01 | Project database model |
| W02 | Project CRUD |
| W03 | Project selection UI |
| W04 | Workspace navigation |
| W05 | Character management |
| W06 | Project references |
| W07 | Script editor and versioning |
| W08 | Scene management |

### Phase 4 — Flow integration

| ID | Task |
| --- | --- |
| G01 | Investigate Flow project navigation |
| G02 | Verified project linking |
| G03 | Discover supported models |
| G04 | Verified generation submission |
| G05 | Generation status tracking |
| G06 | Scene/generation association |
| G07 | Generation history |
| G08 | Individual scene regeneration |

### Phase 5 — AI filmmaking workflow

| ID | Task |
| --- | --- |
| A01 | Structured script breakdown schema |
| A02 | Scene breakdown provider interface |
| A03 | Validated AI script analysis adapter |
| A04 | Editable scene drafts |
| A05 | Batch generation queue |
| A06 | Scene review and approval |
| A07 | Regeneration with prompt revisions |
| A08 | Final project review workspace |

## 11. Definition of done

A task is done only if its acceptance criteria are met, code is formatted and tested, errors are handled, no redundant or nested-`if` code is introduced, secrets are absent, and its focused commit has been pushed successfully to `develop`. Report incomplete/blocked status honestly if any condition fails.

## 12. First task — F01

**Scope:** Reuse existing `go.mod`; create a minimal, idiomatic repository structure, entry point, zero-config defaults (without implementing a full config subsystem), `.gitignore`, README, and a basic startup test. Ensure the application builds and `go test ./...` passes.

**Exclusions:** No database, browser worker, Google auth, project CRUD, frontend components, Docker image, or Helm chart.

**Acceptance:** Builds, tests pass, minimal dependencies, adheres to `AGENTS.md`.

**Commit:** `#feat/initialize-goflow-foundation`
**Push:** `origin develop`
**Stop:** Do not begin F02 until explicitly requested.
