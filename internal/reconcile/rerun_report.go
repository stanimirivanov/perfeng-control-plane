package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

// RerunReportStore distinguishes ordinary Runs from known rerun failures and
// returns validated immutable acceptance plus exact registered child artifacts.
// It grants neither current dispatch admission nor lease ownership.
type RerunReportStore interface {
	LookupRerunExecution(context.Context, string, run.Run) (runrerun.Acceptance, bool, error)
	GetArtifact(context.Context, string, string, string) (run.Artifact, error)
}

// ReportPlanVersionResolver supplies report trust and its evidence schema pin.
type ReportPlanVersionResolver interface {
	ReportPlanResolver
	ContractsVersionResolver
}

// RerunReportPlanResolver uses original rerun selection for report jobs and
// attestation. Only an explicit ordinary association may use the legacy planner.
type RerunReportPlanResolver struct {
	store    RerunReportStore
	ordinary ReportPlanVersionResolver
}

// NewRerunReportPlanResolver requires durable association and ordinary planning.
func NewRerunReportPlanResolver(store RerunReportStore, ordinary ReportPlanVersionResolver) (*RerunReportPlanResolver, error) {
	if store == nil || ordinary == nil {
		return nil, run.ErrValidation
	}
	return &RerunReportPlanResolver{store: store, ordinary: ordinary}, nil
}
func (resolver *RerunReportPlanResolver) lookup(ctx context.Context, principal string, current run.Run) (runrerun.Acceptance, bool, error) {
	if err := ctx.Err(); err != nil {
		return runrerun.Acceptance{}, false, err
	}
	if principal == "" || current.Request.Validate() != nil {
		return runrerun.Acceptance{}, false, run.ErrValidation
	}
	value, known, err := resolver.store.LookupRerunExecution(ctx, principal, current.Clone())
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		return runrerun.Acceptance{}, known, err
	}
	if known && (value.Validate() != nil || value.Child.ID != current.ID || value.ChildPrincipal != principal || value.Child.Request != current.Request) {
		return runrerun.Acceptance{}, true, run.ErrUnavailable
	}
	return value, known, nil
}

// ResolveReportPlan binds only the actual registered child candidate to the
// frozen policy/runtime/baselines. It never rechecks current baseline selection.
func (resolver *RerunReportPlanResolver) ResolveReportPlan(ctx context.Context, principal string, current run.Run, input ReportingInput) (ReportPlan, error) {
	value, known, err := resolver.lookup(ctx, principal, current)
	if err != nil {
		return ReportPlan{}, err
	}
	if !known {
		return resolver.ordinary.ResolveReportPlan(ctx, principal, current.Clone(), input)
	}
	if current.State != run.StateReporting || !validReportCandidate(current.ID, input.Candidate) {
		return ReportPlan{}, run.ErrValidation
	}
	candidate, err := resolver.store.GetArtifact(ctx, principal, current.ID, input.Candidate.ID)
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		return ReportPlan{}, err
	}
	if candidate != input.Candidate {
		return ReportPlan{}, run.ErrConflict
	}
	frozen := value.Inputs.Clone()
	frozen.Candidate = candidate
	if frozen.Validate(current) != nil {
		return ReportPlan{}, run.ErrUnavailable
	}
	payload, err := json.Marshal(frozen)
	if err != nil {
		return ReportPlan{}, run.ErrValidation
	}
	digest := sha256.Sum256(payload)
	return ReportPlan{ContractsVersion: frozen.ContractsVersion, Workload: frozen.Workload, Candidate: candidate, PolicyBytes: frozen.PolicyBytes, PolicyMode: frozen.PolicyMode, Producer: frozen.Producer, Baselines: frozen.Baselines, BaselineSnapshots: frozen.BaselineSnapshots, CaptureSHA256: hex.EncodeToString(digest[:])}, nil
}

// ResolveContractsVersion preserves the accepted rerun schema pin without a
// current-registry lookup; missing known evidence is unavailable, never legacy.
func (resolver *RerunReportPlanResolver) ResolveContractsVersion(ctx context.Context, principal string, current run.Run) (string, error) {
	value, known, err := resolver.lookup(ctx, principal, current)
	if err != nil {
		return "", err
	}
	if !known {
		return resolver.ordinary.ResolveContractsVersion(ctx, principal, current.Clone())
	}
	if current.State != run.StateCollecting && current.State != run.StateAnalyzing && current.State != run.StateReporting {
		return "", run.ErrValidation
	}
	return value.Inputs.ContractsVersion, nil
}
