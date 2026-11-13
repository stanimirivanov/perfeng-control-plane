package postgres

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestRerunAssociationDoesNotDowngradeKnownChild(t *testing.T) {
	r, _, intent := rerunFixture(t)
	value, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	bad := value.Child
	bad.Request.Candidate.GitSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if got, known, err := r.LookupRerunExecution(testContext, value.ChildPrincipal, bad); !known || !errors.Is(err, run.ErrNotFound) || got.ID != "" {
		t.Fatal("known child became ordinary", known, err)
	}
	ordinary := accepted(t, r, "alice", "report-ordinary-01").Run
	if _, known, err := r.LookupRerunExecution(testContext, "alice", ordinary); known || err != nil {
		t.Fatal("ordinary child inferred", known, err)
	}
}

func TestRerunReportPlanSurvivesReopenAndBaselineRetirement(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	child := accepted.Child
	for _, state := range []run.State{run.StateValidating, run.StateProvisioning, run.StateRunning, run.StateCollecting, run.StateAnalyzing, run.StateReporting} {
		child, err = r.Advance(testContext, accepted.ChildPrincipal, child.ID, child.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	candidate := accepted.Inputs.Candidate
	candidate.ID = "6cfa0000-0000-4000-8000-000000000001"
	candidate.RunID = child.ID
	candidate.URI = "s3://evidence/child-normalized.json"
	if err = r.RegisterArtifact(testContext, accepted.ChildPrincipal, candidate); err != nil {
		t.Fatal(err)
	}
	record := accepted.Inputs.BaselineSnapshots[0]
	if _, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "after dispatch"}); err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, dsn)
	legacy := &unexpectedRerunLegacy{t: t}
	resolver, err := reconcile.NewRerunReportPlanResolver(restarted, legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.ResolveReportPlan(testContext, accepted.ChildPrincipal, child, reconcile.ReportingInput{Candidate: candidate})
	if err != nil || plan.Candidate != candidate || !bytes.Equal(plan.PolicyBytes, accepted.Inputs.PolicyBytes) || !reflect.DeepEqual(plan.Baselines, accepted.Inputs.Baselines) || plan.CaptureSHA256 == "" {
		t.Fatal("report replaced frozen selection", err)
	}
	version, err := resolver.ResolveContractsVersion(testContext, accepted.ChildPrincipal, child)
	if err != nil || version != accepted.Inputs.ContractsVersion {
		t.Fatal("report schema pin changed", err)
	}
}

type unexpectedRerunLegacy struct{ t *testing.T }

func (s *unexpectedRerunLegacy) ResolveReportPlan(context.Context, string, run.Run, reconcile.ReportingInput) (reconcile.ReportPlan, error) {
	s.t.Error("rerun reached current registry")
	return reconcile.ReportPlan{}, run.ErrUnavailable
}
func (s *unexpectedRerunLegacy) ResolveContractsVersion(context.Context, string, run.Run) (string, error) {
	s.t.Error("rerun version reached current registry")
	return "", run.ErrUnavailable
}
