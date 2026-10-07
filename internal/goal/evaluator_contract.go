package goal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type FailureClass string

const (
	FailureTest              FailureClass = "TEST_FAILURE"
	FailureMissingCapability FailureClass = "MISSING_CAPABILITY"
	FailurePermissionDenied  FailureClass = "PERMISSION_DENIED"
	FailureArchitecture      FailureClass = "ARCHITECTURE_CONFLICT"
	FailureDependency        FailureClass = "DEPENDENCY_UNAVAILABLE"
	FailurePolicyRejected    FailureClass = "POLICY_REJECTED"
	FailureTimeout           FailureClass = "TIMEOUT"
	FailureResourceExhausted FailureClass = "RESOURCE_EXHAUSTED"
	FailureInvalidAssumption FailureClass = "INVALID_ASSUMPTION"
	FailureUnknown           FailureClass = "UNKNOWN"
)

type Evidence struct {
	Type       string
	ArtifactID string
	Fact       string
}

type ReplanEvidence struct {
	FailureClass FailureClass
	Evidence     []Evidence
}

func (r ReplanEvidence) Validate() error {
	if !validFailureClass(r.FailureClass) {
		return fmt.Errorf("invalid failure_class")
	}
	if len(r.Evidence) == 0 || len(r.Evidence) > 32 {
		return fmt.Errorf("replan requires 1..32 evidence items")
	}
	for _, e := range r.Evidence {
		if strings.TrimSpace(e.Type) == "" || strings.TrimSpace(e.Fact) == "" {
			return fmt.Errorf("evidence type and fact are required")
		}
		if len(e.Type) > 64 || len(e.ArtifactID) > 128 || len(e.Fact) > 4096 {
			return fmt.Errorf("evidence field too large")
		}
	}
	return nil
}

func (r ReplanEvidence) Fingerprint() string {
	items := append([]Evidence(nil), r.Evidence...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Type != items[j].Type {
			return items[i].Type < items[j].Type
		}
		return strings.TrimSpace(items[i].Fact) < strings.TrimSpace(items[j].Fact)
	})
	type semanticEvidence struct {
		Type string
		Fact string
	}
	semantic := make([]semanticEvidence, 0, len(items))
	for _, item := range items {
		semantic = append(semantic, semanticEvidence{
			Type: strings.TrimSpace(item.Type),
			Fact: strings.TrimSpace(item.Fact),
		})
	}
	raw, _ := json.Marshal(struct {
		FailureClass FailureClass
		Evidence     []semanticEvidence
	}{FailureClass: r.FailureClass, Evidence: semantic})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type PlanCanonical struct {
	Strategy             string
	StrategyClass        string
	DependencySignature  string
	RequiredCapabilities []string
	KeyAssumptions       []string
	EvaluationStrategy   string
	SideEffectClass      string
	JobSpecHash          string
}

func (p PlanCanonical) Validate() error {
	if strings.TrimSpace(p.Strategy) == "" || strings.TrimSpace(p.StrategyClass) == "" || strings.TrimSpace(p.DependencySignature) == "" {
		return fmt.Errorf("plan strategy, strategy class and dependency signature are required")
	}
	if len(p.Strategy) > 4096 || len(p.StrategyClass) > 128 || len(p.DependencySignature) > 4096 || len(p.EvaluationStrategy) > 1024 || len(p.SideEffectClass) > 128 {
		return fmt.Errorf("plan canonical field too large")
	}
	if len(p.RequiredCapabilities) > 64 || len(p.KeyAssumptions) > 64 {
		return fmt.Errorf("too many plan canonical entries")
	}
	if p.JobSpecHash != "" && (len(p.JobSpecHash) != 64 || !hexHash(p.JobSpecHash)) {
		return fmt.Errorf("invalid plan job spec hash")
	}
	return nil
}

func hexHash(v string) bool {
	for _, c := range v {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (p PlanCanonical) Fingerprint() string {
	caps := append([]string(nil), p.RequiredCapabilities...)
	assumptions := append([]string(nil), p.KeyAssumptions...)
	sort.Strings(caps)
	sort.Strings(assumptions)
	raw, _ := json.Marshal(struct {
		Strategy             string
		StrategyClass        string
		DependencySignature  string
		RequiredCapabilities []string
		KeyAssumptions       []string
		EvaluationStrategy   string
		SideEffectClass      string
		JobSpecHash          string `json:",omitempty"`
	}{
		Strategy: p.Strategy, StrategyClass: p.StrategyClass, DependencySignature: p.DependencySignature,
		RequiredCapabilities: caps, KeyAssumptions: assumptions,
		EvaluationStrategy: p.EvaluationStrategy, SideEffectClass: p.SideEffectClass,
		JobSpecHash: p.JobSpecHash,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validFailureClass(v FailureClass) bool {
	switch v {
	case FailureTest, FailureMissingCapability, FailurePermissionDenied, FailureArchitecture,
		FailureDependency, FailurePolicyRejected, FailureTimeout, FailureResourceExhausted,
		FailureInvalidAssumption, FailureUnknown:
		return true
	default:
		return false
	}
}

type StrategyDelta struct {
	ChangedDimensions []string
	Summary           string
}

func (d StrategyDelta) Validate() error {
	if len(d.ChangedDimensions) == 0 || len(d.ChangedDimensions) > 16 {
		return fmt.Errorf("strategy_delta requires 1..16 changed dimensions")
	}
	if strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 2048 {
		return fmt.Errorf("strategy_delta summary required and bounded")
	}
	for _, v := range d.ChangedDimensions {
		if strings.TrimSpace(v) == "" || len(v) > 64 {
			return fmt.Errorf("invalid strategy_delta dimension")
		}
	}
	return nil
}

func (p PlanCanonical) StrategySignature() string {
	raw, _ := json.Marshal(struct {
		StrategyClass       string
		DependencySignature string
		SideEffectClass     string
	}{
		StrategyClass:       strings.TrimSpace(p.StrategyClass),
		DependencySignature: strings.TrimSpace(p.DependencySignature),
		SideEffectClass:     strings.TrimSpace(p.SideEffectClass),
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type ProgressSnapshot struct {
	AcceptedChecks      int
	FailedChecks        int
	UnknownChecks       int
	ResolvedAssumptions int
	UnresolvedBlockers  int
}

func (p ProgressSnapshot) Validate() error {
	if p.AcceptedChecks < 0 || p.FailedChecks < 0 || p.UnknownChecks < 0 || p.ResolvedAssumptions < 0 || p.UnresolvedBlockers < 0 {
		return fmt.Errorf("progress counters must be non-negative")
	}
	return nil
}

func (p ProgressSnapshot) ImprovedOver(prev ProgressSnapshot) bool {
	return p.AcceptedChecks > prev.AcceptedChecks ||
		p.FailedChecks < prev.FailedChecks ||
		p.UnknownChecks < prev.UnknownChecks ||
		p.ResolvedAssumptions > prev.ResolvedAssumptions ||
		p.UnresolvedBlockers < prev.UnresolvedBlockers
}
