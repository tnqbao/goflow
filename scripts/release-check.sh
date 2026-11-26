#!/bin/sh
# Verify KubeReel release readiness: version alignment, clean architecture, Go & Helm checks, and chart packaging.
set -eu

RELEASE_TAG="${1:-}"

for tool in go helm python3 sha256sum; do
  command -v "$tool" >/dev/null || { printf 'missing required tool: %s\n' "$tool" >&2; exit 1; }
done

printf '==> Checking release version alignment\n'
python3 - "$RELEASE_TAG" <<'PY'
import pathlib, re, sys, yaml

release_tag = sys.argv[1]
chart = yaml.safe_load(pathlib.Path("charts/kubereel/Chart.yaml").read_text(encoding="utf-8"))
values = yaml.safe_load(pathlib.Path("charts/kubereel/values.yaml").read_text(encoding="utf-8"))

chart_version = str(chart.get("version", ""))
app_version = str(chart.get("appVersion", ""))
image_tag = str(values.get("image", {}).get("tag", ""))

semver = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$")
for label, val in (("Chart.yaml version", chart_version), ("Chart.yaml appVersion", app_version), ("values.yaml image.tag", image_tag)):
    if not semver.match(val):
        raise SystemExit(f"{label} must be valid semver, got {val!r}")

if len({chart_version, app_version, image_tag}) != 1:
    raise SystemExit(
        f"version mismatch: Chart.yaml version={chart_version!r}, appVersion={app_version!r}, values.yaml image.tag={image_tag!r}"
    )

if release_tag:
    expected = release_tag.lstrip("v")
    if chart_version != expected:
        raise SystemExit(f"release tag {release_tag!r} does not match chart version {chart_version!r}")

print(f"Version alignment OK: {chart_version}")
PY

printf '==> Auditing for discontinued GoFlow or browser automation dependencies\n'
if grep -rn -i -E '\bgoflow\b|google flow|\bplaywright\b|\bchromedp\b|\bpuppeteer\b|\bselenium\b|go-rod' \
  main.go internal/ web/src/ web/package.json charts/kubereel/ go.mod; then
  printf 'ERROR: forbidden legacy GoFlow or browser automation reference detected\n' >&2
  exit 1
fi
printf 'Architecture cleanliness OK\n'

printf '==> Running Go fmt, vet, test, and build\n'
GOTOOLCHAIN=local go fmt ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -count=1 ./...
GOTOOLCHAIN=local go build ./...
rm -f kubereel

printf '==> Validating and packaging Helm chart\n'
helm lint ./charts/kubereel
helm template kubereel ./charts/kubereel >/dev/null
python3 charts/kubereel/tests/test_chart.py

workdir=$(mktemp -d /tmp/kubereel-release-check.XXXXXX)
trap 'rm -rf "$workdir"' EXIT
helm package ./charts/kubereel --destination "$workdir"
(cd "$workdir" && sha256sum kubereel-chart-*.tgz > SHA256SUMS && cat SHA256SUMS)

printf '==> Release verification passed\n'
