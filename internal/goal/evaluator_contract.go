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
	DependencySignature  string
	RequiredCapabilities []string
	KeyAssumptions       []string
	EvaluationStrategy   string
	SideEffectClass      string
}

func (p PlanCanonical) Validate() error {
	if strings.TrimSpace(p.Strategy) == "" || strings.TrimSpace(p.DependencySignature) == "" {
		return fmt.Errorf("plan strategy and dependency signature are required")
	}
	if len(p.Strategy) > 4096 || len(p.DependencySignature) > 4096 || len(p.EvaluationStrategy) > 1024 || len(p.SideEffectClass) > 128 {
		return fmt.Errorf("plan canonical field too large")
	}
	if len(p.RequiredCapabilities) > 64 || len(p.KeyAssumptions) > 64 {
		return fmt.Errorf("too many plan canonical entries")
	}
	return nil
}

func (p PlanCanonical) Fingerprint() string {
	caps := append([]string(nil), p.RequiredCapabilities...)
	assumptions := append([]string(nil), p.KeyAssumptions...)
	sort.Strings(caps)
	sort.Strings(assumptions)
	raw, _ := json.Marshal(struct {
		Strategy             string
		DependencySignature  string
		RequiredCapabilities []string
		KeyAssumptions       []string
		EvaluationStrategy   string
		SideEffectClass      string
	}{
		Strategy: p.Strategy, DependencySignature: p.DependencySignature,
		RequiredCapabilities: caps, KeyAssumptions: assumptions,
		EvaluationStrategy: p.EvaluationStrategy, SideEffectClass: p.SideEffectClass,
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
