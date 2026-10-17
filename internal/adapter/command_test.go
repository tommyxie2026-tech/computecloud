package adapter

import (
	"testing"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
)

func TestCommandProviderHasFixedExecutableAndNoModelShellArguments(t *testing.T) {
	p, ok := Lookup("command_exec")
	if !ok {
		t.Fatal("command provider unavailable")
	}
	prepared, e := p.Prepare(PrepareRequest{AttemptID: "attempt", Generation: 1, Runtime: config.Runtime{Executable: "/trusted/check-orders", Version: "1"}, Spec: &pb.TaskSpec{Model: "orders", Input: &pb.Input{Text: `{"parameters":{"text":"$(touch /tmp/should-not-exist)"}}`}}, Input: `{"parameters":{"text":"$(touch /tmp/should-not-exist)"}}`})
	if e != nil {
		t.Fatal(e)
	}
	if prepared.Runtime.Executable != "/trusted/check-orders" || len(prepared.Args) != 0 || prepared.Profile != "command_exec" {
		t.Fatalf("unsafe command %+v", prepared)
	}
	parser := p.Parser(nil)
	if e = parser.Line([]byte(`{"version":"iu-node-output-v1","values":{"result":"FAIL"},"records":[]}`)); e != nil {
		t.Fatal(e)
	}
	out := parser.Outcome()
	if !out.Final || !out.Success {
		t.Fatalf("business failure became execution failure %+v", out)
	}
	if _, e = p.Prepare(PrepareRequest{Spec: &pb.TaskSpec{}, Gateway: &pb.GatewayAccess{}, Runtime: config.Runtime{Executable: "x"}}); e == nil {
		t.Fatal("gateway accepted")
	}
}
