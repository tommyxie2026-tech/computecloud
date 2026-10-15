import unittest

from scripts.security import validate_worker_container as validator


def fixture():
    return {
        "Config": {"User": "10001:10001", "Image": "worker@sha256:" + "a" * 64, "ExposedPorts": None},
        "HostConfig": {
            "ReadonlyRootfs": True,
            "CapDrop": ["ALL"],
            "CapAdd": None,
            "SecurityOpt": ["no-new-privileges:true"],
            "Privileged": False,
            "Memory": 2 * 1024 * 1024 * 1024,
            "NanoCpus": 2_000_000_000,
            "PidsLimit": 256,
            "PortBindings": None,
            "NetworkMode": "computecloud-control",
            "LogConfig": {"Type": "json-file", "Config": {"max-size": "10m", "max-file": "3"}},
        },
        "Mounts": [
            {"Source": "/srv/computecloud/config", "Destination": "/etc/computecloud", "RW": False},
            {"Source": "/srv/computecloud/worker-state", "Destination": "/var/lib/computecloud", "RW": True},
        ],
    }


def policy():
    return {
        "network_mode": "computecloud-control",
        "allowed_mounts": {
            "/etc/computecloud": {"mode": "ro", "source_prefix": "/srv/computecloud/config"},
            "/var/lib/computecloud": {"mode": "rw", "source_prefix": "/srv/computecloud/worker-state"},
        },
    }


class WorkerContainerValidationTests(unittest.TestCase):
    def test_accepts_hardened_worker_container(self):
        self.assertEqual(validator.validate(fixture(), policy()), [])

    def test_rejects_root_worker(self):
        inspect = fixture()
        inspect["Config"]["User"] = "0:0"
        self.assertIn("worker user must be non-root", validator.validate(inspect, policy()))

    def test_rejects_writable_personal_codex_mount(self):
        inspect = fixture()
        inspect["Mounts"].append({"Source": "/Users/alice/.codex", "Destination": "/home/worker/.codex", "RW": True})
        errors = validator.validate(inspect, policy())
        self.assertIn("personal profile mount is present", errors)
        self.assertNotIn("/Users/alice", " ".join(errors))

    def test_rejects_docker_socket_mount(self):
        inspect = fixture()
        inspect["Mounts"].append({"Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock", "RW": True})
        self.assertIn("unapproved mount target is present", validator.validate(inspect, policy()))

    def test_rejects_missing_and_unlimited_resource_limits(self):
        inspect = fixture()
        inspect["HostConfig"].update({"Memory": 0, "NanoCpus": 0, "PidsLimit": 0})
        errors = validator.validate(inspect, policy())
        self.assertIn("worker memory limit is missing", errors)
        self.assertIn("worker cpu limit is missing", errors)
        self.assertIn("worker pid limit is missing", errors)

    def test_rejects_privilege_network_ports_and_unbounded_logs(self):
        inspect = fixture()
        inspect["HostConfig"].update({
            "Privileged": True,
            "CapAdd": ["SYS_ADMIN"],
            "CapDrop": [],
            "SecurityOpt": [],
            "PortBindings": {"8080/tcp": [{"HostPort": "8080"}]},
            "NetworkMode": "host",
            "LogConfig": {"Type": "json-file", "Config": {}},
        })
        errors = validator.validate(inspect, policy())
        self.assertIn("worker container is privileged", errors)
        self.assertIn("worker capabilities are not fully dropped", errors)
        self.assertIn("no-new-privileges is disabled", errors)
        self.assertIn("worker container exposes a host port", errors)
        self.assertIn("worker does not use an operator-created network", errors)
        self.assertIn("worker log rotation is unbounded", errors)

    def test_rejects_missing_read_only_root_and_unapproved_mount_mode(self):
        inspect = fixture()
        inspect["HostConfig"]["ReadonlyRootfs"] = False
        inspect["Mounts"][0]["RW"] = True
        errors = validator.validate(inspect, policy())
        self.assertIn("worker root filesystem is writable", errors)
        self.assertIn("mount access mode violates policy", errors)


if __name__ == "__main__":
    unittest.main()
