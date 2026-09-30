package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/store"
	"github.com/tommyxie2026-tech/computecloud/internal/telemetry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTraceWorkerMetricsAndAccess(t *testing.T) {
	h := newJobHarness(t, true)
	err := createTrace(h.ctx, h.s.db.SQL, "trc_session", "owner", "project", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ id, owner, project string }{{"trc_other", "other", "project"}, {"trc_project", "owner", "forbidden"}} {
		if err = createTrace(h.ctx, h.s.db.SQL, v.id, v.owner, v.project, "conversation"); err != nil {
			t.Fatal(err)
		}
		if _, err = h.s.SubmitJob(withTrace(h.ctx, v.id), "reject-"+v.id, job.JSON(h.spec("single"))); status.Code(err) != codes.NotFound {
			t.Fatal(err)
		}
	}
	j, err := h.s.SubmitJob(withTrace(h.ctx, "trc_session"), "trace-job", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, j.ID)
	if got.State != "SUCCEEDED" || got.TraceID != "trc_session" || got.Usage["coverage"] != "complete" || got.Usage["input_tokens"] != int64(3) || got.Usage["output_tokens"] != int64(2) {
		t.Fatalf("%+v", got)
	}
	again, err := h.s.SubmitJob(h.ctx, "trace-job", job.JSON(h.spec("single")))
	if err != nil || again.ID != j.ID || again.TraceID != j.TraceID {
		t.Fatalf("retry %v %v", again, err)
	}
	err = createTrace(h.ctx, h.s.db.SQL, "trc_different", "owner", "project", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.SubmitJob(withTrace(h.ctx, "trc_different"), "trace-job", job.JSON(h.spec("single"))); status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		_, err = h.s.db.SQL.Exec(`INSERT INTO gateway_requests(id,owner,project,route,model,endpoint,state,started,input_tokens,output_tokens,usage_complete,trace_id) VALUES(?,'model-owner','project','r','m','/v1/responses','COMPLETE',10,7,2,1,'trc_session')`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	trace, err := h.s.JobTrace(h.ctx, j.ID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if trace["has_more"] != true || trace["controller_usage"].(map[string]any)["input_tokens"] != int64(21) {
		t.Fatalf("%v", trace)
	}
	attempts := trace["attempts"].([]TraceAttempt)
	if len(attempts) != 1 || attempts[0].Metrics == nil || attempts[0].Metrics.Process == nil || attempts[0].Metrics.Process.PeakRSSBytes <= 0 {
		t.Fatalf("%+v", attempts)
	}
	next, err := h.s.JobTrace(h.ctx, j.ID, trace["next_cursor"].(string), 2)
	if err != nil || len(next["model_requests"].([]TraceRequest)) != 1 || next["has_more"] != false {
		t.Fatalf("%v %v", next, err)
	}
	// Unknown/invalid cursor must not expand access or expose another trace.
	if _, err = h.s.JobTrace(h.ctx, j.ID, "not-owned", 2); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	_, err = h.s.db.SQL.Exec("UPDATE jobs SET owner='other' WHERE id=?", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.JobTrace(h.ctx, j.ID, "", 20); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	list, err := h.s.ListJobs(h.ctx, "", 20)
	if err != nil || len(list["jobs"].([]map[string]any)) != 0 {
		t.Fatalf("%v %v", list, err)
	}
}
func TestTraceListPaginationAndMCP(t *testing.T) {
	h := newJobHarness(t, false)
	for _, key := range []string{"first", "second", "third"} {
		if _, err := h.s.SubmitJob(h.ctx, key, job.JSON(h.spec("single"))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := h.s.ListJobs(h.ctx, "", 2)
	if err != nil || list["has_more"] != true {
		t.Fatalf("%v %v", list, err)
	}
	page, err := h.s.ListJobs(h.ctx, list["next_cursor"].(string), 2)
	if err != nil || len(page["jobs"].([]map[string]any)) != 1 {
		t.Fatalf("%v %v", page, err)
	}
	code, b := h.request(t, "POST", "/mcp", "", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_jobs","arguments":{"limit":2}}}`))
	if code != 200 || !strings.Contains(string(b), `"has_more":true`) || strings.Contains(string(b), `"isError":true`) {
		t.Fatalf("%d %s", code, b)
	}
}
func TestTraceGatewayConversationIsolation(t *testing.T) {
	s, token := cliGatewayFixture(t)
	w := cliCall(t, s, token, `{"model":"model","input":"hello","prompt_cache_key":"session-a"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	trace := w.Header().Get("X-Computecloud-Trace-ID")
	if trace == "" {
		t.Fatal("missing trace")
	}
	w = cliCall(t, s, token, `{"model":"model","input":"hello","prompt_cache_key":"session-a"}`)
	if w.Header().Get("X-Computecloud-Trace-ID") != trace {
		t.Fatal("conversation not stable")
	}
	w = cliCall(t, s, token, `{"model":"model","input":"hello","prompt_cache_key":"session-b"}`)
	if w.Header().Get("X-Computecloud-Trace-ID") == trace {
		t.Fatal("sessions merged")
	}
	if err := createTrace(context.Background(), s.db.SQL, "trc_foreign", "other", "p", "conversation"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"model","input":"hello"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Computecloud-Trace-ID", "trc_foreign")
	w = httptest.NewRecorder()
	s.modelHandler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestTraceUnknownMetricsAreNotZero(t *testing.T) {
	h := newJobHarness(t, true)
	j, err := h.s.SubmitJob(h.ctx, "unknown", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	h.wait(t, j.ID)
	if _, err = h.s.db.SQL.Exec("DELETE FROM attempt_metrics"); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.GetJob(h.ctx, j.ID)
	if err != nil || got.Usage["coverage"] != "unavailable" || got.Usage["input_tokens"] != nil {
		t.Fatalf("%v %v", got, err)
	}
	attempts, err := h.s.traceAttempts(h.ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(telemetry.Attempt{Source: "codex_exec", UsageComplete: true, NativeFinal: true, Usage: &telemetry.Tokens{Input: 3, Output: 2}})
	if err = h.s.db.Tx(h.ctx, func(q store.Query) error { return saveAttemptMetrics(h.ctx, q, attempts[0].AttemptID, raw) }); err != nil {
		t.Fatal(err)
	}
	got, err = h.s.GetJob(h.ctx, j.ID)
	if err != nil || got.Usage["coverage"] != "complete" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestTraceMetricsAcceptProviderProfileAndRejectSpoofedSource(t *testing.T) {
	h := newJobHarness(t, true)
	j, err := h.s.SubmitJob(h.ctx, "custom-metrics", job.JSON(h.spec("single")))
	if err != nil {
		t.Fatal(err)
	}
	h.wait(t, j.ID)
	attempts, err := h.s.traceAttempts(h.ctx, j.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	a := attempts[0]
	if _, err = h.s.db.SQL.Exec("DELETE FROM attempt_metrics WHERE attempt_id=?", a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = h.s.db.SQL.Exec("UPDATE tasks SET spec=json_set(spec,'$.runtime_profile','remote_fixture') WHERE id=?", a.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"codex_exec", "remote_fixture"} {
		raw := job.JSON(telemetry.Attempt{Source: source})
		err = h.s.db.Tx(h.ctx, func(q store.Query) error { return saveAttemptMetrics(h.ctx, q, a.AttemptID, raw) })
		if source == "codex_exec" && status.Code(err) != codes.InvalidArgument {
			t.Fatalf("spoofed source: %v", err)
		}
		if source == "remote_fixture" && err != nil {
			t.Fatalf("provider rejected: %v", err)
		}
	}
}
