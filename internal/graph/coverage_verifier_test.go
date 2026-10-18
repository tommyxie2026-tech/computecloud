package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func orderCoverageFixture() (Spec, map[string]json.RawMessage) {
	s := coverageFixture()
	goal := `{"version":"iu-orders-goal-v1","currency":"CNY","unit":"cent","order_source_ids":["one","two"],"rule_source_id":"rule","exception_source_ids":[],"required_checks":["source_identity","source_fields","customer_grouping","threshold_once","exception_rules","currency_unit"]}`
	s.Sources = []Source{{ID: "goal", Text: goal}, {ID: "one", Text: `{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":false}`}, {ID: "two", Text: `{"order_id":"b","customer_id":"c","paid_cents":6000,"is_special":false}`}, {ID: "rule", Text: `{"threshold_cents":10000,"discount_cents":1000,"max_discounts_per_customer":1}`}}
	for i := range s.Sources {
		s.Sources[i].SHA256 = Hash([]byte(s.Sources[i].Text))
	}
	s.Coverage.GoalSourceID = "goal"
	s.Coverage.GoalSpecHash = Hash([]byte(goal))
	s.Coverage.Checks = nil
	s.Coverage.Obligations = nil
	for _, id := range OrderCoverageChecks {
		s.Coverage.Checks = append(s.Coverage.Checks, CoverageCheckBinding{ID: id, Outputs: []Output{{Node: "merge", Port: "result"}}})
		s.Coverage.Obligations = append(s.Coverage.Obligations, CoverageObligation{ID: id, CheckID: id, NodeIDs: []string{"b"}, Sources: []Span{{Source: "goal", End: len(goal), SHA256: s.Coverage.GoalSpecHash}}})
	}
	s.Coverage.SHA256 = s.Coverage.Digest()
	return s, map[string]json.RawMessage{"merge/result": json.RawMessage(`{"orders":[{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":false},{"order_id":"b","customer_id":"c","paid_cents":6000,"is_special":false}],"total_cents":11000,"currency":"CNY","unit":"cent"}`)}
}
func coverageRun(s Spec, v map[string]json.RawMessage) CoverageResult {
	return RunCoverageVerifier(context.Background(), CoverageVerifierProfile{Version: "1", Kind: "orders-v1", Scope: "orders", TimeoutMS: 1000}, s, v)
}
func TestCoverageCheckerRejectsOmittedExceptionAndWrongCatalog(t *testing.T) {
	s, v := orderCoverageFixture()
	if r := coverageRun(s, v); r.Verdict != "PASS" {
		t.Fatalf("%+v", r)
	}
	s.Coverage.Checks = s.Coverage.Checks[1:]
	if r := coverageRun(s, v); r.Verdict == "PASS" {
		t.Fatal("incomplete catalog passed")
	}
	s, v = orderCoverageFixture()
	s.Sources = append(s.Sources, Source{ID: "except", Text: `{"type":"unknown"}`})
	s.Sources[0].Text = `{"version":"iu-orders-goal-v1","currency":"CNY","unit":"cent","order_source_ids":["one","two"],"rule_source_id":"rule","exception_source_ids":["except"],"required_checks":["source_identity","source_fields","customer_grouping","threshold_once","exception_rules","currency_unit"]}`
	if r := coverageRun(s, v); r.Verdict == "PASS" {
		t.Fatal("unknown exception passed")
	}
}
func TestCoverageCheckerChecksFieldsDespiteCorrectTotal(t *testing.T) {
	s, v := orderCoverageFixture()
	v["merge/result"] = json.RawMessage(`{"orders":[{"order_id":"wrong","customer_id":"c","paid_cents":6000,"is_special":false},{"order_id":"b","customer_id":"c","paid_cents":6000,"is_special":false}],"total_cents":11000,"currency":"CNY","unit":"cent"}`)
	if r := coverageRun(s, v); r.Verdict != "FAIL" {
		t.Fatalf("%+v", r)
	}
}
func TestCoverageCheckerUnknownInputAndOverflow(t *testing.T) {
	for _, raw := range []string{`{"order_id":"a","customer_id":"c","paid_cents":1.0,"is_special":false}`, fmt.Sprintf(`{"order_id":"a","customer_id":"c","paid_cents":%d,"is_special":false}`, int64(math.MaxInt64)), `{"order_id":"a","order_id":"b"}`, `{"order_id":"a","customer_id":"c","paid_cents":6000}`} {
		s, v := orderCoverageFixture()
		s.Sources[1].Text = raw
		s.Sources[1].SHA256 = Hash([]byte(raw))
		if r := coverageRun(s, v); r.Verdict != "UNKNOWN" {
			t.Fatalf("%s %+v", raw, r)
		}
	}
	s, v := orderCoverageFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := RunCoverageVerifier(ctx, CoverageVerifierProfile{Version: "1", Kind: "orders-v1", Scope: "orders", TimeoutMS: 1}, s, v); r.Verdict != "UNKNOWN" {
		t.Fatal(r)
	}
}
func TestCoverageCheckerCustomerExceptionAndDuplicate(t *testing.T) {
	for _, kind := range []string{"customers", "special", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			s, v := orderCoverageFixture()
			var actual CoverageOrderResult
			json.Unmarshal(v["merge/result"], &actual)
			expected := int64(12000)
			switch kind {
			case "customers":
				s.Sources[2].Text = `{"order_id":"b","customer_id":"other","paid_cents":6000,"is_special":false}`
				actual.Orders[1].Customer = "other"
			case "special":
				s.Sources[1].Text = `{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":true}`
				b := true
				actual.Orders[0].Special = &b
				s.Sources = append(s.Sources, Source{ID: "exception", Text: `{"type":"special_no_discount"}`})
				var goal map[string]any
				json.Unmarshal([]byte(s.Sources[0].Text), &goal)
				goal["exception_source_ids"] = []string{"exception"}
				raw, _ := json.Marshal(goal)
				s.Sources[0].Text = string(raw)
			case "duplicate":
				s.Sources[2].Text = s.Sources[1].Text
				actual.Orders = actual.Orders[:1]
				expected = 6000
			}
			actual.Total = &expected
			v["merge/result"], _ = json.Marshal(actual)
			for i := range s.Sources {
				s.Sources[i].SHA256 = Hash([]byte(s.Sources[i].Text))
			}
			s.Coverage.GoalSpecHash = s.Sources[0].SHA256
			s.Coverage.SHA256 = s.Coverage.Digest()
			if r := coverageRun(s, v); r.Verdict != "PASS" {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestCoverageCheckerRejectsCaseAliasFields(t *testing.T) {
	for _, raw := range []string{`{"order_id":"a","customer_id":"c","paid_cents":6000,"PAID_CENTS":1,"is_special":false}`, `{"order_id":"a","customer_id":"c","PAID_CENTS":6000,"is_special":false}`} {
		s, v := orderCoverageFixture()
		s.Sources[1].Text = raw
		s.Sources[1].SHA256 = Hash([]byte(raw))
		if r := coverageRun(s, v); r.Verdict != "UNKNOWN" {
			t.Fatalf("ambiguous case alias not unknown: %+v", r)
		}
	}
}
