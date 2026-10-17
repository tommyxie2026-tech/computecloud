import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from ci_task_flow import FIXTURE

class GraphProcessFixtureTest(unittest.TestCase):
    def invoke(self, prompt):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "fixture.py"
            path.write_text(FIXTURE)
            result = subprocess.run([sys.executable, str(path), "exec"], input=prompt, text=True,
                                    capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            return events[1]["item"]["text"]

    def test_legacy_json_scalar_is_not_a_graph(self):
        self.assertEqual(self.invoke("null"), "ci fixture single completed")

    def test_graph_extract_preserves_sources(self):
        source = {"source":"order_a","sha256":"a"*64}
        context = {"version":"iu-context-v1","instructions":'Declared output ports: [{"name":"result"}]',
                   "inputs":[],"local":[{"text":'{"paid_cents":6000}',"source":source}],"rules":[]}
        out = json.loads(self.invoke(json.dumps(context)))
        self.assertEqual(out["values"]["result"], 6000)
        self.assertEqual(out["records"][0]["sources"], [{"id":"order_a","sha256":"a"*64}])

    def test_injected_rule_omission_reaches_independent_verifier(self):
        context = {"version":"iu-context-v1",
                   "instructions":'fixture-applied-discount:0 Declared output ports: [{"name":"result"}]',
                   "inputs":[{"value":'{"a":6000,"b":6000}'}],"local":[],
                   "rules":[{"text":'{"threshold_cents":10000,"discount_cents":1000}',
                             "source":{"source":"rule","sha256":"b"*64}}]}
        self.assertEqual(json.loads(self.invoke(json.dumps(context)))["values"]["result"], 12000)

if __name__ == "__main__":
    unittest.main()
