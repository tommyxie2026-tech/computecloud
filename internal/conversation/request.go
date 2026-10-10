// Package conversation adapts the text-only subset of native agent client APIs.
package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
)

var ErrUnsupported = errors.New("CAPABILITY_UNSUPPORTED")

type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type Request struct {
	Protocol        string    `json:"protocol"`
	Model           string    `json:"model"`
	Transcript      []Message `json:"transcript"`
	MaxOutputTokens int       `json:"max_output_tokens"`
	IdempotencyKey  string    `json:"idempotency_key,omitempty"`
	Stream          bool      `json:"stream,omitempty"`
}

type messagesRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	System     json.RawMessage   `json:"system,omitempty"`
	Tools      []json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage   `json:"tool_choice,omitempty"`
	Stream     bool              `json:"stream,omitempty"`
	Metadata   json.RawMessage   `json:"metadata,omitempty"`
}
type responsesRequest struct {
	Model              string            `json:"model"`
	Input              json.RawMessage   `json:"input"`
	Instructions       string            `json:"instructions,omitempty"`
	MaxOutputTokens    int               `json:"max_output_tokens,omitempty"`
	Tools              []json.RawMessage `json:"tools,omitempty"`
	ToolChoice         json.RawMessage   `json:"tool_choice,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	Include            []string          `json:"include,omitempty"`
}

func DecodeMessages(b []byte, maxBytes int, maxOutput int) (Request, error) {
	var in messagesRequest
	if len(b) == 0 || len(b) > maxBytes {
		return Request{}, fmt.Errorf("request body exceeds limit")
	}
	if err := jsonutil.Decode(b, &in); err != nil {
		return Request{}, err
	}
	if !supportedToolChoice(in.ToolChoice) {
		return Request{}, ErrUnsupported
	}
	if in.MaxTokens < 1 || in.MaxTokens > maxOutput || in.Model == "" {
		return Request{}, fmt.Errorf("invalid or unsupported Messages request")
	}
	r := Request{Protocol: "messages", Model: in.Model, MaxOutputTokens: in.MaxTokens, Stream: in.Stream}
	if len(in.System) > 0 {
		s, e := contentText(in.System)
		if e != nil {
			return Request{}, e
		}
		if s != "" {
			r.Transcript = append(r.Transcript, Message{Role: "system", Text: s})
		}
	}
	for _, m := range in.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return Request{}, ErrUnsupported
		}
		s, e := contentText(m.Content)
		if e != nil {
			return Request{}, e
		}
		r.Transcript = append(r.Transcript, Message{Role: m.Role, Text: s})
	}
	if err := validateTranscript(r.Transcript); err != nil {
		return Request{}, err
	}
	return r, nil
}

func DecodeResponses(b []byte, maxBytes int, maxOutput int) (Request, error) {
	var in responsesRequest
	if len(b) == 0 || len(b) > maxBytes {
		return Request{}, fmt.Errorf("request body exceeds limit")
	}
	if err := jsonutil.Decode(b, &in); err != nil {
		return Request{}, err
	}
	if in.MaxOutputTokens == 0 {
		in.MaxOutputTokens = maxOutput
	}
	if !supportedToolChoice(in.ToolChoice) || in.PreviousResponseID != "" || (in.Store != nil && *in.Store) || len(in.Include) > 0 {
		return Request{}, ErrUnsupported
	}
	if in.Model == "" || in.MaxOutputTokens < 1 || in.MaxOutputTokens > maxOutput {
		return Request{}, fmt.Errorf("invalid or unsupported Responses request")
	}
	r := Request{Protocol: "responses", Model: in.Model, MaxOutputTokens: in.MaxOutputTokens, Stream: in.Stream}
	if in.Instructions != "" {
		r.Transcript = append(r.Transcript, Message{Role: "system", Text: in.Instructions})
	}
	var raw []json.RawMessage
	if len(in.Input) == 0 {
		return Request{}, fmt.Errorf("input required")
	}
	if in.Input[0] == '"' {
		var s string
		if err := json.Unmarshal(in.Input, &s); err != nil {
			return Request{}, err
		}
		raw = []json.RawMessage{json.RawMessage(`{"role":"user","content":` + string(mustJSON(s)) + `}`)}
	} else if in.Input[0] == '[' {
		if err := json.Unmarshal(in.Input, &raw); err != nil {
			return Request{}, err
		}
	} else {
		return Request{}, ErrUnsupported
	}
	for _, item := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return Request{}, err
		}
		for key := range fields {
			if key != "role" && key != "content" {
				return Request{}, ErrUnsupported
			}
		}
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Type    string          `json:"type"`
		}
		if err := json.Unmarshal(item, &m); err != nil {
			return Request{}, err
		}
		if m.Type != "" {
			return Request{}, ErrUnsupported
		}
		if m.Role != "user" && m.Role != "assistant" {
			return Request{}, ErrUnsupported
		}
		s, e := contentText(m.Content)
		if e != nil {
			return Request{}, e
		}
		r.Transcript = append(r.Transcript, Message{Role: m.Role, Text: s})
	}
	if err := validateTranscript(r.Transcript); err != nil {
		return Request{}, err
	}
	return r, nil
}

func supportedToolChoice(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name == "auto" || name == "none"
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &choice) != nil {
		return false
	}
	return choice.Name == "" && (choice.Type == "auto" || choice.Type == "none")
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content required")
	}
	if raw[0] == '"' {
		var s string
		if e := json.Unmarshal(raw, &s); e != nil {
			return "", e
		}
		return s, nil
	}
	var parts []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Source    json.RawMessage `json:"source,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
	}
	if e := json.Unmarshal(raw, &parts); e != nil {
		return "", ErrUnsupported
	}
	var out []string
	for _, p := range parts {
		if p.Type != "text" || len(p.Source) > 0 || p.ToolUseID != "" {
			return "", ErrUnsupported
		}
		out = append(out, p.Text)
	}
	return strings.Join(out, ""), nil
}
func validateTranscript(ms []Message) error {
	if len(ms) == 0 {
		return fmt.Errorf("text transcript required")
	}
	for _, m := range ms {
		if strings.TrimSpace(m.Text) == "" {
			return fmt.Errorf("empty text message")
		}
	}
	return nil
}
