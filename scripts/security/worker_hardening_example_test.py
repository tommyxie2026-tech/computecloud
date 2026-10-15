import json
import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class WorkerHardeningExampleTests(unittest.TestCase):
    def test_policy_has_only_dedicated_runtime_mounts(self):
        policy = json.loads((ROOT / "examples/deployment/worker-hardening-policy.json").read_text())
        self.assertEqual(policy["network_mode"], "computecloud-control")
        self.assertEqual(set(policy["allowed_mounts"]), {
            "/etc/computecloud", "/var/lib/computecloud", "/home/computecloud/.codex"
        })
        self.assertEqual(policy["allowed_mounts"]["/etc/computecloud"]["mode"], "ro")
        self.assertEqual(policy["allowed_mounts"]["/home/computecloud/.codex"]["mode"], "rw")
        self.assertTrue(all(
            entry["source_prefix"].startswith("/srv/agent-platform/worker-template/")
            for entry in policy["allowed_mounts"].values()
        ))
        encoded = json.dumps(policy).lower()
        self.assertNotRegex(encoded, r"(?:sk-ant-|sk-[a-z0-9]{16,}|cc_live_|/users/[^\"]+|/root/\.codex)")
        self.assertNotIn("worker-a", encoded)
        self.assertNotIn("private.example.invalid", encoded)

    def test_compose_profile_has_required_hardening(self):
        compose = (ROOT / "examples/deployment/worker-hardening.compose.yaml").read_text()
        for value in (
            "read_only: true", "cap_drop:", "- ALL", "no-new-privileges:true",
            "cpus:", "mem_limit:", "pids_limit:", "max-size:", "max-file:",
            "CODEX_HOME:", ":ro", ":rw", "computecloud-control",
        ):
            with self.subTest(value=value):
                self.assertIn(value, compose)
        self.assertNotRegex(compose, r"(?m)^\s+ports:")
        self.assertNotIn("/var/run/docker.sock", compose)
        self.assertNotIn("${HOME}/.codex", compose)

    def test_server_profile_keeps_ports_loopback_and_limits_resources(self):
        compose = (ROOT / "examples/deployment/server-hardening.compose.yaml").read_text()
        self.assertIn('user: "65532:65532"', compose)
        for value in ("read_only: true", "- ALL", "no-new-privileges:true", "cpus:", "mem_limit:", "pids_limit:", "max-size:", "max-file:"):
            with self.subTest(value=value):
                self.assertIn(value, compose)
        self.assertIn("127.0.0.1:17443:17443", compose)
        self.assertIn("127.0.0.1:17444:17444", compose)
        self.assertNotIn("0.0.0.0:17443", compose)
        self.assertNotIn("0.0.0.0:17444", compose)

if __name__ == "__main__":
    unittest.main()
