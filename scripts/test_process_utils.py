import os
import subprocess
import unittest
from unittest.mock import patch

from process_utils import cpu_seconds, process_alive, process_sample


class ProcessTests(unittest.TestCase):
    def test_cpu_time(self):
        for value, expected in [("00:01.25", 1.25), ("02:03.50", 123.5),
                                ("01:02:03", 3723), ("2-01:02:03", 176523)]:
            self.assertEqual(cpu_seconds(value), expected)

    def test_live_and_reaped(self):
        child = subprocess.Popen(["sleep", "60"])
        try:
            self.assertTrue(process_alive(child.pid))
            cpu, rss = process_sample(os.getpid())
            self.assertGreaterEqual(cpu, 0)
            self.assertGreater(rss, 0)
        finally:
            child.kill()
            child.wait()
        self.assertFalse(process_alive(child.pid))

    def test_darwin_inspection_failure_is_not_cleanup(self):
        with patch("process_utils.sys.platform", "darwin"), patch(
                "process_utils._ps", side_effect=PermissionError("denied")):
            with self.assertRaises(PermissionError):
                process_alive(123)

    def test_darwin_zombie_is_stopped(self):
        with patch("process_utils.sys.platform", "darwin"), patch(
                "process_utils._ps", return_value="Z+"):
            self.assertFalse(process_alive(123))
