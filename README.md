# KubeReel

KubeReel is a self-hosted filmmaking workspace with a single-image Go control plane. Its Kubernetes mode runs mock workflows as worker and render Jobs. The mock provider produces an SVG placeholder, not AI-generated video or images.

Run locally:

```sh
KUBEREEL_DATA_DIR=./data go run . serve
```

The data directory defaults to `/data` and the database is named `kubereel.db`. Health endpoints are available at `http://localhost:8080/healthz` and `/readyz`.

Existing local data and volumes are not moved or deleted automatically; back them up before changing data locations.

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

Open `/workflows` in the web UI to run a mock workflow, or use the REST API and follow the `Location` response header to check its status:

```sh
curl -i -X POST http://localhost:8080/api/workflows \
  -H 'Content-Type: application/json' -d '{"prompt":"A quiet lake"}'
curl http://localhost:8080/api/workflows/WORKFLOW_ID
curl -o mock-scene.svg http://localhost:8080/api/workflows/WORKFLOW_ID/artifact
```

`GET /api/providers` lists execution availability. Workflow requests default to `mock` and may explicitly specify `"provider":"mock"`.

By default, the PVC uses `ReadWriteOnce` and Jobs are placed on the same node as the control plane. For multi-node execution, provide `ReadWriteMany` storage and set `persistence.accessMode=ReadWriteMany`. To use an existing claim, set `persistence.existingClaim` and set `persistence.accessMode` to match the claim.

Standalone `serve` currently exposes health endpoints; workflow execution and its UI require Kubernetes mode. Deployment requirements are documented in [SPEC.md](SPEC.md).

`docker compose up --build` also starts the standalone service. It uses the `kubereel-data` volume.

To verify the Mock workflow on an isolated temporary Kubernetes cluster, install `kind`, Docker, Helm, kubectl and Python 3, then run `tests/e2e-kind.sh`. The script builds an image, creates a temporary kind cluster, runs the workflow and verifies Job cleanup, then removes its test resources.
