package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicSSEEventLimit = 1 << 20
const anthropicStreamLimit = 16 << 20

type anthropicSSEUsage struct {
	line, data, event []byte
	eventBytes        int
	out               *gatewayOutcome
	started           bool
	messageDelta      bool
	stopped           bool
	failed            bool
	input             int64
	output            int64
	cacheCreate       int64
	cacheRead         int64
	hasInput          bool
	hasOutput         bool
}

func (s *anthropicSSEUsage) feed(chunk []byte) error {
	for len(chunk) > 0 {
		at := bytes.IndexByte(chunk, '\n')
		n := len(chunk)
		if at >= 0 {
			n = at
		}
		s.eventBytes += n + 1
		if s.eventBytes > anthropicSSEEventLimit || len(s.line)+n > 64<<10 {
			return errors.New("ANTHROPIC_SSE_EVENT_LIMIT")
		}
		s.line = append(s.line, chunk[:n]...)
		if at < 0 {
			return nil
		}
		line := bytes.TrimSuffix(s.line, []byte{'\r'})
		s.line = nil
		if len(line) == 0 {
			if err := s.consume(); err != nil {
				return err
			}
			s.data = nil
			s.event = nil
			s.eventBytes = 0
		} else if bytes.HasPrefix(line, []byte{':'}) {
			// SSE comments are keepalive pings and carry no protocol state.
		} else if bytes.HasPrefix(line, []byte("event:")) {
			if len(s.event) != 0 {
				return errors.New("ANTHROPIC_SSE_DUPLICATE_EVENT")
			}
			s.event = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:")))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimPrefix(data, []byte{' '})
			s.data = append(s.data, data...)
			s.data = append(s.data, '\n')
		} else {
			return errors.New("ANTHROPIC_SSE_FIELD_UNSUPPORTED")
		}
		chunk = chunk[n+1:]
	}
	return nil
}

func (s *anthropicSSEUsage) consume() error {
	data := bytes.TrimSpace(s.data)
	if len(data) == 0 {
		if len(s.event) != 0 {
			return errors.New("ANTHROPIC_SSE_EVENT_WITHOUT_DATA")
		}
		return nil
	}
	if len(data) > anthropicSSEEventLimit {
		return errors.New("ANTHROPIC_SSE_EVENT_LIMIT")
	}
	var envelope struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Usage   json.RawMessage `json:"usage"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type == "" || (len(s.event) > 0 && envelope.Type != string(s.event)) {
		return errors.New("ANTHROPIC_SSE_INVALID_EVENT")
	}
	if s.stopped {
		return errors.New("ANTHROPIC_SSE_AFTER_MESSAGE_STOP")
	}
	switch envelope.Type {
	case "ping":
		return nil
	case "message_start":
		if s.started || len(envelope.Message) == 0 {
			return errors.New("ANTHROPIC_SSE_INVALID_MESSAGE_START")
		}
		var message struct {
			Type  string          `json:"type"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(envelope.Message, &message) != nil || message.Type != "message" {
			return errors.New("ANTHROPIC_SSE_INVALID_MESSAGE_START")
		}
		if err := s.applyUsage(message.Usage, false); err != nil || !s.hasInput {
			return errors.New("ANTHROPIC_SSE_INVALID_USAGE")
		}
		s.started = true
	case "content_block_start", "content_block_delta", "content_block_stop":
		if !s.started {
			return errors.New("ANTHROPIC_SSE_EVENT_ORDER")
		}
	case "message_delta":
		if !s.started || len(envelope.Usage) == 0 {
			return errors.New("ANTHROPIC_SSE_INVALID_MESSAGE_DELTA")
		}
		if err := s.applyUsage(envelope.Usage, true); err != nil || !s.hasOutput {
			return errors.New("ANTHROPIC_SSE_INVALID_USAGE")
		}
		s.messageDelta = true
	case "message_stop":
		if !s.started || !s.messageDelta || !s.hasInput || !s.hasOutput {
			return errors.New("ANTHROPIC_SSE_INCOMPLETE_MESSAGE")
		}
		s.stopped = true
		usage, err := json.Marshal(map[string]int64{
			"input_tokens":                s.input - s.cacheCreate - s.cacheRead,
			"cache_creation_input_tokens": s.cacheCreate,
			"cache_read_input_tokens":     s.cacheRead,
			"output_tokens":               s.output,
		})
		if err != nil {
			return err
		}
		totalInput := s.input
		s.out.input = &totalInput
		output := s.output
		s.out.output = &output
		s.out.usage = usage
		s.out.known = true
		s.out.state = "COMPLETE"
		s.out.code = ""
	case "error":
		s.failed = true
		s.out.state = "FAILED"
		s.out.code = "UPSTREAM_STREAM_ERROR"
	default:
		// Unknown events are passed through unchanged, as allowed by the SSE
		// protocol's forward-compatible event model. They cannot settle usage.
	}
	return nil
}

func (s *anthropicSSEUsage) applyUsage(raw json.RawMessage, requireOutput bool) error {
	if len(raw) == 0 {
		return errors.New("usage missing")
	}
	var usage struct {
		Input       *int64 `json:"input_tokens"`
		Output      *int64 `json:"output_tokens"`
		CacheCreate *int64 `json:"cache_creation_input_tokens"`
		CacheRead   *int64 `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return errors.New("invalid usage")
	}
	if usage.Input != nil {
		if *usage.Input < 0 {
			return errors.New("negative input usage")
		}
		create, read := int64(0), int64(0)
		if usage.CacheCreate != nil {
			create = *usage.CacheCreate
		}
		if usage.CacheRead != nil {
			read = *usage.CacheRead
		}
		if create < 0 || read < 0 {
			return errors.New("negative cache usage")
		}
		total, ok := addNonNegative(*usage.Input, create)
		if !ok {
			return errors.New("input usage overflow")
		}
		total, ok = addNonNegative(total, read)
		if !ok {
			return errors.New("input usage overflow")
		}
		s.input, s.cacheCreate, s.cacheRead = total, create, read
		s.hasInput = true
	}
	if usage.Output != nil {
		if *usage.Output < 0 || (requireOutput && usage.Output == nil) {
			return errors.New("invalid output usage")
		}
		s.output = *usage.Output
		s.hasOutput = true
	}
	if requireOutput && usage.Output == nil {
		return errors.New("output usage missing")
	}
	return nil
}

func (s *anthropicSSEUsage) finish() error {
	if len(s.line) != 0 || len(s.data) != 0 || len(s.event) != 0 {
		return errors.New("ANTHROPIC_SSE_TRUNCATED_EVENT")
	}
	if s.failed {
		return nil
	}
	if !s.stopped || !s.out.known {
		return errors.New("ANTHROPIC_SSE_FINAL_USAGE_MISSING")
	}
	return nil
}

func (g *modelGateway) streamAnthropicMessages(w http.ResponseWriter, r *http.Request, upstream *http.Response, out *gatewayOutcome) {
	if upstream.StatusCode != http.StatusOK || !strings.HasPrefix(upstream.Header.Get("Content-Type"), "text/event-stream") {
		out.state = "UNKNOWN"
		out.code = "UPSTREAM_STREAM_INVALID"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", out.code)
		return
	}
	if upstream.Header.Get("Content-Encoding") != "" && upstream.Header.Get("Content-Encoding") != "identity" {
		out.code = "UNSUPPORTED_UPSTREAM_ENCODING"
		writeAnthropicError(w, http.StatusBadGateway, "api_error", out.code)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	observer := &anthropicSSEUsage{out: out}
	buf := make([]byte, 16<<10)
	total := 0
	idle := time.Duration(g.s.cfg.ModelGateway.StreamIdleTimeoutSeconds) * time.Second
	timer := time.AfterFunc(idle, func() { _ = upstream.Body.Close() })
	defer timer.Stop()
	for {
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			total += n
			if total > anthropicStreamLimit {
				out.state = "UNKNOWN"
				out.code = "ANTHROPIC_STREAM_LIMIT"
				return
			}
			timer.Reset(idle)
			if parseErr := observer.feed(buf[:n]); parseErr != nil {
				out.state = "UNKNOWN"
				out.code = errorCode(parseErr)
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				out.state = "UNKNOWN"
				out.code = "DOWNSTREAM_DISCONNECTED"
				return
			}
			if flushErr := controller.Flush(); flushErr != nil {
				out.state = "UNKNOWN"
				out.code = "DOWNSTREAM_DISCONNECTED"
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				out.state = "UNKNOWN"
				out.code = "UPSTREAM_INTERRUPTED"
				return
			}
			if finishErr := observer.finish(); finishErr != nil {
				out.state = "UNKNOWN"
				out.code = errorCode(finishErr)
				return
			}
			if observer.failed {
				out.state = "UNKNOWN"
				out.known = false
				out.code = "UPSTREAM_STREAM_ERROR"
			}
			return
		}
	}
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if value == "" || len(value) > 128 {
		return "ANTHROPIC_STREAM_INVALID"
	}
	for _, r := range value {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return "ANTHROPIC_STREAM_INVALID"
		}
	}
	return value
}
