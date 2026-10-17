import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("verifier", Path(__file__).with_name("verify_orders.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)

def context(amount=11000, discount=1000):
    def segment(value):
        return {"text": json.dumps(value)}
    return {"local": [segment({"order_id": "a", "customer_id": "c", "paid_cents": 6000}),
                      segment({"order_id": "b", "customer_id": "c", "paid_cents": 6000})],
            "rules": [segment({"threshold_cents": 10000, "discount_cents": discount})],
            "inputs": [{"port": "amount", "value": json.dumps(amount)}]}

class OrdersVerifierTest(unittest.TestCase):
    def test_independent_total_and_revised_rule(self):
        self.assertEqual(verifier.verify(context())["result"], "PASS")
        self.assertEqual(verifier.verify(context(10000, 2000))["result"], "PASS")

    def test_rejects_model_total_without_discount(self):
        self.assertEqual(verifier.verify(context(12000))["result"], "FAIL")

    def test_unknown_for_missing_or_conflicting_evidence(self):
        c = context()
        c["local"] = []
        self.assertEqual(verifier.verify(c)["result"], "UNKNOWN")
        c = context()
        c["local"].append({"text": json.dumps({"order_id": "a", "customer_id": "c", "paid_cents": 2})})
        self.assertEqual(verifier.verify(c)["result"], "UNKNOWN")

    def test_deduplicates_identical_orders_and_groups_customers(self):
        c = context()
        c["local"].append(c["local"][0])
        self.assertEqual(verifier.verify(c)["result"], "PASS")
        c = context(12000)
        c["local"][1]["text"] = json.dumps({"order_id": "b", "customer_id": "d", "paid_cents": 6000})
        self.assertEqual(verifier.verify(c)["result"], "PASS")

    def test_refuses_boolean_amount(self):
        self.assertEqual(verifier.verify(context(True))["result"], "UNKNOWN")

if __name__ == "__main__":
    unittest.main()
