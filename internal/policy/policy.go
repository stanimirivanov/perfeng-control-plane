// Package policy parses approved performance policies and verifies report verdicts.
package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumPolicyBytes = 1 << 20

var metricName = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// Document is one versioned, non-blocking performance policy.
type Document struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata identifies a policy and its owning team.
type Metadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Owner   string `json:"owner"`
}

// Spec defines non-blocking behavior and the complete ordered rule set.
type Spec struct {
	Mode        string `json:"mode"`
	MissingData string `json:"missingData"`
	Rules       []Rule `json:"rules"`
}

// Rule evaluates one unique metric and statistic.
type Rule struct {
	ID         string              `json:"id"`
	Metric     Metric              `json:"metric"`
	Quality    *QualityRequirement `json:"quality,omitempty"`
	SLO        *SLORequirement     `json:"slo,omitempty"`
	Regression *Regression         `json:"regression,omitempty"`
}

// Metric selects one named statistic and unit.
type Metric struct {
	Name      string `json:"name"`
	Statistic string `json:"statistic"`
	Unit      string `json:"unit"`
}

// QualityRequirement defines optional minimum evidence bounds.
type QualityRequirement struct {
	MinSamples *int64   `json:"minSamples,omitempty"`
	MaxCV      *float64 `json:"maxCv,omitempty"`
}

// SLORequirement defines an inclusive minimum, maximum, or both.
type SLORequirement struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// Regression defines direction, practical difference and pinned reference.
type Regression struct {
	Direction           string              `json:"direction"`
	PracticalDifference PracticalDifference `json:"practicalDifference"`
	Reference           Reference           `json:"reference"`
}

// PracticalDifference defines the minimum absolute or relative degradation.
type PracticalDifference struct {
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
}

// Reference pins one baseline ID and version.
type Reference struct {
	BaselineID string `json:"baselineId"`
	Version    string `json:"version"`
}

// BaselineResolution binds one policy reference to approved evidence, or nil
// when the exact baseline version is unavailable for the candidate context.
type BaselineResolution struct {
	ID       string
	Version  string
	Artifact *run.Artifact
}

// Parse validates one bounded, duplicate-safe policy document.
func Parse(data []byte) (Document, error) {
	if !jsondocument.Valid(data, maximumPolicyBytes) || !validShape(data) {
		return Document{}, run.ErrValidation
	}
	var document Document
	if !jsondocument.Decode(data, &document) || document.Validate() != nil {
		return Document{}, run.ErrValidation
	}

	return document, nil
}

// Validate checks policy identity, rule uniqueness, bounds and references.
func (document Document) Validate() error {
	if document.APIVersion != "performance.perfeng.io/v1" ||
		document.Kind != "PerformancePolicy" ||
		!rawresult.ValidResourceID(document.Metadata.Name) ||
		!rawresult.ValidContractsVersion(document.Metadata.Version) ||
		!validText(document.Metadata.Owner) ||
		(document.Spec.Mode != "observe" && document.Spec.Mode != "inform") ||
		document.Spec.MissingData != "inconclusive" || len(document.Spec.Rules) == 0 {
		return run.ErrValidation
	}

	ids := make(map[string]struct{}, len(document.Spec.Rules))
	selectors := make(map[[2]string]struct{}, len(document.Spec.Rules))
	for _, rule := range document.Spec.Rules {
		if rule.validate() != nil {
			return run.ErrValidation
		}
		if _, exists := ids[rule.ID]; exists {
			return run.ErrValidation
		}
		selector := [2]string{rule.Metric.Name, rule.Metric.Statistic}
		if _, exists := selectors[selector]; exists {
			return run.ErrValidation
		}
		ids[rule.ID] = struct{}{}
		selectors[selector] = struct{}{}
	}

	return nil
}

// BaselineReferences returns each distinct pinned regression reference in rule order.
func (document Document) BaselineReferences() []Reference {
	references := make([]Reference, 0, len(document.Spec.Rules))
	seen := make(map[Reference]struct{}, len(document.Spec.Rules))
	for _, rule := range document.Spec.Rules {
		if rule.Regression == nil {
			continue
		}
		reference := rule.Regression.Reference
		if _, exists := seen[reference]; exists {
			continue
		}
		seen[reference] = struct{}{}
		references = append(references, reference)
	}

	return references
}

func (rule Rule) validate() error {
	if !rawresult.ValidResourceID(rule.ID) || !rule.Metric.valid() ||
		(rule.SLO == nil && rule.Regression == nil) {
		return run.ErrValidation
	}
	if (rule.Quality != nil && !rule.Quality.valid()) ||
		(rule.SLO != nil && !rule.SLO.valid()) ||
		(rule.Regression != nil && !rule.Regression.valid()) {
		return run.ErrValidation
	}

	return nil
}

func (metric Metric) valid() bool {
	if !metricName.MatchString(metric.Name) || !validText(metric.Unit) {
		return false
	}
	switch metric.Statistic {
	case "mean", "median", "p90", "p95", "p99", "min", "max":
		return true
	default:
		return false
	}
}

func (requirement QualityRequirement) valid() bool {
	return (requirement.MinSamples != nil || requirement.MaxCV != nil) &&
		(requirement.MinSamples == nil || *requirement.MinSamples >= 1) &&
		(requirement.MaxCV == nil || finite(*requirement.MaxCV) && *requirement.MaxCV >= 0)
}

func (requirement SLORequirement) valid() bool {
	if (requirement.Min == nil && requirement.Max == nil) ||
		(requirement.Min != nil && !finite(*requirement.Min)) ||
		(requirement.Max != nil && !finite(*requirement.Max)) {
		return false
	}

	return requirement.Min == nil || requirement.Max == nil || *requirement.Min <= *requirement.Max
}

func (regression Regression) valid() bool {
	return (regression.Direction == "lower-is-better" ||
		regression.Direction == "higher-is-better") &&
		(regression.PracticalDifference.Kind == "relative" ||
			regression.PracticalDifference.Kind == "absolute") &&
		finite(regression.PracticalDifference.Value) && regression.PracticalDifference.Value > 0 &&
		rawresult.ValidResourceID(regression.Reference.BaselineID) &&
		rawresult.ValidContractsVersion(regression.Reference.Version)
}

func validText(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validShape(data []byte) bool {
	root, valid := jsondocument.ExactObject(data, "apiVersion", "kind", "metadata", "spec")
	if !valid {
		return false
	}
	if _, valid = jsondocument.ExactObject(root["metadata"], "name", "version", "owner"); !valid {
		return false
	}
	spec, valid := jsondocument.ExactObject(root["spec"], "mode", "missingData", "rules")
	if !valid {
		return false
	}
	var rules []json.RawMessage
	if json.Unmarshal(spec["rules"], &rules) != nil || len(rules) == 0 {
		return false
	}
	for _, rule := range rules {
		if !validRuleShape(rule) {
			return false
		}
	}

	return true
}

func validRuleShape(data json.RawMessage) bool {
	rule, valid := jsondocument.ObjectFields(
		data,
		[]string{"id", "metric"},
		"quality", "slo", "regression",
	)
	if !valid {
		return false
	}
	if _, valid = jsondocument.ExactObject(rule["metric"], "name", "statistic", "unit"); !valid {
		return false
	}
	if value, exists := rule["quality"]; exists {
		fields, exact := jsondocument.ObjectFields(value, nil, "minSamples", "maxCv")
		if !exact || len(fields) == 0 {
			return false
		}
		for _, field := range fields {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return false
			}
		}
	}
	if value, exists := rule["slo"]; exists {
		fields, exact := jsondocument.ObjectFields(value, nil, "min", "max")
		if !exact || len(fields) == 0 {
			return false
		}
		for _, field := range fields {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return false
			}
		}
	}
	if value, exists := rule["regression"]; exists {
		regression, exact := jsondocument.ExactObject(
			value,
			"direction", "practicalDifference", "reference",
		)
		if !exact {
			return false
		}
		if _, exact = jsondocument.ExactObject(
			regression["practicalDifference"], "kind", "value",
		); !exact {
			return false
		}
		if _, exact = jsondocument.ExactObject(
			regression["reference"], "baselineId", "version",
		); !exact {
			return false
		}
	}

	return rule["slo"] != nil || rule["regression"] != nil
}

// VerdictApprover independently checks a report against approved policy bytes.
type VerdictApprover struct{}

// ApproveReportVerdicts checks identity, exact rule coverage and decisive arithmetic.
func (VerdictApprover) ApproveReportVerdicts(
	ctx context.Context,
	policyBytes []byte,
	baselines []BaselineResolution,
	manifest analysisresult.Manifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	document, err := Parse(policyBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(policyBytes)
	if manifest.Validate(manifest.RunID, manifest.ContractsVersion) != nil ||
		manifest.TestID != document.Metadata.Name ||
		manifest.Policy.ID != document.Metadata.Name ||
		manifest.Policy.Version != document.Metadata.Version ||
		manifest.Policy.SHA256 != hex.EncodeToString(digest[:]) ||
		manifest.Policy.Mode != document.Spec.Mode {
		return run.ErrValidation
	}
	resolved, valid := document.resolvedBaselines(baselines)
	if !valid {
		return run.ErrValidation
	}

	evaluations := make(map[string]analysisresult.Evaluation, len(manifest.Evaluations))
	for _, evaluation := range manifest.Evaluations {
		evaluations[evaluation.RuleID] = evaluation
	}
	if len(evaluations) != len(document.Spec.Rules) {
		return run.ErrValidation
	}
	for _, rule := range document.Spec.Rules {
		evaluation, exists := evaluations[rule.ID]
		var approvedReference *run.Artifact
		if rule.Regression != nil {
			approvedReference = resolved[rule.Regression.Reference]
		}
		if !exists || !rule.approves(evaluation, approvedReference) {
			return run.ErrValidation
		}
	}

	return nil
}

func (document Document) resolvedBaselines(
	resolutions []BaselineResolution,
) (map[Reference]*run.Artifact, bool) {
	expected := document.BaselineReferences()
	if len(resolutions) != len(expected) {
		return nil, false
	}
	wanted := make(map[Reference]struct{}, len(expected))
	for _, reference := range expected {
		wanted[reference] = struct{}{}
	}
	resolved := make(map[Reference]*run.Artifact, len(resolutions))
	for _, resolution := range resolutions {
		reference := Reference{BaselineID: resolution.ID, Version: resolution.Version}
		if _, exists := resolved[reference]; exists {
			return nil, false
		}
		if _, exists := wanted[reference]; !exists {
			return nil, false
		}
		delete(wanted, reference)
		if resolution.Artifact != nil &&
			(resolution.Artifact.Validate() != nil || resolution.Artifact.Kind != "normalized" ||
				resolution.Artifact.MediaType != "application/json" ||
				resolution.Artifact.Format != "normalized-result/v1") {
			return nil, false
		}
		if resolution.Artifact == nil {
			resolved[reference] = nil
			continue
		}
		artifact := *resolution.Artifact
		resolved[reference] = &artifact
	}

	return resolved, len(wanted) == 0
}

func (rule Rule) approves(
	evaluation analysisresult.Evaluation,
	approvedReference *run.Artifact,
) bool {
	if evaluation.Metric.Name != rule.Metric.Name ||
		evaluation.Metric.Statistic != rule.Metric.Statistic ||
		evaluation.Metric.Unit != rule.Metric.Unit || !rule.approvesQuality(evaluation.Quality) {
		return false
	}
	if rule.SLO == nil {
		if evaluation.SLO.Status != "NOT_EVALUATED" {
			return false
		}
	} else if evaluation.SLO.Status == "NOT_EVALUATED" || !rule.SLO.approves(evaluation.SLO) {
		return false
	}
	if rule.Regression == nil {
		return evaluation.Regression.Status == "NOT_EVALUATED"
	}

	return evaluation.Regression.Status != "NOT_EVALUATED" &&
		rule.Regression.approves(evaluation.Regression, approvedReference)
}

func (rule Rule) approvesQuality(quality analysisresult.Quality) bool {
	if quality.Status != "PASS" || rule.Quality == nil {
		return true
	}
	return (rule.Quality.MinSamples == nil ||
		quality.Samples != nil && *quality.Samples >= *rule.Quality.MinSamples) &&
		(rule.Quality.MaxCV == nil || quality.CV != nil && *quality.CV <= *rule.Quality.MaxCV)
}

func (requirement SLORequirement) approves(outcome analysisresult.SLO) bool {
	if outcome.Status != "PASS" && outcome.Status != "FAIL" {
		return true
	}
	if outcome.Value == nil {
		return false
	}
	passes := (requirement.Min == nil || *outcome.Value >= *requirement.Min) &&
		(requirement.Max == nil || *outcome.Value <= *requirement.Max)

	return (outcome.Status == "PASS") == passes
}

func (regression Regression) approves(
	outcome analysisresult.Regression,
	approvedReference *run.Artifact,
) bool {
	if outcome.ReferenceArtifactID != nil &&
		(approvedReference == nil || *outcome.ReferenceArtifactID != approvedReference.ID) {
		return false
	}
	if outcome.Status != "PASS" && outcome.Status != "FAIL" {
		return outcome.Status == "INCONCLUSIVE"
	}
	if approvedReference == nil || outcome.ReferenceArtifactID == nil ||
		outcome.CandidateValue == nil || outcome.ReferenceValue == nil || outcome.Effect == nil {
		return false
	}
	if outcome.Effect.Kind != regression.PracticalDifference.Kind {
		return false
	}
	change := *outcome.CandidateValue - *outcome.ReferenceValue
	if regression.Direction == "higher-is-better" {
		change = -change
	}
	if regression.PracticalDifference.Kind == "relative" {
		if *outcome.ReferenceValue == 0 {
			return false
		}
		change /= math.Abs(*outcome.ReferenceValue)
	}
	if !finite(change) || !close(change, outcome.Effect.Value) {
		return false
	}
	fails := change >= regression.PracticalDifference.Value

	return (outcome.Status == "FAIL") == fails
}

func close(left, right float64) bool {
	difference := math.Abs(left - right)
	tolerance := math.Max(1e-12, 1e-9*math.Max(math.Abs(left), math.Abs(right)))

	return difference <= tolerance
}
