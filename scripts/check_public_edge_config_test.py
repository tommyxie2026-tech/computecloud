import copy
import json
from pathlib import Path
import unittest

from scripts.check_public_edge_config import DEFAULT_MANIFEST, validate


class PublicEdgeConfigTests(unittest.TestCase):
    def setUp(self):
        self.manifest = json.loads(DEFAULT_MANIFEST.read_text(encoding="utf-8"))

    def test_example_meets_public_edge_contract(self):
        self.assertEqual(validate(self.manifest), [])

    def test_rejects_extra_public_port_and_docker_socket(self):
        bad = copy.deepcopy(self.manifest)
        bad["public_ports"].append(9000)
        bad["docker"]["published_ports"].append(9000)
        bad["docker"]["mounts"].append("/var/run/docker.sock:/var/run/docker.sock")
        errors = validate(bad)
        self.assertTrue(any("public_ports" in item for item in errors))
        self.assertTrue(any("docker.published_ports" in item for item in errors))
        self.assertTrue(any("Docker mounts" in item for item in errors))

    def test_rejects_http_probe_or_tls_termination_on_grpc(self):
        bad = copy.deepcopy(self.manifest)
        bad["listeners"][0]["mode"] = "http"
        self.assertTrue(any("7443" in item for item in validate(bad)))

    def test_rejects_public_origin_bypass_and_cached_api(self):
        bad = copy.deepcopy(self.manifest)
        bad["origin"]["publicly_reachable"] = True
        bad["listeners"][1]["cache"] = True
        errors = validate(bad)
        self.assertTrue(any("origin" in item for item in errors))
        self.assertTrue(any("7444" in item for item in errors))

    def test_rejects_missing_tls_or_forbidden_service_denial(self):
        bad = copy.deepcopy(self.manifest)
        bad["server_tls"]["enabled"] = False
        bad["forbidden_public_services"].remove("metrics")
        errors = validate(bad)
        self.assertTrue(any("Server TLS" in item for item in errors))
        self.assertTrue(any("metrics" in item for item in errors))


if __name__ == "__main__":
    unittest.main()
