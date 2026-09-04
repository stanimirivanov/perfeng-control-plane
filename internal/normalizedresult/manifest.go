// Package normalizedresult parses and validates normalized-result/v1 envelopes.
package normalizedresult

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumManifestBytes = 16 << 20

var metricNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// Manifest is the transport shape of a normalized-result/v1 envelope.
type Manifest struct {
	SchemaVersion     int                `json:"schemaVersion"`
	Kind              string             `json:"kind"`
	ContractsVersion  string             `json:"contractsVersion"`
	RunID             string             `json:"runId"`
	TestID            string             `json:"testId"`
	Workload          rawresult.Identity `json:"workload"`
	Producer          rawresult.Producer `json:"producer"`
	MeasurementWindow rawresult.Window   `json:"measurementWindow"`
	CreatedAt         string             `json:"createdAt"`
	SourceArtifacts   []run.Artifact     `json:"sourceArtifacts"`
	Results           []Result           `json:"results"`
}

// Result is one result/v2 metric record.
type Result struct {
	SchemaVersion int            `json:"schemaVersion"`
	RunID         string         `json:"runId"`
	Metric        Metric         `json:"metric"`
	Distribution  Distribution   `json:"distribution"`
	Thresholds    *Thresholds    `json:"thresholds,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// Metric identifies a measurement and how changes should be interpreted.
type Metric struct {
	Name      string  `json:"name"`
	Direction string  `json:"direction"`
	Type      *string `json:"type,omitempty"`
	Unit      *string `json:"unit,omitempty"`
}

// Distribution contains only statistics supplied by the normalizer. A nil
// sample count means unavailable, not zero.
type Distribution struct {
	Samples *int64   `json:"samples,omitempty"`
	Mean    *float64 `json:"mean,omitempty"`
	Median  *float64 `json:"median,omitempty"`
	P90     *float64 `json:"p90,omitempty"`
	P95     *float64 `json:"p95,omitempty"`
	P99     *float64 `json:"p99,omitempty"`
	Stddev  *float64 `json:"stddev,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	CV      *float64 `json:"cv,omitempty"`
}

// Thresholds preserves optional legacy SLO and regression evaluations without
// treating them as the platform's final analysis verdict.
type Thresholds struct {
	SLO        map[string]SLOThreshold        `json:"slo,omitempty"`
	Regression map[string]RegressionThreshold `json:"regression,omitempty"`
}

// SLOThreshold is one producer-supplied legacy SLO evaluation.
type SLOThreshold struct {
	Passed    bool     `json:"passed"`
	Threshold *string  `json:"threshold,omitempty"`
	Actual    *float64 `json:"actual,omitempty"`
}

// RegressionThreshold is one producer-supplied legacy comparison.
type RegressionThreshold struct {
	Passed        bool     `json:"passed"`
	BaselineValue *float64 `json:"baselineValue,omitempty"`
	ActualValue   *float64 `json:"actualValue,omitempty"`
	PercentChange *float64 `json:"percentChange,omitempty"`
}

// Parse rejects malformed or unsupported envelopes and returns an isolated
// manifest matching the trusted Run and contracts bundle context.
func Parse(data []byte, expectedRunID, expectedContractsVersion string) (Manifest, error) {
	if !jsondocument.Valid(data, maximumManifestBytes) ||
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

// Validate checks normalized-result/v1 structure without approving the
// normalizer or verifying the normalized artifact's external reference.
func (manifest Manifest) Validate(expectedRunID, expectedContractsVersion string) error {
	common := rawresult.Manifest{
		SchemaVersion:     manifest.SchemaVersion,
		Kind:              "RawResult",
		ContractsVersion:  manifest.ContractsVersion,
		RunID:             manifest.RunID,
		TestID:            manifest.TestID,
		Workload:          manifest.Workload,
		Producer:          manifest.Producer,
		MeasurementWindow: manifest.MeasurementWindow,
		CreatedAt:         manifest.CreatedAt,
		Artifacts:         manifest.SourceArtifacts,
	}
	if manifest.Kind != "NormalizedResult" || len(manifest.Results) == 0 ||
		common.Validate(expectedRunID, expectedContractsVersion) != nil {
		return run.ErrValidation
	}

	metrics := make(map[string]struct{}, len(manifest.Results))
	for _, result := range manifest.Results {
		if result.validate(manifest.RunID) != nil {
			return run.ErrValidation
		}
		if _, exists := metrics[result.Metric.Name]; exists {
			return run.ErrValidation
		}
		metrics[result.Metric.Name] = struct{}{}
	}

	return nil
}

func (result Result) validate(runID string) error {
	if result.SchemaVersion != 2 || result.RunID != runID ||
		!metricNamePattern.MatchString(result.Metric.Name) ||
		(result.Metric.Direction != "lower-is-better" && result.Metric.Direction != "higher-is-better") ||
		!validMetricType(result.Metric.Type) || !validOptionalString(result.Metric.Unit) ||
		!result.Distribution.valid() || !result.Thresholds.valid() || !validMetadata(result.Metadata) {
		return run.ErrValidation
	}

	return nil
}

func validMetricType(value *string) bool {
	if value == nil {
		return true
	}
	switch *value {
	case "latency", "throughput", "error_rate", "resource", "custom":
		return true
	default:
		return false
	}
}

func validOptionalString(value *string) bool {
	return value == nil || utf8.ValidString(*value)
}

func (distribution Distribution) valid() bool {
	if distribution.Samples != nil && *distribution.Samples < 1 {
		return false
	}
	if !finite(distribution.Mean, distribution.Median, distribution.Min, distribution.Max) {
		return false
	}

	return nonnegative(
		distribution.P90,
		distribution.P95,
		distribution.P99,
		distribution.Stddev,
		distribution.CV,
	)
}

func (thresholds *Thresholds) valid() bool {
	if thresholds == nil {
		return true
	}
	for name, threshold := range thresholds.SLO {
		if !utf8.ValidString(name) || !validOptionalString(threshold.Threshold) || !finite(threshold.Actual) {
			return false
		}
	}
	for name, threshold := range thresholds.Regression {
		if !utf8.ValidString(name) || !finite(
			threshold.BaselineValue,
			threshold.ActualValue,
			threshold.PercentChange,
		) {
			return false
		}
	}

	return true
}

func finite(values ...*float64) bool {
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return false
		}
	}

	return true
}

func nonnegative(values ...*float64) bool {
	if !finite(values...) {
		return false
	}
	for _, value := range values {
		if value != nil && *value < 0 {
			return false
		}
	}

	return true
}

func validMetadata(metadata map[string]any) bool {
	if metadata == nil {
		return true
	}
	_, err := json.Marshal(metadata)

	return err == nil
}

func validShape(data []byte) bool {
	root, valid := jsondocument.ExactObject(data,
		"schemaVersion", "kind", "contractsVersion", "runId", "testId", "workload",
		"producer", "measurementWindow", "createdAt", "sourceArtifacts", "results")
	if !valid || !validCommonShape(root) || !validArtifactShape(root["sourceArtifacts"]) {
		return false
	}

	var results []json.RawMessage
	if json.Unmarshal(root["results"], &results) != nil {
		return false
	}
	for _, result := range results {
		if !validResultShape(result) {
			return false
		}
	}

	return true
}

func validCommonShape(root map[string]json.RawMessage) bool {
	if _, valid := jsondocument.ExactObject(root["workload"], "id", "version", "sha256"); !valid {
		return false
	}
	if _, valid := jsondocument.ExactObject(root["producer"], "name", "version", "image"); !valid {
		return false
	}
	_, valid := jsondocument.ExactObject(root["measurementWindow"], "start", "end")

	return valid
}

func validArtifactShape(data json.RawMessage) bool {
	var artifacts []json.RawMessage
	if json.Unmarshal(data, &artifacts) != nil {
		return false
	}
	for _, artifact := range artifacts {
		if _, valid := jsondocument.ExactObject(
			artifact, "id", "runId", "kind", "uri", "sha256", "sizeBytes", "mediaType", "format",
		); !valid {
			return false
		}
	}

	return true
}

func validResultShape(data json.RawMessage) bool {
	result, valid := jsondocument.ObjectFields(
		data,
		[]string{"schemaVersion", "runId", "metric", "distribution"},
		"thresholds",
		"metadata",
	)
	if !valid {
		return false
	}
	if _, valid = jsondocument.ObjectFields(
		result["metric"], []string{"name", "direction"}, "type", "unit",
	); !valid {
		return false
	}
	if _, valid = jsondocument.ObjectFields(
		result["distribution"], nil,
		"samples", "mean", "median", "p90", "p95", "p99", "stddev", "min", "max", "cv",
	); !valid {
		return false
	}
	if thresholds, exists := result["thresholds"]; exists && !isNull(thresholds) {
		return validThresholdShape(thresholds)
	}

	return true
}

func validThresholdShape(data json.RawMessage) bool {
	thresholds, valid := jsondocument.ObjectFields(data, nil, "slo", "regression")
	if !valid {
		return false
	}
	if value, exists := thresholds["slo"]; exists && !isNull(value) &&
		!validThresholdMap(value, []string{"passed"}, "threshold", "actual") {
		return false
	}
	if value, exists := thresholds["regression"]; exists && !isNull(value) &&
		!validThresholdMap(
			value, []string{"passed"}, "baselineValue", "actualValue", "percentChange",
		) {
		return false
	}

	return true
}

func validThresholdMap(data json.RawMessage, required []string, optional ...string) bool {
	var thresholds map[string]json.RawMessage
	if json.Unmarshal(data, &thresholds) != nil {
		return false
	}
	for _, threshold := range thresholds {
		if _, valid := jsondocument.ObjectFields(threshold, required, optional...); !valid {
			return false
		}
	}

	return true
}

func isNull(data json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}
