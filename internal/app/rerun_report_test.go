package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

type reportCaptureCompositionStore struct {
	Database
	runcomparison.Repository
}
type rerunReportCompositionStore struct{ reportCaptureCompositionStore }

func (*rerunReportCompositionStore) LookupRerunExecution(context.Context, string, run.Run) (runrerun.Acceptance, bool, error) {
	return runrerun.Acceptance{}, true, run.ErrNotFound
}

type reportCompositionTrust struct{ ExecutionTrust }

func TestReportCaptureCompositionRequiresRerunSafePlanning(t *testing.T) {
	trust := &reportCompositionTrust{}
	if _, err := composeReportInputs(true, &reportCaptureCompositionStore{}, trust); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("capture accepted a store without known-rerun distinction", err)
	}
	plans, err := composeReportInputs(true, &rerunReportCompositionStore{}, trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plans.(*reconcile.RerunReportPlanResolver); !ok {
		t.Fatal("report composition bypasses frozen rerun planning")
	}
	if _, ok := plans.(reconcile.ContractsVersionResolver); !ok {
		t.Fatal("report composition loses schema pin")
	}
}
