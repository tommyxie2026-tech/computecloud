import hashlib
import json
import unittest
import copy

from scripts.validate_v047_release_transition import TransitionError, validate_transition
from scripts.test_v047_stable_gate import ci_evidence, reports
from scripts.v047_stable_gate import evaluate_gate


CANDIDATE_SHA = "a" * 40
RELEASE_SHA = "b" * 40
REPORTS = reports()
CI_EVIDENCE = ci_evidence()
GATE = evaluate_gate(CANDIDATE_SHA, REPORTS, CI_EVIDENCE)


def gate_bytes(gate=GATE):
    return (json.dumps(gate, indent=2, sort_keys=True) + "\n").encode()


def request(gate=GATE):
    return {
        "schema_version": "release-request.v2",
        "version": "0.4.7",
        "candidate_sha": CANDIDATE_SHA,
        "stable_gate": {
            "path": "release/evidence/v0.4.7-stable-gate.json",
            "sha256": hashlib.sha256(gate_bytes(gate)).hexdigest(),
            "workflow_run_id": 123456,
        },
    }


CHANGED_FILES = {
    "Makefile",
    "README.md",
    "CHANGELOG.md",
    "release/v0.4.7.json",
    "release/evidence/v0.4.7-stable-gate.json",
    "release/evidence/v0.4.7/runtime-report.json",
    "release/evidence/v0.4.7/integration-report.json",
    "release/evidence/v0.4.7/production-report.json",
    "release/evidence/v0.4.7/performance-report.json",
    "release/evidence/v0.4.7/ci-evidence.json",
    "docs/deployment/production-v0.4.7.md",
    "docs/implementation/v0.4.7-release-status.md",
    "docs/implementation/v0.4.7-stable-tracker.md",
    "docs/implementation/v0.4-closeout-plan.md",
}


class ReleaseTransitionTest(unittest.TestCase):
    def validate(self, **overrides):
        values = {
            "request": request(),
            "gate": GATE,
            "gate_content": gate_bytes(),
            "evidence_reports": copy.deepcopy(REPORTS),
            "ci_evidence": copy.deepcopy(CI_EVIDENCE),
            "project_version": "0.4.7",
            "release_sha": RELEASE_SHA,
            "parent_sha": CANDIDATE_SHA,
            "changed_files": CHANGED_FILES,
            "candidate_makefile": "VERSION ?= 0.4.7-rc.2\nGO ?= go\n",
            "release_makefile": "VERSION ?= 0.4.7\nGO ?= go\n",
        }
        values.update(overrides)
        return validate_transition(**values)

    def assert_rejected(self, fragment, **overrides):
        with self.assertRaisesRegex(TransitionError, fragment):
            self.validate(**overrides)

    def test_accepts_direct_metadata_only_release_transition(self):
        result = self.validate()
        self.assertEqual(result["candidate_sha"], CANDIDATE_SHA)
        self.assertEqual(result["release_sha"], RELEASE_SHA)

    def test_rejects_candidate_or_parent_drift(self):
        bad = request()
        bad["candidate_sha"] = "9" * 40
        self.assert_rejected("parent", request=bad)
        self.assert_rejected("parent", parent_sha="9" * 40)

    def test_rejects_failed_or_mismatched_gate(self):
        failed = dict(GATE, status="FAILED", release_request_allowed=False)
        self.assert_rejected("PASSED", gate=failed, gate_content=gate_bytes(failed), request=request(failed))
        mismatch = dict(GATE, candidate_sha="9" * 40)
        self.assert_rejected("candidate", gate=mismatch, gate_content=gate_bytes(mismatch), request=request(mismatch))

    def test_rejects_gate_file_hash_or_run_id_drift(self):
        bad = request()
        bad["stable_gate"] = dict(bad["stable_gate"], sha256="0" * 64)
        self.assert_rejected("sha256", request=bad)
        bad = request()
        bad["stable_gate"] = dict(bad["stable_gate"], workflow_run_id=999)
        self.assert_rejected("workflow_run_id", request=bad)

    def test_rejects_evidence_that_does_not_reproduce_gate(self):
        changed = copy.deepcopy(REPORTS)
        changed["runtime"]["status"] = "BLOCKED_ENV"
        self.assert_rejected("committed stable evidence", evidence_reports=changed)

    def test_rejects_code_or_workflow_changes(self):
        self.assert_rejected("not allowed", changed_files=CHANGED_FILES | {"internal/server/server.go"})
        self.assert_rejected("not allowed", changed_files=CHANGED_FILES | {".github/workflows/ci.yml"})

    def test_rejects_makefile_changes_beyond_version(self):
        self.assert_rejected(
            "Makefile",
            release_makefile="VERSION ?= 0.4.7\nGO ?= go1.99\n",
        )

    def test_rejects_missing_required_release_files(self):
        self.assert_rejected("required", changed_files=CHANGED_FILES - {"release/v0.4.7.json"})
        self.assert_rejected("required", changed_files=CHANGED_FILES - {"release/evidence/v0.4.7-stable-gate.json"})


if __name__ == "__main__":
    unittest.main()
