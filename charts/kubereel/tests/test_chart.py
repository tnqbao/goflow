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
    return {item["kind"]: item for item in yaml.safe_load_all(result.stdout) if item}


class ChartTest(unittest.TestCase):
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
        self.assertEqual(env["KUBEREEL_SHARED_STORAGE"], "false")
        self.assertEqual(env["KUBEREEL_PVC"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(docs["Role"]["rules"][0]["verbs"], ["get", "list", "create", "delete"])
        self.assertEqual(container["livenessProbe"]["httpGet"]["path"], "/healthz")
        self.assertEqual(container["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(pod["securityContext"]["fsGroup"], 10001)
        self.assertEqual(container["volumeMounts"][0]["mountPath"], "/data")
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(docs["Service"]["spec"]["selector"], deployment["selector"]["matchLabels"])
        self.assertNotIn("storageClassName", docs["PersistentVolumeClaim"]["spec"])

    def test_overrides(self):
        docs = render(
            "--set", "image.repository=example/kubereel",
            "--set", "image.tag=v1",
            "--set", "persistence.storageClass=fast",
            "--set", "persistence.size=5Gi",
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
        docs = render("--set", "persistence.existingClaim=shared-data", "--set", "persistence.accessMode=ReadWriteMany")
        self.assertNotIn("PersistentVolumeClaim", docs)
        pod = docs["Deployment"]["spec"]["template"]["spec"]
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], "shared-data")
        env = {item["name"]: item.get("value") for item in pod["containers"][0]["env"]}
        self.assertEqual(env["KUBEREEL_SHARED_STORAGE"], "true")

    def test_invalid_access_mode(self):
        result = subprocess.run(["helm", "template", "test", str(CHART), "--set", "persistence.accessMode=ReadOnlyMany"], text=True, capture_output=True)
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
