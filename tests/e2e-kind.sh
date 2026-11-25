#!/bin/sh
# Run the mock workflow on an isolated, temporary kind cluster.
set -eu

for tool in kind docker helm kubectl python3; do
  command -v "$tool" >/dev/null || { printf 'missing tool: %s\n' "$tool" >&2; exit 1; }
done

prebuilt_image=${1:-}
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

if [ -n "$prebuilt_image" ]; then
  docker tag "$prebuilt_image" "$image"
else
  docker build -t "$image" .
fi
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
        type: input
  inputs:
    topic: {stringValue: forest}
    clips: {integerValue: 2}
    enabled: {booleanValue: false}
    scale: {numberValue: 1.5}
  execution: {maxConcurrentSteps: 2, timeoutSeconds: 60}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/videorun.yaml"
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
if kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --type=merge -p '{"spec":{"inputs":{"topic":{"stringValue":"changed"}}}}' >"$workdir/immutable-input.log" 2>&1; then
  printf 'VideoRun input update was accepted\n' >&2
  exit 1
fi
if ! grep -q 'run inputs are immutable' "$workdir/immutable-input.log"; then
  cat "$workdir/immutable-input.log" >&2
  exit 1
fi
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --subresource=status --type=merge -p '{"status":{"phase":"Running","steps":[{"name":"script","phase":"Running","attempts":1,"providerRequestID":"request-1"}],"artifacts":["artifact-1"],"startedAt":"2026-01-01T00:00:00Z"}}'
kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun sample-run -o json >"$workdir/videorun.json"
python3 - "$workdir/videorun.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as source:
    run = json.load(source)
assert run["spec"]["cancelRequested"] is False
assert run["spec"]["inputs"]["enabled"]["booleanValue"] is False
assert run["spec"]["workflowSnapshot"]["steps"][0]["name"] == "script"
assert run["status"]["phase"] == "Running"
assert run["status"]["steps"][0]["providerRequestID"] == "request-1"
assert run["status"]["artifacts"] == ["artifact-1"]
PY
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun sample-run --type=merge -p '{"spec":{"cancelRequested":true}}'
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
cat >"$workdir/mock-image-run.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: mock-image-run
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: image
        type: image
        providerRef: mock
        timeoutSeconds: 60
        resources:
          requests: {cpu: 100m, memory: 128Mi}
          limits: {cpu: "1", memory: 512Mi}
  inputs:
    prompt: {stringValue: "VideoRun mock image"}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/mock-image-run.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Succeeded videorun/mock-image-run --timeout=120s
artifact=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun mock-image-run -o jsonpath='{.status.artifacts[0]}')
kubectl --kubeconfig "$kubeconfig" -n kubereel-test exec deployment/kubereel-kubereel -- cat "/data/$artifact" >"$workdir/image.svg"
if ! grep -q 'VideoRun mock image' "$workdir/image.svg"; then
  printf 'VideoRun image artifact did not contain the prompt\n' >&2
  exit 1
fi
if printf '%s\n' 'apiVersion: kubereel.io/v1alpha1' 'kind: VideoWorkflow' 'metadata: {name: bad-ffmpeg, namespace: kubereel-test}' 'spec: {steps: [{name: render, type: ffmpeg}]}' | kubectl --kubeconfig "$kubeconfig" apply --dry-run=server -f - >"$workdir/ffmpeg-invalid.log" 2>&1; then
  printf 'ffmpeg step without configuration was accepted\n' >&2
  exit 1
fi
if printf '%s\n' 'apiVersion: kubereel.io/v1alpha1' 'kind: VideoWorkflow' 'metadata: {name: bad-ffmpeg-provider, namespace: kubereel-test}' 'spec: {steps: [{name: render, type: ffmpeg, providerRef: mock, ffmpeg: {operation: normalize, inputs: [inputs/source.mp4]}}]}' | kubectl --kubeconfig "$kubeconfig" apply --dry-run=server -f - >>"$workdir/ffmpeg-invalid.log" 2>&1; then
  printf 'ffmpeg step with providerRef was accepted\n' >&2
  exit 1
fi
kubectl --kubeconfig "$kubeconfig" -n kubereel-test exec deployment/kubereel-kubereel -- sh -eu -c '
  mkdir -p /data/inputs
  ffmpeg -nostdin -y -v error -f lavfi -i color=c=teal:s=640x480:r=10:d=1.2 -an -c:v mpeg4 /data/inputs/source.mp4
'
cat >"$workdir/ffmpeg-run.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: ffmpeg-chain-run
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: trim
        type: ffmpeg
        timeoutSeconds: 60
        ffmpeg:
          operation: trim
          inputs: [inputs/source.mp4]
          startSeconds: 0.1
          durationSeconds: 0.6
      - name: resize
        type: ffmpeg
        dependsOn: [trim]
        timeoutSeconds: 60
        ffmpeg:
          operation: resize
          width: 320
          height: 180
    outputs:
      - name: final
        from: resize
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/ffmpeg-run.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Succeeded videorun/ffmpeg-chain-run --timeout=120s
resize_job=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun ffmpeg-chain-run -o jsonpath='{.status.steps[1].jobName}')
resize_id=$(printf '%s' "$resize_job" | sed 's/^kubereel-//;s/-render$//')
ffmpeg_artifact="workflows/$resize_id/output.mp4"
if ! kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun ffmpeg-chain-run -o jsonpath='{.status.artifacts[*]}' | grep -Fq "$ffmpeg_artifact"; then
  printf 'missing rendered artifact in VideoRun status: %s\n' "$ffmpeg_artifact" >&2
  exit 1
fi
ffmpeg_probe=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test exec deployment/kubereel-kubereel -- ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 "/data/$ffmpeg_artifact")
if [ "$ffmpeg_probe" != "h264,320,180" ]; then
  printf 'unexpected FFmpeg output stream: %s\n' "$ffmpeg_probe" >&2
  exit 1
fi
cat >"$workdir/mock-e2e-run.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: mock-e2e-video-run
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: script
        type: llm
        providerRef: mock
        timeoutSeconds: 60
      - name: image-1
        type: image
        providerRef: mock
        dependsOn: [script]
        timeoutSeconds: 60
      - name: image-2
        type: image
        providerRef: mock
        dependsOn: [script]
        timeoutSeconds: 60
      - name: video-1
        type: video
        providerRef: mock
        dependsOn: [image-1]
        timeoutSeconds: 60
      - name: video-2
        type: video
        providerRef: mock
        dependsOn: [image-2]
        timeoutSeconds: 60
      - name: render
        type: ffmpeg
        dependsOn: [video-1, video-2]
        timeoutSeconds: 60
        ffmpeg:
          operation: render_mp4
          aspectRatio: "9:16"
          fps: 24
    outputs:
      - name: final-video
        from: render
  inputs:
    topic: {stringValue: "Unknown creatures in the forest"}
    style: {stringValue: "Found footage horror"}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/mock-e2e-run.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Succeeded videorun/mock-e2e-video-run --timeout=180s
e2e_render_job=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun mock-e2e-video-run -o jsonpath='{.status.steps[5].jobName}')
e2e_render_id=$(printf '%s' "$e2e_render_job" | sed 's/^kubereel-//;s/-render$//')
e2e_mp4_probe=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test exec deployment/kubereel-kubereel -- ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 "/data/workflows/$e2e_render_id/output.mp4")
if [ "$e2e_mp4_probe" != "h264,720,1280" ]; then
  printf 'unexpected Mock E2E MP4 stream: %s\n' "$e2e_mp4_probe" >&2
  exit 1
fi
cat >"$workdir/cancel-run.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata:
  name: cancel-run
  namespace: kubereel-test
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: image
        type: image
        providerRef: mock
        resources:
          requests: {cpu: "1000000", memory: 128Mi}
          limits: {cpu: "1000000", memory: 512Mi}
  inputs:
    prompt: {stringValue: "Cancel this Job"}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/cancel-run.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Running videorun/cancel-run --timeout=60s
cancel_job=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun cancel-run -o jsonpath='{.status.steps[0].jobName}')
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch videorun cancel-run --type=merge -p '{"spec":{"cancelRequested":true}}'
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Cancelled videorun/cancel-run --timeout=120s
if [ -n "$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get job "$cancel_job" --ignore-not-found -o name)" ]; then
  printf 'cancelled Job still exists\n' >&2
  exit 1
fi
cat >"$workdir/recovery-runs.yaml" <<'YAML'
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata: {name: recover-existing, namespace: kubereel-test}
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: image
        type: image
        providerRef: mock
        resources:
          requests: {cpu: "1000000", memory: 128Mi}
          limits: {cpu: "1000000", memory: 512Mi}
  inputs:
    prompt: {stringValue: "Recover existing Job"}
---
apiVersion: kubereel.io/v1alpha1
kind: VideoRun
metadata: {name: recover-missing, namespace: kubereel-test}
spec:
  workflowRef: sample
  workflowSnapshot:
    steps:
      - name: image
        type: image
        providerRef: mock
        resources:
          requests: {cpu: "1000000", memory: 128Mi}
          limits: {cpu: "1000000", memory: 512Mi}
  inputs:
    prompt: {stringValue: "Recover missing Job"}
YAML
kubectl --kubeconfig "$kubeconfig" apply -f "$workdir/recovery-runs.yaml"
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Running videorun/recover-existing videorun/recover-missing --timeout=60s
existing_uid=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun recover-existing -o jsonpath='{.metadata.uid}')
missing_uid=$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get videorun recover-missing -o jsonpath='{.metadata.uid}')
python3 - "$kubeconfig" "$existing_uid" "$missing_uid" "$workdir/recovery-jobs" <<'PY'
import selectors, subprocess, sys

names = []
for uid in sys.argv[2:4]:
    command = ["kubectl", "--kubeconfig", sys.argv[1], "-n", "kubereel-test", "get", "jobs", "-l", "kubereel.io/videorun=" + uid, "--watch", "-o", "name"]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    try:
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        if not selector.select(timeout=60):
            raise RuntimeError("timed out waiting for recovery Job")
        name = process.stdout.readline().strip()
        if not name:
            raise RuntimeError("Job watch ended without a Job")
        names.append(name)
    finally:
        process.terminate()
        process.wait(timeout=5)
with open(sys.argv[4], "w", encoding="utf-8") as output:
    output.write("\n".join(names))
PY
existing_job=$(sed -n '1p' "$workdir/recovery-jobs")
missing_job=$(sed -n '2p' "$workdir/recovery-jobs")
kubectl --kubeconfig "$kubeconfig" -n kubereel-test scale deployment/kubereel-kubereel --replicas=0
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=delete pod -l kubereel.io/control-plane=kubereel --timeout=90s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test patch "$existing_job" --subresource=status --type=merge -p '{"status":{"active":0,"failed":1,"conditions":[{"type":"FailureTarget","status":"True","reason":"SimulatedFailure"},{"type":"Failed","status":"True","reason":"SimulatedFailure"}]}}'
kubectl --kubeconfig "$kubeconfig" -n kubereel-test delete "$missing_job" --cascade=foreground --wait=true --timeout=90s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test set env deployment/kubereel-kubereel JOB_TTL_SECONDS=7200
kubectl --kubeconfig "$kubeconfig" -n kubereel-test scale deployment/kubereel-kubereel --replicas=1
kubectl --kubeconfig "$kubeconfig" -n kubereel-test rollout status deployment/kubereel-kubereel --timeout=120s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=Failed videorun/recover-existing --timeout=60s
kubectl --kubeconfig "$kubeconfig" -n kubereel-test wait --for=jsonpath='{.status.phase}'=RequiresReview videorun/recover-missing --timeout=60s
if [ -n "$(kubectl --kubeconfig "$kubeconfig" -n kubereel-test get jobs -l "kubereel.io/videorun=$missing_uid" -o name)" ]; then
  printf 'missing Job was unexpectedly resubmitted\n' >&2
  exit 1
fi
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
import http.cookiejar, json, re, sys, time, urllib.error, urllib.parse, urllib.request

base = f'http://127.0.0.1:{sys.argv[1]}'

# 1. Management API must reject unauthenticated access prior to setup.
try:
    urllib.request.urlopen(base + '/api/providers', timeout=5)
except urllib.error.HTTPError as error:
    if error.code != 401:
        raise
else:
    raise RuntimeError('unauthenticated /api/providers succeeded before setup')

# 2. Complete one-time initial administrator setup via REST API; verify duplicate setup returns 409.
setup_req = urllib.request.Request(
    base + '/api/v1/auth/setup',
    data=b'{"username":"admin","password":"kubereel-e2e-password-123"}',
    headers={'Content-Type': 'application/json'},
    method='POST',
)
with urllib.request.urlopen(setup_req, timeout=5) as response:
    setup_payload = json.load(response)
api_token = setup_payload['token']
auth_headers = {'Authorization': f'Bearer {api_token}'}

dup_setup = urllib.request.Request(
    base + '/api/v1/auth/setup',
    data=b'{"username":"admin2","password":"kubereel-e2e-password-123"}',
    headers={'Content-Type': 'application/json'},
    method='POST',
)
try:
    urllib.request.urlopen(dup_setup, timeout=5)
except urllib.error.HTTPError as error:
    if error.code != 409:
        raise
else:
    raise RuntimeError('duplicate setup succeeded')

# Verify versioned provider management against real Kubernetes RBAC and CRD validation.
def provider_request(method, path='', payload=None, expected=200):
    data = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        base + '/api/v1/providers' + path, data=data, method=method,
        headers={**auth_headers, 'Content-Type': 'application/json'},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        assert response.status == expected
        body = response.read()
    assert b'e2e-fake-provider-key' not in body and b'secretRef' not in body
    return json.loads(body) if body else None

provider_request('POST', payload={'id': 'managed-mock', 'type': 'mock', 'capabilities': ['image']}, expected=201)
assert provider_request('POST', '/managed-mock/test')['check'] == 'local_configuration'
assert provider_request('PATCH', '/managed-mock', {'enabled': False})['enabled'] is False
assert provider_request('GET', '/managed-mock')['type'] == 'mock'
assert len(provider_request('GET', '?limit=1')['items']) == 1
provider_request('DELETE', '/managed-mock', expected=204)
provider_request('POST', payload={
    'id': 'managed-gemini', 'type': 'gemini', 'capabilities': ['text'],
    'apiKey': 'e2e-fake-provider-key',
}, expected=201)
assert provider_request('PATCH', '/managed-gemini', {'apiKey': 'e2e-fake-provider-key-rotated'})['credentialsReferenced']
provider_request('DELETE', '/managed-gemini', expected=204)

# Exercise workflow definition CRUD through real CRD validation and RBAC.
def workflow_request(method, path='', payload=None, expected=200, headers=auth_headers):
    request = urllib.request.Request(
        base + '/api/v1/workflows' + path,
        data=None if payload is None else json.dumps(payload).encode(),
        method=method, headers={**headers, 'Content-Type': 'application/json'},
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            assert response.status == expected
            body = response.read()
        return json.loads(body) if body else None
    except urllib.error.HTTPError as error:
        if error.code != expected:
            raise RuntimeError(f'workflow API {method} {path}: {error.code} {error.read()}') from error

workflow_request('GET', expected=401, headers={})
good_step = {'name': 'image', 'type': 'image', 'providerRef': 'mock', 'retry': {'maxAttempts': 2}}
workflow_request('POST', payload={
    'id': 'invalid-definition', 'spec': {'steps': [{**good_step, 'unexpectedField': True}]},
}, expected=400)
workflow_request('POST', payload={
    'id': 'invalid-definition', 'spec': {'steps': [{**good_step, 'dependsOn': ['missing']}]},
}, expected=400)
created = workflow_request('POST', payload={
    'id': 'managed-definition', 'spec': {'steps': [good_step], 'outputs': [{'name': 'image', 'from': 'image'}]},
}, expected=201)
version = created['resourceVersion']
assert version and created['spec']['steps'] == [good_step]
workflow_request('POST', payload={'id': 'managed-definition', 'spec': {'steps': [good_step]}}, expected=409)
page = workflow_request('GET', '?limit=1')
assert len(page['items']) == 1 and page['continue']
next_page = workflow_request('GET', '?limit=1&' + urllib.parse.urlencode({'continue': page['continue']}))
assert len(next_page['items']) == 1 and next_page['items'][0]['id'] != page['items'][0]['id']
workflow_request('PATCH', '/managed-definition', {'spec': {'outputs': [{'name': 'bad', 'from': 'missing'}]}}, expected=400)
updated = workflow_request('PATCH', '/managed-definition', {'resourceVersion': version, 'spec': {'description': 'updated'}})
assert updated['spec']['steps'] == [good_step] and updated['spec']['description'] == 'updated'
workflow_request('PATCH', '/managed-definition', {'resourceVersion': version, 'spec': {'description': 'stale'}}, expected=409)
assert workflow_request('GET', '/managed-definition')['spec']['description'] == 'updated'
# Submit the persisted definition, inspect results and logs, then retry a failed run.
def run_request(method, path, payload=None, expected=200):
    request = urllib.request.Request(
        base + '/api/v1' + path,
        data=None if payload is None else json.dumps(payload).encode(),
        method=method, headers={**auth_headers, 'Content-Type': 'application/json'},
    )
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            assert response.status == expected
            return json.load(response)
    except urllib.error.HTTPError as error:
        if error.code != expected:
            raise RuntimeError(f'execution API {method} {path}: {error.code} {error.read()}') from error

def wait_run(path, phase):
    deadline = time.monotonic() + 75
    while time.monotonic() < deadline:
        run = run_request('GET', path)
        if run['phase'] == phase:
            return run
        if run['phase'] in ['Succeeded', 'Failed', 'RequiresReview', 'Cancelled']:
            raise RuntimeError(f'unexpected execution state: {run}')
        time.sleep(1)
    raise RuntimeError(f'run did not reach {phase}: {path}')

launch = '/workflows/managed-definition/runs'
run = run_request('POST', launch, {'inputs': {'prompt': {'stringValue': 'Mock via run API'}}}, 202)
run_path = '/runs/' + run['id']
assert run['workflowId'] == 'managed-definition'
assert run_request('GET', '/runs?limit=1')['items']
completed = wait_run(run_path, 'Succeeded')
assert completed['steps'][0]['name'] == 'image'
assert len(run_request('GET', run_path + '/artifacts')['items']) == 1
artifact_page = run_request('GET', '/artifacts?' + urllib.parse.urlencode({'runId': run['id'], 'limit': 1}))
artifact = artifact_page['items'][0]
assert artifact['runId'] == run['id'] and artifact['name'] == 'scene.svg' and artifact['sizeBytes'] > 0
artifact_path = '/artifacts/' + artifact['id']
assert run_request('GET', artifact_path) == artifact
download_url = base + '/api/v1' + artifact_path + '/download'
try:
    urllib.request.urlopen(download_url, timeout=10)
except urllib.error.HTTPError as error:
    assert error.code == 401
else:
    raise RuntimeError('unauthenticated artifact download succeeded')
ranged = urllib.request.Request(download_url, headers={**auth_headers, 'Range': 'bytes=0-3'})
with urllib.request.urlopen(ranged, timeout=10) as response:
    assert response.status == 206 and response.read() == b'<svg'
    assert response.headers['Content-Disposition'].startswith('attachment')
deleted = urllib.request.Request(base + '/api/v1' + artifact_path, method='DELETE', headers=auth_headers)
with urllib.request.urlopen(deleted, timeout=10) as response:
    assert response.status == 204
run_request('GET', artifact_path, expected=404)
assert run_request('GET', '/artifacts?' + urllib.parse.urlencode({'runId': run['id']}))['items'] == []

assert run_request('GET', run_path + '/logs?step=image&tailLines=10')['step'] == 'image'
run_request('POST', run_path + '/cancel', expected=409)
run_request('POST', run_path + '/retry', {}, 409)
failed = run_request('POST', launch, {}, 202)
failed_path = '/runs/' + failed['id']
wait_run(failed_path, 'Failed')
retry = run_request('POST', failed_path + '/retry', {}, 202)
assert retry['id'] != failed['id']
assert run_request('POST', failed_path + '/retry', {}, 202)['id'] == retry['id']

# An input node keeps this run pending until the cancellation request is reconciled.
workflow_request('POST', payload={'id': 'cancellable-definition', 'spec': {
    'steps': [{'name': 'input', 'type': 'input'}],
}}, expected=201)
cancellable = run_request('POST', '/workflows/cancellable-definition/runs', {}, 202)
cancel_path = '/runs/' + cancellable['id']
run_request('POST', cancel_path + '/cancel', expected=202)
wait_run(cancel_path, 'Cancelled')
workflow_request('DELETE', '/cancellable-definition', expected=204)

workflow_request('DELETE', '/managed-definition', expected=204)
workflow_request('GET', '/managed-definition', expected=404)
assert run_request('GET', run_path)['phase'] == 'Succeeded'

# 3. Verify authenticated API workflow execution and protected MP4 artifact download.
providers_req = urllib.request.Request(base + '/api/providers', headers=auth_headers)
with urllib.request.urlopen(providers_req, timeout=5) as response:
    providers = json.load(response)
if not any(p['id'] == 'mock' and p['available'] for p in providers):
    raise RuntimeError('Mock provider is unavailable')
if len(providers) != 1:
    raise RuntimeError('unexpected provider in registry')

blocked = urllib.request.Request(
    base + '/api/workflows',
    data=b'{"prompt":"test","provider":"unknown"}',
    headers={**auth_headers, 'Content-Type': 'application/json'},
    method='POST',
)
try:
    urllib.request.urlopen(blocked, timeout=5)
except urllib.error.HTTPError as error:
    if error.code != 400:
        raise
else:
    raise RuntimeError('unavailable provider was accepted')

request = urllib.request.Request(
    base + '/api/workflows',
    data=b'{"prompt":"KubeReel mock workflow","provider":"mock"}',
    headers={**auth_headers, 'Content-Type': 'application/json'},
    method='POST',
)
with urllib.request.urlopen(request, timeout=5) as response:
    workflow = json.load(response)
workflow_id = workflow['id']
with open(sys.argv[2], 'w', encoding='utf-8') as output:
    output.write(workflow_id)
deadline = time.monotonic() + 120
while time.monotonic() < deadline:
    status_req = urllib.request.Request(base + '/api/workflows/' + workflow_id, headers=auth_headers)
    with urllib.request.urlopen(status_req, timeout=5) as response:
        workflow = json.load(response)
    if workflow['status'] == 'failed':
        raise RuntimeError(workflow.get('error', 'workflow failed'))
    if workflow['status'] == 'succeeded':
        try:
            urllib.request.urlopen(base + '/api/workflows/' + workflow_id + '/artifact', timeout=5)
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise
        else:
            raise RuntimeError('unauthenticated artifact download succeeded')

        dl_req = urllib.request.Request(base + '/api/workflows/' + workflow_id + '/artifact', headers=auth_headers)
        with urllib.request.urlopen(dl_req, timeout=5) as response:
            content_type = response.headers.get('Content-Type', '')
            artifact = response.read()
        if content_type != 'video/mp4' or len(artifact) < 24 or artifact[4:8] != b'ftyp':
            raise RuntimeError(f'unexpected MP4 artifact: type={content_type}, len={len(artifact)}')
        print('Mock workflow and protected MP4 artifact verified')
        break
    time.sleep(2)
else:
    raise RuntimeError('workflow timed out')

# 4. Sign into the Web UI with session cookie + CSRF token and run a UI workflow.
jar = http.cookiejar.CookieJar()
browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

login_data = urllib.parse.urlencode({
    'username': 'admin',
    'password': 'kubereel-e2e-password-123',
    'next': '/workflows',
}).encode('utf-8')
login_req = urllib.request.Request(
    base + '/login',
    data=login_data,
    headers={'Content-Type': 'application/x-www-form-urlencoded'},
    method='POST',
)
with browser.open(login_req, timeout=5) as response:
    form_html = response.read().decode('utf-8')
    if 'Run mock workflow' not in form_html:
        raise RuntimeError('mock workflow form missing after login')

csrf_match = re.search(r'name="csrf_token"\s+value="([^"]+)"', form_html)
if not csrf_match:
    raise RuntimeError('missing csrf_token in workflow form')
csrf_token = csrf_match.group(1)

form_body = urllib.parse.urlencode({
    'prompt': 'UI mock workflow',
    'csrf_token': csrf_token,
}).encode('utf-8')
form = urllib.request.Request(
    base + '/workflows',
    data=form_body,
    headers={'Content-Type': 'application/x-www-form-urlencoded'},
    method='POST',
)
with browser.open(form, timeout=5) as response:
    ui_id = response.geturl().rsplit('/', 1)[-1]
    if b'Workflow status' not in response.read():
        raise RuntimeError('mock workflow status page missing')
deadline = time.monotonic() + 120
while time.monotonic() < deadline:
    ui_status_req = urllib.request.Request(base + '/api/workflows/' + ui_id, headers=auth_headers)
    with urllib.request.urlopen(ui_status_req, timeout=5) as response:
        workflow = json.load(response)
    if workflow['status'] == 'failed':
        raise RuntimeError(workflow.get('error', 'UI workflow failed'))
    if workflow['status'] == 'succeeded':
        with browser.open(base + '/workflows/' + ui_id, timeout=5) as response:
            body = response.read()
            if b'Download mock MP4' not in body or b'<video' not in body:
                raise RuntimeError('completed UI workflow has no MP4 player or download link')
        print('Authenticated Mock workflow UI verified')
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
