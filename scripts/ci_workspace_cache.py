#!/usr/bin/env python3
"""Cache correctness and explicitly scoped provider performance evidence."""
import argparse
import json
import os
import pathlib
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--output', required=True)
parser.add_argument('--benchmark', action='store_true')
args = parser.parse_args()
path = pathlib.Path(args.output)
path.parent.mkdir(parents=True, exist_ok=True)
env = dict(os.environ)
if args.benchmark:
    env['COMPUTECLOUD_CACHE_BENCHMARK'] = '1'
command = ['go', 'test', './internal/workspace', './internal/worker', '-race', '-count=1', '-v', '-run', 'TestCache|TestPreparedWorkspace']
run = subprocess.run(command, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
path.with_suffix('.log').write_text(run.stdout)
reports = []
for line in run.stdout.splitlines():
    if 'CACHE_BENCHMARK ' in line:
        reports.append(json.loads(line.split('CACHE_BENCHMARK ', 1)[1]))
passed = run.returncode == 0 and (not args.benchmark or len(reports) == 1)
result = {'status': 'PASSED' if passed else 'FAILED', 'real_model_calls': False, 'benchmark': reports, 'command': command}
path.write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(result))
if not passed:
    print(run.stdout)
    raise SystemExit(1)
