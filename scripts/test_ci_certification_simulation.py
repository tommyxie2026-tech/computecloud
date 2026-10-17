import copy
import math
import unittest

import ci_certification_simulation as gate


def cache(warm=4.0):
    return {"scope": "provider_fixture_not_job_benchmark", "cold_samples": 10, "warm_samples": 9,
            "cold_ms": [10.0] * 10, "warm_ms": [warm] * 9, "concurrent_ms": [8.0] * 10,
            "concurrent_attempts": 10, "concurrent_cache_hits": 10,
            "cold_p50_ms": 10.0, "warm_p50_ms": warm, "warm_over_cold_p50": warm / 10}


class CertificationTests(unittest.TestCase):
    def test_threshold_is_inclusive_and_observed_failure_stays_red(self):
        self.assertTrue(gate.cache_gate(cache()))
        self.assertFalse(gate.cache_gate(cache(4.45473)))

    def test_missing_forged_nonfinite_or_short_samples_are_rejected(self):
        for key, value in [("cold_ms", []), ("warm_ms", [4.0]), ("warm_ms", [math.nan] * 9),
                           ("warm_ms", [True] * 9), ("warm_over_cold_p50", 0.1),
                           ("cold_p50_ms", 100), ("concurrent_cache_hits", 9)]:
            report = cache()
            report[key] = value
            self.assertFalse(gate.cache_gate(report), key)
        self.assertFalse(gate.cache_gate(None))

    def test_comparison_retains_every_round_and_cannot_waive_candidate_failure(self):
        rows = [{"round": i, "arm": arm, "cache": cache(4.5 if i == 1 and arm == "candidate" else 3.0),
                 "exit_code": 0, "test_action": "pass", "source_sha": arm + "-sha"}
                for i in range(3) for arm in ("baseline", "candidate")]
        comparison = gate.comparison_report(rows, 3)
        self.assertEqual(len(comparison["rounds"]), 6)
        self.assertTrue(comparison["complete"])
        self.assertFalse(comparison["candidate_passed"])
        self.assertEqual(comparison["candidate_ratios"], [0.3, 0.45, 0.3])
        self.assertFalse(gate.comparison_report(rows[:-1], 3)["complete"])
        rows[0]["exit_code"] = 1
        self.assertFalse(gate.comparison_report(rows, 3)["complete"])

    def test_duplicate_round_cannot_replace_missing_round(self):
        rows = [{"round": 0, "arm": arm, "cache": cache(3.0), "exit_code": 0,
                 "test_action": "pass", "source_sha": arm} for arm in ("baseline", "candidate")]
        self.assertFalse(gate.comparison_report(rows * 3, 3)["complete"])

    def test_skipped_or_failed_required_test_is_not_pass(self):
        rows = [{"round": i, "arm": arm, "cache": cache(3.0), "exit_code": 0,
                 "test_action": "pass", "source_sha": arm} for i in range(3) for arm in ("baseline", "candidate")]
        for action in ("skip", "fail", "missing"):
            changed = copy.deepcopy(rows)
            changed[-1]["test_action"] = action
            self.assertFalse(gate.comparison_report(changed, 3)["complete"])


if __name__ == "__main__":
    unittest.main()
