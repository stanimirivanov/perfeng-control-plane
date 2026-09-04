// Package analysisresult parses and validates analysis-result/v1 reports.
package analysisresult

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumReportBytes = 16 << 20

var metricNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// Manifest is the transport shape of an analysis-result/v1 report.
type Manifest struct {
	SchemaVersion      int                `json:"schemaVersion"`
	Kind               string             `json:"kind"`
	ContractsVersion   string             `json:"contractsVersion"`
	RunID              string             `json:"runId"`
	TestID             string             `json:"testId"`
	CreatedAt          string             `json:"createdAt"`
	Producer           rawresult.Producer `json:"producer"`
	Policy             Policy             `json:"policy"`
	CandidateArtifact  run.Artifact       `json:"candidateArtifact"`
	ReferenceArtifacts []run.Artifact     `json:"referenceArtifacts"`
	Blocking           bool               `json:"blocking"`
	Evaluations        []Evaluation       `json:"evaluations"`
}

// Policy identifies the exact policy used to generate a report.
type Policy struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode"`
}

// Evaluation contains independent quality, SLO and regression outcomes.
type Evaluation struct {
	RuleID     string     `json:"ruleId"`
	Metric     Metric     `json:"metric"`
	Quality    Quality    `json:"quality"`
	SLO        SLO        `json:"slo"`
	Regression Regression `json:"regression"`
}

// Metric selects one statistic and unit from the normalized candidate.
type Metric struct {
	Name      string `json:"name"`
	Statistic string `json:"statistic"`
	Unit      string `json:"unit"`
}

// Quality records whether evidence is suitable for decisive evaluation.
type Quality struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Samples *int64   `json:"samples,omitempty"`
	CV      *float64 `json:"cv,omitempty"`
}

// SLO records the policy requirement outcome for one metric.
type SLO struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Value   *float64 `json:"value,omitempty"`
}

// Regression records a candidate/reference comparison or why it was unavailable.
type Regression struct {
	Status              string   `json:"status"`
	Reasons             []string `json:"reasons"`
	CandidateValue      *float64 `json:"candidateValue,omitempty"`
	ReferenceValue      *float64 `json:"referenceValue,omitempty"`
	ReferenceArtifactID *string  `json:"referenceArtifactId,omitempty"`
	Effect              *Effect  `json:"effect,omitempty"`
	Method              *Method  `json:"method,omitempty"`
}

// Effect records the absolute or relative practical difference.
type Effect struct {
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
}

// Method identifies the comparison method used by the report producer.
type Method struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Parse rejects malformed or unsupported reports and returns an isolated
// document matching the trusted Run and contracts bundle context.
func Parse(data []byte, expectedRunID, expectedContractsVersion string) (Manifest, error) {
	if !jsondocument.Valid(data, maximumReportBytes) ||
		!rawresult.ValidContractsVersion(expectedContractsVersion) || !validShape(data) {
		return Manifest{}, run.ErrValidation
	}

	var manifest Manifest
	if !jsondocument.Decode(data, &manifest) ||
		manifest.Validate(expectedRunID, expectedContractsVersion) != nil {
		return Manifest{}, run.ErrValidation
	}

	return manifest, nil
}

// Validate checks report structure and internal consistency without approving
// the policy, producer, candidate bytes or reference selection.
func (manifest Manifest) Validate(expectedRunID, expectedContractsVersion string) error {
	if manifest.SchemaVersion != 1 || manifest.Kind != "AnalysisResult" ||
		manifest.ContractsVersion != expectedContractsVersion || manifest.RunID != expectedRunID ||
		!rawresult.ValidContractsVersion(manifest.ContractsVersion) ||
		!rawresult.ValidResourceID(manifest.TestID) || !rawresult.ValidTimestamp(manifest.CreatedAt) ||
		manifest.Producer.Validate() != nil || manifest.Blocking ||
		manifest.Policy.validate() != nil || len(manifest.Evaluations) == 0 ||
		!validNormalizedArtifact(manifest.CandidateArtifact) ||
		manifest.CandidateArtifact.RunID != manifest.RunID {
		return run.ErrValidation
	}

	references := make(map[string]struct{}, len(manifest.ReferenceArtifacts))
	artifactIDs := map[string]struct{}{manifest.CandidateArtifact.ID: {}}
	artifactURIs := map[string]struct{}{manifest.CandidateArtifact.URI: {}}
	for _, artifact := range manifest.ReferenceArtifacts {
		if !validNormalizedArtifact(artifact) || artifact.RunID == manifest.RunID {
			return run.ErrValidation
		}
		if _, exists := artifactIDs[artifact.ID]; exists {
			return run.ErrValidation
		}
		if _, exists := artifactURIs[artifact.URI]; exists {
			return run.ErrValidation
		}
		artifactIDs[artifact.ID] = struct{}{}
		artifactURIs[artifact.URI] = struct{}{}
		references[artifact.ID] = struct{}{}
	}

	rules := make(map[string]struct{}, len(manifest.Evaluations))
	metrics := make(map[Metric]struct{}, len(manifest.Evaluations))
	for _, evaluation := range manifest.Evaluations {
		if evaluation.validate(references) != nil {
			return run.ErrValidation
		}
		if _, exists := rules[evaluation.RuleID]; exists {
			return run.ErrValidation
		}
		if _, exists := metrics[evaluation.Metric]; exists {
			return run.ErrValidation
		}
		rules[evaluation.RuleID] = struct{}{}
		metrics[evaluation.Metric] = struct{}{}
	}

	return nil
}

func (policy Policy) validate() error {
	identity := rawresult.Identity{ID: policy.ID, Version: policy.Version, SHA256: policy.SHA256}
	if identity.Validate() != nil || (policy.Mode != "observe" && policy.Mode != "inform") {
		return run.ErrValidation
	}

	return nil
}

func (evaluation Evaluation) validate(references map[string]struct{}) error {
	if !rawresult.ValidResourceID(evaluation.RuleID) || !evaluation.Metric.valid() ||
		!evaluation.Quality.valid() || !evaluation.SLO.valid() ||
		!evaluation.Regression.valid(references) {
		return run.ErrValidation
	}
	if evaluation.Quality.Status != "PASS" &&
		(decisive(evaluation.SLO.Status) || decisive(evaluation.Regression.Status)) {
		return run.ErrValidation
	}

	return nil
}

func (metric Metric) valid() bool {
	if !metricNamePattern.MatchString(metric.Name) || strings.TrimSpace(metric.Unit) == "" {
		return false
	}
	switch metric.Statistic {
	case "mean", "median", "p90", "p95", "p99", "min", "max":
		return true
	default:
		return false
	}
}

func (quality Quality) valid() bool {
	if !validVerdict(
		quality.Status,
		quality.Reasons,
		"PASS", "INVALID", "UNSTABLE", "INCONCLUSIVE", "NOT_EVALUATED",
	) || !finite(quality.CV) {
		return false
	}
	return (quality.Samples == nil || *quality.Samples >= 1) &&
		(quality.CV == nil || *quality.CV >= 0)
}

func (slo SLO) valid() bool {
	return validVerdict(slo.Status, slo.Reasons, "PASS", "FAIL", "INCONCLUSIVE", "NOT_EVALUATED") &&
		finite(slo.Value) && (!decisive(slo.Status) || slo.Value != nil)
}

func (regression Regression) valid(references map[string]struct{}) bool {
	if !validVerdict(
		regression.Status,
		regression.Reasons,
		"PASS", "FAIL", "INCONCLUSIVE", "NOT_EVALUATED",
	) || !finite(regression.CandidateValue, regression.ReferenceValue) {
		return false
	}
	if regression.ReferenceArtifactID != nil {
		if _, exists := references[*regression.ReferenceArtifactID]; !exists {
			return false
		}
	}
	if regression.Effect != nil &&
		((regression.Effect.Kind != "relative" && regression.Effect.Kind != "absolute") ||
			math.IsNaN(regression.Effect.Value) || math.IsInf(regression.Effect.Value, 0)) {
		return false
	}
	if regression.Method != nil &&
		(strings.TrimSpace(regression.Method.Name) == "" ||
			!rawresult.ValidContractsVersion(regression.Method.Version)) {
		return false
	}
	if !decisive(regression.Status) {
		return true
	}
	if regression.CandidateValue == nil || regression.ReferenceValue == nil ||
		regression.ReferenceArtifactID == nil || regression.Effect == nil || regression.Method == nil {
		return false
	}

	return true
}

func validVerdict(status string, reasons []string, allowed ...string) bool {
	validStatus := false
	for _, candidate := range allowed {
		if status == candidate {
			validStatus = true
			break
		}
	}
	if !validStatus || (status != "PASS" && len(reasons) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(reasons))
	for _, reason := range reasons {
		if strings.TrimSpace(reason) == "" {
			return false
		}
		if _, exists := seen[reason]; exists {
			return false
		}
		seen[reason] = struct{}{}
	}

	return true
}

func decisive(status string) bool {
	return status == "PASS" || status == "FAIL"
}

func finite(values ...*float64) bool {
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return false
		}
	}

	return true
}

func validNormalizedArtifact(artifact run.Artifact) bool {
	return artifact.Validate() == nil && artifact.Kind == "normalized" &&
		artifact.MediaType == "application/json" && artifact.Format == "normalized-result/v1"
}

func validShape(data []byte) bool {
	root, valid := jsondocument.ExactObject(
		data,
		"schemaVersion", "kind", "contractsVersion", "runId", "testId", "createdAt",
		"producer", "policy", "candidateArtifact", "referenceArtifacts", "blocking", "evaluations",
	)
	if !valid {
		return false
	}
	if _, valid = jsondocument.ExactObject(root["producer"], "name", "version", "image"); !valid {
		return false
	}
	if _, valid = jsondocument.ExactObject(root["policy"], "id", "version", "sha256", "mode"); !valid {
		return false
	}
	if !validArtifactShape(root["candidateArtifact"]) ||
		!validArtifactArrayShape(root["referenceArtifacts"]) {
		return false
	}

	var evaluations []json.RawMessage
	if isNull(root["evaluations"]) || json.Unmarshal(root["evaluations"], &evaluations) != nil {
		return false
	}
	for _, evaluation := range evaluations {
		if !validEvaluationShape(evaluation) {
			return false
		}
	}

	return true
}

func validArtifactArrayShape(data json.RawMessage) bool {
	var artifacts []json.RawMessage
	if isNull(data) || json.Unmarshal(data, &artifacts) != nil {
		return false
	}
	for _, artifact := range artifacts {
		if !validArtifactShape(artifact) {
			return false
		}
	}

	return true
}

func validArtifactShape(data json.RawMessage) bool {
	_, valid := jsondocument.ExactObject(
		data, "id", "runId", "kind", "uri", "sha256", "sizeBytes", "mediaType", "format",
	)

	return valid
}

func validEvaluationShape(data json.RawMessage) bool {
	evaluation, valid := jsondocument.ExactObject(
		data, "ruleId", "metric", "quality", "slo", "regression",
	)
	if !valid {
		return false
	}
	if _, valid = jsondocument.ExactObject(evaluation["metric"], "name", "statistic", "unit"); !valid {
		return false
	}
	if !validOutcomeShape(evaluation["quality"], []string{"samples", "cv"}, false) ||
		!validOutcomeShape(evaluation["slo"], []string{"value"}, true) {
		return false
	}

	regression, valid := jsondocument.ObjectFields(
		evaluation["regression"],
		[]string{"status", "reasons"},
		"candidateValue", "referenceValue", "referenceArtifactId", "effect", "method",
	)
	if !valid || isNull(regression["reasons"]) {
		return false
	}
	for _, name := range []string{"referenceArtifactId", "effect", "method"} {
		if value, exists := regression[name]; exists && isNull(value) {
			return false
		}
	}
	if effect, exists := regression["effect"]; exists {
		if _, valid = jsondocument.ExactObject(effect, "kind", "value"); !valid {
			return false
		}
	}
	if method, exists := regression["method"]; exists {
		if _, valid = jsondocument.ExactObject(method, "name", "version"); !valid {
			return false
		}
	}

	return decisiveFieldRequirements(regression)
}

func validOutcomeShape(data json.RawMessage, optional []string, decisiveValue bool) bool {
	outcome, valid := jsondocument.ObjectFields(data, []string{"status", "reasons"}, optional...)
	if !valid || isNull(outcome["reasons"]) {
		return false
	}
	if !decisiveValue {
		for _, name := range optional {
			if value, exists := outcome[name]; exists && isNull(value) {
				return false
			}
		}

		return true
	}

	return decisiveFieldRequirements(outcome)
}

func decisiveFieldRequirements(outcome map[string]json.RawMessage) bool {
	var status string
	if json.Unmarshal(outcome["status"], &status) != nil || !decisive(status) {
		return true
	}
	if _, regression := outcome["candidateValue"]; regression {
		for _, name := range []string{
			"candidateValue", "referenceValue", "referenceArtifactId", "effect", "method",
		} {
			value, exists := outcome[name]
			if !exists || isNull(value) {
				return false
			}
		}
		return true
	}
	value, exists := outcome["value"]

	return exists && !isNull(value)
}

func isNull(data json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
