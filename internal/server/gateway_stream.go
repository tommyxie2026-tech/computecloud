package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type gatewayOutcome struct {
	state, code, upstreamID string
	input, output           *int64
	usage                   []byte
	known                   bool
}

func (o *gatewayOutcome) readUsage(raw []byte) {
	var v struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.Usage) == 0 || len(v.Usage) > 64<<10 {
		return
	}
	var u struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	}
	if json.Unmarshal(v.Usage, &u) != nil || u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 {
		return
	}
	o.input = u.Input
	o.output = u.Output
	o.usage = append([]byte(nil), v.Usage...)
	o.known = true
}

type sseUsage struct {
	line, event []byte
	size        int
	out         *gatewayOutcome
}

func (s *sseUsage) feed(b []byte) error {
	for len(b) > 0 {
		at := bytes.IndexByte(b, '\n')
		n := len(b)
		if at >= 0 {
			n = at
		}
		s.size += n + 1
		if s.size > 4<<20 {
			return errors.New("SSE event exceeds 4 MiB")
		}
		s.line = append(s.line, b[:n]...)
		if at < 0 {
			return nil
		}
		line := bytes.TrimSuffix(s.line, []byte{'\r'})
		if len(line) == 0 {
			s.consume()
			s.event = nil
			s.size = 0
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimPrefix(data, []byte(" "))
			s.event = append(s.event, data...)
			s.event = append(s.event, '\n')
		}
		s.line = nil
		b = b[n+1:]
	}
	return nil
}
func (s *sseUsage) consume() {
	data := bytes.TrimSpace(s.event)
	if len(data) == 0 || string(data) == "[DONE]" {
		return
	}
	var v struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &v) != nil {
		return
	}
	if v.Type == "response.completed" {
		s.out.state = "COMPLETE"
		s.out.code = ""
		s.out.readUsage(v.Response)
	} else if v.Type == "response.failed" || v.Type == "response.incomplete" || v.Type == "error" {
		s.out.state = "FAILED"
		s.out.code = strings.ToUpper(strings.ReplaceAll(v.Type, ".", "_"))
		s.out.readUsage(v.Response)
	}
}
