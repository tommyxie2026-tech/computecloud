package iuexperiment

import (
	"context"
	"encoding/json"
	"github.com/tommyxie2026-tech/computecloud/internal/graph"
	"testing"
	"time"
)

func TestIndependentVerificationContrastRetainsRejectedSamples(t *testing.T) {
	p := graph.RecordVerifierProfile{Version: "1", Kind: "source_json_equal", Scope: "paid", SourcePointer: "/paid", ClaimPointer: "/value", RecordKinds: []string{"observation"}, TimeoutMS: 100}
	src := graph.Source{ID: "order", Text: `{"paid":6000}`}
	src.SHA256 = graph.Hash([]byte(src.Text))
	samples := []VerificationObservation{}
	for _, tc := range []struct {
		id, payload string
		correct     bool
	}{{"valid", `{"value":6000}`, true}, {"wrong", `{"value":11000}`, false}, {"missing", `{}`, false}} {
		start := time.Now()
		result := graph.RunRecordVerifier(context.Background(), p, src, graph.MemoryRecord{ID: "claim", Kind: "observation", Payload: json.RawMessage(tc.payload), Validity: "VALID", Validation: "unchecked"})
		samples = append(samples, VerificationObservation{ID: tc.id, ExpectedCorrect: tc.correct, AcceptedWithoutVerifier: true, Verdict: result.Verdict, VerifierCalls: 1, VerifierMicros: time.Since(start).Microseconds()})
	}
	report := ContrastVerification(samples)
	if len(report.Samples) != 3 || report.WithoutFalseAccepts != 2 || report.WithFalseAccepts != 0 || report.MissedValidClaims != 0 || report.Unknown != 1 || report.VerifierCalls != 3 {
		t.Fatalf("contrast %+v", report)
	}
}
