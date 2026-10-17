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
		r.InputTokens = &out.Usage.Input
		r.OutputTokens = &out.Usage.Output
	}
	if out.CostComplete && out.CostUnits != nil {
		cost := float64(*out.CostUnits) / 1e6
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
				required += (len(c.Orders) + 2) * (*repeats)
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
	versionCmd := exec.CommandContext(probeCtx, path, "--version")
	version, versionErr := versionCmd.Output()
	cancel()
	if versionErr != nil {
		return errors.New("CLI version probe failed")
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
	samples := []iuexperiment.Sample{}
runs:
	for repeat := 0; repeat < *repeats; repeat++ {
		for i, c := range selected {
			arms := []string{"monolithic", "iu"}
			if (repeat+i)%2 == 1 {
				arms[0], arms[1] = arms[1], arms[0]
			}
			for _, arm := range arms {
				if runCtx.Err() != nil {
					break runs
				}
				sample := iuexperiment.Run(runCtx, c, arm, *model, call)
				sample.Repetition = repeat + 1
				samples = append(samples, sample)
				fmt.Fprintf(os.Stderr, "%s/%s: correct=%v calls=%d wall_ms=%d error=%s\n", c.ID, arm, sample.Correct, sample.ModelCalls, sample.WallMS, sample.Error)
			}
		}
	}
	report := map[string]any{"version": "iu-local-quality-experiment-v1", "execution": "local_cli_no_server_worker_or_task", "requested_model": *model, "effort": *effort, "runtime": *runtime, "cli_version": strings.TrimSpace(string(version)), "cli_invocation_attempts": attempts, "cases": selected, "samples": samples, "summary": iuexperiment.Summarize(samples), "go_version": goruntime.Version(), "platform": goruntime.GOOS + "/" + goruntime.GOARCH, "root_parallelism": 2, "call_errors": errorsSeen, "limitations": []string{"Small synthetic sample; no statistical or cost-efficiency conclusion.", "Root extraction concurrency is capped at two; arm order alternates.", "Uses real graph context/output/memory contracts; does not measure Task/Worker recovery or distributed overhead.", "Unknown usage and provider cost remain null. No derived price estimates.", "Unchecked provenance records are assembled by harness from extracted outputs; no hidden chain of thought collected."}}
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
