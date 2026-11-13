package postgres

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestRerunEvidenceChainAcrossReopenUsesOriginalSelection(t *testing.T) {
	q := newRerunEvidenceQualification(t)
	q.entry.ReportProducer.Version = "9.9.9" // Later report runtime review must not reselect retained reporting.
	record := q.accepted.Inputs.BaselineSnapshots[0]
	if _, err := q.r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "after dispatch"}); err != nil {
		t.Fatal(err)
	}
	if err := serviceGrant(t, q.r.db, "search", "maya", "run:rerun", false); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, q.dsn)
	raw, _, _ := q.adapters(t, reopened, q.entry)
	collected, err := raw.CollectRawArtifacts(testContext, q.accepted.ChildPrincipal, q.current)
	if err != nil || collected.Manifest != q.input.Manifest || !reflect.DeepEqual(collected.Artifacts, q.input.Sources) {
		t.Fatal("raw child provenance failed", err)
	}
	for _, ref := range append([]run.Artifact{collected.Manifest}, collected.Artifacts...) {
		if err = reopened.RegisterArtifact(testContext, q.accepted.ChildPrincipal, ref); err != nil {
			t.Fatal(err)
		}
	}
	q.current, err = reopened.Advance(testContext, q.accepted.ChildPrincipal, q.current.ID, q.current.Revision, run.Change{State: run.StateAnalyzing})
	if err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, q.dsn)
	_, normalized, plans := q.adapters(t, restarted, q.entry)
	plan, err := plans.ResolveAnalysisPlan(testContext, q.accepted.ChildPrincipal, q.current, q.input)
	if err != nil || plan.Source != q.input.Sources[0] || !plan.Producer.Equal(q.entry.NormalizerProducer) || plan.Spec != q.entry.Normalization {
		t.Fatal("normalization reselected evidence", err)
	}
	candidate, err := normalized.CollectNormalizedArtifact(testContext, q.accepted.ChildPrincipal, q.current, q.input)
	if err != nil || candidate != q.normalized {
		t.Fatal("normalized child provenance failed", err)
	}
	if err = restarted.RegisterArtifact(testContext, q.accepted.ChildPrincipal, candidate); err != nil {
		t.Fatal(err)
	}
	q.current, err = restarted.Advance(testContext, q.accepted.ChildPrincipal, q.current.ID, q.current.Revision, run.Change{State: run.StateReporting})
	if err != nil {
		t.Fatal(err)
	}
	reports, err := reconcile.NewRerunReportPlanResolver(openTest(t, q.dsn), &unexpectedRerunLegacy{t: t})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reports.ResolveReportPlan(testContext, q.accepted.ChildPrincipal, q.current, reconcile.ReportingInput{Candidate: candidate})
	if err != nil || report.Candidate != candidate || report.ContractsVersion != q.accepted.Inputs.ContractsVersion || !bytes.Equal(report.PolicyBytes, q.accepted.Inputs.PolicyBytes) || !reflect.DeepEqual(report.Baselines, q.accepted.Inputs.Baselines) || !reflect.DeepEqual(report.BaselineSnapshots, q.accepted.Inputs.BaselineSnapshots) || !report.Producer.Equal(q.accepted.Inputs.Producer) || q.current.Request != q.accepted.Child.Request {
		t.Fatal("report lost original pins", err)
	}
}

func TestRerunEvidenceQualificationRejectsTamperingAndCurrentWithdrawal(t *testing.T) {
	for _, mode := range []string{"raw-bytes", "raw-review", "normalizer-review"} {
		t.Run(mode, func(t *testing.T) {
			q := newRerunEvidenceQualification(t)
			entry := q.entry
			if mode == "raw-bytes" {
				q.objects[q.input.Sources[0]] = []byte("tampered synthetic bytes")
			}
			if mode == "raw-review" {
				entry.RawProducer.Version = "9.9.9"
			}
			raw, _, _ := q.adapters(t, openTest(t, q.dsn), entry)
			_, err := raw.CollectRawArtifacts(testContext, q.accepted.ChildPrincipal, q.current)
			if mode != "normalizer-review" {
				if !errors.Is(err, reconcile.ErrInvalidArtifacts) {
					t.Fatal("untrusted raw evidence accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range append([]run.Artifact{q.input.Manifest}, q.input.Sources...) {
				if err = q.r.RegisterArtifact(testContext, q.accepted.ChildPrincipal, ref); err != nil {
					t.Fatal(err)
				}
			}
			q.current, err = q.r.Advance(testContext, q.accepted.ChildPrincipal, q.current.ID, q.current.Revision, run.Change{State: run.StateAnalyzing})
			if err != nil {
				t.Fatal(err)
			}
			entry.NormalizerProducer.Version = "9.9.9"
			_, normalized, _ := q.adapters(t, openTest(t, q.dsn), entry)
			if _, err = normalized.CollectNormalizedArtifact(testContext, q.accepted.ChildPrincipal, q.current, q.input); !errors.Is(err, reconcile.ErrAnalysisFailed) {
				t.Fatal("withdrawn normalizer output approved", err)
			}
		})
	}
}
