package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tommyxie2026-tech/computecloud/internal/job"
	"github.com/tommyxie2026-tech/computecloud/internal/jsonutil"
	"math"
	"reflect"
	"time"
	"unicode/utf8"
)

var OrderCoverageChecks = []string{"source_identity", "source_fields", "customer_grouping", "threshold_once", "exception_rules", "currency_unit"}

type CoverageVerifierProfile struct {
	Version   string `json:"version" yaml:"version"`
	Kind      string `json:"kind" yaml:"kind"`
	Scope     string `json:"scope" yaml:"scope"`
	TimeoutMS int    `json:"timeout_ms" yaml:"timeout_ms"`
}

func (p CoverageVerifierProfile) Valid() bool {
	return job.Ref(p.Version) && keyPattern.MatchString(p.Scope) && (p.Kind == "orders-v1" || p.Kind == "unavailable") && p.TimeoutMS > 0 && p.TimeoutMS <= 1000
}

type CoverageCheckResult struct {
	ID           string `json:"check_id"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	EvidenceHash string `json:"evidence_sha256"`
}
type CoverageResult struct {
	Verdict      string                `json:"verdict"`
	Checks       []CoverageCheckResult `json:"checks"`
	EvidenceHash string                `json:"evidence_sha256"`
}
type CoverageOrder struct {
	ID       string `json:"order_id"`
	Customer string `json:"customer_id"`
	Paid     *int64 `json:"paid_cents"`
	Special  *bool  `json:"is_special"`
}
type CoverageOrderResult struct {
	Orders   []CoverageOrder `json:"orders"`
	Total    *int64          `json:"total_cents"`
	Currency string          `json:"currency"`
	Unit     string          `json:"unit"`
}
type coverageGoal struct {
	Version    string   `json:"version"`
	Currency   string   `json:"currency"`
	Unit       string   `json:"unit"`
	Orders     []string `json:"order_source_ids"`
	Rule       string   `json:"rule_source_id"`
	Exceptions []string `json:"exception_source_ids"`
	Required   []string `json:"required_checks"`
}
type coverageRule struct {
	Threshold *int64 `json:"threshold_cents"`
	Discount  *int64 `json:"discount_cents"`
	Max       *int64 `json:"max_discounts_per_customer"`
}

func validCoverageOrder(o CoverageOrder) bool {
	return o.ID != "" && len(o.ID) <= 256 && o.Customer != "" && len(o.Customer) <= 256 && o.Paid != nil && *o.Paid >= 0 && o.Special != nil
}
func unknownCoverage(s Spec, reason string) CoverageResult {
	r := CoverageResult{Verdict: "UNKNOWN", Checks: []CoverageCheckResult{}}
	if s.Coverage != nil {
		for _, c := range s.Coverage.Checks {
			r.Checks = append(r.Checks, CoverageCheckResult{ID: c.ID, Verdict: "UNKNOWN", Reason: reason})
		}
	}
	r.EvidenceHash = Hash(canonical(r))
	return r
}

// The verifier only interprets iu-orders-goal-v1. Opaque natural language is unknown.
func RunCoverageVerifier(ctx context.Context, p CoverageVerifierProfile, s Spec, values map[string]json.RawMessage) CoverageResult {
	unknown := func(reason string) CoverageResult { return unknownCoverage(s, reason) }
	if !p.Valid() || p.Kind == "unavailable" || ctx.Err() != nil || s.Coverage == nil {
		return unknown("VERIFIER_UNAVAILABLE")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	c := s.Coverage
	if c.ProfileVersion != p.Version || c.Scope != p.Scope || c.SHA256 != c.Digest() {
		return unknown("CATALOG_BINDING_MISMATCH")
	}
	sources := map[string]Source{}
	for _, src := range s.Sources {
		if _, ok := sources[src.ID]; ok || src.SHA256 != Hash([]byte(src.Text)) {
			return unknown("SOURCE_AMBIGUOUS")
		}
		sources[src.ID] = src
	}
	goalSource, ok := sources[c.GoalSourceID]
	if !ok || goalSource.SHA256 != c.GoalSpecHash {
		return unknown("GOAL_SOURCE_MISSING")
	}
	var g coverageGoal
	if decodeCoverageJSON([]byte(goalSource.Text), &g) != nil || g.Version != "iu-orders-goal-v1" || len(g.Currency) != 3 || g.Unit != "cent" || len(g.Orders) < 1 || len(g.Orders) > 32 || g.Exceptions == nil {
		return unknown("GOAL_UNSUPPORTED")
	}
	required := map[string]bool{}
	for _, id := range OrderCoverageChecks {
		required[id] = true
	}
	have := map[string]bool{}
	for _, id := range g.Required {
		if !required[id] || have[id] {
			return unknown("GOAL_CHECKS_INCOMPLETE")
		}
		have[id] = true
	}
	if len(have) != len(required) || len(c.Checks) != len(required) {
		return unknown("CATALOG_INCOMPLETE")
	}
	bindings := map[string]CoverageCheckBinding{}
	for _, b := range c.Checks {
		if !required[b.ID] || len(b.Outputs) < 1 || len(b.Outputs) > 32 {
			return unknown("CATALOG_INCOMPLETE")
		}
		if _, ok := bindings[b.ID]; ok {
			return unknown("CATALOG_AMBIGUOUS")
		}
		bindings[b.ID] = b
	}
	used := map[string]bool{c.GoalSourceID: true}
	read := func(id string, target any) bool {
		src, ok := sources[id]
		if !ok || used[id] {
			return false
		}
		used[id] = true
		return decodeCoverageJSON([]byte(src.Text), target) == nil
	}
	originals := map[string]CoverageOrder{}
	for _, id := range g.Orders {
		var o CoverageOrder
		if !read(id, &o) || !validCoverageOrder(o) {
			return unknown("ORDER_AMBIGUOUS")
		}
		if old, ok := originals[o.ID]; ok && string(canonical(old)) != string(canonical(o)) {
			return unknown("ORDER_CONFLICT")
		}
		originals[o.ID] = o
	}
	var rule coverageRule
	if !read(g.Rule, &rule) || rule.Threshold == nil || rule.Discount == nil || rule.Max == nil || *rule.Threshold < 1 || *rule.Discount < 0 || *rule.Max != 1 {
		return unknown("RULE_UNSUPPORTED")
	}
	specialExcluded := false
	for _, id := range g.Exceptions {
		var e struct {
			Type string `json:"type"`
		}
		if !read(id, &e) || e.Type != "special_no_discount" {
			return unknown("EXCEPTION_UNSUPPORTED")
		}
		specialExcluded = true
	}
	if len(used) != len(sources) {
		return unknown("UNCLASSIFIED_SOURCE")
	}
	var total int64
	groups := map[string]int64{}
	for _, o := range originals {
		if *o.Paid > math.MaxInt64-total {
			return unknown("AMOUNT_OVERFLOW")
		}
		total += *o.Paid
		if !specialExcluded || !*o.Special {
			if *o.Paid > math.MaxInt64-groups[o.Customer] {
				return unknown("AMOUNT_OVERFLOW")
			}
			groups[o.Customer] += *o.Paid
		}
	}
	for _, amount := range groups {
		if amount >= *rule.Threshold {
			discount := *rule.Discount
			if discount > amount {
				discount = amount
			}
			total -= discount
		}
	}
	r := CoverageResult{Verdict: "PASS", Checks: []CoverageCheckResult{}}
	for _, id := range OrderCoverageChecks {
		verdict, reason := "PASS", "CHECK_MATCHES"
		b := bindings[id]
		collected := map[string]CoverageOrder{}
		for _, out := range b.Outputs {
			raw, ok := values[out.Node+"/"+out.Port]
			var actual CoverageOrderResult
			if !ok || len(raw) > 64<<10 || decodeCoverageJSON(raw, &actual) != nil || actual.Total == nil || actual.Orders == nil {
				if verdict != "FAIL" {
					verdict, reason = "UNKNOWN", "OUTPUT_INCOMPLETE"
				}
				continue
			}
			if id == "threshold_once" || id == "customer_grouping" || id == "exception_rules" {
				if *actual.Total != total {
					verdict = "FAIL"
					reason = "TOTAL_DIFFERS"
				}
			}
			if id == "currency_unit" && (actual.Currency != g.Currency || actual.Unit != g.Unit) {
				verdict = "FAIL"
				reason = "UNIT_DIFFERS"
			}
			for _, o := range actual.Orders {
				if !validCoverageOrder(o) {
					if verdict != "FAIL" {
						verdict, reason = "UNKNOWN", "OUTPUT_FIELDS_INCOMPLETE"
					}
					continue
				}
				expected, ok := originals[o.ID]
				if !ok {
					verdict = "FAIL"
					reason = "ORDER_ID_DIFFERS"
					continue
				}
				if old, ok := collected[o.ID]; ok && string(canonical(old)) != string(canonical(o)) {
					verdict = "FAIL"
					reason = "OUTPUT_ORDER_CONFLICT"
				}
				collected[o.ID] = o
				if id == "source_fields" || id == "customer_grouping" || id == "exception_rules" {
					if string(canonical(expected)) != string(canonical(o)) {
						verdict = "FAIL"
						reason = "SOURCE_FIELDS_DIFFER"
					}
				}
			}
		}
		if verdict == "PASS" && len(collected) != len(originals) {
			verdict = "FAIL"
			reason = "ORDER_COVERAGE_DIFFERS"
		}
		evidence := Hash(canonical(struct {
			Check    string
			Goal     string
			Values   map[string]json.RawMessage
			Expected int64
		}{id, c.GoalSpecHash, values, total}))
		r.Checks = append(r.Checks, CoverageCheckResult{id, verdict, reason, evidence})
		if verdict == "FAIL" {
			r.Verdict = "FAIL"
		} else if verdict == "UNKNOWN" && r.Verdict != "FAIL" {
			r.Verdict = "UNKNOWN"
		}
	}
	if ctx.Err() != nil {
		return unknown("VERIFIER_TIMEOUT")
	}
	r.EvidenceHash = Hash(canonical(struct {
		Profile CoverageVerifierProfile
		Catalog string
		Sources []Source
		Result  []CoverageCheckResult
	}{p, c.SHA256, s.Sources, r.Checks}))
	return r
}

// Go's decoder accepts case-insensitive aliases; this business contract does not.
func decodeCoverageJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF-8")
	}
	if err := coverageJSONShape(raw, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	return jsonutil.Decode(raw, target)
}

// Check exact keys at every struct boundary, including elements in output arrays.
func coverageJSONShape(raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if string(raw) == "null" {
		return nil // Required business values are checked after decoding.
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := jsonutil.Decode(raw, &fields); err != nil {
			return err
		}
		if len(fields) != typ.NumField() {
			return fmt.Errorf("coverage fields incomplete or unknown")
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			value, ok := fields[field.Tag.Get("json")]
			if !ok {
				return fmt.Errorf("coverage field name mismatch")
			}
			if err := coverageJSONShape(value, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := jsonutil.Decode(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := coverageJSONShape(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
