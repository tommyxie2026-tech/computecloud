package graph

import "testing"

func TestRuleChangeInvalidatesDependentNodes(t *testing.T) {
	b := ReplayBinding{Version: "1", RuntimeProfile: "codex", RuntimeVersion: "pinned", Model: "frozen", EnvironmentHash: Hash([]byte("env")), RuntimeConfigHash: Hash([]byte("runtime config")), PolicyHash: Hash([]byte("policy")), ReadOnly: true}
	base := ReuseKey{NodeHash: Hash([]byte("node")), ContextHash: Hash([]byte("input")), ReadViewHash: EmptyView().SHA256, OperatorHash: Hash([]byte("operator")), SchemaHash: Hash([]byte("schema")), TemplateHash: Hash([]byte("template")), WorkspaceHash: Hash([]byte("workspace")), Binding: b}
	for _, field := range []string{"context", "model", "tool", "policy", "template", "workspace", "view"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			switch field {
			case "context":
				changed.ContextHash = Hash([]byte("discount=2000"))
			case "model":
				changed.Binding.Model = "other"
			case "tool":
				changed.Binding.EnvironmentHash = Hash([]byte("new tool"))
			case "policy":
				changed.Binding.PolicyHash = Hash([]byte("new policy"))
			case "template":
				changed.TemplateHash = Hash([]byte("new template"))
			case "workspace":
				changed.WorkspaceHash = Hash([]byte("new workspace"))
			case "view":
				changed.ReadViewHash = NewView(nil).SHA256 + "invalid"
			}
			if DecideReuse(base, changed, true).Decision != "RECOMPUTE" {
				t.Fatal("changed dependency reused")
			}
		})
	}
	if DecideReuse(base, base, true).Decision != "REUSE" {
		t.Fatal("unchanged not reused")
	}
	if DecideReuse(base, base, false).Decision != "RECOMPUTE" {
		t.Fatal("invalid evidence reused")
	}
}

func TestReuseV3ContractKeepsEarlierVersionsFrozen(t *testing.T) {
	s := fixture()
	s.Version = ReuseVersion
	s.Nodes[0].ReplayProfile = "frozen"
	s.Reuse = []ReuseBinding{{GrantID: "reuse_a", NodeID: s.Nodes[0].ID}}
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Decode(canonical(p.Spec)); e != nil {
		t.Fatal(e)
	}
	s.Version = ImportVersion
	if _, e = Compile(s); e == nil {
		t.Fatal("v2 accepted replay fields")
	}
	s.Version = ReuseVersion
	s.Imports = []ImportBinding{{GrantID: "reuse_a", NodeIDs: []string{s.Nodes[0].ID}}}
	if _, e = Compile(s); e == nil {
		t.Fatal("grant consumed twice")
	}
}
