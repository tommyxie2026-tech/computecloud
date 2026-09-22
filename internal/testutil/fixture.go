// Package testutil provides protocol fixtures; it never calls a model provider.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func Repository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s: %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	run("init")
	if e := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test repository\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "README.md")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	return dir, run("rev-parse", "HEAD")
}
func Token(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if e := os.WriteFile(p, []byte(name+"-012345678901234567890123456789"), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func CLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent-fixture")
	body := `#!/bin/sh
if [ "$1" = '--version' ]; then echo fixture-1; exit 0; fi
prompt=$(cat)
if [ "$1" = 'exec' ]; then
  echo '{"type":"thread.started","thread_id":"fixture-session"}'
  echo '{"type":"turn.started"}'
else
  echo '{"type":"system","session_id":"fixture-session"}'
fi
case "$prompt" in
  *slow*) sleep 60 & echo $! > descendant.pid; wait ;;
  *fail*) echo '{"type":"turn.failed"}'; exit 2 ;;
  *missing*) exit 0 ;;
  *) sleep 0.2 ;;
esac
printf 'fixture result\n' > result.txt
if [ "$1" = 'exec' ]; then
  echo '{"type":"item.completed","item":{"type":"agent_message","text":"fixture completed"}}'
  echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
else
  echo '{"type":"result","subtype":"success","is_error":false,"result":"fixture completed","session_id":"fixture-session"}'
fi
`
	if e := os.WriteFile(p, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	return p
}

// JobCLI speaks the native CLI protocols and follows the versioned Job contract.
// Its deterministic findings/patches exercise real processes, Git and artifact RPCs.
func JobCLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "job-fixture")
	body := `#!/usr/bin/python3
import json, pathlib, sys, time, os, urllib.request
if sys.argv[1:] == ['--version']:
    print('fixture-1'); sys.exit(0)
codex = sys.argv[1] == 'exec'
def emit(v): print(json.dumps(v), flush=True)
prompt = sys.stdin.read()
if os.environ.get('COMPUTECLOUD_MODEL_TOKEN'):
    setting = next(v for v in sys.argv if v.startswith('model_providers.computecloud.base_url='))
    base = json.loads(setting.split('=',1)[1])
    model = sys.argv[sys.argv.index('--model')+1]
    req = urllib.request.Request(base+'/responses', data=json.dumps({'model':model,'input':'fixture request'}).encode(), headers={'Content-Type':'application/json','Authorization':'Bearer '+os.environ['COMPUTECLOUD_MODEL_TOKEN']})
    with urllib.request.urlopen(req) as response: response.read()
    print(os.environ['COMPUTECLOUD_MODEL_TOKEN'], file=sys.stderr)
emit({'type':'thread.started' if codex else 'system','thread_id':'fixture','session_id':'fixture'})
if 'slow-job' in prompt: time.sleep(60)
result = 'single fixture completed'
if 'COMPUTECLOUD JOB CONTRACT:' in prompt:
    meta = json.loads(prompt.splitlines()[-1])
    if 'report' in prompt.split('COMPUTECLOUD JOB CONTRACT:')[1].lower() or 'findings' in prompt.split('COMPUTECLOUD JOB CONTRACT:')[1]:
        if meta['stage'] == 'map':
            finding = {'id':'f1','severity':'info','summary':'Fixture evidence','path':'README.md','line_start':1,'line_end':1,'evidence':'test repository'}
            if 'bad-evidence' in prompt: finding['evidence'] = 'invented evidence'
            result = {'schema_version':'findings.v1','base_commit':meta['base_commit'],'partition_key':meta['partition_key'],'findings':[finding]}
        else:
            manifest = json.loads((pathlib.Path(meta['input_directory'])/'manifest.json').read_text())
            findings = []
            for item in manifest['items']:
                source = json.loads((pathlib.Path(meta['input_directory'])/item['partition_key']/'findings.json').read_text())
                for f in source['findings']:
                    f.update(id=item['partition_key']+'-'+f['id'],origin='map',sources=[{'partition_key':item['partition_key'],'finding_id':f['id']}])
                    findings.append(f)
            result = {'schema_version':'merged-report.v1','base_commit':meta['base_commit'],'manifest_sha256':meta['manifest_sha256'],'summary':'Merged fixture review','findings':findings}
        result = json.dumps(result)
    elif meta['stage'] == 'map':
        path = meta['scope_paths'][0]
        if 'out-of-scope' in prompt: path = 'unexpected.txt'
        pathlib.Path(path).parent.mkdir(parents=True, exist_ok=True)
        pathlib.Path(path).write_text('patch '+meta['partition_key']+'\n')
        result = 'patch completed'
    else:
        assert pathlib.Path('a.txt').exists() and pathlib.Path('b.txt').exists()
        if 'mutate-reduce' in prompt: pathlib.Path('a.txt').write_text('untrusted edit\n')
        result = 'merged patches'
if codex:
    emit({'type':'item.completed','item':{'type':'agent_message','text':result}})
    emit({'type':'turn.completed','usage':{'input_tokens':3,'output_tokens':2}})
else:
    emit({'type':'result','subtype':'success','is_error':False,'result':result,'session_id':'fixture'})
`
	if e := os.WriteFile(p, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
