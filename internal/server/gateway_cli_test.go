package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/testutil"
)

func cliGatewayFixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "cli")
	script := `#!/usr/bin/env python3
import sys,json,time,os
if sys.argv[1:]==['--version']:
 print('bridge-fixture-1');sys.exit(0)
assert '--ignore-user-config' in sys.argv
assert '--ephemeral' in sys.argv
assert 'features.shell_tool=false' in sys.argv
request=json.loads(sys.stdin.read().split('REQUEST JSON:\n',1)[1])
value=request['input']
if value=='slow':
 time.sleep(60)
if value=='fail': sys.exit(1)
output=[{'type':'message','name':'','content':'fixture answer'}]
if value=='tool': output=[{'type':'function_call','name':'lookup','content':'{"city":"Tokyo"}'}]
if value=='custom': output=[{'type':'custom_tool_call','name':'run','content':'print(1)'}]
if isinstance(value,list):
 assert value[-1]['type']=='function_call_output'
 assert value[-1]['output']=='sunny'
 output=[{'type':'message','name':'','content':'Tokyo is sunny'}]
if value=='bad-tool': output=[{'type':'function_call','name':'undeclared','content':'{}'}]
if value=='local-tool': print(json.dumps({'type':'item.started','item':{'type':'command_execution','command':'false'}}));sys.stdout.flush();time.sleep(60)
print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':json.dumps({'output':output})}}))
print(json.dumps({'type':'turn.completed','usage':{'input_tokens':20,'output_tokens':5}}))
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	tokenFile := testutil.Token(t, "cli-model")
	token, _ := config.Token(tokenFile)
	cfg := config.Server{DataDir: filepath.Join(dir, "server"), TLS: config.TLS{InsecureLoopback: true}, LeaseSeconds: 6, TickMS: 20, MaxArtifactBytes: 32 << 20, MaxProjectTasks: 4, Users: []config.Identity{{TokenFile: tokenFile, Owner: "caller", Projects: []string{"p"}, Scopes: []string{"models:invoke"}, ModelProject: "p", ModelRoute: "cli"}}, ModelGateway: config.ModelGateway{Enabled: true, MaxInflight: 1, RequestTimeoutSeconds: 10, Routes: map[string]config.ModelRoute{"cli": {Backend: "codex_cli", CLI: &config.CodexCLI{Executable: exe, Version: "bridge-fixture-1"}, AllowedModels: []string{"model"}, MaxInflight: 1}}}}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, token
}
func cliCall(t *testing.T, s *Server, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.modelHandler.ServeHTTP(w, r)
	return w
}
func TestCLIGatewayTextToolsAndStreaming(t *testing.T) {
	s, token := cliGatewayFixture(t)
	for _, tc := range []struct {
		body, want string
		code       int
	}{
		{`{"model":"model","input":"hello"}`, `fixture answer`, 200},
		{`{"model":"model","input":"tool","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"required"}`, `function_call`, 200},
		{`{"model":"model","input":"custom","tools":[{"type":"custom","name":"run"}]}`, `custom_tool_call`, 200},
		{`{"model":"model","input":[{"type":"function_call_output","call_id":"call_test","output":"sunny"}]}`, `Tokyo is sunny`, 200},
		{`{"model":"model","input":"hello","stream":true}`, `response.output_text.delta`, 200},
		{`{"model":"model","input":"tool","stream":true,"tools":[{"type":"function","name":"lookup"}]}`, `response.function_call_arguments.done`, 200},
		{`{"model":"model","input":"bad-tool"}`, `CLI_UNDECLARED_TOOL_CALL`, 503},
		{`{"model":"model","input":"fail"}`, `CLI_RUNTIME_FAILED`, 503},
		{`{"model":"model","input":"fail","stream":true}`, `response.failed`, 200},
		{`{"model":"model","input":"local-tool"}`, `CLI_PROTOCOL_ERROR`, 503},
		{`{"model":"forbidden","input":"hello"}`, `MODEL_NOT_ALLOWED`, 403},
		{`{"model":"model","input":"hello","max_output_tokens":2}`, `CLI_UNSUPPORTED_MAX_OUTPUT_TOKENS`, 400},
		{`{"model":"model","input":[{"type":"compaction","encrypted_content":"x"}]}`, `CLI_COMPACTION_UNSUPPORTED`, 400},
	} {
		w := cliCall(t, s, token, tc.body)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.code == 200 && !strings.Contains(tc.body, `"fail"`) && !strings.Contains(w.Body.String(), `"total_tokens":25`) {
			t.Fatal("Responses usage requires total_tokens")
		}
		if strings.Contains(tc.body, `"stream":true`) && tc.code == 200 {
			observer := &sseUsage{out: &gatewayOutcome{}}
			if err := observer.feed(w.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
			expected := "COMPLETE"
			if strings.Contains(tc.body, `"fail"`) {
				expected = "FAILED"
			}
			if observer.out.state != expected {
				t.Fatalf("stream not settled: %+v", observer.out)
			}
		}
	}
	w := cliCall(t, s, "wrong", `{"model":"model","input":"hello"}`)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	var count int
	if err := s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE state='COMPLETE' AND usage_complete=1 AND input_tokens=20 AND output_tokens=5").Scan(&count); err != nil || count < 6 {
		t.Fatalf("usage: %d %v", count, err)
	}
	dirs, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "gateway-cli"))
	if err != nil || len(dirs) != 0 {
		t.Fatalf("unclean directories: %v %v", dirs, err)
	}
}
func TestCLIGatewayCancelAndAdmission(t *testing.T) {
	s, token := cliGatewayFixture(t)
	server := httptest.NewServer(s.modelHandler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", bytes.NewBufferString(`{"model":"model","input":"slow","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	if _, err = res.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	w := cliCall(t, s, token, `{"model":"model","input":"hello"}`)
	if w.Code != 429 {
		t.Fatalf("slot not held: %d", w.Code)
	}
	cancel()
	res.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active int
		err = s.db.SQL.QueryRow("SELECT count(*) FROM gateway_requests WHERE state='STARTED'").Scan(&active)
		if err == nil && active == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w = cliCall(t, s, token, `{"model":"model","input":"hello"}`)
	if w.Code != 200 {
		t.Fatalf("slot not released: %d %s", w.Code, w.Body.String())
	}
}
func TestCLIRecoveryStopsRecordedProcess(t *testing.T) {
	s, _ := cliGatewayFixture(t)
	g := s.modelHandler.(*modelGateway)
	dir := filepath.Join(g.cliRoot(), "crashed")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	identity, err := process.Identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveCLIJournal(dir, cliJournal{cmd.Process.Pid, identity}); err != nil {
		t.Fatal(err)
	}
	if err = g.recoverCLI(); err != nil {
		t.Fatal(err)
	}
	if alive, err := process.Alive(cmd.Process.Pid); err != nil || alive {
		t.Fatalf("survivor %v %v", alive, err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = saveCLIJournal(dir, cliJournal{}); err != nil {
		t.Fatal(err)
	}
	if err = g.recoverCLI(); err == nil {
		t.Fatal("unknown spawn must block recovery")
	}
}
func TestCLIDecisionRestrictions(t *testing.T) {
	fields := map[string]json.RawMessage{"tools": json.RawMessage(`[{"type":"function","name":"f"}]`), "tool_choice": json.RawMessage(`"none"`)}
	if _, err := cliOutput(`{"output":[{"type":"function_call","name":"f","content":"{}"}]}`, fields); err == nil {
		t.Fatal("ignored none")
	}
	fields["tool_choice"] = json.RawMessage(`"required"`)
	if _, err := cliOutput(`{"output":[{"type":"message","name":"","content":"answer"}]}`, fields); err == nil {
		t.Fatal("ignored required")
	}
	fields["parallel_tool_calls"] = json.RawMessage(`false`)
	if _, err := cliOutput(`{"output":[{"type":"function_call","name":"f","content":"{}"},{"type":"function_call","name":"f","content":"{}"}]}`, fields); err == nil {
		t.Fatal("ignored parallel false")
	}
}

func TestCLINamespacedDynamicTools(t *testing.T) {
	var f map[string]json.RawMessage
	err := json.Unmarshal([]byte(`{"model":"model","client_metadata":{"session":"x"},"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec","format":{"type":"text"}}]}]},{"role":"user","content":"run"}]}`), &f)
	if err != nil {
		t.Fatal(err)
	}
	if err = normalizeCLIRequest(f); err != nil {
		t.Fatal(err)
	}
	if err = validateModelBody(f, false); err != nil {
		t.Fatal(err)
	}
	out, err := cliOutput(`{"output":[{"type":"custom_tool_call","name":"functions.exec","content":"text(1)"}]}`, f)
	if err != nil || len(out) != 1 || out[0]["namespace"] != "functions" || out[0]["name"] != "exec" {
		t.Fatalf("%v %v", out, err)
	}
	_, err = cliOutput(`{"output":[{"type":"custom_tool_call","name":"other.exec","content":"text(1)"}]}`, f)
	if err == nil {
		t.Fatal("undeclared namespace accepted")
	}
}
