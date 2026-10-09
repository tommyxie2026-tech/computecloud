import copy
import json
import argparse
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from dual_host_preflight import collect, verify_records


def record(role, node_id, machine_hash, data_dir):
    return {
        "schema_version": 1,
        "run_id": "dual-host-001",
        "source_sha": "a" * 40,
        "role": role,
        "node_id": node_id,
        "machine_id_sha256": machine_hash * 64,
        "binary_sha256": "d" * 64,
        "os": "Linux",
        "data_dir": data_dir,
        "runtime_versions": {
            "codex": {"version": "codex 1", "sha256": "e" * 64},
            "claude": {"version": "claude 1", "sha256": "f" * 64},
        } if role == "worker" else {},
        "mcp_version": "mcp 1" if role == "worker" else None,
    }


class DualHostPreflightTest(unittest.TestCase):
    def setUp(self):
        self.records = [
            record("server", "server", "1", "/srv/computecloud"),
            record("worker", "worker-a", "2", "/var/lib/worker-a"),
            record("worker", "worker-b", "3", "/var/lib/worker-b"),
        ]

    def test_independent_worker_hosts(self):
        result = verify_records(self.records)
        self.assertTrue(result["worker_hosts_independent"])
        self.assertTrue(result["server_host_independent"])
        self.assertEqual(len(result["v047_production_hosts"]), 3)
        self.assertEqual(
            {item["node_id"] for item in result["v047_production_hosts"]},
            {"server", "worker-a", "worker-b"},
        )
        self.assertNotIn("data_dir", result["v047_production_hosts"][0])

    def test_shared_server_host_is_disclosed(self):
        records = copy.deepcopy(self.records)
        records[0]["machine_id_sha256"] = records[1]["machine_id_sha256"]
        result = verify_records(records)
        self.assertFalse(result["server_host_independent"])

    def test_same_worker_host_is_rejected(self):
        records = copy.deepcopy(self.records)
        records[2]["machine_id_sha256"] = records[1]["machine_id_sha256"]
        with self.assertRaisesRegex(ValueError, "same machine-id"):
            verify_records(records)

    def test_shared_data_dir_is_rejected(self):
        records = copy.deepcopy(self.records)
        records[0]["machine_id_sha256"] = records[1]["machine_id_sha256"]
        records[0]["data_dir"] = records[1]["data_dir"]
        with self.assertRaisesRegex(ValueError, "share a data directory"):
            verify_records(records)

    def test_mismatched_commit_is_rejected(self):
        records = copy.deepcopy(self.records)
        records[2]["source_sha"] = "b" * 40
        with self.assertRaisesRegex(ValueError, "source_sha differs"):
            verify_records(records)

    def test_missing_real_runtime_version_is_rejected(self):
        records = copy.deepcopy(self.records)
        del records[1]["runtime_versions"]["claude"]
        with self.assertRaisesRegex(ValueError, "runtime/MCP versions"):
            verify_records(records)

    def test_missing_source_sha_is_rejected(self):
        records = copy.deepcopy(self.records)
        for item in records:
            item["source_sha"] = None
        with self.assertRaisesRegex(ValueError, "source_sha is not a full Git SHA"):
            verify_records(records)

    def test_missing_runtime_checksum_is_rejected(self):
        records = copy.deepcopy(self.records)
        del records[1]["runtime_versions"]["codex"]["sha256"]
        with self.assertRaisesRegex(ValueError, "runtime executable checksum"):
            verify_records(records)

    def test_collector_records_versions_without_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binary = root / "computecloud"
            binary.write_text("#!/bin/sh\necho computecloud-test\n", encoding="utf-8")
            binary.chmod(0o700)
            runtimes = []
            for name in ("codex", "claude"):
                path = root / name
                path.write_text(f"#!/bin/sh\necho {name}-test\n", encoding="utf-8")
                path.chmod(0o700)
                runtimes.append(f"{name}={path}")
            output = root / "preflight.json"
            args = argparse.Namespace(
                run_id="dual-host-001", source_sha="a" * 40, role="worker",
                node_id="worker-a", data_dir=str(root), binary=str(binary),
                runtime=runtimes, mcp_version="mcp-test", out=str(output),
            )
            with patch("dual_host_preflight.platform.system", return_value="Linux"), patch(
                "dual_host_preflight.machine_id_hash", return_value="1" * 64
            ):
                collect(args)
            payload = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(payload["runtime_versions"]["codex"]["version"], "codex-test")
            self.assertEqual(payload["mcp_version"], "mcp-test")
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            with self.assertRaisesRegex(ValueError, "refusing to overwrite"):
                with patch("dual_host_preflight.platform.system", return_value="Linux"), patch(
                    "dual_host_preflight.machine_id_hash", return_value="1" * 64
                ):
                    collect(args)


if __name__ == "__main__":
    unittest.main()
