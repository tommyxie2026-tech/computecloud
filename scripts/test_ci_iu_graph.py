import unittest
from ci_iu_graph import build_report, REQUIRED_TESTS

class CIReportTest(unittest.TestCase):
    def healthy(self):
        return ([{"name": name, "exit_code": 0} for name in ("verifier", "contracts", "native", "cli", "simulation", "build", "processes")],
                {key: "pass" for key in REQUIRED_TESTS},
                {"real_model_calls": False, "checks_passed": True,
                 "scenarios": [{"injected_fault": "none" if i < 8 else "fault", "check_passed": True} for i in range(18)]})

    def process_report(self):
        return {"summary":{"status":"PASSED"},"steps":[{"name":"native_iu_graphs","status":"PASSED",
          "evidence":{"real_model_calls":False,"worker_processes":2,"invalidation":{"applied":True,"server_process_restart":True,"attempts_unchanged":True,"unauthorized_denied":True,"stale_pass_denied":True,"revision":1,"goal_state":"FAILED"},"cases":[{"server_process_restart":i==0} for i in range(4)]}}]}

    def test_missing_invalidation_process_evidence_cannot_pass(self):
        stages, events, simulation = self.healthy()
        report = self.process_report()
        report["steps"][0]["evidence"].pop("invalidation", None)
        self.assertEqual(build_report(stages, events, simulation, report)["status"], "FAILED")

    def test_requires_all_native_checks_and_simulation(self):
        stages, events, simulation = self.healthy()
        self.assertEqual(build_report(stages, events, simulation, self.process_report())["status"], "PASSED")

    def test_skip_missing_or_failed_stage_cannot_turn_green(self):
        for action in ("skip", "fail", "missing"):
            stages, events, simulation = self.healthy()
            events[REQUIRED_TESTS[0]] = action
            self.assertEqual(build_report(stages, events, simulation, self.process_report())["status"], "FAILED")
        stages, events, simulation = self.healthy()
        stages[0]["exit_code"] = 1
        self.assertEqual(build_report(stages, events, simulation, self.process_report())["status"], "FAILED")

    def test_missing_or_real_model_report_is_rejected(self):
        stages, events, simulation = self.healthy()
        simulation["scenarios"] = []
        self.assertEqual(build_report(stages, events, simulation, self.process_report())["status"], "FAILED")
        stages, events, simulation = self.healthy()
        simulation["real_model_calls"] = True
        self.assertEqual(build_report(stages, events, simulation, self.process_report())["status"], "FAILED")

if __name__ == "__main__":
    unittest.main()
