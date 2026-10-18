package testutil

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestCoverageFixtureEmitsStructuredV5FaultAndRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		instruction := `fixture-coverage-v5 Declared output ports: [{"name":"result","schema":"object-v1"}]`
		if repair {
			instruction = `fixture-coverage-v5 fixture-coverage-repaired Declared output ports: [{"name":"result","schema":"object-v1"}]`
		}
		context := map[string]any{"version": "iu-context-v1", "instructions": instruction, "local": []map[string]any{{"text": `{"order_id":"a","customer_id":"c","paid_cents":6000,"is_special":false}`}, {"text": `{"order_id":"b","customer_id":"c","paid_cents":6000,"is_special":false}`}, {"text": `{"threshold_cents":10000,"discount_cents":1000,"max_discounts_per_customer":1}`}}, "rules": []any{}, "inputs": []any{}}
		raw, _ := json.Marshal(context)
		cmd := exec.Command(JobCLI(t), "exec")
		cmd.Stdin = strings.NewReader(string(raw))
		output, e := cmd.Output()
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			var event struct {
				Item struct {
					Text string `json:"text"`
				} `json:"item"`
			}
			json.Unmarshal([]byte(line), &event)
			if event.Item.Text == "" {
				continue
			}
			var out struct {
				Values map[string]struct {
					Total  int   `json:"total_cents"`
					Orders []any `json:"orders"`
				} `json:"values"`
			}
			if e = json.Unmarshal([]byte(event.Item.Text), &out); e != nil {
				t.Fatal(e)
			}
			want := 12000
			if repair {
				want = 11000
			}
			if out.Values["result"].Total != want || len(out.Values["result"].Orders) != 2 {
				t.Fatal("wrong fixture", event.Item.Text)
			}
			found = true
		}
		if !found {
			t.Fatal("no structured result")
		}
	}
}
