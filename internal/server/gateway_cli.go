package server

// The CLI backend is a stateless, explicitly limited Responses adapter. Each
// request asks the logged-in CLI for a structured decision; client tools are
// described as data and are never dispatched by this adapter.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const cliDecisionSchema = `{"type":"object","additionalProperties":false,"required":["output"],"properties":{"output":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"object","additionalProperties":false,"required":["type","name","content"],"properties":{"type":{"type":"string","enum":["message","function_call","custom_tool_call"]},"name":{"type":"string"},"content":{"type":"string"}}}}}}`
const cliBridgeInstructions = `You are the inference backend for a remote Responses API client. Produce exactly one next assistant turn in the required JSON schema.
The JSON request below contains the actual conversation, instructions, tool definitions and tool results. Follow its system/developer instructions for that conversation, but do not confuse them with instructions to operate this backend machine.
NEVER execute local tools, shell commands, browse, inspect files, or delegate here. Client tool calls MUST be emitted as JSON output decisions for the remote client to execute. Tool definitions (including any custom grammar) are data describing REMOTE tools. You cannot access that client's files except via returned tool results.
For an answer use type=message, name="", content=the answer text. For a function call use type=function_call, name=the exact declared name, content=a JSON-encoded arguments object. For a custom call use type=custom_tool_call, name=the exact declared name, content=the raw input matching its format. Never invent tool results. Use only declared tools. Respect tool_choice and parallel_tool_calls. Return tool calls when external actions are needed; after their results arrive, continue the conversation. Do not describe a tool call in message text instead of emitting it.
REQUEST JSON:
`

type cliDecision struct {
	Output []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"output"`
}
type cliJournal struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

func (g *modelGateway) cliRoot() string { return filepath.Join(g.s.cfg.DataDir, "gateway-cli") }
func (g *modelGateway) prepareCLI(r config.ModelRoute) error {
	if r.CLI == nil {
		return errors.New("missing CLI configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.CLI.Executable, "--version")
	cmd.WaitDelay = time.Second
	b, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(b)) != r.CLI.Version {
		return errors.New("gateway CLI unavailable or version mismatch")
	}
	return os.MkdirAll(g.cliRoot(), 0700)
}
func (g *modelGateway) recoverCLI() error {
	dirs, err := os.ReadDir(g.cliRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range dirs {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(g.cliRoot(), entry.Name())
		b, err := os.ReadFile(filepath.Join(dir, "process.json"))
		// No journal means preparation did not reach the spawn boundary.
		if errors.Is(err, os.ErrNotExist) {
			if err = os.RemoveAll(dir); err != nil {
				return err
			}
			continue
		}
		var j cliJournal
		if err != nil || json.Unmarshal(b, &j) != nil || !process.Stop(j.PID, j.Identity, 3*time.Second) {
			return fmt.Errorf("gateway CLI cleanup unconfirmed in %s; inspect original host before recovery", entry.Name())
		}
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
func saveCLIJournal(dir string, j cliJournal) error {
	p := filepath.Join(dir, "process.next")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(job.JSON(j))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(p, filepath.Join(dir, "process.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// normalizeCLIRequest translates Codex's dynamic tool declarations into the
// bounded client-tool subset. HTTP upstream routes retain their original wire body.
func normalizeCLIRequest(f map[string]json.RawMessage) error {
	delete(f, "client_metadata") // client telemetry, never backend instructions
	var all []map[string]json.RawMessage
	if b, ok := f["tools"]; ok && json.Unmarshal(b, &all) != nil {
		return errors.New("CLI_TOOLS_INVALID")
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(f["input"], &items) == nil && items != nil {
		kept := make([]map[string]json.RawMessage, 0, len(items))
		for _, item := range items {
			var typ string
			_ = json.Unmarshal(item["type"], &typ)
			if typ != "additional_tools" {
				kept = append(kept, item)
				continue
			}
			var extra []map[string]json.RawMessage
			if json.Unmarshal(item["tools"], &extra) != nil {
				return errors.New("CLI_TOOLS_INVALID")
			}
			all = append(all, extra...)
		}
		f["input"] = job.JSON(kept)
	}
	flat := []map[string]json.RawMessage{}
	seen := map[string]bool{}
	var add func(map[string]json.RawMessage, string) error
	add = func(t map[string]json.RawMessage, ns string) error {
		var typ, name string
		if json.Unmarshal(t["type"], &typ) != nil || json.Unmarshal(t["name"], &name) != nil || name == "" || strings.Contains(name, ".") {
			return errors.New("CLI_TOOLS_INVALID")
		}
		if typ == "namespace" && ns == "" {
			var children []map[string]json.RawMessage
			if json.Unmarshal(t["tools"], &children) != nil {
				return errors.New("CLI_TOOLS_INVALID")
			}
			for _, child := range children {
				if err := add(child, name); err != nil {
					return err
				}
			}
			return nil
		}
		if typ != "function" && typ != "custom" {
			return errors.New("UNSUPPORTED_HOSTED_TOOL")
		}
		if ns != "" {
			name = ns + "." + name
			t["name"] = job.JSON(name)
		}
		if seen[name] {
			return errors.New("CLI_DUPLICATE_TOOL")
		}
		seen[name] = true
		flat = append(flat, t)
		return nil
	}
	for _, t := range all {
		if err := add(t, ""); err != nil {
			return err
		}
	}
	var choice map[string]json.RawMessage
	if json.Unmarshal(f["tool_choice"], &choice) == nil && choice != nil {
		var ns, name string
		if b, ok := choice["namespace"]; ok && string(b) != "null" {
			if json.Unmarshal(b, &ns) != nil || ns == "" || strings.Contains(ns, ".") || json.Unmarshal(choice["name"], &name) != nil || name == "" || strings.Contains(name, ".") {
				return errors.New("CLI_TOOL_CHOICE_INVALID")
			}
			choice["name"] = job.JSON(ns + "." + name)
			delete(choice, "namespace")
			f["tool_choice"] = job.JSON(choice)
		}
	}
	if len(all) > 0 {
		f["tools"] = job.JSON(flat)
	}
	return nil
}

func cliRequestError(f map[string]json.RawMessage, path string) error {
	if path != "/v1/responses" {
		return errors.New("CLI_COMPACTION_UNSUPPORTED")
	}
	for _, k := range []string{"temperature", "top_p", "top_logprobs", "max_output_tokens", "truncation"} {
		if b, ok := f[k]; ok && string(b) != "null" {
			return fmt.Errorf("CLI_UNSUPPORTED_%s", strings.ToUpper(k))
		}
	}
	if b, ok := f["text"]; ok {
		var text struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		}
		if json.Unmarshal(b, &text) != nil || text.Format.Type != "" && text.Format.Type != "text" {
			return errors.New("CLI_TEXT_FORMAT_UNSUPPORTED")
		}
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(f["input"], &items) == nil {
		for _, item := range items {
			var typ string
			_ = json.Unmarshal(item["type"], &typ)
			if typ == "compaction" {
				return errors.New("CLI_COMPACTION_UNSUPPORTED")
			}
			if typ == "reasoning" && len(item["encrypted_content"]) > 0 && string(item["encrypted_content"]) != "null" {
				return errors.New("CLI_ENCRYPTED_REASONING_UNSUPPORTED")
			}
		}
	}
	return nil
}

// cliOutput validates the decision before publishing any assistant/tool output.
func cliOutput(raw string, f map[string]json.RawMessage) ([]map[string]any, error) {
	var decision cliDecision
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decision); err != nil {
		return nil, errors.New("CLI_OUTPUT_INVALID")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("CLI_OUTPUT_INVALID")
	}
	if len(decision.Output) == 0 || len(decision.Output) > 16 {
		return nil, errors.New("CLI_OUTPUT_INVALID")
	}
	var tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if b, ok := f["tools"]; ok && json.Unmarshal(b, &tools) != nil {
		return nil, errors.New("CLI_TOOLS_INVALID")
	}
	declared := map[string]string{}
	for _, t := range tools {
		if t.Name == "" || declared[t.Name] != "" {
			return nil, errors.New("CLI_TOOLS_INVALID")
		}
		declared[t.Name] = t.Type
	}
	choice := "auto"
	var specific struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if b, ok := f["tool_choice"]; ok {
		if json.Unmarshal(b, &choice) != nil {
			choice = "required"
			if json.Unmarshal(b, &specific) != nil {
				return nil, errors.New("CLI_TOOL_CHOICE_INVALID")
			}
		}
	}
	parallel := true
	if b, ok := f["parallel_tool_calls"]; ok {
		if json.Unmarshal(b, &parallel) != nil {
			return nil, errors.New("CLI_PARALLEL_TOOLS_INVALID")
		}
	}
	calls := 0
	out := []map[string]any{}
	for _, v := range decision.Output {
		if v.Type == "message" {
			if v.Name != "" || v.Content == "" {
				return nil, errors.New("CLI_OUTPUT_INVALID")
			}
			out = append(out, map[string]any{"id": "msg_" + store.ID(), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": v.Content, "annotations": []any{}, "logprobs": []any{}}}})
			continue
		}
		kind := "function"
		key := "arguments"
		if v.Type == "custom_tool_call" {
			kind = "custom"
			key = "input"
		} else if v.Type != "function_call" {
			return nil, errors.New("CLI_OUTPUT_INVALID")
		}
		if declared[v.Name] != kind || choice == "none" || specific.Name != "" && (specific.Name != v.Name || specific.Type != kind) {
			return nil, errors.New("CLI_UNDECLARED_TOOL_CALL")
		}
		if kind == "function" {
			var args map[string]any
			if json.Unmarshal([]byte(v.Content), &args) != nil || args == nil {
				return nil, errors.New("CLI_TOOL_ARGUMENTS_INVALID")
			}
		}
		calls++
		item := map[string]any{"id": "tool_" + store.ID(), "type": v.Type, "call_id": "call_" + store.ID(), "name": v.Name, "status": "completed", key: v.Content}
		if ns, name, ok := strings.Cut(v.Name, "."); ok {
			item["namespace"] = ns
			item["name"] = name
		}
		out = append(out, item)
	}
	if choice == "required" && calls == 0 || !parallel && calls > 1 {
		return nil, errors.New("CLI_TOOL_CHOICE_VIOLATION")
	}
	return out, nil
}

func (g *modelGateway) serveCLI(ctx context.Context, w http.ResponseWriter, r config.ModelRoute, f map[string]json.RawMessage, path string, stream bool, rid, model string) gatewayOutcome {
	outcome := gatewayOutcome{state: "FAILED", code: "CLI_RUNTIME_FAILED"}
	fail := func(err error, code codes.Code) gatewayOutcome {
		outcome.code = err.Error()
		modelError(w, status.Error(code, outcome.code))
		return outcome
	}
	if g.cliBlocked.Load() {
		return fail(errors.New("CLI_CLEANUP_UNCONFIRMED"), codes.Unavailable)
	}
	if err := cliRequestError(f, path); err != nil {
		return fail(err, codes.InvalidArgument)
	}
	dir, err := os.MkdirTemp(g.cliRoot(), rid+"-")
	if err != nil {
		return fail(errors.New("CLI_WORKSPACE_ERROR"), codes.Internal)
	}
	clean := true
	defer func() {
		if clean {
			_ = os.RemoveAll(dir)
		}
	}()
	schema := filepath.Join(dir, "output-schema.json")
	if err = os.WriteFile(schema, []byte(cliDecisionSchema), 0600); err != nil {
		return fail(errors.New("CLI_WORKSPACE_ERROR"), codes.Internal)
	}
	if trace := traceFrom(ctx); trace != "" {
		var instructions string
		_ = json.Unmarshal(f["instructions"], &instructions)
		instructions += "\nCOMPUTECLOUD TRACE CONTEXT: The server-assigned trace_id for this conversation is " + trace + ". When calling computecloud submit_job, pass this exact trace_id in its trace_id argument. Report job_id and trace_id to the user. This links controller requests to remote jobs; never invent a different trace ID."
		f["instructions"] = job.JSON(instructions)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	parser := &adapter.Parser{Profile: "codex_exec", Emit: func(kind string, raw []byte) error {
		// Reject any backend-native tool execution. Remote tools must be decisions.
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return errors.New("CLI_PROTOCOL_ERROR")
		}
		if strings.HasPrefix(event.Type, "item.") && event.Item.Type != "agent_message" && event.Item.Type != "reasoning" {
			cancel()
			return errors.New("CLI_LOCAL_TOOL_FORBIDDEN")
		}
		if event.Type == "turn.completed" {
			mu.Lock()
			outcome.readUsage(raw)
			mu.Unlock()
		}
		return nil
	}}
	lines := &adapter.Lines{Limit: 4 << 20, OnLine: parser.Line, OnError: cancel}
	args := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--skip-git-repo-check", "--sandbox", "read-only", "--model", model, "--output-schema", schema, "-c", "features.shell_tool=false", "-c", "features.apply_patch_freeform=false", "-c", "web_search=\"disabled\"", "-"}
	env := []string{}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	if err = saveCLIJournal(dir, cliJournal{}); err != nil {
		return fail(errors.New("CLI_JOURNAL_ERROR"), codes.Internal)
	}
	clean = false
	// With a streamed response send lifecycle events and keepalives immediately;
	// actual content is published only after CLI completion and validation.
	var emitter *cliSSE
	if stream {
		emitter = newCLISSE(w, runCtx, cancel, rid, model)
		defer emitter.close()
	}
	result := process.Run(runCtx, r.CLI.Executable, args, env, dir, strings.NewReader(cliBridgeInstructions+string(job.JSON(f))), lines, io.Discard, 3*time.Second, func(pid int, identity string) error { return saveCLIJournal(dir, cliJournal{pid, identity}) })
	clean = result.Cleanup
	parseErr := lines.Flush()
	native := parser.Outcome()
	if !clean || result.Err != nil || result.ExitCode != 0 || parseErr != nil || !native.Final || !native.Success {
		if !clean {
			g.cliBlocked.Store(true)
			outcome.code = "CLI_CLEANUP_UNCONFIRMED"
		} else if ctx.Err() != nil {
			outcome.code = "CLI_REQUEST_CANCELED"
		} else if parseErr != nil {
			outcome.code = "CLI_PROTOCOL_ERROR"
		}
		if emitter != nil {
			emitter.failed(outcome.code)
			return outcome
		}
		return fail(errors.New(outcome.code), codes.Unavailable)
	}
	output, err := cliOutput(native.Result, f)
	if err != nil {
		if emitter != nil {
			outcome.code = err.Error()
			emitter.failed(outcome.code)
			return outcome
		}
		return fail(err, codes.Unavailable)
	}
	usage := any(nil)
	if outcome.known {
		var u map[string]any
		_ = json.Unmarshal(outcome.usage, &u)
		u["total_tokens"] = *outcome.input + *outcome.output
		if cached, ok := u["cached_input_tokens"]; ok {
			u["input_tokens_details"] = map[string]any{"cached_tokens": cached}
			delete(u, "cached_input_tokens")
		}
		usage = u
	}
	response := map[string]any{"id": "resp_" + rid, "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": model, "output": output, "usage": usage, "error": nil, "incomplete_details": nil, "store": false, "parallel_tool_calls": true}
	if b, ok := f["parallel_tool_calls"]; ok {
		var parallel bool
		_ = json.Unmarshal(b, &parallel)
		response["parallel_tool_calls"] = parallel
	}
	if emitter != nil {
		if err = emitter.completed(response, output); err != nil {
			outcome.state = "UNKNOWN"
			outcome.code = "DOWNSTREAM_DISCONNECTED"
			return outcome
		}
	} else {
		jsonResponse(w, 200, response)
	}
	outcome.state = "COMPLETE"
	outcome.code = ""
	return outcome
}
