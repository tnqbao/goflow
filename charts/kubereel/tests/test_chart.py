#!/usr/bin/env python3
"""Render the chart and verify its deployment invariants. Requires Helm and PyYAML."""
import pathlib
import subprocess
import unittest

import yaml

CHART = pathlib.Path(__file__).resolve().parents[1]


def render(*values):
    result = subprocess.run(
        ["helm", "template", "test", str(CHART), *values],
        text=True,
        capture_output=True,
        check=True,
    )
    return {item["metadata"]["name"] if item["kind"] == "CustomResourceDefinition" else item["kind"]: item for item in yaml.safe_load_all(result.stdout) if item}


class ChartTest(unittest.TestCase):
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

    def test_video_run_crd(self):
        docs = render("--include-crds")
        run = docs["videoruns.kubereel.io"]
        self.assertEqual(run["spec"]["scope"], "Namespaced")
        version = run["spec"]["versions"][0]
        self.assertEqual(version["name"], "v1alpha1")
        self.assertEqual(version["subresources"], {"status": {}})
        schema = version["schema"]["openAPIV3Schema"]["properties"]
        job_config = schema["status"]["properties"]["steps"]["items"]["properties"]["jobConfig"]
        self.assertEqual(set(job_config["required"]), {"image", "claim", "instance", "sharedStorage", "pullPolicy", "ttlSeconds"})
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
        self.assertEqual(set(spec["properties"]), {"type", "capabilities", "models", "baseURL", "secretRef", "maxConcurrent", "timeoutSeconds"})
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
        self.assertEqual(env["KUBEREEL_EXECUTION_MODE"], "kubernetes")
        self.assertEqual(env["KUBEREEL_MAX_CONCURRENT"], "5")
        self.assertEqual(env["KUBEREEL_JOB_TTL_SECONDS"], "86400")
        self.assertEqual(container["image"], "kubereel:0.1.0")
        self.assertEqual(env["KUBEREEL_JOB_IMAGE"], container["image"])
        self.assertEqual(env["KUBEREEL_SHARED_STORAGE"], "false")
        self.assertEqual(env["KUBEREEL_PVC"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(docs["Role"]["rules"][0]["verbs"], ["get", "list", "watch", "create", "delete"])
        self.assertIn({"apiGroups": ["kubereel.io"], "resources": ["videoruns"], "verbs": ["get", "list", "watch"]}, docs["Role"]["rules"])
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
        self.assertEqual(env["KUBEREEL_SHARED_STORAGE"], "true")

    def test_invalid_access_mode(self):
        result = subprocess.run(["helm", "template", "test", str(CHART), "--set", "storage.accessMode=ReadOnlyMany"], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_unsupported_settings_are_rejected(self):
        for value in ("replicaCount=2", "execution.mode=embedded", "execution.maxConcurrentJobs=0", "storage.type=s3", "providers.mock.enabled=false", "service.type=LoadBalancer", "image.tag=latest", "persistence.existingClaim=old-claim", "workflow.maxConcurrent=4"):
            with self.subTest(value=value):
                result = subprocess.run(["helm", "template", "test", str(CHART), "--set", value], text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)

    def test_ingress_requires_host(self):
        result = subprocess.run(
            ["helm", "template", "test", str(CHART), "--set", "ingress.enabled=true"],
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ingress.host is required", result.stderr)


if __name__ == "__main__":
    unittest.main()
