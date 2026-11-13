package app

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
)

func composeReportInputs(enabled bool, database Database, trust ExecutionTrust) (reconcile.ReportPlanResolver, error) {
	if !enabled {
		return nil, nil
	}
	store, ok := database.(runcomparison.Repository)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	selected, err := reconcile.NewTrustedReportPlanResolver(database, trust)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	retained, err := reconcile.NewRetainedReportPlanResolver(store, selected)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return retained, nil
}

type comparisonContracts struct {
	captured, legacy reconcile.ContractsVersionResolver
}

func (selector comparisonContracts) ResolveContractsVersion(ctx context.Context, principal string, current run.Run) (string, error) {
	version, err := selector.captured.ResolveContractsVersion(ctx, principal, current)
	if errors.Is(err, run.ErrNotFound) {
		return selector.legacy.ResolveContractsVersion(ctx, principal, current)
	}
	return version, err
}
