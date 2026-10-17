package testutil

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestIUFixturePreservesOrderIdentityAndDeduplicates(t *testing.T) {
	for _, tc := range []struct {
		instruction string
		local       []map[string]string
		inputs      []map[string]string
		want        string
	}{
		{"fixture-order-object", []map[string]string{{"text": `{"order_id":"a","customer_id":"c","paid_cents":6000}`}}, nil, `{"order_id":"a","customer_id":"c","paid_cents":6000}`},
		{"fixture-customer-total:2000", nil, []map[string]string{{"value": `{"a":{"order_id":"a","customer_id":"c","paid_cents":6000},"b":{"order_id":"a","customer_id":"c","paid_cents":6000}}`}}, `6000`},
		{"fixture-customer-total:2000", nil, []map[string]string{{"value": `{"a":{"order_id":"a","customer_id":"c","paid_cents":6000},"b":{"order_id":"b","customer_id":"d","paid_cents":6000}}`}}, `12000`},
	} {
		t.Run(tc.instruction+tc.want, func(t *testing.T) {
			prompt, _ := json.Marshal(map[string]any{"version": "iu-context-v1", "instructions": tc.instruction + ` Declared output ports: [{"name":"result"}]`, "local": tc.local, "inputs": tc.inputs})
			cmd := exec.Command(JobCLI(t), "exec")
			cmd.Stdin = strings.NewReader(string(prompt))
			raw, e := cmd.CombinedOutput()
			if e != nil {
				t.Fatalf("%s %v", raw, e)
			}
			for _, line := range strings.Split(string(raw), "\n") {
				var event struct {
					Item struct {
						Text string `json:"text"`
					} `json:"item"`
				}
				json.Unmarshal([]byte(line), &event)
				if event.Item.Text != "" {
					var out struct {
						Values map[string]json.RawMessage `json:"values"`
					}
					if e = json.Unmarshal([]byte(event.Item.Text), &out); e != nil {
						t.Fatal(e)
					}
					var a, b any
					json.Unmarshal(out.Values["result"], &a)
					json.Unmarshal([]byte(tc.want), &b)
					aa, _ := json.Marshal(a)
					bb, _ := json.Marshal(b)
					if string(aa) != string(bb) {
						t.Fatalf("got %s want %s", aa, bb)
					}
					return
				}
			}
			t.Fatal("missing fixture output")
		})
	}
}
