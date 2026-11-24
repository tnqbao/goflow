#!/bin/sh
# Run the mock workflow on an isolated, temporary kind cluster.
set -eu

for tool in kind docker helm kubectl python3; do
  command -v "$tool" >/dev/null || { printf 'missing tool: %s\n' "$tool" >&2; exit 1; }
done

workdir=$(mktemp -d /tmp/kubereel-e2e.XXXXXX)
suffix=$(basename "$workdir" | cut -d. -f2 | tr '[:upper:]' '[:lower:]')
cluster="kr-e2e-$suffix"
image="kubereel-e2e:$suffix"
kubeconfig="$workdir/kubeconfig"
forward_pid=""
cluster_owned=0
image_owned=0

cleanup() {
  if [ -n "$forward_pid" ]; then
    kill "$forward_pid" 2>/dev/null || :
    wait "$forward_pid" 2>/dev/null || :
  fi
  if [ "$cluster_owned" -eq 1 ]; then
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig" >/dev/null || printf 'failed to delete test cluster %s\n' "$cluster" >&2
  fi
  if [ "$image_owned" -eq 1 ]; then
    docker image rm "$image" >/dev/null || printf 'failed to remove test image %s\n' "$image" >&2
  fi
  python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1])' "$workdir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if kind get clusters | grep -Fxq "$cluster"; then
  printf 'test cluster already exists: %s\n' "$cluster" >&2
  exit 1
fi
if docker image inspect "$image" >/dev/null 2>&1; then
  printf 'test image already exists: %s\n' "$image" >&2
  exit 1
fi

docker build -t "$image" .
image_owned=1
cluster_owned=1
kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --wait 120s
kind load docker-image "$image" --name "$cluster"
helm install kubereel ./charts/kubereel --kubeconfig "$kubeconfig" --namespace kubereel-test --create-namespace \
  --set image.repository=kubereel-e2e --set "image.tag=$suffix" --set image.pullPolicy=Never --wait --timeout 120s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test port-forward --address 127.0.0.1 service/kubereel-kubereel :8080 >"$workdir/forward.log" 2>&1 &
forward_pid=$!
port=""
for attempt in $(seq 1 30); do
  port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) -> 8080$/\1/p' "$workdir/forward.log" | head -1)
  if [ -n "$port" ]; then break; fi
  if ! kill -0 "$forward_pid" 2>/dev/null; then cat "$workdir/forward.log" >&2; exit 1; fi
  sleep 1
done
if [ -z "$port" ]; then printf 'port-forward did not become ready\n' >&2; exit 1; fi

python3 - "$port" "$workdir/workflow-id" <<'PY'
import json, sys, time, urllib.error, urllib.request

base = f'http://127.0.0.1:{sys.argv[1]}'
with urllib.request.urlopen(base + '/api/providers', timeout=5) as response:
    providers = json.load(response)
if not any(p['id'] == 'mock' and p['available'] for p in providers):
    raise RuntimeError('Mock provider is unavailable')
if not any(p['id'] == 'google-flow' and not p['available'] for p in providers):
    raise RuntimeError('Google Flow availability is incorrect')
blocked = urllib.request.Request(base + '/api/workflows', data=b'{"prompt":"test","provider":"google-flow"}', headers={'Content-Type': 'application/json'}, method='POST')
try:
    urllib.request.urlopen(blocked, timeout=5)
except urllib.error.HTTPError as error:
    if error.code != 400:
        raise
else:
    raise RuntimeError('unavailable provider was accepted')
request = urllib.request.Request(base + '/api/workflows', data=b'{"prompt":"KubeReel mock workflow","provider":"mock"}', headers={'Content-Type': 'application/json'}, method='POST')
with urllib.request.urlopen(request, timeout=5) as response:
    workflow = json.load(response)
workflow_id = workflow['id']
with open(sys.argv[2], 'w', encoding='utf-8') as output:
    output.write(workflow_id)
deadline = time.monotonic() + 120
while time.monotonic() < deadline:
    with urllib.request.urlopen(base + '/api/workflows/' + workflow_id, timeout=5) as response:
        workflow = json.load(response)
    if workflow['status'] == 'failed':
        raise RuntimeError(workflow.get('error', 'workflow failed'))
    if workflow['status'] == 'succeeded':
        with urllib.request.urlopen(base + '/api/workflows/' + workflow_id + '/artifact', timeout=5) as response:
            artifact = response.read()
        if b'KubeReel mock workflow' not in artifact:
            raise RuntimeError('artifact did not contain the prompt')
        print('Mock workflow and SVG artifact verified')
        break
    time.sleep(2)
else:
    raise RuntimeError('workflow timed out')
PY

workflow_id=$(cat "$workdir/workflow-id")
for attempt in $(seq 1 30); do
  remaining=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get jobs -l "kubereel.io/workflow=$workflow_id" -o name)
  if [ -z "$remaining" ]; then printf 'Workflow Jobs cleaned up\n'; exit 0; fi
  sleep 1
done
printf 'Workflow Jobs were not cleaned up\n' >&2
exit 1
