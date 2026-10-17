// Package graph defines frozen, bounded IU plans. It does not execute tasks.
package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	graphv1 "github.com/tommyxie2026-tech/computecloud/api/graph/v1"
	graphv2 "github.com/tommyxie2026-tech/computecloud/api/graph/v2"
	graphv3 "github.com/tommyxie2026-tech/computecloud/api/graph/v3"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

const Version = "iu-graph-v1"
const ImportVersion = "iu-graph-v2"
const ReuseVersion = "iu-graph-v3"

type ReuseBinding struct {
	GrantID string `json:"grant_id"`
	NodeID  string `json:"node_id"`
}

type ImportBinding struct {
	GrantID string   `json:"grant_id"`
	NodeIDs []string `json:"node_ids"`
}

const MaxRequestBytes = 256 << 10

type Source struct {
	Role         string `json:"role,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
	ID           string `json:"id"`
	Text         string `json:"text"`
	SHA256       string `json:"sha256,omitempty"`
}

type Span struct {
	Source string `json:"source"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	SHA256 string `json:"sha256"`
}

type Port struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
}

type Operator struct {
	ID             string         `json:"id"`
	Version        string         `json:"version"`
	Kind           string         `json:"kind"`
	Inputs         []Port         `json:"inputs,omitempty"`
	Outputs        []Port         `json:"outputs"`
	Execution      *job.Execution `json:"execution,omitempty"`
	CommandProfile string         `json:"command_profile,omitempty"`
}

type Node struct {
	Rules         []Span          `json:"rules,omitempty"`
	ReplayProfile string          `json:"replay_profile,omitempty"`
	ID            string          `json:"id"`
	Operator      string          `json:"operator"`
	Instructions  string          `json:"instructions,omitempty"`
	Spans         []Span          `json:"spans,omitempty"`
	ScopePaths    []string        `json:"scope_paths,omitempty"`
	JoinPolicy    string          `json:"join_policy,omitempty"`
	Parameters    json.RawMessage `json:"parameters,omitempty"`
}

type Edge struct {
	FromNode string `json:"from_node"`
	FromPort string `json:"from_port,omitempty"`
	ToNode   string `json:"to_node"`
	ToPort   string `json:"to_port,omitempty"`
	Type     string `json:"type"`
	When     string `json:"when,omitempty"`
}

type Output struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

type Limits struct {
	Parallelism        int    `json:"parallelism"`
	TimeoutSeconds     int64  `json:"timeout_seconds"`
	MaxAttemptsPerTask int    `json:"max_attempts_per_task"`
	MaxReplans         int    `json:"max_replans,omitempty"`
	MaxCostUnits       *int64 `json:"max_cost_units,omitempty"`
	MaxTokens          *int64 `json:"max_tokens,omitempty"`
}

type Spec struct {
	Reuse     []ReuseBinding             `json:"reuse,omitempty"`
	Imports   []ImportBinding            `json:"imports,omitempty"`
	Schemas   map[string]json.RawMessage `json:"schemas"`
	Evaluator Evaluator                  `json:"evaluator"`
	Version   string                     `json:"version"`
	ProjectID string                     `json:"project_id"`
	Workspace job.Workspace              `json:"workspace"`
	Sources   []Source                   `json:"sources"`
	Rules     []Span                     `json:"rules,omitempty"`
	Operators []Operator                 `json:"operators"`
	Nodes     []Node                     `json:"nodes"`
	Edges     []Edge                     `json:"edges"`
	Outputs   []Output                   `json:"outputs"`
	Limits    Limits                     `json:"limits"`
}

type Evaluator struct {
	Profile string `json:"profile"`
	Version string `json:"version"`
}

type Plan struct {
	Spec                 Spec     `json:"spec"`
	Digest               string   `json:"sha256"`
	Order                []string `json:"order"`
	RequestedParallelism int      `json:"requested_parallelism"`
	EffectiveParallelism int      `json:"effective_parallelism"`
	BudgetMode           string   `json:"budget_execution_mode"`
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func canonical(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func invalid(reason string) error { return fmt.Errorf("GRAPH_INVALID: %s", reason) }

// Decode rejects unknown fields, duplicate object keys, trailing JSON and oversize requests.
func Decode(raw []byte) (Spec, error) {
	var s Spec
	if len(raw) > MaxRequestBytes {
		return s, invalid("request limit")
	}
	if e := jsonutil.Decode(raw, &s); e != nil {
		return s, e
	}
	var instance any
	if e := json.Unmarshal(raw, &instance); e != nil {
		return s, e
	}
	schema := requestSchema()
	if s.Version == ImportVersion {
		schema = requestSchemaV2()
	}
	if s.Version == ReuseVersion {
		schema = requestSchemaV3()
	}
	if e := schema.Validate(instance); e != nil {
		return s, invalid("schema: " + e.Error())
	}
	p, e := Compile(s)
	if e != nil {
		return s, e
	}
	return p.Spec, nil
}

var requestSchema = sync.OnceValue(func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if e := json.Unmarshal(graphv1.Schema, &s); e != nil {
		panic(e)
	}
	r, e := s.Resolve(nil)
	if e != nil {
		panic(e)
	}
	return r
})

var requestSchemaV2 = sync.OnceValue(func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if e := json.Unmarshal(graphv2.Schema, &s); e != nil {
		panic(e)
	}
	r, e := s.Resolve(nil)
	if e != nil {
		panic(e)
	}
	return r
})

var requestSchemaV3 = sync.OnceValue(func() *jsonschema.Resolved {
	var s jsonschema.Schema
	if e := json.Unmarshal(graphv3.Schema, &s); e != nil {
		panic(e)
	}
	r, e := s.Resolve(nil)
	if e != nil {
		panic(e)
	}
	return r
})

// Compile clones and normalizes its input, preserving source text byte-for-byte.
// Sorted declarations and Kahn traversal make the plan independent of array order.
func Compile(in Spec) (Plan, error) {
	var p Plan
	raw, e := json.Marshal(in)
	if e != nil {
		return p, e
	}
	if len(raw) > MaxRequestBytes {
		return p, invalid("request limit")
	}
	var s Spec
	if e = json.Unmarshal(raw, &s); e != nil {
		return p, e
	}
	fail := func(reason string) (Plan, error) { return Plan{}, invalid(reason) }
	if (s.Version != Version && s.Version != ImportVersion && s.Version != ReuseVersion) || !job.Ref(s.ProjectID) || !job.Ref(s.Workspace.RepositoryRef) || !commitPattern.MatchString(s.Workspace.BaseCommit) {
		return fail("version, project or workspace")
	}
	if !job.Ref(s.Evaluator.Profile) || !job.Ref(s.Evaluator.Version) {
		return fail("frozen evaluator required")
	}
	l := s.Limits
	if l.Parallelism < 1 || l.Parallelism > 8 || l.TimeoutSeconds < 1 || l.TimeoutSeconds > 86400 || l.MaxAttemptsPerTask < 1 || l.MaxAttemptsPerTask > 3 || l.MaxReplans < 0 || l.MaxReplans > 8 {
		return fail("limits")
	}
	if l.MaxTokens != nil || l.MaxCostUnits != nil && *l.MaxCostUnits < 1 {
		return Plan{}, fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
	}
	if len(s.Nodes) < 1 || len(s.Nodes) > 32 || len(s.Edges) > 128 || len(s.Operators) < 1 || len(s.Operators) > 32 || len(s.Sources) > 128 || len(s.Rules) > 128 || len(s.Outputs) < 1 || len(s.Outputs) > 32 {
		return fail("graph size")
	}
	sources := map[string]Source{}
	for i := range s.Sources {
		src := &s.Sources[i]
		if src.MessageIndex < 0 || src.Role != "" && src.Role != "system" && src.Role != "developer" && src.Role != "user" && src.Role != "assistant" && src.Role != "tool" {
			return fail("source message identity")
		}
		if !keyPattern.MatchString(src.ID) || !utf8.ValidString(src.Text) {
			return fail("source")
		}
		if _, ok := sources[src.ID]; ok {
			return fail("duplicate source")
		}
		h := Hash([]byte(src.Text))
		if src.SHA256 != "" && src.SHA256 != h {
			return fail("source hash")
		}
		src.SHA256 = h
		sources[src.ID] = *src
	}
	for _, span := range s.Rules {
		if _, e := readSpan(sources, span); e != nil {
			return p, e
		}
	}
	ops := map[string]Operator{}
	if len(s.Schemas) < 1 || len(s.Schemas) > 64 {
		return fail("frozen schemas required")
	}
	for name, definition := range s.Schemas {
		if !job.Ref(name) || len(definition) > 64<<10 {
			return fail("port schema bounds")
		}
		if _, e := resolvePortSchema(definition); e != nil {
			return fail("port schema: " + e.Error())
		}
	}
	for _, op := range s.Operators {
		if !keyPattern.MatchString(op.ID) || !job.Ref(op.Version) {
			return fail("operator identity")
		}
		if _, ok := ops[op.ID]; ok {
			return fail("duplicate operator")
		}
		if len(op.Inputs) > 32 || len(op.Outputs) < 1 || len(op.Outputs) > 32 {
			return fail("operator ports")
		}
		for _, ports := range [][]Port{op.Inputs, op.Outputs} {
			seen := map[string]bool{}
			for _, port := range ports {
				if _, ok := s.Schemas[port.Schema]; !ok {
					return fail("port schema undefined")
				}
				if !keyPattern.MatchString(port.Name) || !job.Ref(port.Schema) || seen[port.Name] {
					return fail("invalid or duplicate port")
				}
				seen[port.Name] = true
			}
		}
		switch op.Kind {
		case "agent":
			if op.Execution == nil || op.CommandProfile != "" {
				return fail("agent execution")
			}
			if e := op.Execution.Validate(); e != nil {
				return p, e
			}
			if l.MaxAttemptsPerTask > 1 && !op.Execution.ReplaySafe {
				return fail("retry requires replay_safe")
			}
			if l.MaxCostUnits != nil && op.Execution.RuntimeProfile != "claude_http" {
				return Plan{}, fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
			}
		case "command":
			if !keyPattern.MatchString(op.CommandProfile) || op.Execution != nil {
				return fail("command profile")
			}
			if l.MaxCostUnits != nil {
				return Plan{}, fmt.Errorf("GOAL_RUNTIME_BUDGET_UNSUPPORTED")
			}
			if l.MaxAttemptsPerTask > 1 {
				return fail("command retries require separate replay contract")
			}
		case "join":
			if op.Execution != nil || op.CommandProfile != "" {
				return fail("join cannot execute")
			}
		default:
			return fail("operator kind")
		}
		ops[op.ID] = op
	}
	nodes := map[string]Node{}
	indegree := map[string]int{}
	next := map[string][]string{}
	for _, n := range s.Nodes {
		if !keyPattern.MatchString(n.ID) || !utf8.ValidString(n.Instructions) || len(n.Instructions) > 64<<10 || len(n.Spans) > 128 || len(n.Rules) > 128 || len(n.ScopePaths) > 64 {
			return fail("node")
		}
		if _, ok := nodes[n.ID]; ok {
			return fail("duplicate node")
		}
		if len(n.Rules) > 0 && s.Version != ReuseVersion {
			return fail("node rules version")
		}
		if n.ReplayProfile != "" && (s.Version != ReuseVersion || !keyPattern.MatchString(n.ReplayProfile)) {
			return fail("replay profile version")
		}
		op, ok := ops[n.Operator]
		if !ok {
			return fail("missing operator")
		}
		if op.Kind == "join" {
			if n.JoinPolicy != "all_success" && n.JoinPolicy != "selected_success" {
				return fail("join policy")
			}
		} else if n.JoinPolicy != "" {
			return fail("non-join policy")
		}
		for _, span := range append(append([]Span{}, n.Spans...), n.Rules...) {
			if _, e := readSpan(sources, span); e != nil {
				return p, e
			}
		}
		for _, scope := range n.ScopePaths {
			if !job.ValidPath(scope) {
				return fail("node scope")
			}
		}
		nodes[n.ID] = n
		indegree[n.ID] = 0
	}
	if len(s.Imports) > 8 || s.Version == Version && len(s.Imports) > 0 {
		return fail("import version or bounds")
	}
	if len(s.Reuse) > 32 || len(s.Reuse) > 0 && s.Version != ReuseVersion {
		return fail("reuse version or bounds")
	}
	reusedNodes := map[string]bool{}
	seenGrants := map[string]bool{}
	for _, b := range s.Reuse {
		n, ok := nodes[b.NodeID]
		if !ok || n.ReplayProfile == "" || !keyPattern.MatchString(b.GrantID) || seenGrants[b.GrantID] || reusedNodes[b.NodeID] {
			return fail("reuse binding")
		}
		seenGrants[b.GrantID] = true
		reusedNodes[b.NodeID] = true
	}
	sort.Slice(s.Reuse, func(i, j int) bool { return s.Reuse[i].NodeID < s.Reuse[j].NodeID })
	for i := range s.Imports {
		b := &s.Imports[i]
		if !keyPattern.MatchString(b.GrantID) || seenGrants[b.GrantID] || len(b.NodeIDs) < 1 || len(b.NodeIDs) > 32 {
			return fail("import binding")
		}
		seenGrants[b.GrantID] = true
		seen := map[string]bool{}
		for _, id := range b.NodeIDs {
			if _, ok := nodes[id]; !ok || seen[id] {
				return fail("import target")
			}
			seen[id] = true
		}
		sort.Strings(b.NodeIDs)
	}
	sort.Slice(s.Imports, func(i, j int) bool { return s.Imports[i].GrantID < s.Imports[j].GrantID })
	bound := map[string]bool{}
	seenEdges := map[string]bool{}
	conditions := map[string]map[string]bool{}
	for _, edge := range s.Edges {
		from, fok := nodes[edge.FromNode]
		to, tok := nodes[edge.ToNode]
		if !fok || !tok || edge.FromNode == edge.ToNode {
			return fail("edge node")
		}
		key := string(canonical(edge))
		if seenEdges[key] {
			return fail("duplicate edge")
		}
		seenEdges[key] = true
		if edge.When != "" && edge.When != "PASS" && edge.When != "FAIL" && edge.When != "UNKNOWN" {
			return fail("edge condition")
		}
		if edge.When != "" {
			port, ok := findPort(ops[from.Operator].Outputs, edge.FromPort)
			if !ok || ops[from.Operator].Kind != "command" || port.Schema != "verdict-v1" {
				return fail("condition requires trusted verdict-v1 command output")
			}
			group := from.ID + "/" + edge.FromPort
			if conditions[group] == nil {
				conditions[group] = map[string]bool{}
			}
			conditions[group][edge.When] = true
		}
		switch edge.Type {
		case "artifact", "memory":
			a, aok := findPort(ops[from.Operator].Outputs, edge.FromPort)
			b, bok := findPort(ops[to.Operator].Inputs, edge.ToPort)
			if !aok || !bok || a.Schema != b.Schema {
				return fail("edge port or schema")
			}
			input := edge.ToNode + "/" + edge.ToPort
			if bound[input] {
				return fail("duplicate input binding")
			}
			bound[input] = true
		case "control":
			if edge.ToPort != "" {
				return fail("control input port")
			}
			if edge.When != "" {
				if _, ok := findPort(ops[from.Operator].Outputs, edge.FromPort); !ok {
					return fail("condition port")
				}
			} else if edge.FromPort != "" {
				return fail("control output port")
			}
		default:
			return fail("edge type")
		}
		indegree[to.ID]++
		next[from.ID] = append(next[from.ID], to.ID)
	}
	for _, branches := range conditions {
		if !branches["PASS"] || !branches["FAIL"] || !branches["UNKNOWN"] {
			return fail("conditional output requires explicit PASS/FAIL/UNKNOWN handlers")
		}
	}
	for _, n := range s.Nodes {
		for _, port := range ops[n.Operator].Inputs {
			if !bound[n.ID+"/"+port.Name] {
				return fail("unbound required input")
			}
		}
	}
	var order []string
	for len(order) < len(nodes) {
		ready := []string{}
		for id, n := range indegree {
			if n == 0 {
				ready = append(ready, id)
			}
		}
		if len(ready) == 0 {
			return fail("cycle")
		}
		sort.Strings(ready)
		id := ready[0]
		delete(indegree, id)
		order = append(order, id)
		for _, child := range next[id] {
			indegree[child]--
		}
	}
	outputSeen := map[string]bool{}
	for _, out := range s.Outputs {
		n, ok := nodes[out.Node]
		if !ok {
			return fail("output node")
		}
		if _, ok = findPort(ops[n.Operator].Outputs, out.Port); !ok {
			return fail("output port")
		}
		key := out.Node + "/" + out.Port
		if outputSeen[key] {
			return fail("duplicate graph output")
		}
		outputSeen[key] = true
	}
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].ID < s.Sources[j].ID })
	sort.Slice(s.Operators, func(i, j int) bool { return s.Operators[i].ID < s.Operators[j].ID })
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	sort.Slice(s.Edges, func(i, j int) bool { return string(canonical(s.Edges[i])) < string(canonical(s.Edges[j])) })
	sort.Slice(s.Outputs, func(i, j int) bool { return string(canonical(s.Outputs[i])) < string(canonical(s.Outputs[j])) })
	p = Plan{Spec: s, Digest: Hash(canonical(s)), Order: order, RequestedParallelism: l.Parallelism, EffectiveParallelism: l.Parallelism, BudgetMode: "unmetered"}
	if l.MaxCostUnits != nil {
		p.EffectiveParallelism = 1
		p.BudgetMode = "claude_estimated_cost_serial"
	}
	return p, nil
}

func findPort(ports []Port, name string) (Port, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}
func readSpan(sources map[string]Source, span Span) (string, error) {
	src, ok := sources[span.Source]
	if !ok || span.SHA256 != src.SHA256 || span.Start < 0 || span.End <= span.Start || span.End > len(src.Text) {
		return "", invalid("source span")
	}
	part := src.Text[span.Start:span.End]
	if !utf8.ValidString(part) {
		return "", invalid("UTF-8 span boundary")
	}
	return part, nil
}

// Lower describes legacy graph shape only; it does not change the legacy executor.
func Lower(in job.Spec) (Spec, error) {
	if e := in.Validate(32, 8); e != nil {
		return Spec{}, e
	}
	s := Spec{Version: Version, ProjectID: in.ProjectID, Workspace: in.Workspace, Schemas: map[string]json.RawMessage{"legacy-result-bundle-v1": json.RawMessage(`{}`)}, Evaluator: Evaluator{Profile: "job-artifact-v1", Version: "1"}, Limits: Limits{Parallelism: 1, TimeoutSeconds: in.Limits.TimeoutSeconds, MaxAttemptsPerTask: in.Limits.MaxAttemptsPerTask}}
	add := func(id, text string, ex job.Execution, inputs []Port, scopes []string) {
		s.Operators = append(s.Operators, Operator{ID: id, Version: "legacy-v02", Kind: "agent", Execution: &ex, Inputs: inputs, Outputs: []Port{{Name: "result", Schema: "legacy-result-bundle-v1"}}})
		s.Nodes = append(s.Nodes, Node{ID: id, Operator: id, Instructions: text, ScopePaths: scopes})
	}
	if in.Mode == "single" {
		add("single", in.Input.Text, *in.Execution, nil, nil)
		s.Outputs = []Output{{Node: "single", Port: "result"}}
		return s, nil
	}
	if len(in.Map.Partitions) > 31 {
		return Spec{}, invalid("legacy lowering requires at most 31 maps plus reduce")
	}
	s.Limits.Parallelism = in.Map.Parallelism
	var inputs []Port
	for _, part := range in.Map.Partitions {
		id := "map_" + part.Key
		if len(id) > 64 {
			id = "map_" + Hash([]byte(part.Key))[:60]
		}
		add(id, in.Input.Text+"\n\n"+part.Input.Text, part.Execution, nil, part.ScopePaths)
		inputs = append(inputs, Port{Name: part.Key, Schema: "legacy-result-bundle-v1"})
		s.Edges = append(s.Edges, Edge{FromNode: id, FromPort: "result", ToNode: "reduce", ToPort: part.Key, Type: "artifact"})
	}
	add("reduce", in.Input.Text+"\n\n"+in.Reduce.Input.Text, in.Reduce.Execution, inputs, nil)
	s.Outputs = []Output{{Node: "reduce", Port: "result"}}
	return s, nil
}
