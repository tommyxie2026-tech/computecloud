package main

import (
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/config"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/server"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryCLIUnknownNeverReadsOrPublishes(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(server.GraphStatus{Generation: 1, GoalID: "g", EvaluationID: "ev", Evaluation: json.RawMessage(`{"verdict":"UNKNOWN"}`)})
	}))
	defer srv.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	os.WriteFile(token, []byte("test-token-01234567890123456789"), 0600)
	recipe := filepath.Join(dir, "recipe.json")
	os.WriteFile(recipe, []byte(`{"version":"iu-recovery-recipe-v1","id":"repair","prior_plan_sha256":"expected","evaluation_id":"ev","evaluation_sha256":"expected","reads":[],"instructions":{}}`), 0600)
	e := runJob(context.Background(), config.Client{HTTPURL: srv.URL, TokenFile: token, TLS: config.TLS{InsecureLoopback: true}}, jobOptions{op: "recover-graph", id: "job", file: recipe})
	if e == nil || calls != 1 {
		t.Fatal("unknown side effects", calls, e)
	}
}
func TestRecoveryCLIKeepsStableReadsAcrossRestart(t *testing.T) { runRecoveryCLITest(t, false) }
func TestRecoveryCLIReleasesLeaseAndStopsOnCAS(t *testing.T)    { runRecoveryCLITest(t, true) }
func runRecoveryCLITest(t *testing.T, stopOnCAS bool) {
	raw, e := os.ReadFile("../../examples/iu-graph/coverage-v5/orders.json")
	if e != nil {
		t.Fatal(e)
	}
	spec, e := graph.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := graph.Compile(spec)
	if e != nil {
		t.Fatal(e)
	}
	src := plan.Spec.Sources[0]
	span := graph.Span{Source: src.ID, End: len(src.Text), SHA256: src.SHA256}
	proof := server.GraphCoverageReceipt{Version: "iu-coverage-receipt-v1", GoalID: "g", Generation: 1, CoverageResult: graph.CoverageResult{Verdict: "FAIL"}}
	ref := &server.GraphCoverageProofRef{ReceiptID: "proof", SHA256: graph.Hash(job.JSON(proof))}
	ev := server.GraphEvaluation{Verdict: "FAIL", CoverageProof: ref}
	st := server.GraphStatus{GoalID: "g", Generation: 1, Plan: plan, EvaluationID: "ev", Evaluation: job.JSON(ev)}
	var reads server.GraphSourceReadStatus
	posts, publishes, leases, releases := 0, 0, 0, 0
	var published []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/jobs/job/graph":
			json.NewEncoder(w).Encode(st)
		case r.URL.Path == "/v1/jobs/job/graph/coverage-verifications/proof":
			json.NewEncoder(w).Encode(proof)
		case r.Method == "GET" && r.URL.Path == "/v1/jobs/job/graph/source-reads/extract":
			json.NewEncoder(w).Encode(reads)
		case r.Method == "POST" && r.URL.Path == "/v1/jobs/job/graph/source-reads":
			posts++
			if stopOnCAS {
				w.WriteHeader(409)
				return
			}
			var req server.GraphSourceReadRequest
			json.NewDecoder(r.Body).Decode(&req)
			receipt := server.GraphSourceReadReceipt{ReceiptID: "stored-receipt", GoalID: "g", Generation: 1, PlanHash: plan.Digest, NodeID: "extract", Span: req.Span, Text: src.Text, SHA256: graph.Hash([]byte(src.Text)), ReadsUsed: 1}
			reads.ReadsUsed = 1
			reads.Reads = append(reads.Reads, server.GraphSourceReadEntry{Request: req, Receipt: receipt})
			json.NewEncoder(w).Encode(receipt)
		case r.URL.Path == "/v1/jobs/job/control-lease":
			if r.Method == "POST" {
				leases++
				json.NewEncoder(w).Encode(server.ControlWriteLease{JobID: "job", HolderID: "holder", LeaseToken: "lease", ExpiresAtMS: 123456789})
			} else {
				releases++
				w.Write([]byte(`{}`))
			}
		case r.URL.Path == "/v1/jobs/job/graph/recovery-proposals":
			publishes++
			if r.Header.Get("X-Control-Lease") != "lease" {
				t.Error("missing lease")
			}
			var body json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if published != nil && string(published) != string(body) {
				t.Error("proposal changed on restart")
			}
			published = body
			w.Write([]byte(`{"job":{"id":"next"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	os.WriteFile(token, []byte("test-token-01234567890123456789"), 0600)
	recipe := RecoveryRecipe{Version: "iu-recovery-recipe-v1", ID: "repair", PriorPlanHash: plan.Digest, EvaluationID: "ev", EvaluationHash: graph.Hash(st.Evaluation), Reads: []RecoveryRead{{NodeID: "extract", Span: span}}, Instructions: map[string]string{"extract": "\nReview frozen evidence again."}}
	file := filepath.Join(dir, "recipe.json")
	os.WriteFile(file, job.JSON(recipe), 0600)
	c := config.Client{HTTPURL: srv.URL, TokenFile: token, TLS: config.TLS{InsecureLoopback: true}}
	if stopOnCAS {
		e = runJob(context.Background(), c, jobOptions{op: "recover-graph", id: "job", file: file})
		if e == nil || posts != 1 || publishes != 0 || leases != 0 {
			t.Fatal("CAS did not stop", posts, publishes, leases, e)
		}
		return
	}
	for range 2 {
		if e = runJob(context.Background(), c, jobOptions{op: "recover-graph", id: "job", file: file}); e != nil {
			t.Fatal(e)
		}
	}
	if posts != 1 || publishes != 2 || leases != 2 || releases != 2 {
		t.Fatal(posts, publishes, leases)
	}
}
