#!/usr/bin/env python3
"""Validate the metadata-only transition from a gated v0.4.7 candidate."""

import argparse
import hashlib
import json
import pathlib
import re
import sys

try:
    from scripts.v047_stable_gate import GateError, evaluate_gate
except ModuleNotFoundError:
    from v047_stable_gate import GateError, evaluate_gate


SHA_RE = re.compile(r"^[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
GATE_PATH = "release/evidence/v0.4.7-stable-gate.json"
EVIDENCE_PATHS = {
    "runtime": "release/evidence/v0.4.7/runtime-report.json",
    "integration": "release/evidence/v0.4.7/integration-report.json",
    "production": "release/evidence/v0.4.7/production-report.json",
    "performance": "release/evidence/v0.4.7/performance-report.json",
}
CI_EVIDENCE_PATH = "release/evidence/v0.4.7/ci-evidence.json"
ALLOWED_CHANGED_FILES = frozenset(
    {
        "Makefile",
        "README.md",
        "CHANGELOG.md",
        "release/v0.4.7.json",
        GATE_PATH,
        *EVIDENCE_PATHS.values(),
        CI_EVIDENCE_PATH,
        "docs/deployment/production-v0.4.7.md",
        "docs/implementation/v0.4.7-release-status.md",
        "docs/implementation/v0.4.7-stable-tracker.md",
        "docs/implementation/v0.4-closeout-plan.md",
    }
)
REQUIRED_CHANGED_FILES = ALLOWED_CHANGED_FILES


class TransitionError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise TransitionError(message)


def _without_version(makefile):
    lines = makefile.splitlines(keepends=True)
    version_lines = [line for line in lines if line.startswith("VERSION ?= ")]
    require(len(version_lines) == 1, "Makefile must contain exactly one VERSION assignment")
    return "".join(line for line in lines if not line.startswith("VERSION ?= "))


def validate_transition(
    *,
    request,
    gate,
    gate_content,
    evidence_reports,
    ci_evidence,
    project_version,
    release_sha,
    parent_sha,
    changed_files,
    candidate_makefile,
    release_makefile,
):
    require(project_version == "0.4.7", "project version must be 0.4.7")
    require(SHA_RE.fullmatch(release_sha or ""), "release SHA is invalid")
    require(SHA_RE.fullmatch(parent_sha or ""), "release parent SHA is invalid")
    require(isinstance(request, dict), "release request must be an object")
    require(
        set(request) == {"schema_version", "version", "candidate_sha", "stable_gate"},
        "release request fields do not match release-request.v2",
    )
    require(request["schema_version"] == "release-request.v2", "release request schema_version must be release-request.v2")
    require(request["version"] == project_version, "release request version does not match project version")
    candidate_sha = request["candidate_sha"]
    require(SHA_RE.fullmatch(candidate_sha or ""), "release request candidate_sha is invalid")
    require(parent_sha == candidate_sha, "release commit parent must equal the gated candidate SHA")

    stable_gate = request["stable_gate"]
    require(isinstance(stable_gate, dict), "release request stable_gate must be an object")
    require(
        set(stable_gate) == {"path", "sha256", "workflow_run_id"},
        "release request stable_gate fields are invalid",
    )
    require(stable_gate["path"] == GATE_PATH, f"stable_gate.path must be {GATE_PATH}")
    require(DIGEST_RE.fullmatch(stable_gate["sha256"] or ""), "stable_gate.sha256 is invalid")
    require(
        hashlib.sha256(gate_content).hexdigest() == stable_gate["sha256"],
        "stable_gate.sha256 does not match the committed Gate report",
    )
    try:
        parsed_gate = json.loads(gate_content)
    except (TypeError, json.JSONDecodeError) as exc:
        raise TransitionError(f"stable Gate report is invalid JSON: {exc}") from exc
    require(parsed_gate == gate, "stable Gate content does not match the parsed report")
    require(gate.get("schema_version") == "computecloud.v0.4.7.stable-gate.v1", "stable Gate schema is unsupported")
    require(gate.get("version") == project_version, "stable Gate version does not match project version")
    require(gate.get("status") == "PASSED", "stable Gate status must be PASSED")
    require(gate.get("release_request_allowed") is True, "stable Gate must allow the release request")
    require(gate.get("candidate_sha") == candidate_sha, "stable Gate candidate does not match the release request")
    gate_ci = gate.get("ci")
    require(isinstance(gate_ci, dict), "stable Gate CI evidence is missing")
    require(gate_ci.get("source_sha") == candidate_sha, "stable Gate CI candidate does not match the release request")
    require(gate_ci.get("conclusion") == "success", "stable Gate CI conclusion must be success")
    require(
        stable_gate["workflow_run_id"] == gate_ci.get("workflow_run_id"),
        "stable_gate.workflow_run_id does not match the Gate report",
    )
    try:
        regenerated_gate = evaluate_gate(candidate_sha, evidence_reports, ci_evidence)
    except (TypeError, ValueError, GateError) as exc:
        raise TransitionError(f"committed stable evidence was rejected: {exc}") from exc
    require(regenerated_gate == gate, "committed evidence does not reproduce the stable Gate report")

    changed = set(changed_files)
    unexpected = sorted(changed - ALLOWED_CHANGED_FILES)
    missing = sorted(REQUIRED_CHANGED_FILES - changed)
    require(not unexpected, f"release transition contains files that are not allowed: {unexpected}")
    require(not missing, f"release transition is missing required files: {missing}")
    require(_without_version(candidate_makefile) == _without_version(release_makefile), "Makefile may change only VERSION")
    require("VERSION ?= 0.4.7\n" in release_makefile, "Makefile VERSION must be 0.4.7")
    return {
        "schema_version": "computecloud.v0.4.7.release-transition.v1",
        "status": "PASSED",
        "version": project_version,
        "candidate_sha": candidate_sha,
        "release_sha": release_sha,
        "gate_sha256": stable_gate["sha256"],
        "workflow_run_id": stable_gate["workflow_run_id"],
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request", type=pathlib.Path, required=True)
    parser.add_argument("--gate", type=pathlib.Path, required=True)
    parser.add_argument("--project-version", required=True)
    parser.add_argument("--release-sha", required=True)
    parser.add_argument("--parent-sha", required=True)
    parser.add_argument("--changed-files", type=pathlib.Path, required=True)
    parser.add_argument("--candidate-makefile", type=pathlib.Path, required=True)
    parser.add_argument("--release-makefile", type=pathlib.Path, required=True)
    args = parser.parse_args(argv)
    try:
        gate_content = args.gate.read_bytes()
        result = validate_transition(
            request=json.loads(args.request.read_text(encoding="utf-8")),
            gate=json.loads(gate_content),
            gate_content=gate_content,
            evidence_reports={name: json.loads(pathlib.Path(path).read_text(encoding="utf-8")) for name, path in EVIDENCE_PATHS.items()},
            ci_evidence=json.loads(pathlib.Path(CI_EVIDENCE_PATH).read_text(encoding="utf-8")),
            project_version=args.project_version,
            release_sha=args.release_sha,
            parent_sha=args.parent_sha,
            changed_files=[line for line in args.changed_files.read_text(encoding="utf-8").splitlines() if line],
            candidate_makefile=args.candidate_makefile.read_text(encoding="utf-8"),
            release_makefile=args.release_makefile.read_text(encoding="utf-8"),
        )
    except (OSError, json.JSONDecodeError, TransitionError) as exc:
        print(f"v0.4.7 release transition rejected: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
