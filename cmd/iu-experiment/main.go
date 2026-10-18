// iu-experiment measures fixed orders quality through local CLI calls and the
// real graph contracts. It is intentionally not a distributed execution engine.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/adapter"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/iuexperiment"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"github.com/tommyxie2026-tech/computecloud/internal/process"
)

func parseCLI(profile string, raw []byte) (iuexperiment.Reply, error) {
	p, e := adapter.NewParser(profile, nil)
	if e != nil {
		return iuexperiment.Reply{}, e
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if profile == "codex_exec" {
			var event struct {
				Type string
				Item struct{ Type string }
			}
			if e = json.Unmarshal(line, &event); e != nil {
				return iuexperiment.Reply{}, e
			}
			if event.Type == "item.completed" && event.Item.Type != "agent_message" && event.Item.Type != "reasoning" {
				return iuexperiment.Reply{}, errors.New("unexpected tool use")
			}
		}
		if e = p.Line(line); e != nil {
			return iuexperiment.Reply{}, e
		}
	}
	if e = scanner.Err(); e != nil {
		return iuexperiment.Reply{}, e
	}
	out := p.Outcome()
	if !out.Final || !out.Success {
		return iuexperiment.Reply{}, errors.New("no successful final output")
	}
	r := iuexperiment.Reply{Raw: json.RawMessage(out.Result)}
	if out.UsageComplete && out.Usage != nil {
		r.UsageOrigin = "agent_reported"
		r.InputTokens = &out.Usage.Input
		r.OutputTokens = &out.Usage.Output
	}
	if out.CostComplete && out.CostUnits != nil {
		cost := float64(*out.CostUnits) / 1e6
		r.CostOrigin = "agent_reported"
		r.CostUSD = &cost
	}
	return r, nil
}

func classifyFailure(raw []byte) string {
	text := string(raw)
	if strings.Contains(text, "in-process app-server client") && strings.Contains(text, "Operation not permitted") {
		return "CLI_IPC_PERMISSION_DENIED"
	}
	if strings.Contains(text, "Request too large") {
		return "CLI_REQUEST_TOO_LARGE"
	}
	return "CLI_OR_PROTOCOL_FAILED"
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, errors.New("CLI output limit")
	}
	return b.Buffer.Write(p)
}

func runCLI(ctx context.Context, path string, args []string, dir string, input io.Reader) ([]byte, []byte, process.Result) {
	var stdout, stderr cappedBuffer
	result := process.Run(ctx, path, args, nil, dir, input, &stdout, &stderr, 200*time.Millisecond, nil)
	return stdout.Bytes(), stderr.Bytes(), result
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	coveragePlan := flag.Bool("coverage-plan", false, "write frozen coverage protocol; with --simulate run 72 fixture arms")
	coveragePath := flag.String("coverage-protocol", "", "execute frozen semantic coverage protocol")
	pilotPlan := flag.Bool("pilot-plan", false, "write a frozen 20-sample workflow pilot protocol without model calls")
	protocolPath := flag.String("protocol", "", "execute a frozen pilot protocol")
	expectedCLI := flag.String("expected-cli-version", "", "pinned CLI version required for pilot plan")
	armsFlag := flag.String("arms", "monolithic,iu", "monolithic,iu,iu_no_memory,iu_no_verifier")
	simulate := flag.Bool("simulate", false, "deterministic CI simulation; no model credentials or calls")
	runtime := flag.String("runtime", "codex_exec", "codex_exec or claude_print")
	executable := flag.String("executable", "", "CLI executable, defaults to codex/claude")
	model := flag.String("model", "", "required exact configured model identifier")
	effort := flag.String("effort", "low", "fixed effort: low, medium or high")
	casesFlag := flag.String("cases", "cross_threshold,rule_revision", "comma-separated synthetic cases")
	repeats := flag.Int("repeats", 1, "paired repetitions (alternating arm order)")
	maxCalls := flag.Int("max-model-calls", 8, "hard preflight cap on total attempted CLI calls")
	timeout := flag.Duration("timeout", 60*time.Second, "timeout per CLI call")
	output := flag.String("output", "", "new report path; never overwrites")
	flag.Parse()
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	var coverageProtocol *iuexperiment.CoverageProtocol
	if (*coveragePlan || *coveragePath != "") && (*pilotPlan || *protocolPath != "") {
		return errors.New("coverage and pilot modes are mutually exclusive")
	}
	if *coveragePlan {
		if *coveragePath != "" || *output == "" {
			return errors.New("coverage plan requires output and no live protocol")
		}
		if *simulate {
			return writeNewReport(*output, iuexperiment.SimulateCoverage(runCtx))
		}
		p, e := iuexperiment.NewCoverageProtocol(*model, *runtime, *expectedCLI, *effort)
		if e != nil {
			return e
		}
		return writeNewReport(*output, p)
	}
	if *coveragePath != "" {
		if *simulate {
			return errors.New("use --coverage-plan --simulate for fixtures")
		}
		raw, e := os.ReadFile(*coveragePath)
		if e != nil {
			return e
		}
		var p iuexperiment.CoverageProtocol
		if e = jsonutil.Decode(raw, &p); e != nil {
			return e
		}
		if e = p.Validate(); e != nil {
			return e
		}
		if explicit["model"] && *model != p.Model || explicit["runtime"] && *runtime != p.Runtime || explicit["effort"] && *effort != p.Effort || explicit["max-model-calls"] && *maxCalls != p.MaxCalls || explicit["timeout"] && *timeout != time.Duration(p.CallTimeoutSeconds)*time.Second || explicit["arms"] || explicit["cases"] || explicit["repeats"] {
			return errors.New("COVERAGE_PROTOCOL_OVERRIDE_REJECTED")
		}
		*model = p.Model
		*runtime = p.Runtime
		*effort = p.Effort
		*maxCalls = p.MaxCalls
		*timeout = time.Duration(p.CallTimeoutSeconds) * time.Second
		coverageProtocol = &p
	}
	var protocol *iuexperiment.PilotProtocol
	if *simulate && (*pilotPlan || *protocolPath != "") {
		return errors.New("simulation and live protocol modes are mutually exclusive")
	}
	if *pilotPlan {
		if *output == "" {
			return errors.New("output required")
		}
		p, e := iuexperiment.NewPilotProtocol(*model, *runtime, *expectedCLI, *effort)
		if e != nil {
			return e
		}
		return writeNewReport(*output, p)
	}
	if *protocolPath != "" {
		raw, e := os.ReadFile(*protocolPath)
		if e != nil {
			return e
		}
		var p iuexperiment.PilotProtocol
		if e = jsonutil.Decode(raw, &p); e != nil {
			return e
		}
		if e = p.Validate(); e != nil {
			return e
		}
		if explicit["model"] && *model != p.Model || explicit["runtime"] && *runtime != p.Runtime || explicit["effort"] && *effort != p.Effort || explicit["max-model-calls"] && *maxCalls != p.MaxCalls || explicit["timeout"] && *timeout != time.Duration(p.CallTimeoutSeconds)*time.Second || explicit["repeats"] && *repeats != 5 || explicit["cases"] || explicit["arms"] {
			return errors.New("PILOT_PROTOCOL_OVERRIDE_REJECTED")
		}
		*model = p.Model
		*runtime = p.Runtime
		*effort = p.Effort
		*maxCalls = p.MaxCalls
		*timeout = time.Duration(p.CallTimeoutSeconds) * time.Second
		*repeats = 5
		*casesFlag = "cross_threshold,rule_revision,separate_customers,duplicate_order"
		*armsFlag = strings.Join(p.Arms, ",")
		protocol = &p
		deadlineCtx, cancelDeadline := context.WithTimeout(runCtx, time.Duration(p.OverallTimeoutSeconds)*time.Second)
		defer cancelDeadline()
		runCtx = deadlineCtx
	}
	arms := strings.Split(*armsFlag, ",")
	seenArms := map[string]bool{}
	for _, arm := range arms {
		if arm != "monolithic" && arm != "iu" && arm != "iu_no_memory" && arm != "iu_no_verifier" || seenArms[arm] {
			return errors.New("invalid or repeated arm")
		}
		seenArms[arm] = true
	}
	if *simulate {
		if *output == "" {
			return errors.New("output required")
		}
		if e := os.MkdirAll(filepath.Dir(*output), 0700); e != nil {
			return e
		}
		f, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		report := iuexperiment.Simulate(runCtx)
		encoder := json.NewEncoder(f)
		encoder.SetIndent("", "  ")
		if e = encoder.Encode(report); e != nil {
			return e
		}
		if !report.Passed {
			return errors.New("simulation checks failed")
		}
		fmt.Fprintf(os.Stderr, "simulation: %d checks passed; real_model_calls=false\n", len(report.Scenarios))
		return nil
	}

	if *model == "" || *output == "" || *repeats < 1 || *repeats > 20 || *timeout <= 0 || *maxCalls < 1 {
		return errors.New("model, new output path, positive repeats/timeout/call cap required")
	}
	if *effort != "low" && *effort != "medium" && *effort != "high" {
		return errors.New("invalid effort")
	}
	if *runtime != "codex_exec" && *runtime != "claude_print" {
		return errors.New("unsupported runtime")
	}
	if *executable == "" {
		if *runtime == "codex_exec" {
			*executable = "codex"
		} else {
			*executable = "claude"
		}
	}
	path, e := exec.LookPath(*executable)
	if e != nil {
		return e
	}
	selected := []iuexperiment.Case{}
	seen := map[string]bool{}
	required := 0
	for _, id := range strings.Split(*casesFlag, ",") {
		if seen[id] {
			return errors.New("duplicate case")
		}
		seen[id] = true
		found := false
		for _, c := range iuexperiment.Cases() {
			if c.ID == id {
				selected = append(selected, c)
				for _, arm := range arms {
					calls := len(c.Orders) + 1
					if arm == "monolithic" {
						calls = 1
					}
					required += calls * (*repeats)
				}
				found = true
				break
			}
		}
		if !found {
			return errors.New("unknown case: " + id)
		}
	}
	if required > *maxCalls {
		return fmt.Errorf("requires %d model calls, cap is %d", required, *maxCalls)
	}
	if e = os.MkdirAll(filepath.Dir(*output), 0700); e != nil {
		return e
	}
	// Reserve the report before spending inference calls.
	file, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer file.Close()
	root, e := os.MkdirTemp("", "iu-experiment-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	probeCtx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	version, _, probeResult := runCLI(probeCtx, path, []string{"--version"}, root, nil)
	versionErr := probeResult.Err
	if !probeResult.Cleanup {
		versionErr = errors.New("CLI_CLEANUP_UNCONFIRMED")
	}
	cancel()
	if versionErr != nil {
		_ = json.NewEncoder(file).Encode(map[string]any{"version": "iu-preflight-v1", "status": "FAILED", "stage": "cli_version_probe", "model_calls": 0, "protocol": protocol, "error_code": "CLI_VERSION_PROBE_FAILED"})
		return errors.New("CLI version probe failed")
	}
	if protocol != nil && strings.TrimSpace(string(version)) != protocol.CLIVersion {
		_ = json.NewEncoder(file).Encode(map[string]any{"version": "iu-preflight-v1", "status": "FAILED", "stage": "cli_version_probe", "model_calls": 0, "protocol": protocol, "error_code": "CLI_VERSION_DRIFT"})
		return errors.New("CLI_VERSION_DRIFT")
	}
	if coverageProtocol != nil && strings.TrimSpace(string(version)) != coverageProtocol.CLIVersion {
		_ = json.NewEncoder(file).Encode(map[string]any{"version": "iu-preflight-v1", "status": "FAILED", "error_code": "CLI_VERSION_DRIFT", "coverage_protocol": coverageProtocol})
		return errors.New("CLI_VERSION_DRIFT")
	}
	var mu sync.Mutex
	attempts := 0
	errorsSeen := []string{}
	call := func(ctx context.Context, m graph.ContextManifest) (iuexperiment.Reply, error) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n > *maxCalls {
			return iuexperiment.Reply{}, errors.New("model call cap")
		}
		callCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		args := []string{}
		if *runtime == "codex_exec" {
			args = []string{"exec", "--ephemeral", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ignore-rules", "-C", root, "-m", *model, "-c", "model_reasoning_effort=" + *effort, "-"}
		} else {
			args = []string{"-p", "--output-format", "json", "--tools", "", "--no-session-persistence", "--safe-mode", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--model", *model, "--effort", *effort}
		}
		stdout, stderr, result := runCLI(callCtx, path, args, root, strings.NewReader(m.Text))
		err := result.Err
		if !result.Cleanup {
			err = errors.New("CLI_CLEANUP_UNCONFIRMED")
		}
		var reply iuexperiment.Reply
		if err == nil {
			reply, err = parseCLI(*runtime, stdout)
		}
		if err != nil {
			code := classifyFailure(append(append([]byte{}, stdout...), stderr...))
			if callCtx.Err() != nil {
				code = "CLI_TIMEOUT"
			}
			if !result.Cleanup {
				code = "CLI_CLEANUP_UNCONFIRMED"
			}
			mu.Lock()
			errorsSeen = append(errorsSeen, code)
			mu.Unlock()
			return iuexperiment.Reply{}, errors.New(code)
		}
		return reply, nil
	}
	if coverageProtocol != nil {
		report, e := iuexperiment.RunCoverageProtocol(runCtx, *coverageProtocol, call)
		if e != nil {
			return e
		}
		return json.NewEncoder(file).Encode(report)
	}
	samples := []iuexperiment.Sample{}
	for repeat := 0; repeat < *repeats; repeat++ {
		for i, c := range selected {
			orderedArms := append([]string{}, arms...)
			rotation := (repeat + i) % len(orderedArms)
			orderedArms = append(orderedArms[rotation:], orderedArms[:rotation]...)
			for _, arm := range orderedArms {
				if runCtx.Err() != nil {
					samples = append(samples, iuexperiment.Sample{UsageOrigin: "unknown", CostOrigin: "unknown", CaseID: c.ID, Arm: arm, Repetition: repeat + 1, Error: "NOT_ATTEMPTED_CANCELLED", FailureClass: "not_attempted"})
					continue
				}
				sample := iuexperiment.Run(runCtx, c, arm, *model, call)
				sample.Repetition = repeat + 1
				samples = append(samples, sample)
				fmt.Fprintf(os.Stderr, "%s/%s: correct=%v calls=%d wall_ms=%d error=%s\n", c.ID, arm, sample.Correct, sample.ModelCalls, sample.WallMS, sample.Error)
			}
		}
	}
	report := map[string]any{"version": "iu-local-quality-experiment-v1", "execution": "local_cli_no_server_worker_or_task", "requested_model": *model, "effort": *effort, "runtime": *runtime, "cli_version": strings.TrimSpace(string(version)), "cli_invocation_attempts": attempts, "cases": selected, "samples": samples, "summary": iuexperiment.Summarize(samples), "quality_metrics": iuexperiment.DetailedMetrics(samples), "protocol": protocol, "provider_model_revision_verified": false, "go_version": goruntime.Version(), "platform": goruntime.GOOS + "/" + goruntime.GOARCH, "root_parallelism": 2, "call_errors": errorsSeen, "limitations": []string{"Small synthetic sample; no statistical or cost-efficiency conclusion.", "Root extraction concurrency is capped at two; arm order alternates.", "Uses real graph context/output/memory contracts; does not measure Task/Worker recovery or distributed overhead.", "Unknown usage and provider cost remain null. No derived price estimates.", "Unchecked provenance records are assembled by harness from extracted outputs; no hidden chain of thought collected."}}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if e = encoder.Encode(report); e != nil {
		return e
	}
	for _, sample := range samples {
		if sample.Error != "" {
			return errors.New("experiment contains execution failures; inspect saved report")
		}
	}
	return nil
}

func writeNewReport(path string, value any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
