# KubeReel Release Guide (MVP `v0.1.0`)

This document describes the release checklist, automated publishing pipeline, and MVP acceptance verification for KubeReel.

## 1. MVP Acceptance Criteria Audit

| Criterion (`SPEC.md` §24) | Status | Verification |
| --- | --- | --- |
| Helm installs one permanent application container | Verified | `charts/kubereel/tests/test_chart.py`, `./tests/e2e-kind.sh` |
| No obsolete Google Flow functionality in active code | Verified | `./scripts/release-check.sh` |
| No browser automation dependencies remain | Verified | `./scripts/release-check.sh`, `go.mod`, `web/package.json` |
| Embedded Web Dashboard served by Go binary | Verified | `internal/web/dashboard.go`, `npm run build --prefix web` |
| Users can configure AI providers | Verified | `internal/api`, `internal/service`, `./tests/e2e-kind.sh` |
| Users can create and execute workflows | Verified | Visual Workflow Editor, `/api/v1/workflows`, `./tests/e2e-kind.sh` |
| Operator dynamically provisions Kubernetes Jobs | Verified | `internal/kubernetes/job.go`, `./tests/e2e-kind.sh` |
| Independent workflow steps execute concurrently | Verified | `internal/kubernetes/videorun_test.go`, `./tests/e2e-kind.sh` |
| Mock Provider produces a complete downloadable MP4 | Verified | `internal/renderer`, `./tests/e2e-kind.sh` |
| Official Gemini and fal.ai integrations validated | Verified | `internal/provider/gemini`, `internal/provider/fal` |
| Controller restarts preserve execution state | Verified | `internal/kubernetes/videorun_test.go`, `./tests/e2e-kind.sh` |
| Duplicate reconciliation prevents duplicate workloads | Verified | Deterministic step Job naming + `validateStepJob` |
| Provider credentials remain protected | Verified | Namespace `Secret` (`secretKeyRef`), never returned by API |
| Automated tests pass | Verified | `go test -race ./...`, `test_chart.py`, `npm test --prefix web`, `./tests/e2e-kind.sh` |
| Installation and operations documentation complete | Verified | `README.md`, `docs/helm-operations.md`, `docs/observability.md`, `CHANGELOG.md` |

## 2. Pre-Release Verification

Before tagging a release (e.g., `v0.1.0`), verify that versions are synchronized across:

- `charts/kubereel/Chart.yaml` (`version` and `appVersion`)
- `charts/kubereel/values.yaml` (`image.tag`)
- `CHANGELOG.md`

Run the automated release gate locally:

```sh
./scripts/release-check.sh v0.1.0
```

Optionally run the full race detector and live Kind E2E suite before pushing the tag:

```sh
go test -race ./...
./tests/e2e-kind.sh
```

## 3. Publishing a Release

Pushing an annotated semver tag (`vX.Y.Z`) triggers `.github/workflows/pipeline.yml`:

```sh
git tag -a v0.1.0 -m "KubeReel v0.1.0 MVP"
git push origin v0.1.0
```

### Automated Pipeline Outputs

1. **`build` job:**
   - Verifies the Git tag matches `charts/kubereel/Chart.yaml` (`version` & `appVersion`) and `charts/kubereel/values.yaml` (`image.tag`).
   - Validates the embedded frontend bundle (`npm ci`, `npm test`, `npm run build` with clean git diff).
   - Runs `go fmt`, `go vet`, `go build`, and builds the Docker image with `KUBEREEL_VERSION=v0.1.0`.
2. **`test` job:**
   - Runs `go test -race ./...`.
   - Runs `helm lint`, `helm template`, and `charts/kubereel/tests/test_chart.py`.
   - Runs `./tests/e2e-kind.sh` on an isolated Kind `v1.32.2` cluster.
3. **`push` job:**
   - Pushes verified container images to Docker Hub (`iamqbao/kubereel:v0.1.0`, `iamqbao/kubereel:0.1.0`, and `sha-<commit>`).
   - Packages `charts/kubereel` and pushes the Helm OCI chart to `oci://registry-1.docker.io/iamqbao/kubereel-chart:0.1.0`.
   - Uploads `kubereel-chart-0.1.0.tgz` and `SHA256SUMS`.
4. **`release` job:**
   - Creates the GitHub Release for `v0.1.0` attaching `kubereel-chart-0.1.0.tgz` and `SHA256SUMS`.

## 4. Post-Release Smoke Verification

```sh
helm install kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
  --version 0.1.0 \
  --namespace kubereel \
  --create-namespace \
  --wait
```
