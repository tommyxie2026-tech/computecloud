import unittest

from scripts.security import validate_worker_runtime_config as validator


def fixture():
    return {
        "Config": {
            "Image": "runtime@sha256:" + "a" * 64,
            "Cmd": ["--runtime-image", "runtime@sha256:" + "b" * 64, "--public-strict", "--egress-network", "cc-egress", "--seccomp-profile", "/etc/computecloud/seccomp.json", "--memory-bytes", "2147483648", "--nano-cpus", "2000000000", "--pids-limit", "256"],
            "ExposedPorts": None,
        },
        "HostConfig": {"Privileged": False, "CapAdd": None, "PortBindings": None},
        "Mounts": [{"Source": "/var/run/docker.sock"}, {"Source": "/srv/computecloud/workspaces"}],
    }


class RuntimeConfigValidationTests(unittest.TestCase):
    def test_accepts_strict_controller_contract(self):
        self.assertEqual(validator.validate(fixture()), [])

    def test_rejects_unsafe_public_runtime_settings(self):
        config = fixture()
        config["Config"]["Image"] = "runtime:latest"
        config["Config"]["Cmd"] = ["--runtime-image", "runtime:latest", "--egress-network", "bridge"]
        config["Config"]["ExposedPorts"] = {"8080/tcp": {}}
        config["HostConfig"]["Privileged"] = True
        config["HostConfig"]["CapAdd"] = ["SYS_ADMIN"]
        config["HostConfig"]["PortBindings"] = {"8080/tcp": [{"HostPort": "8080"}]}
        config["Mounts"].append({"Source": "/Users/operator/.codex"})
        errors = validator.validate(config)
        self.assertGreaterEqual(len(errors), 6)


if __name__ == "__main__":
    unittest.main()
