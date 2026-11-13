package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
)

// RetainedReportPlanResolver captures before dispatch and replays before any new registry review.
type RetainedReportPlanResolver struct {
	store    runcomparison.Repository
	selected ReportPlanResolver
}

// NewRetainedReportPlanResolver requires durable storage and trusted first selection.
func NewRetainedReportPlanResolver(store runcomparison.Repository, selected ReportPlanResolver) (*RetainedReportPlanResolver, error) {
	if store == nil || selected == nil {
		return nil, run.ErrValidation
	}
	return &RetainedReportPlanResolver{store: store, selected: selected}, nil
}

// ResolveReportPlan preserves original inputs on retry, including an explicit missing baseline.
func (resolver *RetainedReportPlanResolver) ResolveReportPlan(ctx context.Context, principal string, current run.Run, input ReportingInput) (ReportPlan, error) {
	if err := ctx.Err(); err != nil {
		return ReportPlan{}, err
	}
	frozen, err := resolver.store.GetReportInputs(ctx, principal, current.ID)
	if errors.Is(err, run.ErrNotFound) {
		plan, selectErr := resolver.selected.ResolveReportPlan(ctx, principal, current.Clone(), input)
		if selectErr != nil {
			return ReportPlan{}, selectErr
		}
		frozen = runcomparison.Inputs{ContractsVersion: plan.ContractsVersion, Request: current.Request, Candidate: plan.Candidate, Workload: plan.Workload, PolicyBytes: plan.PolicyBytes, PolicyMode: plan.PolicyMode, Producer: plan.Producer, Baselines: plan.Baselines, BaselineSnapshots: plan.BaselineSnapshots}
		frozen, err = resolver.store.RetainReportInputs(ctx, principal, current, frozen)
		if errors.Is(err, runcomparison.ErrNotEnrolled) {
			return plan.Clone(), nil
		}
	}
	if err != nil {
		return ReportPlan{}, err
	}
	if frozen.Validate(current) != nil || frozen.Candidate != input.Candidate {
		return ReportPlan{}, run.ErrConflict
	}
	frozen = frozen.Clone()
	payload, err := json.Marshal(frozen)
	if err != nil {
		return ReportPlan{}, run.ErrValidation
	}
	digest := sha256.Sum256(payload)
	return ReportPlan{ContractsVersion: frozen.ContractsVersion, Candidate: frozen.Candidate, Workload: frozen.Workload, PolicyBytes: frozen.PolicyBytes, PolicyMode: frozen.PolicyMode, Producer: frozen.Producer, Baselines: frozen.Baselines, BaselineSnapshots: frozen.BaselineSnapshots, CaptureSHA256: hex.EncodeToString(digest[:])}, nil
}

// ResolveContractsVersion reuses the report-time schema pin; missing history
// returns ErrNotFound for the explicit legacy fallback in application composition.
func (resolver *RetainedReportPlanResolver) ResolveContractsVersion(ctx context.Context, principal string, current run.Run) (string, error) {
	inputs, err := resolver.store.GetReportInputs(ctx, principal, current.ID)
	if err != nil {
		return "", err
	}
	if inputs.Validate(current) != nil {
		return "", run.ErrUnavailable
	}
	return inputs.ContractsVersion, nil
}
