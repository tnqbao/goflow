#!/bin/sh
# One-command local startup for KubeReel (Kind + Docker + Helm + Studio UI on http://localhost:8080).
# Usage:
#   ./scripts/dev-up.sh                   # Build, deploy/upgrade, and port-forward http://localhost:8080
#   ./scripts/dev-up.sh --no-port-forward # Build and deploy/upgrade without blocking on port-forward
set -eu

CLUSTER_NAME="${KUBEREEL_CLUSTER:-kubereel-dev}"
NAMESPACE="${KUBEREEL_NAMESPACE:-kubereel}"
RELEASE_NAME="${KUBEREEL_RELEASE:-kubereel}"
IMAGE_REPO="${KUBEREEL_IMAGE_REPO:-kubereel}"
IMAGE_TAG="${KUBEREEL_IMAGE_TAG:-local}"
LOCAL_PORT="${KUBEREEL_PORT:-8080}"
ADMIN_USER="${KUBEREEL_ADMIN_USER:-admin}"
ADMIN_PASS="${KUBEREEL_ADMIN_PASSWORD:-KubeReelDev123!}"
NO_PORT_FORWARD=0

for arg in "$@"; do
  case "$arg" in
    --no-port-forward) NO_PORT_FORWARD=1 ;;
    -h|--help)
      printf 'Usage: %s [--no-port-forward]\n' "$0"
      exit 0
      ;;
    *)
      printf 'Unknown argument: %s\n' "$arg" >&2
      exit 1
      ;;
  esac
done

if command -v go >/dev/null 2>&1; then
  GOPATH_BIN="$(go env GOPATH)/bin"
  case ":$PATH:" in
    *":$GOPATH_BIN:"*) ;;
    *) PATH="$GOPATH_BIN:$PATH"; export PATH ;;
  esac
fi

for tool in docker kind helm kubectl; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf 'Missing required tool: %s\n' "$tool" >&2
    exit 1
  }
done

FULL_IMAGE="${IMAGE_REPO}:${IMAGE_TAG}"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"

printf '==> [1/5] Building Docker image %s\n' "$FULL_IMAGE"
docker build -t "$FULL_IMAGE" .

if kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
  printf '==> [2/5] Reusing existing Kind cluster %s\n' "$CLUSTER_NAME"
else
  printf '==> [2/5] Creating Kind cluster %s\n' "$CLUSTER_NAME"
  kind create cluster --name "$CLUSTER_NAME" --wait 120s
fi

printf '==> [3/5] Loading image %s into Kind\n' "$FULL_IMAGE"
kind load docker-image "$FULL_IMAGE" --name "$CLUSTER_NAME"

printf '==> [4/5] Applying CRDs, bootstrap admin secret, and Helm release\n'
kubectl --context "$KUBE_CONTEXT" apply --server-side -f ./charts/kubereel/crds/ >/dev/null
kubectl --context "$KUBE_CONTEXT" create namespace "$NAMESPACE" --dry-run=client -o yaml \
  | kubectl --context "$KUBE_CONTEXT" apply -f - >/dev/null

if ! kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" get secret kubereel-dev-admin >/dev/null 2>&1; then
  kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" create secret generic kubereel-dev-admin \
    --from-literal=password="$ADMIN_PASS" >/dev/null
fi

helm upgrade --install "$RELEASE_NAME" ./charts/kubereel \
  --kube-context "$KUBE_CONTEXT" \
  --namespace "$NAMESPACE" \
  --set "image.repository=${IMAGE_REPO}" \
  --set "image.tag=${IMAGE_TAG}" \
  --set image.pullPolicy=Never \
  --set auth.existingSecret=kubereel-dev-admin \
  --set "auth.username=${ADMIN_USER}" \
  --set auth.passwordKey=password \
  --wait --timeout 180s

kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" rollout restart "deployment/${RELEASE_NAME}-kubereel" >/dev/null
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" rollout status "deployment/${RELEASE_NAME}-kubereel" --timeout=120s

printf '\n==========================================================\n'
printf '  KubeReel Studio is ready!\n'
printf '  URL:      http://localhost:%s\n' "$LOCAL_PORT"
printf '  Username: %s\n' "$ADMIN_USER"
printf '  Password: %s\n' "$ADMIN_PASS"
printf '  Stop all: ./scripts/dev-down.sh\n'
printf '==========================================================\n\n'

if [ "$NO_PORT_FORWARD" -eq 1 ]; then
  printf 'Port-forward skipped (--no-port-forward).\n'
  printf 'Run manually:\n  kubectl --context %s -n %s port-forward service/%s-kubereel %s:8080\n' \
    "$KUBE_CONTEXT" "$NAMESPACE" "$RELEASE_NAME" "$LOCAL_PORT"
  exit 0
fi

printf '==> [5/5] Forwarding http://localhost:%s -> service/%s-kubereel:8080 (Ctrl+C to stop)\n' \
  "$LOCAL_PORT" "$RELEASE_NAME"
exec kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" port-forward "service/${RELEASE_NAME}-kubereel" "${LOCAL_PORT}:8080"
