package iuexperiment

// VerificationObservation records an explicitly defined ablation: accepting
// schema-valid candidates without the independent check versus scoped PASS.
// Fixture labels are not model intelligence measurements.
type VerificationObservation struct {
	ID                      string `json:"id"`
	ExpectedCorrect         bool   `json:"expected_correct"`
	AcceptedWithoutVerifier bool   `json:"accepted_without_verifier"`
	Verdict                 string `json:"verifier_verdict"`
	VerifierCalls           int    `json:"verifier_calls"`
	VerifierMicros          int64  `json:"verifier_us"`
	Retries                 int    `json:"retries"`
}
type VerificationContrast struct {
	Samples             []VerificationObservation `json:"samples"`
	WithoutFalseAccepts int                       `json:"without_false_accepts"`
	WithFalseAccepts    int                       `json:"with_false_accepts"`
	MissedValidClaims   int                       `json:"missed_valid_claims"`
	Unknown             int                       `json:"unknown"`
	VerifierCalls       int                       `json:"verifier_calls"`
	VerifierMicros      int64                     `json:"verifier_us"`
	Retries             int                       `json:"retries"`
}

func ContrastVerification(samples []VerificationObservation) VerificationContrast {
	out := VerificationContrast{Samples: append([]VerificationObservation{}, samples...)}
	for _, s := range samples {
		if !s.ExpectedCorrect && s.AcceptedWithoutVerifier {
			out.WithoutFalseAccepts++
		}
		if !s.ExpectedCorrect && s.Verdict == "PASS" {
			out.WithFalseAccepts++
		}
		if s.ExpectedCorrect && s.Verdict != "PASS" {
			out.MissedValidClaims++
		}
		if s.Verdict == "UNKNOWN" {
			out.Unknown++
		}
		out.VerifierCalls += s.VerifierCalls
		out.VerifierMicros += s.VerifierMicros
		out.Retries += s.Retries
	}
	return out
}
