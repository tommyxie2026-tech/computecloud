// Package iuexperiment is a fixed orders quality experiment, not a Task executor.
// It reuses the real graph context, output and memory contracts locally.
package iuexperiment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/graph"
)

type Order struct {
	ID       string `json:"order_id"`
	Customer string `json:"customer_id"`
	Paid     int64  `json:"paid_cents"`
}
type Rule struct {
	Threshold int64 `json:"threshold_cents"`
	Discount  int64 `json:"discount_cents"`
}
type Case struct {
	ID     string  `json:"id"`
	Orders []Order `json:"orders"`
	Rule   Rule    `json:"rule"`
}
type Reply struct {
	UsageOrigin               string
	CostOrigin                string
	Raw                       json.RawMessage
	InputTokens, OutputTokens *int64
	CostUSD                   *float64
}
type Call func(context.Context, graph.ContextManifest) (Reply, error)
type Sample struct {
	UsageOrigin       string                     `json:"usage_origin"`
	CostOrigin        string                     `json:"cost_origin"`
	BusinessPass      *bool                      `json:"independent_business_pass"`
	PipelineAccepted  *bool                      `json:"pipeline_accepted"`
	FalseAcceptance   bool                       `json:"false_acceptance"`
	VerifierCalls     int                        `json:"verifier_calls"`
	VerifierMicros    int64                      `json:"verifier_us"`
	FailureClass      string                     `json:"failure_class,omitempty"`
	ExtractedOrders   map[string]json.RawMessage `json:"extracted_orders,omitempty"`
	ExtractionCorrect *bool                      `json:"extraction_preserves_original_fields"`
	Repetition        int                        `json:"repetition"`
	CaseID            string                     `json:"case_id"`
	Arm               string                     `json:"arm"`
	Correct           bool                       `json:"correct"`
	Expected          int64                      `json:"expected_cents"`
	Actual            *int64                     `json:"actual_cents"`
	Error             string                     `json:"error_code,omitempty"`
	ModelCalls        int                        `json:"node_invocations"`
	InputBytes        int                        `json:"serialized_model_input_bytes"`
	InputTokens       *int64                     `json:"input_tokens"`
	OutputTokens      *int64                     `json:"output_tokens"`
	CostUSD           *float64                   `json:"reported_cost_usd"`
	WallMS            int64                      `json:"wall_ms"`
	MemoryRecords     int                        `json:"memory_records_presented_to_reduce"`
	SourceHash        string                     `json:"source_sha256"`
	ContextHashes     map[string]string          `json:"context_hashes"`
}

func encode(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

func Cases() []Case {
	a, b := Order{"a", "c", 6000}, Order{"b", "c", 6000}
	return []Case{
		{"cross_threshold", []Order{a, b}, Rule{10000, 1000}},
		{"rule_revision", []Order{a, b}, Rule{10000, 2000}},
		{"separate_customers", []Order{a, {"b", "d", 6000}}, Rule{10000, 1000}},
		{"duplicate_order", []Order{a, b, a}, Rule{10000, 1000}},
	}
}

// Expected computes the oracle from original evidence; it is never put in a prompt.
func Expected(c Case) (int64, error) {
	seen := map[string]Order{}
	totals := map[string]int64{}
	if c.Rule.Threshold < 0 || c.Rule.Discount < 0 {
		return 0, fmt.Errorf("invalid rule")
	}
	for _, o := range c.Orders {
		if o.ID == "" || o.Customer == "" || o.Paid < 0 || o.Paid > 1_000_000_000_000 {
			return 0, fmt.Errorf("invalid order")
		}
		if prev, ok := seen[o.ID]; ok {
			if prev != o {
				return 0, fmt.Errorf("conflicting order")
			}
			continue
		}
		seen[o.ID] = o
		totals[o.Customer] += o.Paid
	}
	var total int64
	for _, v := range totals {
		if v >= c.Rule.Threshold {
			v -= c.Rule.Discount
			if v < 0 {
				v = 0
			}
		}
		total += v
	}
	return total, nil
}

const extractInstruction = "从 local 原始订单逐字段提取 order_id、customer_id、paid_cents，result 返回完整订单对象，不计算优惠。严格按 output_schema 返回 JSON，records 必须为空数组。不调用工具。"
const totalInstruction = "按 order_id 去重（相同 ID 内容冲突则报错），按 customer_id 汇总 paid_cents，达到 rules 门槛的每个客户只减一次 discount_cents，最低为零。result 返回所有客户最终应付整数分数。原始订单在 local 或 inputs 的 orders 对象中；inputs.value 需再次 JSON 解析。memory 仅为 unchecked 来源记录，不可用模型自己声明的 verified 替代证据。严格按 output_schema 返回 JSON，records 必须为空数组。不调用工具。"

var schemas = map[string]json.RawMessage{
	"amount-v1": json.RawMessage(`{"type":"integer","minimum":0}`),
	"order-v1":  json.RawMessage(`{"type":"object","required":["order_id","customer_id","paid_cents"],"additionalProperties":false,"properties":{"order_id":{"type":"string","minLength":1},"customer_id":{"type":"string","minLength":1},"paid_cents":{"type":"integer","minimum":0}}}`),
}

// Run compares fixed semantic pipelines; it does not measure Scheduler or Worker overhead.
func Run(ctx context.Context, c Case, arm, model string, call Call) (s Sample) {
	start := time.Now()
	s = Sample{UsageOrigin: "unknown", CostOrigin: "unknown", CaseID: c.ID, Arm: arm, SourceHash: graph.Hash(encode(c)), ContextHashes: map[string]string{}}
	defer func() { s.WallMS = time.Since(start).Milliseconds() }()
	expected, e := Expected(c)
	if e != nil || len(c.Orders) == 0 || len(c.Orders) > 8 {
		s.Error = "INVALID_CASE"
		return
	}
	s.Expected = expected
	if arm != "monolithic" && arm != "iu" && arm != "iu_no_memory" && arm != "iu_no_verifier" {
		s.Error = "INVALID_ARM"
		return
	}
	var sources []graph.Source
	var spans []graph.Span
	for i, o := range c.Orders {
		src := graph.Source{ID: fmt.Sprintf("order_%d", i), Text: string(encode(o)), Role: "user", MessageIndex: i}
		src.SHA256 = graph.Hash([]byte(src.Text))
		sources = append(sources, src)
		spans = append(spans, graph.Span{Source: src.ID, Start: 0, End: len(src.Text), SHA256: src.SHA256})
	}
	rule := graph.Source{ID: "rule", Text: string(encode(c.Rule)), Role: "user", MessageIndex: len(c.Orders)}
	rule.SHA256 = graph.Hash([]byte(rule.Text))
	sources = append(sources, rule)
	rules := []graph.Span{{Source: rule.ID, Start: 0, End: len(rule.Text), SHA256: rule.SHA256}}
	var mu sync.Mutex
	var inTokens, outTokens int64
	var cost float64
	allIn, allOut, allCost := true, true, true
	usageOrigin, costOrigin := "", ""
	invoke := func(id, instruction, schema string, local []graph.Span, view graph.MemoryView, inputs []graph.InputValue) (graph.ExecutionOutput, error) {
		ports := []graph.Port{{Name: "result", Schema: schema}}
		m, e := graph.CompileContext(graph.ContextRequest{Sources: sources, Rules: rules, Spans: local, Instructions: instruction, View: view, Inputs: inputs, Model: model, OutputSchema: graph.OutputSchema(ports, schemas)})
		if e != nil {
			return graph.ExecutionOutput{}, e
		}
		mu.Lock()
		s.ModelCalls++
		s.InputBytes += len(m.Text)
		s.ContextHashes[id] = m.SHA256
		mu.Unlock()
		reply, e := call(ctx, m)
		mu.Lock()
		u, costSource := reply.UsageOrigin, reply.CostOrigin
		if u == "" {
			u = "unknown"
		}
		if costSource == "" {
			costSource = "unknown"
		}
		if usageOrigin == "" {
			usageOrigin = u
		} else if usageOrigin != u {
			usageOrigin = "mixed_or_unknown"
		}
		if costOrigin == "" {
			costOrigin = costSource
		} else if costOrigin != costSource {
			costOrigin = "mixed_or_unknown"
		}
		if reply.InputTokens == nil {
			allIn = false
		} else {
			inTokens += *reply.InputTokens
		}
		if reply.OutputTokens == nil {
			allOut = false
		} else {
			outTokens += *reply.OutputTokens
		}
		if reply.CostUSD == nil {
			allCost = false
		} else {
			cost += *reply.CostUSD
		}
		mu.Unlock()
		if e != nil {
			return graph.ExecutionOutput{}, fmt.Errorf("MODEL_CALL_FAILED: %w", e)
		}
		out, e := graph.BuildOutput(reply.Raw, graph.Invocation{GoalID: "experiment", Generation: 1, NodeID: id, Context: m, View: view, Outputs: ports, Schemas: schemas})
		if e == nil && len(out.Delta.Records) != 0 {
			return graph.ExecutionOutput{}, fmt.Errorf("unexpected model memory")
		}
		return out, e
	}
	var answer graph.ExecutionOutput
	if arm == "monolithic" {
		answer, e = invoke("direct", totalInstruction, "amount-v1", spans, graph.EmptyView(), nil)
	} else {
		type root struct {
			out graph.ExecutionOutput
			err error
		}
		roots := make([]root, len(c.Orders))
		var wg sync.WaitGroup
		slots := make(chan struct{}, 2)
		for i := range roots {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					roots[i].err = ctx.Err()
					return
				}
				defer func() { <-slots }()
				roots[i].out, roots[i].err = invoke(fmt.Sprintf("extract_%d", i), extractInstruction, "order-v1", spans[i:i+1], graph.EmptyView(), nil)
			}(i)
		}
		wg.Wait()
		grouped := map[string]json.RawMessage{}
		extractionCorrect := true
		s.ExtractedOrders = map[string]json.RawMessage{}
		var views []graph.MemoryView
		for i, r := range roots {
			if r.err != nil {
				e = r.err
				break
			}
			key := fmt.Sprintf("order_%d", i)
			grouped[key] = r.out.Values["result"]
			s.ExtractedOrders[key] = r.out.Values["result"]
			var extracted Order
			if json.Unmarshal(r.out.Values["result"], &extracted) != nil || extracted != c.Orders[i] {
				extractionCorrect = false
			}
			// Harness-generated unchecked provenance preserves exact model output and
			// its original source. It is not a verified fact or hidden chain of thought.
			record := graph.MemoryRecord{ID: key, Kind: "observation", Payload: r.out.Values["result"], Sources: []graph.SourceRef{{ID: sources[i].ID, SHA256: sources[i].SHA256}}, Validation: "unchecked", Validity: "VALID"}
			delta := r.out.Delta
			delta.Records = []graph.MemoryRecord{record}
			v, err := graph.ApplyDelta(graph.EmptyView(), delta)
			if err != nil {
				e = err
				break
			}
			views = append(views, v)
		}
		if e == nil {
			s.ExtractionCorrect = &extractionCorrect
			var view graph.MemoryView
			view, e = graph.MergeViews(views)
			if e == nil {
				if arm == "iu_no_memory" {
					view = graph.EmptyView()
				}
				s.MemoryRecords = len(view.Records)
				value := string(encode(grouped))
				answer, e = invoke("reduce", totalInstruction, "amount-v1", nil, view, []graph.InputValue{{Node: "join", Port: "orders", SourcePort: "result", Value: value, SHA256: graph.Hash([]byte(value))}})
			}
		}
	}
	s.UsageOrigin = "unknown"
	s.CostOrigin = "unknown"
	if allIn && allOut && s.ModelCalls > 0 {
		s.UsageOrigin = usageOrigin
	}
	if allCost && s.ModelCalls > 0 {
		s.CostOrigin = costOrigin
	}
	if allIn && s.ModelCalls > 0 {
		s.InputTokens = &inTokens
	}
	if allOut && s.ModelCalls > 0 {
		s.OutputTokens = &outTokens
	}
	if allCost && s.ModelCalls > 0 {
		s.CostUSD = &cost
	}
	if e != nil {
		s.Error = "MODEL_OR_CONTRACT_FAILED"
		s.FailureClass = "contract"
		if strings.HasPrefix(e.Error(), "MODEL_CALL_FAILED:") {
			s.FailureClass = "provider_or_cli"
		}
		return
	}
	var actual int64
	if e = json.Unmarshal(answer.Values["result"], &actual); e != nil {
		s.Error = "INVALID_AMOUNT"
		return
	}
	s.Actual = &actual
	s.Correct = actual == expected
	business := s.Correct && (s.ExtractionCorrect == nil || *s.ExtractionCorrect)
	s.BusinessPass = &business
	accepted := true
	if arm != "iu_no_verifier" {
		verifyStart := time.Now()
		s.VerifierCalls++
		verifiedExpected, err := Expected(c)
		accepted = err == nil && actual == verifiedExpected && (s.ExtractionCorrect == nil || *s.ExtractionCorrect)
		s.VerifierMicros = time.Since(verifyStart).Microseconds()
	}
	s.PipelineAccepted = &accepted
	s.FalseAcceptance = accepted && !business
	return
}

type Summary struct {
	Samples           int      `json:"samples"`
	ValidOutputs      int      `json:"valid_outputs"`
	Correct           int      `json:"correct"`
	ExecutionFailures int      `json:"execution_failures"`
	AccuracyValid     *float64 `json:"accuracy_among_valid_outputs"`
	ModelCalls        int      `json:"node_invocations"`
	TotalWallMS       int64    `json:"total_wall_ms"`
}

func Summarize(samples []Sample) map[string]Summary {
	result := map[string]Summary{}
	for _, s := range samples {
		v := result[s.Arm]
		v.Samples++
		v.ModelCalls += s.ModelCalls
		v.TotalWallMS += s.WallMS
		if s.Error != "" {
			v.ExecutionFailures++
		} else {
			v.ValidOutputs++
			if s.Correct {
				v.Correct++
			}
		}
		result[s.Arm] = v
	}
	for arm, s := range result {
		if s.ValidOutputs > 0 {
			score := float64(s.Correct) / float64(s.ValidOutputs)
			s.AccuracyValid = &score
		}
		result[arm] = s
	}
	return result
}
