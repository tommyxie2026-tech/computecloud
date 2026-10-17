package adapter

import (
	"context"
	"encoding/json"
	"errors"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

// commandProvider uses the same local execution/inspection/stop and environment
// path as CLI agents. Its executable is operator configuration, never task text.
type commandProvider struct{ codexProvider }

func (commandProvider) Profile() string                                     { return "command_exec" }
func (commandProvider) SupportsGateway() bool                               { return false }
func (commandProvider) Args(*pb.TaskSpec, config.Policy) ([]string, error)  { return nil, nil }
func (p commandProvider) Probe(ctx context.Context, r config.Runtime) error { return probeCLI(ctx, r) }
func (p commandProvider) Capabilities() CapabilitySet {
	return CapabilitySet{Runtime: []string{"event_stream", "cancel", "local_cli"}, Tools: []string{"job_io_v1"}, Environment: []string{"process"}, Legacy: []string{"event_stream", "cancel", "job_io_v1"}}
}
func (p commandProvider) Prepare(r PrepareRequest) (PreparedExecution, error) {
	if r.Gateway != nil || r.Budget != nil || r.Spec == nil || r.Runtime.Executable == "" {
		return PreparedExecution{}, errors.New("invalid command runtime binding")
	}
	return prepareCLI(p, r)
}
func (p commandProvider) Start(ctx context.Context, r PreparedExecution, started func(ExecutionRef) error) StartResult {
	return startLocalCLI(ctx, p, r, started)
}
func (commandProvider) Parser(emit func(string, []byte) error) StreamParser {
	return &commandParser{emit: emit}
}

type commandParser struct {
	out  Outcome
	emit func(string, []byte) error
}

func (p *commandParser) Line(line []byte) error {
	if p.out.Final || len(line) > 64<<10 {
		return errors.New("command must return one bounded JSON result")
	}
	var out struct {
		Version string `json:"version"`
	}
	if e := json.Unmarshal(line, &out); e != nil {
		return e
	}
	if out.Version != "iu-node-output-v1" {
		return errors.New("command output version")
	}
	p.out = Outcome{Final: true, Success: true, Result: string(line)}
	if p.emit != nil {
		return p.emit("runtime.output", line)
	}
	return nil
}
func (p *commandParser) Outcome() Outcome { return p.out }
func init() {
	if e := Register(commandProvider{}); e != nil {
		panic(e)
	}
}
