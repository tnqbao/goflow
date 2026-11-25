# Helm Installation, Upgrade, Backup, CRD Migration, and Uninstall

`charts/kubereel` deploys KubeReel as a single permanent control-plane container (`kubereel serve`) with namespace-scoped RBAC, three CustomResourceDefinitions (`VideoWorkflow`, `VideoRun`, `VideoProvider`), and PVC-backed storage (`/data`). Temporary `worker` and `render` Kubernetes Jobs reuse the same pinned container image on demand.

## 1. Installation

### Zero-Config Install (Single-Node / RWO Storage)

On any Kubernetes cluster (`v1.29+` recommended for CEL CRD validation rules) with a default `StorageClass`:

```sh
helm install kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
  --version 0.1.0 \
  --namespace kubereel \
  --create-namespace \
  --wait
```

Access the dashboard over a trusted local port-forward and complete the one-time initial administrator setup at `/setup`:

```sh
kubectl -n kubereel port-forward service/kubereel-kubereel 8080:8080
```

### Headless Bootstrap & Multi-Node Shared Storage

For automated/GitOps installations or multi-node Job scheduling with `ReadWriteMany` storage:

```sh
kubectl create namespace kubereel
kubectl -n kubereel create secret generic kubereel-admin \
  --from-literal=password='replace-with-12-to-72-char-password'

helm install kubereel ./charts/kubereel \
  --namespace kubereel \
  --set auth.existingSecret=kubereel-admin \
  --set auth.username=admin \
  --set storage.existingClaim=shared-rwx-pvc \
  --set storage.accessMode=ReadWriteMany \
  --set networkPolicy.enabled=true \
  --wait
```

### Optional Resources

- **Ingress (`ingress.enabled=true`):** Requires `ingress.host` (and `ingress.tls` for HTTPS termination). Complete initial administrator setup before exposing an Ingress publicly.
- **NetworkPolicy (`networkPolicy.enabled=true`):** Creates two policies:
  - `<release>-kubereel`: restricts inbound traffic to the control-plane HTTP port (`8080`).
  - `<release>-kubereel-jobs`: blocks all inbound traffic (`ingress: []`) to temporary `worker` and `render` Job Pods and restricts egress to DNS (`53`) and HTTPS (`443`).
- **ServiceMonitor (`serviceMonitor.enabled=true`):** Renders a `monitoring.coreos.com/v1` `ServiceMonitor` scraping `/metrics` using a Kubernetes `Secret` containing a KubeReel Bearer API token (`serviceMonitor.bearerTokenSecret.name` and `key`).

---

## 2. CRD Migration & Helm Upgrade Behavior

### Why CRDs Require Explicit Application on Upgrade

Following Helm v3 safety conventions, manifests in `charts/kubereel/crds/` are installed automatically during `helm install`, **never modified** during `helm upgrade`, and **never deleted** during `helm uninstall` (to prevent accidental cluster-wide deletion of `VideoWorkflow`, `VideoRun`, and `VideoProvider` objects).

### Safe Upgrade Procedure

1. **Apply updated CRDs first** using server-side apply:

   ```sh
   kubectl apply --server-side -f charts/kubereel/crds/
   kubectl wait --for=condition=Established \
     crd/videoworkflows.kubereel.io \
     crd/videoruns.kubereel.io \
     crd/videoproviders.kubereel.io \
     --timeout=60s
   ```

2. **Run `helm upgrade`:**

   ```sh
   helm upgrade kubereel oci://registry-1.docker.io/iamqbao/kubereel-chart \
     --version 0.1.0 \
     --namespace kubereel \
     --reuse-values \
     --wait
   ```

### Runtime Behavior During Upgrade & Rollback

- **Deployment strategy (`Recreate`):** The control plane terminates the old Pod before starting the new Pod so `ReadWriteOnce` volumes detach cleanly and SQLite (`/data/kubereel.db`) never experiences concurrent writers.
- **State & Job continuity:** Administrator accounts, API tokens, SQLite migrations, `/data/workflows/*` media artifacts, and `VideoWorkflow`/`VideoRun`/`VideoProvider` custom resources persist across upgrades. Running worker/render Jobs continue undisturbed in the namespace; when the new Pod starts, the operator reconciles active Jobs deterministically without duplicating workloads.
- **Rollback:** `helm rollback kubereel <REVISION> -n kubereel --wait` restores the previous Deployment specification. Note that forward SQLite schema migrations in `/data/kubereel.db` are additive; take a backup before major version upgrades.

---

## 3. Backup and Restore

KubeReel state consists of two parts:
1. **Kubernetes resources** in the release namespace: `VideoWorkflow`, `VideoProvider`, `VideoRun` (optional history), and provider credential `Secret`s (`kubereel-provider-*` plus any custom referenced `Secret`s).
2. **PVC data (`/data`)**: `/data/kubereel.db` (administrator account, hashed session/API tokens, legacy Mock demo records) and `/data/workflows/` (generated scripts, images, clips, and final MP4 videos).

### Backup Procedure

```sh
NAMESPACE=kubereel
BACKUP_DIR=./kubereel-backup-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$BACKUP_DIR"

# 1. Export reusable workflow and provider definitions
kubectl -n "$NAMESPACE" get videoworkflows,videoproviders -o yaml > "$BACKUP_DIR/custom-resources.yaml"

# 2. Export provider credential Secrets (handle backup files securely!)
kubectl -n "$NAMESPACE" get secrets -o yaml > "$BACKUP_DIR/secrets.yaml"

# 3. Create a consistent archive of /data (SQLite DB + media artifacts)
kubectl -n "$NAMESPACE" exec deployment/kubereel-kubereel -- \
  tar -C /data -czf - . > "$BACKUP_DIR/data.tar.gz"
chmod 600 "$BACKUP_DIR"/*
```

> For zero-write consistency on busy clusters, either use volume snapshots (`VolumeSnapshot`) or briefly scale `deployment/kubereel-kubereel` to `0` replicas while archiving `/data` from a temporary helper Pod mounting the PVC.

### Restore Procedure

1. Install the Helm chart into the target namespace (using `--set storage.size=...` large enough for the archive).
2. Scale the control plane to `0` while restoring `/data` so SQLite files are not open:

   ```sh
   kubectl -n "$NAMESPACE" scale deployment/kubereel-kubereel --replicas=0
   kubectl -n "$NAMESPACE" wait --for=delete pod -l app.kubernetes.io/name=kubereel --timeout=90s
   ```

3. Restore `/data` into the PVC using a temporary non-root Pod (`uid=10001`), re-apply provider `Secret`s and `VideoWorkflow`/`VideoProvider` manifests (stripping cluster-specific `resourceVersion`/`uid` metadata if restoring to a new cluster), and scale `deployment/kubereel-kubereel` back to `1`:

   ```sh
   kubectl -n "$NAMESPACE" scale deployment/kubereel-kubereel --replicas=1
   kubectl -n "$NAMESPACE" rollout status deployment/kubereel-kubereel --timeout=120s
   ```

---

## 4. Uninstall Behavior

### Standard Uninstall

Retained `worker` and `render` Job Pods (`execution.jobTTLSeconds`, default `86400`) mount the release PVC. Kubernetes `kubernetes.io/pvc-protection` keeps the PVC in `Terminating` until all Pods mounting it are removed. To release the PVC immediately during `helm uninstall --wait`, delete active/retained runs and workload Jobs first:

```sh
kubectl -n kubereel delete videoruns --all
kubectl -n kubereel delete jobs -l app.kubernetes.io/name=kubereel
helm uninstall kubereel --namespace kubereel --wait
```

If `helm uninstall kubereel -n kubereel` is run without deleting retained Jobs first, Helm removes the control-plane resources immediately and Kubernetes finalizes PVC deletion automatically once `jobTTLSeconds` expires (or when those Jobs are deleted).

- **Removed by `helm uninstall`:** `Deployment`, `Service`, `ServiceAccount`, `Role`, `RoleBinding`, optional `Ingress` / `NetworkPolicy` / `ServiceMonitor`, and the chart-created `PersistentVolumeClaim` (when `storage.existingClaim` is empty).
- **Preserved by `helm uninstall`:**
  - External PVCs referenced via `storage.existingClaim`.
  - Cluster-scoped CRDs (`videoworkflows.kubereel.io`, `videoruns.kubereel.io`, `videoproviders.kubereel.io`) and existing `VideoWorkflow` / `VideoProvider` resources.
  - Provider credential `Secret`s.

### Full Cluster Purge (Destructive)

To permanently remove all KubeReel custom resources, secrets, CRDs, and the namespace:

```sh
kubectl -n kubereel delete videoruns,videoworkflows,videoproviders --all
kubectl delete namespace kubereel
kubectl delete -f charts/kubereel/crds/
```
