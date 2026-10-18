import unittest
from ci_iu_graph import build_report, REQUIRED_TESTS

class CIReportTest(unittest.TestCase):
    def healthy(self):
        return ([{"name": name, "exit_code": 0} for name in ("verifier", "contracts", "native", "cli", "simulation", "build", "processes")],
                {key: "pass" for key in REQUIRED_TESTS},
                {"real_model_calls": False, "checks_passed": True,
                 "coverage":{"checks_passed":True,"provider_calls":0,"provider_status":"NOT_RUN","fixture_invocations":72,
                          "protocol":{"samples":[{} for _ in range(18)]},
                          "samples":[{"arm":arm,"node_invocations":1,"input_tokens":None,"reported_cost_usd":None,"false_acceptance":arm=="iu_no_verifier" and i==1,"verdict":"UNKNOWN" if i==2 else "PASS","attempts":[{"verdict":"FAIL"},{"verdict":"PASS"}]} for arm in ("monolithic","iu","iu_no_memory","iu_no_verifier") for i in range(18)]},
                 "pilot":{"checks_passed":True,"provider_calls":0,"fixture_invocations":215,
                          "protocol":{"samples":[{} for _ in range(20)]},
                          "samples":[{"arm":arm,"independent_business_pass":True,"input_tokens":None,"reported_cost_usd":None}
                                     for arm in ("monolithic","iu","iu_no_memory","iu_no_verifier") for _ in range(20)]},
                 "scenarios": [{"injected_fault": "none" if i < 8 else "fault", "check_passed": True} for i in range(18)]})

    def test_missing_pilot_evidence_cannot_pass(self):
        stages,events,simulation=self.healthy()
        simulation.pop("pilot",None)
        self.assertEqual(build_report(stages,events,simulation,self.process_report())["status"],"FAILED")

    def test_missing_import_recovery_evidence_cannot_pass(self):
        stages,events,simulation=self.healthy()
        process=self.process_report()
        process["steps"][0]["evidence"].pop("import_recovery",None)
        self.assertEqual(build_report(stages,events,simulation,process)["status"],"FAILED")

    def test_recovery_requires_pre_restart_execution_and_actual_revocation_evidence(self):
        for field in ("attempts_before_restart","revoked_denial"):
            stages,events,simulation=self.healthy()
            process=self.process_report()
            for case in process["steps"][0]["evidence"]["import_recovery"]:case.pop(field,None)
            self.assertEqual(build_report(stages,events,simulation,process)["status"],"FAILED")

    def test_missing_coverage_or_native_recovery_cannot_pass(self):
        stages,events,simulation=self.healthy()
        simulation.pop("coverage",None)
        self.assertEqual(build_report(stages,events,simulation,self.process_report())["status"],"FAILED")
        stages,events,simulation=self.healthy()
        process=self.process_report()
        process["steps"][0]["evidence"].pop("coverage_recovery",None)
        self.assertEqual(build_report(stages,events,simulation,process)["status"],"FAILED")

    def process_report(self):
        return {"summary":{"status":"PASSED"},"steps":[{"name":"native_iu_graphs","status":"PASSED",
          "evidence":{"coverage_recovery":{"generation":2,"initial_verdict":"FAIL","final_verdict":"PASS","goal_state":"SUCCEEDED","server_process_restart":True,"publication_idempotent":True,"publications":1,"attempts_before":2,"attempts_after":2,"reads_before":1,"reads_after":1,"old_context_unchanged":True,"origin_pins":2},"import_recovery":[dict(mode=mode,generation=2,attempts_before_restart=0,revoked_denial="IMPORT_GRANT_NOT_USABLE",origin_pins=1,attempts=1 if mode=="memory" else 0,tasks=1 if mode=="memory" else 0,source_output=6000,recovered_output=6000,
              **{key:True for key in ("grant_consumed","server_process_restart","submissions_disabled_recovery","publication_idempotent","revoked_denied","verification_authorized","submitter_verification_denied","source_read_receipt_preserved","origin_forwarded")}) for mode in ("memory","node_result")],"real_model_calls":False,"worker_processes":2,"invalidation":{"applied":True,"server_process_restart":True,"attempts_unchanged":True,"unauthorized_denied":True,"stale_pass_denied":True,"revision":1,"goal_state":"FAILED"},"cases":[{"server_process_restart":i==0} for i in range(4)]}}]}

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
