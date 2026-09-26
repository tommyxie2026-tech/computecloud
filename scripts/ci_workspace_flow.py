#!/usr/bin/env python3
import argparse
import json
import pathlib
import subprocess
import sys
import time

def run(cmd):
    started = time.time()
    p = subprocess.run(cmd, text=True, capture_output=True)
    return {
        'command': cmd,
        'returncode': p.returncode,
        'duration_ms': int((time.time() - started) * 1000),
        'stdout': p.stdout,
        'stderr': p.stderr,
    }

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--output', required=True)
    args = ap.parse_args()
    out = pathlib.Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    logs = out.parent / 'logs'
    logs.mkdir(parents=True, exist_ok=True)

    tests = [
        'TestWorkspaceLifecycleOwnershipRetentionAndGC',
        'TestWorkspaceGenerationIsolationAndOwnership',
        'TestWorkspaceRecoveryUsesPreSpawnProofAndQuarantinesUnknown',
        'TestWorkspaceBootstrapAdoptsOnlyKnownLegacyRuns',
        'TestPathRejectsTraversal',
        'TestQuotaCheckAndWatcher',
        'TestWorkerV3WorkspaceLifecycleSchema',
    ]
    pattern = '^(' + '|'.join(tests) + ')$'
    result = run(['go','test','./internal/worker','./internal/workspace','./internal/store','-run',pattern,'-count=1','-v'])
    (logs / 'go-test.stdout.log').write_text(result['stdout'], encoding='utf-8')
    (logs / 'go-test.stderr.log').write_text(result['stderr'], encoding='utf-8')

    report = {
        'schema_version': 'ci-workspace-flow.v1',
        'real_model_calls': False,
        'status': 'PASSED' if result['returncode'] == 0 else 'FAILED',
        'coverage': {
            'attempt_generation_ownership': True,
            'distinct_generation_paths': True,
            'retention_requires_completed_run': True,
            'two_phase_delete_recovery': True,
            'restart_quarantine_for_unknown_spawn': True,
            'legacy_workspace_adoption': True,
            'workspace_path_guard': True,
            'workspace_quota_guard': True,
            'worker_schema_v3_guards': True,
        },
        'tests': tests,
        'command': result['command'],
        'duration_ms': result['duration_ms'],
        'returncode': result['returncode'],
    }
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n', encoding='utf-8')
    print(json.dumps({'status': report['status'], 'report': str(out)}))
    if result['returncode'] != 0:
        sys.stderr.write(result['stdout'])
        sys.stderr.write(result['stderr'])
        raise SystemExit(result['returncode'])

if __name__ == '__main__':
    main()