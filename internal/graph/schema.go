package graph

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

func resolvePortSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var document any
	if e := json.Unmarshal(raw, &document); e != nil {
		return nil, e
	}
	var check func(any) error
	check = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if key == "$id" || key == "$dynamicRef" || key == "$anchor" || key == "$dynamicAnchor" {
					return fmt.Errorf("schema identity and dynamic references are prohibited")
				}
				if key == "$ref" {
					ref, ok := value.(string)
					if !ok || (ref != "#" && !strings.HasPrefix(ref, "#/")) {
						return fmt.Errorf("external schema references are prohibited")
					}
				}
				if e := check(value); e != nil {
					return e
				}
			}
		case []any:
			for _, value := range x {
				if e := check(value); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := check(document); e != nil {
		return nil, e
	}
	var s jsonschema.Schema
	if e := json.Unmarshal(raw, &s); e != nil {
		return nil, e
	}
	return s.Resolve(nil)
}

// ValidateValues validates actual payloads against locally frozen schema bytes.
// Schema references are never fetched from a registry during execution.
func ValidateValues(values map[string]json.RawMessage, ports []Port, schemas map[string]json.RawMessage) error {
	if len(values) != len(ports) {
		return invalid("output port count")
	}
	for _, port := range ports {
		raw, ok := values[port.Name]
		if !ok {
			return invalid("output missing")
		}
		var value any
		if e := json.Unmarshal(raw, &value); e != nil {
			return e
		}
		definition, ok := schemas[port.Schema]
		if !ok {
			return invalid("output schema missing")
		}
		resolved, e := resolvePortSchema(definition)
		if e != nil {
			return e
		}
		if e = resolved.Validate(value); e != nil {
			return invalid("output schema mismatch: " + e.Error())
		}
		if port.Schema == "verdict-v1" && value != "PASS" && value != "FAIL" && value != "UNKNOWN" {
			return invalid("verdict enum")
		}
	}
	return nil
}

// OutputSchema publishes the result envelope to the model as part of its actual
// hashed context; field names are data, not prose the model must guess.
func OutputSchema(ports []Port, schemas map[string]json.RawMessage) json.RawMessage {
	values := map[string]any{}
	definitions := map[string]any{}
	names := []string{}
	for _, p := range ports {
		prefix := "#/$defs/" + strings.ReplaceAll(strings.ReplaceAll(p.Schema, "~", "~0"), "/", "~1")
		var definition any
		_ = json.Unmarshal(schemas[p.Schema], &definition) // Compile already validated frozen definitions.
		var rebase func(any)
		rebase = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for key, value := range x {
					if key == "$ref" {
						if ref, ok := value.(string); ok {
							x[key] = prefix + strings.TrimPrefix(ref, "#")
						}
					} else {
						rebase(value)
					}
				}
			case []any:
				for _, value := range x {
					rebase(value)
				}
			}
		}
		rebase(definition)
		definitions[p.Schema] = definition
		values[p.Name] = map[string]any{"$ref": prefix}
		names = append(names, p.Name)
	}
	record := map[string]any{"type": "object", "required": []string{"id", "kind", "payload", "validation", "validity"}, "properties": map[string]any{"id": map[string]any{"type": "string"}, "kind": map[string]any{"enum": []string{"observation", "hypothesis", "decision", "constraint", "unresolved", "result"}}, "payload": map[string]any{}, "sources": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"id", "sha256"}, "properties": map[string]any{"id": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}}}}, "validation": map[string]any{"const": "unchecked"}, "validity": map[string]any{"const": "VALID"}}}
	return canonical(map[string]any{"$defs": definitions, "type": "object", "additionalProperties": false, "required": []string{"version", "values", "records"}, "properties": map[string]any{"version": map[string]any{"const": "iu-node-output-v1"}, "values": map[string]any{"type": "object", "additionalProperties": false, "required": names, "properties": values}, "records": map[string]any{"type": "array", "maxItems": 32, "items": record}}})
}
