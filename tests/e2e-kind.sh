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
./tests/docker-smoke.sh "$image"
cluster_owned=1
kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" \
  --image kindest/node:v1.32.2@sha256:f226345927d7e348497136874b6d207e0b32cc52154ad8323129352923a3142f --wait 120s
kind load docker-image "$image" --name "$cluster"
helm install kubereel ./charts/kubereel --kubeconfig "$kubeconfig" --namespace kubereel-test --create-namespace \
  --set image.repository=kubereel-e2e --set "image.tag=$suffix" --set image.pullPolicy=Never --wait --timeout 120s
kubectl --kubeconfig "$kubeconfig" wait --for=condition=Established crd/videoworkflows.kubereel.io --timeout=60s
cat >"$workdir/videoworkflow.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoWorkflow
metadata:
  name: sample
  namespace: kubereel-test
spec:
  inputs:
    - name: topic
      type: string
      required: true
  steps:
    - name: script
      type: llm
      providerRef: mock
      retry: {maxAttempts: 2}
      timeoutSeconds: 60
      resources:
        requests: {cpu: 100m, memory: 128Mi}
    - name: result
      type: output
      dependsOn: [script]
  outputs:
    - name: video
      from: result
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/videoworkflow.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videoworkflow sample -o json >"$workdir/videoworkflow.json"
python3 - "$workdir/videoworkflow.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    spec = json.load(source)["spec"]
assert spec["inputs"][0]["required"] is True
assert spec["steps"][0]["providerRef"] == "mock"
assert spec["steps"][0]["retry"]["maxAttempts"] == 2
assert spec["steps"][0]["timeoutSeconds"] == 60
assert spec["steps"][0]["resources"]["requests"]["cpu"] == "100m"
assert spec["steps"][1]["dependsOn"] == ["script"]
assert spec["outputs"][0]["from"] == "result"
PY
if printf '%s\n' 'apiVersion: kubereel.io/v1alpha1' 'kind: VideoWorkflow' 'metadata: {name: invalid, namespace: kubereel-test}' 'spec: {steps: []}' | kubectl --kubeconfig "$kubeconfig" apply --dry-run=server -f - >"$workdir/invalid.log" 2>&1; then
  printf 'invalid VideoWorkflow was accepted\n' >&2
  exit 1
fi
if ! grep -q 'spec.steps' "$workdir/invalid.log"; then
  cat "$workdir/invalid.log" >&2
  exit 1
fi
kubectl --kubeconfig "$kubeconfig" wait --for=condition=Established crd/videoruns.kubereel.io --timeout=60s
cat >"$workdir/videorun.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: sample-run
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: script
        type: llm
        providerRef: mock
  inputs:
    topic: {stringValue: forest}
    clips: {integerValue: 2}
    enabled: {booleanValue: false}
    scale: {numberValue: 1.5}
  execution: {maxConcurrentSteps: 2, timeoutSeconds: 60}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/videorun.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --type=merge -p '{"spec":{"cancelRequested":true}}'
if kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --type=merge -p '{"spec":{"workflowSnapshot":{"steps":[{"name":"changed","type":"output"}]}}}' >"$workdir/immutable.log" 2>&1; then
  printf 'VideoRun snapshot update was accepted\n' >&2
  exit 1
fi
if ! grep -q 'workflow snapshot is immutable' "$workdir/immutable.log"; then
  cat "$workdir/immutable.log" >&2
  exit 1
fi
if kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --type=merge -p '{"spec":{"inputs":{"topic":{"integerValue":1}}}}' >"$workdir/invalid-input.log" 2>&1; then
  printf 'VideoRun input with multiple values was accepted\n' >&2
  exit 1
fi
if ! grep -q 'exactly one input value is required' "$workdir/invalid-input.log"; then
  cat "$workdir/invalid-input.log" >&2
  exit 1
fi
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --subresource=status --type=merge -p '{"status":{"phase":"Running","steps":[{"name":"script","phase":"Running","attempts":1,"providerRequestID":"request-1"}],"artifacts":["artifact-1"],"startedAt":"2026-01-01T00:00:00Z"}}'
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun sample-run -o json >"$workdir/videorun.json"
python3 - "$workdir/videorun.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    run = json.load(source)
assert run["spec"]["cancelRequested"] is True
assert run["spec"]["inputs"]["enabled"]["booleanValue"] is False
assert run["spec"]["workflowSnapshot"]["steps"][0]["name"] == "script"
assert run["status"]["phase"] == "Running"
assert run["status"]["steps"][0]["providerRequestID"] == "request-1"
assert run["status"]["artifacts"] == ["artifact-1"]
PY
cat >"$workdir/reconcile-runs.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: dag-valid
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - {name: first, type: input}
      - {name: second, type: output, dependsOn: [first]}
      - {name: independent, type: image}
---
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: dag-invalid
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - {name: first, type: input, dependsOn: [missing]}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/reconcile-runs.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Pending videorun/dag-valid --timeout=60s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Failed videorun/dag-invalid --timeout=60s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun dag-valid -o json >"$workdir/dag-valid.json"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun dag-invalid -o json >"$workdir/dag-invalid.json"
python3 - "$workdir/dag-valid.json" "$workdir/dag-invalid.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    valid = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    invalid = json.load(source)
steps = {step["name"]: step for step in valid["status"]["steps"]}
assert steps["first"]["ready"] is True
assert steps["second"]["ready"] is False
assert steps["independent"]["ready"] is True
assert "unknown dependency" in invalid["status"]["error"]
PY
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun dag-valid --subresource=status --type=merge -p '{"status":{"steps":[{"name":"first","phase":"Succeeded"},{"name":"second","phase":"Pending"},{"name":"independent","phase":"Pending"}]}}'
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.steps[1].ready}'=true videorun/dag-valid --timeout=60s
kubectl --kubeconfig "$kubeconfig" wait --for=condition=Established crd/videoproviders.kubereel.io --timeout=60s
cat >"$workdir/videoprovider.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoProvider
metadata:
  name: sample-provider
  namespace: kubereel-test
spec:
  type: gemini
  capabilities: [text]
  models: [example-model]
  baseURL: https://example.com/api
  secretRef: {name: provider-credentials, key: api-key}
  maxConcurrent: 2
  timeoutSeconds: 60
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/videoprovider.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videoprovider sample-provider -o json >"$workdir/videoprovider.json"
python3 - "$workdir/videoprovider.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    spec = json.load(source)["spec"]
assert spec["type"] == "gemini"
assert spec["capabilities"] == ["text"]
assert spec["models"] == ["example-model"]
assert spec["baseURL"] == "https://example.com/api"
assert spec["secretRef"] == {"name": "provider-credentials", "key": "api-key"}
assert spec["maxConcurrent"] == 2
assert spec["timeoutSeconds"] == 60
PY
if kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videoprovider sample-provider --type=json -p='[{"op":"remove","path":"/spec/secretRef"}]' >"$workdir/provider-invalid.log" 2>&1; then
  printf 'provider without a Secret reference was accepted\n' >&2
  exit 1
fi
if ! grep -q 'a Secret reference is required' "$workdir/provider-invalid.log"; then
  cat "$workdir/provider-invalid.log" >&2
  exit 1
fi
if printf '%s\n' 'apiVersion: kubereel.io/v1alpha1' 'kind: VideoProvider' 'metadata: {name: invalid, namespace: kubereel-test}' 'spec: {type: mock, capabilities: [text], apiKey: not-a-real-key}' | kubectl --kubeconfig "$kubeconfig" apply --dry-run=server --validate=strict -f - >"$workdir/provider-secret.log" 2>&1; then
  printf 'provider accepted a credential field\n' >&2
  exit 1
fi
if ! grep -q 'spec.apiKey' "$workdir/provider-secret.log"; then
  cat "$workdir/provider-secret.log" >&2
  exit 1
fi
printf '%s\n' 'apiVersion: kubereel.io/v1alpha1' 'kind: VideoProvider' 'metadata: {name: mock, namespace: kubereel-test}' 'spec: {type: mock, capabilities: [text, image]}' | kubectl --kubeconfig "$kubeconfig" apply -f -
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
if len(providers) != 1:
    raise RuntimeError('unexpected provider in registry')
blocked = urllib.request.Request(base + '/api/workflows', data=b'{"prompt":"test","provider":"unknown"}', headers={'Content-Type': 'application/json'}, method='POST')
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

with urllib.request.urlopen(base + '/workflows', timeout=5) as response:
    if b'Run mock workflow' not in response.read():
        raise RuntimeError('mock workflow form missing')
form = urllib.request.Request(base + '/workflows', data=b'prompt=UI+mock+workflow', headers={'Content-Type': 'application/x-www-form-urlencoded'}, method='POST')
with urllib.request.urlopen(form, timeout=5) as response:
    ui_id = response.geturl().rsplit('/', 1)[-1]
    if b'Workflow status' not in response.read():
        raise RuntimeError('mock workflow status page missing')
deadline = time.monotonic() + 120
while time.monotonic() < deadline:
    with urllib.request.urlopen(base + '/api/workflows/' + ui_id, timeout=5) as response:
        workflow = json.load(response)
    if workflow['status'] == 'failed':
        raise RuntimeError(workflow.get('error', 'UI workflow failed'))
    if workflow['status'] == 'succeeded':
        with urllib.request.urlopen(base + '/workflows/' + ui_id, timeout=5) as response:
            if b'Download mock SVG' not in response.read():
                raise RuntimeError('completed UI workflow has no artifact link')
        print('Mock workflow UI verified')
        break
    time.sleep(2)
else:
    raise RuntimeError('UI workflow timed out')
PY

workflow_id=$(cat "$workdir/workflow-id")
for attempt in $(seq 1 30); do
  remaining=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get jobs -l "kubereel.io/workflow=$workflow_id" -o name)
  if [ -z "$remaining" ]; then printf 'Workflow Jobs cleaned up\n'; exit 0; fi
  sleep 1
done
printf 'Workflow Jobs were not cleaned up\n' >&2
exit 1
