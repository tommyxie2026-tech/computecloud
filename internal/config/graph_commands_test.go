package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGraphCommandConfigurationLoadsExplicitRuntimeBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `server:
  jobs:
    graphs_enabled: true
    graph_commands:
      orders:
        execution:
          runtime_profile: command_exec
          model: orders
          credential_ref: verifier
          policy_ref: review
          acceptance_profile: check
        parameters_schema: '{"type":"object","additionalProperties":false}'
`
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	p := c.Server.Jobs.GraphCommands["orders"]
	if !c.Server.Jobs.GraphsEnabled || p.Execution.RuntimeProfile != "command_exec" || p.Execution.Model != "orders" || p.Execution.Validate() != nil {
		t.Fatalf("binding %+v", p)
	}
}
