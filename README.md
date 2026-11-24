# KubeReel

KubeReel is a self-hosted filmmaking workspace transitioning from GoFlow to a single-image Go control plane. Its Kubernetes mode runs mock workflows as worker and render Jobs. The mock provider produces an SVG placeholder, not AI-generated video or images.

Run locally:

```sh
KUBEREEL_DATA_DIR=./data go run ./cmd/kubereel serve
```

Open `http://localhost:8080`. For compatibility, existing installations may still use `GOFLOW_DATA_DIR`; `KUBEREEL_DATA_DIR` takes precedence. The existing `goflow.db` filename is retained to preserve data.

Build the single application image:

```sh
docker build -t kubereel:local .
docker run --rm -p 127.0.0.1:8080:8080 -v kubereel-data:/data kubereel:local serve
```

The Helm chart uses one permanent control-plane container and creates temporary worker and render Jobs using the same image. The default image is `kubereel:latest`; make that image available to the cluster nodes, or configure a registry image:

```sh
helm install kubereel ./charts/kubereel --set image.repository=YOUR_REGISTRY/kubereel --set image.tag=TAG
kubectl port-forward service/kubereel-kubereel 8080:8080
```

Create a mock workflow and follow the `Location` response header to check its status:

```sh
curl -i -X POST http://localhost:8080/api/workflows \
  -H 'Content-Type: application/json' -d '{"prompt":"A quiet lake"}'
curl http://localhost:8080/api/workflows/WORKFLOW_ID
curl -o mock-scene.svg http://localhost:8080/api/workflows/WORKFLOW_ID/artifact
```

By default, the PVC uses `ReadWriteOnce` and Jobs are placed on the same node as the control plane. For multi-node execution, provide `ReadWriteMany` storage and set `persistence.accessMode=ReadWriteMany`. To use an existing claim, set `persistence.existingClaim` and set `persistence.accessMode` to match the claim. The previous `charts/goflow` release used different resource names: back up and preserve its PVC before migrating; do not uninstall it until data has been migrated or safely retained.

Standalone `serve` currently starts the web UI without workflow execution. The desired deployment architecture and current task are documented in [SPEC.md](SPEC.md).
