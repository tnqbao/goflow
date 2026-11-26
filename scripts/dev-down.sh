#!/bin/sh
# Tear down the local Kind development cluster created by ./scripts/dev-up.sh.
set -eu

CLUSTER_NAME="${KUBEREEL_CLUSTER:-kubereel-dev}"

if command -v go >/dev/null 2>&1; then
  GOPATH_BIN="$(go env GOPATH)/bin"
  case ":$PATH:" in
    *":$GOPATH_BIN:"*) ;;
    *) PATH="$GOPATH_BIN:$PATH"; export PATH ;;
  esac
fi

command -v kind >/dev/null 2>&1 || {
  printf 'Missing required tool: kind\n' >&2
  exit 1
}

if kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
  printf '==> Deleting Kind cluster %s\n' "$CLUSTER_NAME"
  kind delete cluster --name "$CLUSTER_NAME"
else
  printf 'Kind cluster %s does not exist.\n' "$CLUSTER_NAME"
fi
