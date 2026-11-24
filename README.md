# KubeReel

KubeReel is a self-hosted filmmaking workspace transitioning from GoFlow to a single-image Go control plane. The current foundation serves an embedded web UI and health endpoints. The Kubernetes operator, workflow engine, scheduler, worker, render commands, and mock-provider workflow are not implemented yet.

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

The current Helm chart is in `charts/goflow`; it is not yet the complete KubeReel installation. The desired deployment architecture and current task are documented in [SPEC.md](SPEC.md).
