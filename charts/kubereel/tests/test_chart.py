#!/usr/bin/env python3
"""Render the chart and verify its deployment invariants. Requires Helm and PyYAML."""
import pathlib
import subprocess
import tarfile
import tempfile
import unittest

import yaml

CHART = pathlib.Path(__file__).resolve().parents[1]


def render_list(*values):
    result = subprocess.run(
        ["helm", "template", "test", str(CHART), *values],
        text=True,
        capture_output=True,
        check=True,
    )
    return [item for item in yaml.safe_load_all(result.stdout) if item]


def render(*values):
    return {
        item["metadata"]["name"] if item["kind"] == "CustomResourceDefinition" else item["kind"]: item
        for item in render_list(*values)
    }


class ChartTest(unittest.TestCase):
    def test_artifact_limits_are_rendered_as_decimal_integers(self):
        container = render()["Deployment"]["spec"]["template"]["spec"]["containers"][0]
        env = {item["name"]: item.get("value") for item in container["env"]}
        self.assertEqual(env["ARTIFACT_MAX_BYTES"], "1073741824")
        self.assertEqual(env["ARTIFACT_RETENTION_SECONDS"], "0")

    def test_video_workflow_crd_is_included(self):
        docs = render("--include-crds")
        crd = docs["videoworkflows.kubereel.io"]
        self.assertEqual(crd["metadata"]["name"], "videoworkflows.kubereel.io")
        self.assertEqual(crd["spec"]["scope"], "Namespaced")
        version = crd["spec"]["versions"][0]
        self.assertEqual(version["name"], "v1alpha1")
        self.assertTrue(version["served"] and version["storage"])
        spec = version["schema"]["openAPIV3Schema"]["properties"]["spec"]
        self.assertEqual(set(spec["properties"]), {"description", "inputs", "steps", "outputs"})
        step_schema = spec["properties"]["steps"]["items"]
        self.assertIn("ffmpeg", step_schema["properties"])
        self.assertEqual(
            set(step_schema["properties"]["ffmpeg"]["properties"]["operation"]["enum"]),
            {"merge", "trim", "resize", "aspect_ratio", "add_audio", "normalize", "render_mp4"},
        )

    def test_video_run_crd(self):
        docs = render("--include-crds")
        run = docs["videoruns.kubereel.io"]
        self.assertEqual(run["spec"]["scope"], "Namespaced")
        version = run["spec"]["versions"][0]
        self.assertEqual(version["name"], "v1alpha1")
        self.assertEqual(version["subresources"], {"status": {}})
        schema = version["schema"]["openAPIV3Schema"]["properties"]
        job_config = schema["status"]["properties"]["steps"]["items"]["properties"]["jobConfig"]
        self.assertEqual(set(job_config["required"]), {"image", "instance", "sharedStorage", "pullPolicy", "ttlSeconds"})
        self.assertTrue({"storageType", "s3Bucket", "s3Region", "workerServiceAccount"}.issubset(job_config["properties"]))
        self.assertEqual(set(schema["spec"]["required"]), {"workflowRef", "workflowSnapshot"})
        self.assertEqual(schema["spec"]["x-kubernetes-validations"][0]["message"], "run inputs are immutable")
        snapshot = dict(schema["spec"]["properties"]["workflowSnapshot"])
        self.assertEqual(snapshot.pop("x-kubernetes-validations")[0]["rule"], "self == oldSelf")
        workflow = docs["videoworkflows.kubereel.io"]["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]["spec"]
        self.assertEqual(snapshot, workflow)

    def test_video_provider_crd(self):
        crd = render("--include-crds")["videoproviders.kubereel.io"]
        self.assertEqual(crd["spec"]["scope"], "Namespaced")
        version = crd["spec"]["versions"][0]
        self.assertEqual(version["name"], "v1alpha1")
        self.assertTrue(version["served"] and version["storage"])
        spec = version["schema"]["openAPIV3Schema"]["properties"]["spec"]
        self.assertEqual(set(spec["properties"]), {"type", "enabled", "capabilities", "models", "baseURL", "secretRef", "maxConcurrent", "timeoutSeconds"})
        self.assertEqual(set(spec["properties"]["secretRef"]["required"]), {"name", "key"})

    def test_defaults(self):
        docs = render()
        self.assertEqual(set(docs), {"Deployment", "Service", "PersistentVolumeClaim", "ServiceAccount", "Role", "RoleBinding"})
        deployment = docs["Deployment"]["spec"]
        self.assertEqual(deployment["replicas"], 1)
        self.assertEqual(deployment["strategy"]["type"], "Recreate")
        pod = deployment["template"]["spec"]
        self.assertEqual(len(pod["containers"]), 1)
        container = pod["containers"][0]
        env = {item["name"]: item.get("value") for item in container["env"]}
        self.assertEqual(env["EXECUTION_MODE"], "kubernetes")
        self.assertEqual(env["MAX_CONCURRENT_JOBS"], "5")
        self.assertEqual(env["JOB_TTL_SECONDS"], "86400")
        self.assertEqual(env["AUTH_SESSION_TTL_SECONDS"], "86400")
        self.assertNotIn("AUTH_BOOTSTRAP_PASSWORD", env)
        self.assertEqual(container["image"], "iamqbao/kubereel:0.1.3")
        self.assertEqual(env["JOB_IMAGE"], container["image"])
        self.assertEqual(env["SHARED_STORAGE"], "false")
        self.assertEqual(env["PVC_NAME"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(env["INSTANCE_NAME"], "test")
        self.assertEqual(docs["Role"]["rules"][0]["verbs"], ["get", "list", "watch", "create", "delete"])
        self.assertIn({"apiGroups": ["kubereel.io"], "resources": ["videoruns", "videoproviders"], "verbs": ["get", "list", "watch"]}, docs["Role"]["rules"])
        self.assertIn({"apiGroups": ["kubereel.io"], "resources": ["videoworkflows"], "verbs": ["get", "list", "create", "update", "delete"]}, docs["Role"]["rules"])
        self.assertIn({"apiGroups": ["kubereel.io"], "resources": ["videoruns"], "verbs": ["create", "update"]}, docs["Role"]["rules"])
        self.assertIn({"apiGroups": [""], "resources": ["pods/log"], "verbs": ["get"]}, docs["Role"]["rules"])
        self.assertIn({"apiGroups": ["kubereel.io"], "resources": ["videoruns/status"], "verbs": ["update"]}, docs["Role"]["rules"])
        self.assertEqual(container["livenessProbe"]["httpGet"]["path"], "/healthz")
        self.assertEqual(container["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(pod["securityContext"]["fsGroup"], 10001)
        self.assertFalse(container["securityContext"]["allowPrivilegeEscalation"])
        self.assertEqual(container["securityContext"]["capabilities"]["drop"], ["ALL"])
        self.assertEqual(container["securityContext"]["seccompProfile"]["type"], "RuntimeDefault")
        self.assertEqual(container["resources"]["limits"]["memory"], "1Gi")
        self.assertEqual(container["volumeMounts"][0]["mountPath"], "/data")
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(docs["Service"]["spec"]["selector"], deployment["selector"]["matchLabels"])
        self.assertNotIn("storageClassName", docs["PersistentVolumeClaim"]["spec"])
        self.assertEqual(docs["PersistentVolumeClaim"]["spec"]["resources"]["requests"]["storage"], "10Gi")

    def test_overrides(self):
        docs = render(
            "--set", "image.repository=example/kubereel",
            "--set", "image.tag=v1",
            "--set", "storage.storageClass=fast",
            "--set", "storage.size=5Gi",
            "--set", "service.port=9090",
            "--set", "resources.requests.memory=256Mi",
            "--set", "ingress.enabled=true",
            "--set", "ingress.host=kubereel.example.com",
            "--set", "ingress.className=nginx",
        )
        container = docs["Deployment"]["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(container["image"], "example/kubereel:v1")
        self.assertEqual(container["resources"]["requests"]["memory"], "256Mi")
        claim = docs["PersistentVolumeClaim"]["spec"]
        self.assertEqual(claim["storageClassName"], "fast")
        self.assertEqual(claim["resources"]["requests"]["storage"], "5Gi")
        ingress = docs["Ingress"]["spec"]
        self.assertEqual(ingress["ingressClassName"], "nginx")
        self.assertEqual(ingress["rules"][0]["host"], "kubereel.example.com")
        self.assertEqual(ingress["rules"][0]["http"]["paths"][0]["backend"]["service"]["port"]["number"], 9090)
        self.assertEqual(docs["Service"]["spec"]["ports"][0]["port"], 9090)

    def test_shared_existing_claim(self):
        docs = render("--set", "storage.existingClaim=shared-data", "--set", "storage.accessMode=ReadWriteMany")
        self.assertNotIn("PersistentVolumeClaim", docs)
        pod = docs["Deployment"]["spec"]["template"]["spec"]
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], "shared-data")
        env = {item["name"]: item.get("value") for item in pod["containers"][0]["env"]}
        self.assertEqual(env["SHARED_STORAGE"], "true")

    def test_invalid_access_mode(self):
        result = subprocess.run(["helm", "template", "test", str(CHART), "--set", "storage.accessMode=ReadOnlyMany"], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_auth_existing_secret_binding(self):
        docs = render(
            "--set", "auth.existingSecret=admin-bootstrap",
            "--set", "auth.username=ops-admin",
            "--set", "auth.passwordKey=admin-pass",
            "--set", "auth.setupTokenKey=setup-tok",
            "--set", "auth.sessionTTLSeconds=7200",
        )
        env_items = {item["name"]: item for item in docs["Deployment"]["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(env_items["AUTH_SESSION_TTL_SECONDS"]["value"], "7200")
        self.assertEqual(env_items["AUTH_BOOTSTRAP_USERNAME"]["value"], "ops-admin")
        self.assertEqual(
            env_items["AUTH_BOOTSTRAP_PASSWORD"]["valueFrom"]["secretKeyRef"],
            {"name": "admin-bootstrap", "key": "admin-pass"},
        )
        self.assertEqual(
            env_items["AUTH_SETUP_TOKEN"]["valueFrom"]["secretKeyRef"],
            {"name": "admin-bootstrap", "key": "setup-tok"},
        )

    def test_unsupported_settings_are_rejected(self):
        for value in ("replicaCount=2", "execution.mode=embedded", "execution.maxConcurrentJobs=0", "auth.sessionTTLSeconds=10", "storage.type=s3", "storage.type=nfs", "providers.mock.enabled=false", "service.type=LoadBalancer", "image.tag=latest", "persistence.existingClaim=old-claim", "workflow.maxConcurrent=4"):
            with self.subTest(value=value):
                result = subprocess.run(["helm", "template", "test", str(CHART), "--set", value], text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)

    def test_s3_workload_identity_and_secret_modes(self):
        items = render_list(
            "--set", "storage.type=s3",
            "--set", "storage.s3.bucket=kubereel-cloud-artifacts",
            "--set", "storage.s3.region=us-west-2",
            "--set", "storage.s3.prefix=prod/eks",
            "--set", r"serviceAccount.annotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::123456789012:role/kubereel-cp",
            "--set", r"workerServiceAccount.annotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::123456789012:role/kubereel-worker",
            "--set", "networkPolicy.enabled=true",
        )
        sas = {item["metadata"]["name"]: item for item in items if item["kind"] == "ServiceAccount"}
        self.assertEqual(set(sas), {"test-kubereel", "test-kubereel-worker"})
        self.assertEqual(
            sas["test-kubereel"]["metadata"]["annotations"]["eks.amazonaws.com/role-arn"],
            "arn:aws:iam::123456789012:role/kubereel-cp",
        )
        self.assertEqual(
            sas["test-kubereel-worker"]["metadata"]["annotations"]["eks.amazonaws.com/role-arn"],
            "arn:aws:iam::123456789012:role/kubereel-worker",
        )

        # Worker ServiceAccount must never be bound to control-plane Kubernetes RBAC.
        bindings = [item for item in items if item["kind"] == "RoleBinding"]
        self.assertEqual(len(bindings), 1)
        self.assertEqual(bindings[0]["subjects"][0]["name"], "test-kubereel")

        deployment = next(item for item in items if item["kind"] == "Deployment")
        env_items = {item["name"]: item for item in deployment["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(env_items["STORAGE_TYPE"]["value"], "s3")
        self.assertEqual(env_items["S3_BUCKET"]["value"], "kubereel-cloud-artifacts")
        self.assertEqual(env_items["S3_REGION"]["value"], "us-west-2")
        self.assertEqual(env_items["S3_PREFIX"]["value"], "prod/eks")
        self.assertEqual(env_items["WORKER_SERVICE_ACCOUNT"]["value"], "test-kubereel-worker")
        self.assertNotIn("AWS_ACCESS_KEY_ID", env_items)
        self.assertNotIn("S3_CREDENTIALS_SECRET", env_items)

        jobs_np = next(item for item in items if item["kind"] == "NetworkPolicy" and item["metadata"]["name"] == "test-kubereel-jobs")
        self.assertEqual(len(jobs_np["spec"]["egress"]), 3)

        # Optional static Secret credentials for non-AWS S3-compatible object stores.
        secret_items = render_list(
            "--set", "storage.type=s3",
            "--set", "storage.s3.bucket=kubereel-minio",
            "--set", "storage.s3.region=us-east-1",
            "--set", "storage.s3.endpoint=https://minio.example.com",
            "--set", "storage.s3.forcePathStyle=true",
            "--set", "storage.s3.existingSecret=minio-s3-secret",
        )
        secret_dep = next(item for item in secret_items if item["kind"] == "Deployment")
        sec_env = {item["name"]: item for item in secret_dep["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(sec_env["S3_ENDPOINT"]["value"], "https://minio.example.com")
        self.assertEqual(sec_env["S3_FORCE_PATH_STYLE"]["value"], "true")
        self.assertEqual(sec_env["S3_CREDENTIALS_SECRET"]["value"], "minio-s3-secret")
        self.assertEqual(
            sec_env["AWS_ACCESS_KEY_ID"]["valueFrom"]["secretKeyRef"],
            {"name": "minio-s3-secret", "key": "accessKeyId"},
        )

    def test_ingress_requires_host(self):
        result = subprocess.run(
            ["helm", "template", "test", str(CHART), "--set", "ingress.enabled=true"],
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ingress.host is required", result.stderr)

    def test_network_policy_isolates_control_plane_and_jobs(self):
        items = render_list("--set", "networkPolicy.enabled=true")
        policies = {item["metadata"]["name"]: item for item in items if item["kind"] == "NetworkPolicy"}
        self.assertEqual(set(policies), {"test-kubereel", "test-kubereel-jobs"})
        cp_spec = policies["test-kubereel"]["spec"]
        self.assertEqual(cp_spec["policyTypes"], ["Ingress"])
        self.assertEqual(cp_spec["ingress"][0]["ports"], [{"protocol": "TCP", "port": "http"}])

        jobs_spec = policies["test-kubereel-jobs"]["spec"]
        self.assertEqual(jobs_spec["policyTypes"], ["Ingress", "Egress"])
        self.assertEqual(jobs_spec["ingress"], [])
        self.assertEqual(jobs_spec["podSelector"]["matchLabels"], {"app.kubernetes.io/name": "kubereel", "kubereel.io/instance": "test"})

    def test_service_monitor_requires_bearer_secret_and_renders(self):
        missing = subprocess.run(
            ["helm", "template", "test", str(CHART), "--set", "serviceMonitor.enabled=true"],
            text=True,
            capture_output=True,
        )
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("serviceMonitor.bearerTokenSecret.name is required", missing.stderr)

        docs = render(
            "--set", "serviceMonitor.enabled=true",
            "--set", "serviceMonitor.bearerTokenSecret.name=prom-kubereel-token",
            "--set", "serviceMonitor.bearerTokenSecret.key=api-token",
            "--set", "serviceMonitor.labels.release=prometheus",
        )
        sm = docs["ServiceMonitor"]
        self.assertEqual(sm["apiVersion"], "monitoring.coreos.com/v1")
        self.assertEqual(sm["metadata"]["labels"]["release"], "prometheus")
        endpoint = sm["spec"]["endpoints"][0]
        self.assertEqual(endpoint["path"], "/metrics")
        self.assertEqual(endpoint["port"], "http")
        self.assertEqual(
            endpoint["authorization"],
            {"type": "Bearer", "credentials": {"name": "prom-kubereel-token", "key": "api-token"}},
        )

    def test_helm_package_includes_crds_schema_and_excludes_tests(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            subprocess.run(["helm", "package", str(CHART), "--destination", tmpdir], check=True, capture_output=True)
            archives = list(pathlib.Path(tmpdir).glob("kubereel-chart-*.tgz"))
            self.assertEqual(len(archives), 1)
            with tarfile.open(archives[0], "r:gz") as archive:
                names = set(archive.getnames())
            self.assertIn("kubereel-chart/values.schema.json", names)
            self.assertIn("kubereel-chart/templates/NOTES.txt", names)
            self.assertIn("kubereel-chart/crds/kubereel.io_videoworkflows.yaml", names)
            self.assertIn("kubereel-chart/crds/kubereel.io_videoruns.yaml", names)
            self.assertIn("kubereel-chart/crds/kubereel.io_videoproviders.yaml", names)
            self.assertFalse(any("/tests/" in entry for entry in names))


if __name__ == "__main__":
    unittest.main()
