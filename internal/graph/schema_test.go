package graph

import (
	"encoding/json"
	"testing"
)

func TestOutputSchemaRebasesFrozenLocalDefinitions(t *testing.T) {
	schemas := map[string]json.RawMessage{"amount/v1": json.RawMessage(`{"$defs":{"number":{"type":"integer"}},"$ref":"#/$defs/number"}`)}
	ports := []Port{{Name: "result", Schema: "amount/v1"}}
	r, e := resolvePortSchema(OutputSchema(ports, schemas))
	if e != nil {
		t.Fatal(e)
	}
	var value any
	if e = json.Unmarshal([]byte(`{"version":"iu-node-output-v1","values":{"result":11000},"records":[]}`), &value); e != nil {
		t.Fatal(e)
	}
	if e = r.Validate(value); e != nil {
		t.Fatal(e)
	}
	if _, e = resolvePortSchema(json.RawMessage(`{"$ref":"https://example.invalid/schema"}`)); e == nil {
		t.Fatal("external lookup allowed")
	}
}
