// Package runcomparison owns immutable report-time selections, not HTTP projections.
package runcomparison

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrNotEnrolled preserves legacy reporting without inventing historical capture.
var ErrNotEnrolled = errors.New("run is not enrolled for report comparison capture")

// Inputs freezes the accepted request and the exact selections used for reporting.
// Snapshot absence for a policy reference records unavailability, never later approval.
type Inputs struct {
	ContractsVersion  string
	Request           run.Request
	Candidate         run.Artifact
	Workload          rawresult.Identity
	PolicyBytes       []byte
	PolicyMode        string
	Producer          rawresult.Producer
	Baselines         []policy.BaselineResolution
	BaselineSnapshots []baseline.Record
}

// Clone returns independent policy, resolution and approval snapshots.
func (inputs Inputs) Clone() Inputs {
	inputs.PolicyBytes = slices.Clone(inputs.PolicyBytes)
	inputs.Producer = inputs.Producer.Clone()
	inputs.Baselines = slices.Clone(inputs.Baselines)
	for i := range inputs.Baselines {
		if inputs.Baselines[i].Artifact != nil {
			artifact := *inputs.Baselines[i].Artifact
			inputs.Baselines[i].Artifact = &artifact
		}
	}
	inputs.BaselineSnapshots = slices.Clone(inputs.BaselineSnapshots)
	for i, record := range inputs.BaselineSnapshots {
		inputs.BaselineSnapshots[i] = record.Clone()
		inputs.BaselineSnapshots[i].Qualification.Reasons = slices.Clone(record.Qualification.Reasons)
	}
	return inputs
}

// Validate checks frozen identity and complete policy-reference/snapshot coverage.
// It proves no live authorization; first persistence must recheck approval under locks.
func (inputs Inputs) Validate(current run.Run) error {
	document, err := policy.Parse(inputs.PolicyBytes)
	digest := sha256.Sum256(inputs.PolicyBytes)
	if err != nil || inputs.Request != current.Request || current.Request.Validate() != nil || inputs.Candidate.RunID != current.ID ||
		inputs.Candidate.Validate() != nil || inputs.Candidate.Kind != "normalized" || inputs.Candidate.Format != "normalized-result/v1" || inputs.Candidate.MediaType != "application/json" ||
		!rawresult.ValidContractsVersion(inputs.ContractsVersion) || inputs.Workload.Validate() != nil || inputs.Producer.Validate() != nil || inputs.PolicyMode != document.Spec.Mode || document.Metadata.Name != current.Request.Policy.ID ||
		document.Metadata.Version != current.Request.Policy.Version || hex.EncodeToString(digest[:]) != current.Request.Policy.SHA256 {
		return run.ErrValidation
	}
	expected := map[policy.Reference]bool{}
	for _, reference := range document.BaselineReferences() {
		expected[reference] = true
	}
	snapshots := map[policy.Reference]baseline.Record{}
	for _, record := range inputs.BaselineSnapshots {
		key := policy.Reference{BaselineID: record.ID, Version: record.Version}
		if _, exists := snapshots[key]; exists || record.Validate() != nil || record.State != baseline.StateApproved || record.Workload != inputs.Workload || record.TestID != current.Request.TestSuite {
			return run.ErrValidation
		}
		snapshots[key] = record
	}
	for _, resolution := range inputs.Baselines {
		key := policy.Reference{BaselineID: resolution.ID, Version: resolution.Version}
		if !expected[key] {
			return run.ErrValidation
		}
		delete(expected, key)
		record, found := snapshots[key]
		if resolution.Artifact == nil {
			if found {
				return run.ErrValidation
			}
			continue
		}
		if !found || record.Artifact != *resolution.Artifact || record.Artifact.RunID == current.ID {
			return run.ErrValidation
		}
		delete(snapshots, key)
	}
	if len(expected) != 0 || len(snapshots) != 0 {
		return run.ErrValidation
	}
	return nil
}

// Repository stores principal-scoped immutable inputs; observations grant no authority.
type Repository interface {
	// GetReportInputs returns owned retained inputs or ErrNotFound, never current reconstruction.
	GetReportInputs(context.Context, string, string) (Inputs, error)
	// RetainReportInputs rechecks Run/candidate/approval under locks. Equal replay
	// precedes current-state checks; changed inputs conflict. Commit errors are uncertain.
	// ErrNotEnrolled permits legacy execution but must never mark it captured.
	RetainReportInputs(context.Context, string, run.Run, Inputs) (Inputs, error)
}
