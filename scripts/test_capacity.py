"""Unit checks for the dependency-free capacity report helpers."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

import capacity


class CapacityTests(unittest.TestCase):
    def test_percentiles(self):
        self.assertEqual(capacity.distribution([]), {"count": 0, "p50": None, "p95": None, "max": None})
        self.assertEqual(capacity.distribution([8, 1, 3, 2]), {"count": 4, "p50": 2, "p95": 8, "max": 8})

    def test_dimensions(self):
        self.assertEqual(capacity.dimensions("1,2,8"), [1, 2, 8])
        for value in ["0", "9", "1,1", "a", "", "1,"]:
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                capacity.dimensions(value)

    @unittest.skipUnless(Path("/proc/self/stat").exists(), "Linux /proc required")
    def test_process_sample(self):
        cpu, rss = capacity.process_sample(os.getpid())
        self.assertGreaterEqual(cpu, 0)
        self.assertGreater(rss, 0)

    def test_database_queue_uses_dispatch_not_completion(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.db"
            with sqlite3.connect(path) as db:
                db.executescript("""
                    CREATE TABLE tasks(id,created,state,worker);
                    CREATE TABLE job_events(type,recorded_at,body);
                    CREATE TABLE events(body);
                    CREATE TABLE artifacts(size);
                    INSERT INTO tasks VALUES('t',100,'SUCCEEDED','worker-0');
                    INSERT INTO artifacts VALUES(20);
                """)
                db.executemany("INSERT INTO job_events VALUES('job.task_state',?,?)", [
                    (125, json.dumps({"task_id": "t", "to": "STARTING"})),
                    (200, json.dumps({"task_id": "t", "to": "SUCCEEDED"}))])
            result = capacity.collect_database(path)
            self.assertEqual(result["task_queue_ms"]["p95"], 25)
            self.assertEqual(result["task_queue_ms"]["count"], 1)
            self.assertEqual(result["artifact_stored_bytes"], 20)
            self.assertIsNone(result["sqlite_write_latency_ms"])
            self.assertEqual(result["task_states"], {"SUCCEEDED": 1})


if __name__ == "__main__":
    unittest.main()
