package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
)

type cliSSE struct {
	mu       sync.Mutex
	w        http.ResponseWriter
	cancel   context.CancelFunc
	seq      int
	err      error
	done     chan struct{}
	stopped  chan struct{}
	response map[string]any
}

func newCLISSE(w http.ResponseWriter, ctx context.Context, cancel context.CancelFunc, rid, model string) *cliSSE {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Computecloud-Backend", "codex_cli")
	e := &cliSSE{w: w, cancel: cancel, done: make(chan struct{}), stopped: make(chan struct{}), response: map[string]any{"id": "resp_" + rid, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress", "model": model, "output": []any{}, "error": nil}}
	_ = e.event("response.created", map[string]any{"response": e.response})
	_ = e.event("response.in_progress", map[string]any{"response": e.response})
	go func() {
		defer close(e.stopped)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.done:
				return
			case <-ticker.C:
				e.mu.Lock()
				if e.err == nil {
					e.err = e.write(": keepalive\n\n")
				}
				if e.err != nil {
					cancel()
				}
				e.mu.Unlock()
			}
		}
	}()
	return e
}
func (e *cliSSE) close() { close(e.done); <-e.stopped }
func (e *cliSSE) write(s string) error {
	_ = http.NewResponseController(e.w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := fmt.Fprint(e.w, s); err != nil {
		return err
	}
	return http.NewResponseController(e.w).Flush()
}
func (e *cliSSE) event(typ string, v map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	v["type"] = typ
	v["sequence_number"] = e.seq
	e.seq++
	e.err = e.write("event: " + typ + "\ndata: " + string(job.JSON(v)) + "\n\n")
	if e.err != nil {
		e.cancel()
	}
	return e.err
}
func (e *cliSSE) failed(code string) {
	r := map[string]any{}
	for k, v := range e.response {
		r[k] = v
	}
	r["status"] = "failed"
	r["error"] = map[string]any{"code": code, "message": code}
	_ = e.event("response.failed", map[string]any{"response": r})
}
func (e *cliSSE) completed(response map[string]any, output []map[string]any) error {
	for index, item := range output {
		initial := map[string]any{}
		for k, v := range item {
			initial[k] = v
		}
		initial["status"] = "in_progress"
		typ := item["type"].(string)
		if typ == "message" {
			initial["content"] = []any{}
		} else if typ == "function_call" {
			initial["arguments"] = ""
		} else {
			initial["input"] = ""
		}
		if err := e.event("response.output_item.added", map[string]any{"output_index": index, "item": initial}); err != nil {
			return err
		}
		if typ == "message" {
			part := item["content"].([]any)[0].(map[string]any)
			text := part["text"].(string)
			if err := e.event("response.content_part.added", map[string]any{"output_index": index, "item_id": item["id"], "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}}); err != nil {
				return err
			}
			if err := e.event("response.output_text.delta", map[string]any{"output_index": index, "item_id": item["id"], "content_index": 0, "delta": text, "logprobs": []any{}}); err != nil {
				return err
			}
			if err := e.event("response.output_text.done", map[string]any{"output_index": index, "item_id": item["id"], "content_index": 0, "text": text, "logprobs": []any{}}); err != nil {
				return err
			}
			if err := e.event("response.content_part.done", map[string]any{"output_index": index, "item_id": item["id"], "content_index": 0, "part": part}); err != nil {
				return err
			}
		} else {
			prefix, key := "response.function_call_arguments", "arguments"
			if typ == "custom_tool_call" {
				prefix, key = "response.custom_tool_call_input", "input"
			}
			if err := e.event(prefix+".delta", map[string]any{"output_index": index, "item_id": item["id"], "delta": item[key]}); err != nil {
				return err
			}
			if err := e.event(prefix+".done", map[string]any{"output_index": index, "item_id": item["id"], key: item[key]}); err != nil {
				return err
			}
		}
		if err := e.event("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	return e.event("response.completed", map[string]any{"response": response})
}
