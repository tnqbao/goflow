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
        self.assertEqual(set(docs), {"Deployment", "Service", "PersistentVolumeClaim"})
        deployment = docs["Deployment"]["spec"]
        self.assertEqual(deployment["replicas"], 1)
        self.assertEqual(deployment["strategy"]["type"], "Recreate")
        pod = deployment["template"]["spec"]
        container = pod["containers"][0]
        self.assertEqual(container["livenessProbe"]["httpGet"]["path"], "/healthz")
        self.assertEqual(container["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(pod["securityContext"]["fsGroup"], 10001)
        self.assertEqual(container["volumeMounts"][0]["mountPath"], "/data")
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], docs["PersistentVolumeClaim"]["metadata"]["name"])
        self.assertEqual(docs["Service"]["spec"]["selector"], deployment["selector"]["matchLabels"])
        self.assertNotIn("storageClassName", docs["PersistentVolumeClaim"]["spec"])

    def test_overrides(self):
        docs = render(
            "--set", "image.repository=example/goflow",
            "--set", "image.tag=v1",
            "--set", "persistence.storageClass=fast",
            "--set", "persistence.size=5Gi",
            "--set", "service.port=9090",
            "--set", "resources.requests.memory=256Mi",
            "--set", "ingress.enabled=true",
            "--set", "ingress.host=goflow.example.com",
            "--set", "ingress.className=nginx",
        )
        container = docs["Deployment"]["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(container["image"], "example/goflow:v1")
        self.assertEqual(container["resources"]["requests"]["memory"], "256Mi")
        claim = docs["PersistentVolumeClaim"]["spec"]
        self.assertEqual(claim["storageClassName"], "fast")
        self.assertEqual(claim["resources"]["requests"]["storage"], "5Gi")
        ingress = docs["Ingress"]["spec"]
        self.assertEqual(ingress["ingressClassName"], "nginx")
        self.assertEqual(ingress["rules"][0]["host"], "goflow.example.com")
        self.assertEqual(ingress["rules"][0]["http"]["paths"][0]["backend"]["service"]["port"]["number"], 9090)
        self.assertEqual(docs["Service"]["spec"]["ports"][0]["port"], 9090)

    def test_ingress_requires_host(self):
        result = subprocess.run(
            ["helm", "template", "test", str(CHART), "--set", "ingress.enabled=true"],
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ingress.host is required", result.stderr)


if __name__ == "__main__":
    unittest.main()
